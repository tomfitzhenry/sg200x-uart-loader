package loader

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"sync"
	"time"

	"github.com/tomfitzhenry/sg200x-uart-loader/internal/rom"
	"github.com/tomfitzhenry/sg200x-uart-loader/internal/serial"
)

type Options struct {
	Port         string
	Baud         int
	FIP          []byte
	Log          *Logger
	Verbose      bool
	PowerCmd     string
	AckTimeout   time.Duration
	Retries      int
	WaitTimeout  time.Duration
	Post         time.Duration
	FollowBaud   bool
	Baud2        int
	FinalBaud    int
	ResetRetries int
	PacketDelay  time.Duration
	PacketDelay2 time.Duration
	DataSize     int
	Checksum     byte
	URPLDelay    time.Duration
	URPLMaxWait  time.Duration
	StartDelay   time.Duration
	Drain        time.Duration
	CH347Recover bool
}

type Loader struct {
	opts Options
	log  *Logger

	port        *serial.Port
	parser      StreamParser
	events      chan Event
	done        chan struct{}
	currentBaud int

	readErr error
}

func New(opts Options) *Loader {
	if opts.Log == nil {
		opts.Log, _ = NewLogger("", opts.Verbose, nil)
	}
	if opts.AckTimeout == 0 {
		opts.AckTimeout = 5 * time.Second
	}
	if opts.Retries == 0 {
		opts.Retries = 10
	}
	if opts.WaitTimeout == 0 {
		opts.WaitTimeout = 180 * time.Second
	}
	if opts.Post == 0 {
		opts.Post = 30 * time.Second
	}
	if opts.Baud2 == 0 {
		opts.Baud2 = 1500000
	}
	if opts.ResetRetries == 0 {
		opts.ResetRetries = 3
	}
	if opts.DataSize == 0 {
		opts.DataSize = 88
	}
	if opts.Checksum == 0 {
		opts.Checksum = '1'
	}
	if opts.URPLDelay == 0 {
		opts.URPLDelay = 100 * time.Millisecond
	}
	if opts.URPLMaxWait == 0 {
		opts.URPLMaxWait = 4 * time.Second
	}
	if opts.StartDelay == 0 {
		opts.StartDelay = 150 * time.Millisecond
	}
	if opts.Drain == 0 {
		opts.Drain = 150 * time.Millisecond
	}
	return &Loader{opts: opts, log: opts.Log, currentBaud: opts.Baud}
}

func (l *Loader) reader() {
	defer close(l.done)
	buf := make([]byte, 4096)
	for {
		n, err := l.port.Read(buf, 200*time.Millisecond)
		if n > 0 {
			l.log.RX(buf[:n])
			for _, ev := range l.parser.Feed(buf[:n]) {
				l.events <- ev
			}
		}
		if err == serial.ErrTimeout {
			continue
		}
		if err != nil {
			l.readErr = err
			return
		}
	}
}

func runPowerCommand(cmd string, log *Logger) error {
	if cmd == "" {
		return nil
	}
	log.Progress("running power command: %s", cmd)
	c := exec.Command("/bin/sh", "-c", cmd)
	out, err := c.CombinedOutput()
	if len(out) > 0 {
		log.Console(out)
	}
	if err != nil {
		return fmt.Errorf("power command failed: %w", err)
	}
	return nil
}

func (l *Loader) Run() error {
	port, err := serial.Open(l.opts.Port, l.opts.Baud)
	if err != nil {
		return err
	}
	l.port = port
	l.currentBaud = l.opts.Baud
	defer func() { l.port.Close() }()

	l.parser.SetChecksumType(l.opts.Checksum)
	l.events = make(chan Event, 4096)
	l.done = make(chan struct{})
	go l.reader()

	if err := runPowerCommand(l.opts.PowerCmd, l.log); err != nil {
		return err
	}

	if l.opts.FinalBaud != 0 {
		l.log.Progress("mode: FINAL baud %d (start %d); the UART will NOT switch back for U-Boot/Linux",
			l.opts.FinalBaud, l.opts.Baud)
	} else if l.opts.FollowBaud {
		l.log.Progress("mode: follow baud (start %d, FSBL %d, then back to %d)",
			l.opts.Baud, l.opts.Baud2, l.opts.Baud)
	}
	l.log.Progress("waiting for URPL (port %s, %d 8N1)", l.opts.Port, l.opts.Baud)
	st := &state{}
	start := time.Now()

	const idleBeforePost = 5 * time.Second

	for {
		timeout := time.Second
		if len(st.served) == 0 {
			if remain := l.opts.WaitTimeout - time.Since(start); remain < timeout {
				timeout = remain
			}
		}
		if st.postStarted {
			if remain := time.Until(st.postDeadline); remain < timeout {
				timeout = remain
			}
		}
		if st.urplSeen && !st.uartMode && timeout > 100*time.Millisecond {
			timeout = 100 * time.Millisecond
		}
		if timeout < 0 {
			timeout = 0
		}

		select {
		case ev := <-l.events:
			if ev.Kind == EventText {
				l.handleText(ev.Text, st)
			} else {
				l.log.Verbose("unexpected Kermit packet outside transfer: %s", ev.Packet)
			}
		case <-l.done:
			if l.readErr != nil {
				return fmt.Errorf("serial reader stopped: %w", l.readErr)
			}
			return fmt.Errorf("serial reader stopped")
		case <-time.After(timeout):
		}

		l.maybeSendMagic(st)

		for len(st.pending) > 0 {
			req := st.pending[0]
			st.pending = st.pending[1:]
			if err := l.transfer(req, st); err != nil {
				if errors.Is(err, errBoardReset) {
					st.resets++
					if st.resets > l.opts.ResetRetries {
						return fmt.Errorf("board reset during %d transfers; giving up", st.resets)
					}
					l.log.Progress("board reset mid-transfer (attempt %d/%d); re-arming for the next URPL",
						st.resets, l.opts.ResetRetries)
					l.setBaud(l.opts.Baud)
					st.uartMode = false
					st.magicSent = false
					st.magicPending = false
					st.answerURPL = true
					st.lastText = time.Now()
					st.firstURPL = time.Time{}
					st.urplSeen = false
					st.served = nil
					st.ranges = nil
					st.servedBytes = 0
					st.pending = nil
					st.deferred = nil
					st.lineBuf = nil
					st.roll = nil
					st.postStarted = false
					st.postDeadline = time.Time{}
					st.baudSwitches = 0
					st.baudHint = false
					start = time.Now()
					break
				}
				return err
			}
			if len(st.deferred) > 0 {
				d := st.deferred
				st.deferred = nil
				l.parseRequests(d, st)
			}
		}

		now := time.Now()
		if len(st.served) == 0 && now.Sub(start) >= l.opts.WaitTimeout {
			return fmt.Errorf("timed out after %s waiting for URPL/EK request", l.opts.WaitTimeout)
		}
		if len(st.served) > 0 {
			if !st.postStarted && now.Sub(st.lastActivity) >= idleBeforePost {
				st.postStarted = true
				st.postDeadline = now.Add(l.opts.Post)
				l.log.Progress("FIP transfer idle (served %d bytes, coverage %.1f%%); capturing console for %s",
					st.servedBytes, st.coveragePercent(len(l.opts.FIP)), l.opts.Post)
			}
			if st.postStarted && now.After(st.postDeadline) {
				l.log.Progress("done: served %d bytes in %d requests, coverage %.1f%%",
					st.servedBytes, len(st.served), st.coveragePercent(len(l.opts.FIP)))
				return nil
			}
		}
	}
}

type state struct {
	lineBuf []byte
	roll    []byte

	uartMode     bool
	magicN       int
	magicSent    bool
	magicPending bool
	answerURPL   bool
	lastMagic    time.Time
	lastText     time.Time
	firstURPL    time.Time
	urplSeen     bool

	served      []rom.Request
	ranges      [][2]int
	servedBytes int

	pending  []rom.Request
	deferred []byte

	lastActivity time.Time
	postStarted  bool
	postDeadline time.Time

	banner        bool
	exception     bool
	baudHint      bool
	baudSwitches  int
	handoffLogged bool
	resets        int
}

func (st *state) coveragePercent(n int) float64 {
	if n == 0 {
		return 0
	}
	return 100 * float64(st.servedBytes) / float64(n)
}

func addRange(ranges [][2]int, a, b int) ([][2]int, int) {
	ranges = append(ranges, [2]int{a, b})
	sort.Slice(ranges, func(i, j int) bool { return ranges[i][0] < ranges[j][0] })
	merged := ranges[:0]
	for _, r := range ranges {
		if len(merged) == 0 || r[0] > merged[len(merged)-1][1] {
			merged = append(merged, r)
		} else if r[1] > merged[len(merged)-1][1] {
			merged[len(merged)-1][1] = r[1]
		}
	}
	total := 0
	for _, r := range merged {
		total += r[1] - r[0]
	}
	return merged, total
}

// maybeSendMagic answers exactly one URPL with exactly one URDL.  A cold
// boot prints an early URPL, then the failing eMMC path resets the SoC ~2.5 s
// later; answering that early prompt makes the transfer race the reset.  So
// answer only after the reset marker (answerURPL) or a missed-magic prompt,
// with a bounded timeout in case the loader started after the reset.
func (l *Loader) maybeSendMagic(st *state) {
	if !st.urplSeen || st.uartMode {
		return
	}
	ready := st.magicPending || st.answerURPL
	if !ready && !st.firstURPL.IsZero() && time.Since(st.firstURPL) >= l.opts.URPLMaxWait {
		ready = true
	}
	if !ready {
		return
	}
	if l.opts.URPLDelay > 0 {
		time.Sleep(l.opts.URPLDelay)
	}
	l.sendMagic(st)
	st.urplSeen = false
	st.firstURPL = time.Time{}
	st.answerURPL = false
}

func (l *Loader) sendMagic(st *state) {
	raw := []byte{'U', 'R', 'D', 'L'}
	if err := l.port.Write(raw); err != nil {
		l.log.Event("ERROR writing URDL: %v", err)
		return
	}
	st.magicN++
	st.magicSent = true
	st.magicPending = true
	st.lastMagic = time.Now()
	l.log.TX(raw)
	l.log.Progress("sent URDL (magic #%d, exactly once for this prompt)", st.magicN)
}

// drain reads and handles console text for d without letting stale boot-log
// bytes reach the Kermit sender.  New EK requests are still parsed.
func (l *Loader) drain(st *state, d time.Duration) {
	if d <= 0 {
		return
	}
	deadline := time.Now().Add(d)
	for {
		remain := time.Until(deadline)
		if remain <= 0 {
			return
		}
		select {
		case ev := <-l.events:
			if ev.Kind == EventText {
				l.handleText(ev.Text, st)
			} else {
				l.log.Verbose("draining stale Kermit packet outside transfer: %s", ev.Packet)
			}
		case <-time.After(remain):
			return
		case <-l.done:
			return
		}
	}
}

// observeText logs/echoes console text, watches for the ROM prompt and U-Boot
// output, and sends the UART-download magic on each fresh URPL prompt.
func (l *Loader) observeText(b []byte, st *state) {
	l.log.Console(b)
	st.lastText = time.Now()
	// The eMMC-failure path ends with this reset and is followed by the
	// stable UART-download prompt.  Forget any prompt seen before it.
	if bytes.Contains(b, []byte("RESET:plat/mars")) || bytes.Contains(b, []byte("E:Boot failed")) {
		st.answerURPL = true
		st.urplSeen = false
		st.firstURPL = time.Time{}
	}
	st.roll = append(st.roll, b...)
	if len(st.roll) > 4096 {
		st.roll = st.roll[len(st.roll)-4096:]
	}

	for {
		i := bytes.LastIndex(st.roll, []byte("URPL."))
		if i < 0 {
			break
		}
		st.roll = st.roll[i+len("URPL."):]
		// Do not answer an early prompt: on a cold boot the eMMC failure
		// path can reset the SoC ~2.5 s after the first URPL.  Mark the
		// prompt and let maybeSendMagic wait for the console to settle.
		st.urplSeen = true
		if st.firstURPL.IsZero() {
			st.firstURPL = time.Now()
		}
	}

	if !st.banner {
		for _, marker := range [][]byte{[]byte("U-Boot 20"), []byte("U-Boot 202"), []byte("soph#"), []byte("=> ")} {
			if bytes.Contains(st.roll, marker) {
				st.banner = true
				l.log.Progress("U-Boot banner? yes (%q seen)", string(marker))
				break
			}
		}
	}
	if !st.exception {
		for _, marker := range [][]byte{[]byte("Synchronous Abort"), []byte("\"Error\" handler"), []byte("Exception"), []byte("RESET:")} {
			if bytes.Contains(st.roll, marker) {
				st.exception = true
				l.log.Progress("U-Boot exception output detected (%q)", string(marker))
				break
			}
		}
	}
	if !st.baudHint {
		if bytes.Contains(st.roll, []byte("1500000")) || bytes.Contains(st.roll, []byte("1,500,000")) {
			st.baudHint = true
			if l.opts.FinalBaud != 0 && st.baudSwitches == 0 {
				l.switchBaud(st, l.opts.FinalBaud)
			} else if l.opts.FollowBaud && st.baudSwitches == 0 {
				l.switchBaud(st, l.opts.Baud2)
			} else {
				l.log.Progress("board mentions 1,500,000 baud; staying at %d (pass --final-baud 1500000 to switch)", l.opts.Baud)
			}
		}
	}
	handoff := bytes.Contains(st.roll, []byte("Jump to monitor")) ||
		bytes.Contains(st.roll, []byte("Loader_2nd loaded"))
	if l.opts.FinalBaud != 0 && handoff && !st.handoffLogged {
		st.handoffLogged = true
		l.log.Progress("HANDOFF: FSBL -> BL31/U-Boot; leaving the tty at %d baud and never switching back", l.currentBaud)
	}
	if l.opts.FollowBaud && st.baudSwitches == 1 && handoff {
		// The FSBL hands off to BL31/U-Boot, whose console is normally back
		// at the original baud.  Switch at the first hand-off marker so the
		// first U-Boot output is not lost.
		l.switchBaud(st, l.opts.Baud)
	}
}

func isBootROMReset(b []byte) bool {
	return bytes.Contains(b, []byte("B.SCS/0/0.")) ||
		bytes.Contains(b, []byte("URPL.")) ||
		bytes.Contains(b, []byte("RESET:plat/mars"))
}

// recoverCH347 clears a wedged CH347 TX path by de-authorizing and
// re-authorizing its USB device, then reopening the tty at the current baud.
// The Oz64 keeps running; only the host-side USB link is reset.
func (l *Loader) recoverCH347(st *state) error {
	l.log.Progress("CH347 recovery: closing %s and re-authorizing the USB device", l.opts.Port)
	l.port.Close()
	select {
	case <-l.done:
	case <-time.After(2 * time.Second):
	}
	if err := serial.Reauthorize(l.opts.Port); err != nil {
		return err
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(l.opts.Port); err == nil {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s did not reappear after USB re-authorize", l.opts.Port)
		}
		time.Sleep(200 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond)
	p, err := serial.Open(l.opts.Port, l.currentBaud)
	if err != nil {
		return err
	}
	l.port = p
	l.parser.SetChecksumType(l.opts.Checksum)
	l.events = make(chan Event, 4096)
	l.done = make(chan struct{})
	l.readErr = nil
	go l.reader()
	l.drain(st, 200*time.Millisecond)
	l.log.Progress("CH347 recovery: reopened %s at %d baud", l.opts.Port, l.currentBaud)
	return nil
}

func (l *Loader) setBaud(baud int) error {
	if err := l.port.SetBaud(baud); err != nil {
		return err
	}
	l.currentBaud = baud
	return nil
}

func (l *Loader) switchBaud(st *state, baud int) {
	st.baudSwitches++
	if err := l.setBaud(baud); err != nil {
		l.log.Progress("ERROR switching to %d baud: %v", baud, err)
		return
	}
	if l.opts.FinalBaud != 0 {
		l.log.Progress("switched UART to %d baud (final; staying here)", baud)
	} else {
		l.log.Progress("switched UART to %d baud (following FSBL/U-Boot)", baud)
	}
}

func (l *Loader) parseRequests(b []byte, st *state) {
	st.lineBuf = append(st.lineBuf, b...)
	for {
		i := bytes.IndexAny(st.lineBuf, "\r\n")
		if i < 0 {
			break
		}
		line := st.lineBuf[:i]
		st.lineBuf = st.lineBuf[i+1:]
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		for _, req := range rom.FindRequests(line) {
			st.pending = append(st.pending, req)
			// The ROM accepted our URDL and is now in UART-download mode.
			st.magicPending = false
			st.answerURPL = false
		}
	}
}

func (l *Loader) handleText(b []byte, st *state) {
	l.observeText(b, st)
	l.parseRequests(b, st)
}

func (l *Loader) pacing() time.Duration {
	if l.currentBaud != l.opts.Baud {
		return l.opts.PacketDelay2
	}
	return l.opts.PacketDelay
}

// attemptFile runs one complete Kermit file transfer.
func (l *Loader) attemptFile(req rom.Request, data []byte, st *state) error {
	abortCh := make(chan struct{})
	var abortOnce sync.Once
	s := &sender{
		port:        l.port,
		events:      l.events,
		log:         l.log,
		abort:       abortCh,
		timeout:     l.opts.AckTimeout,
		retries:     l.opts.Retries,
		delay:       l.pacing(),
		dataSize:    l.opts.DataSize,
		checksum:    l.opts.Checksum,
		setChecksum: l.parser.SetChecksumType,
		onText: func(b []byte) {
			l.observeText(b, st)
			st.deferred = append(st.deferred, b...)
			if isBootROMReset(b) {
				abortOnce.Do(func() { close(abortCh) })
			}
		},
	}
	return s.sendFile("fip.bin", data)
}

func (l *Loader) transfer(req rom.Request, st *state) error {
	st.uartMode = true
	if err := req.Validate(len(l.opts.FIP)); err != nil {
		return fmt.Errorf("bad ROM request: %w", err)
	}
	data := l.opts.FIP[req.Offset : req.Offset+req.Size]
	l.log.Progress("request offset=%d size=%d (%d bytes available)", req.Offset, req.Size, len(data))

	// The ROM finished printing UL_START; give it time to switch from its
	// prompt routine into the E-Kermit receiver and flush any boot-log bytes
	// that are still queued in the CH347 before the Send-Init.
	if l.opts.StartDelay > 0 {
		time.Sleep(l.opts.StartDelay)
	}
	l.drain(st, l.opts.Drain)

	err := l.attemptFile(req, data, st)
	if err != nil && l.opts.CH347Recover && !errors.Is(err, errBoardReset) && errors.Is(err, errACKTimeout) {
		l.log.Progress("no Kermit ACK for offset=%d; assuming CH347 TX wedge, re-authorizing USB and retrying once", req.Offset)
		if rerr := l.recoverCH347(st); rerr != nil {
			l.log.Progress("CH347 recovery failed: %v", rerr)
		} else {
			err = l.attemptFile(req, data, st)
		}
	}
	if err != nil {
		l.log.Progress("ERROR sending offset=%d size=%d: %v", req.Offset, req.Size, err)
		return err
	}
	l.log.Progress("sent %d bytes at offset %d", len(data), req.Offset)

	st.served = append(st.served, req)
	if st.baudSwitches == 0 && req.Offset == 0x1000 {
		// The vendor BL2 ends at FIP offset 0xe800.  The FSBL switches its
		// UART to 1.5 Mbaud and immediately asks for the next slice, so
		// switch as soon as the BL2 transfer's B packet is acknowledged.
		if l.opts.FinalBaud != 0 {
			l.switchBaud(st, l.opts.FinalBaud)
		} else if l.opts.FollowBaud {
			l.switchBaud(st, l.opts.Baud2)
		}
	}
	var total int
	st.ranges, total = addRange(st.ranges, req.Offset, req.Offset+req.Size)
	st.servedBytes = total
	st.lastActivity = time.Now()
	st.postStarted = false
	st.postDeadline = time.Time{}

	if total >= len(l.opts.FIP) {
		st.postStarted = true
		st.postDeadline = time.Now().Add(l.opts.Post)
		l.log.Progress("FIP complete: %d/%d bytes served", total, len(l.opts.FIP))
	}
	return nil
}
