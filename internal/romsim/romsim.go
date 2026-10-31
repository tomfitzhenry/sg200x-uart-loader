// Package romsim simulates the SG200x/CV181x-family BootROM and its E-Kermit
// receiver over a serial stream.
//
// A Simulator drives a real loader binary through a PTY master: it prints the
// cold-boot URPL prompt, answers the loader's URDL magic, emits EK/... slice
// requests, and acts as the E-Kermit 1.8 receiver for each request.  It can
// inject the eMMC failure reset, an early prompt, a mid-transfer BootROM reset,
// and an out-of-range request.  The exported Result records what the loader did
// so end-to-end tests can assert on the wire behaviour and on the final tty
// baud rate.
package romsim

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/tomfitzhenry/sg200x-uart-loader/internal/kermit"
	"github.com/tomfitzhenry/sg200x-uart-loader/internal/rom"
)

// Config controls one simulated boot and FIP download.  Zero values select
// sensible defaults; in particular SliceSize defaults to 4096 bytes.
type Config struct {
	// FIP is the image the simulated ROM asks for.  Serve slices it at
	// SliceSize boundaries and verifies every received byte against it.
	FIP []byte
	// SliceSize is the size of each EK/... request.  Defaults to 4096.
	SliceSize int
	// EarlyPrompt emits a cold-boot URPL and waits EarlyPromptWait for a
	// URDL before printing the eMMC failure and the real prompt.
	EarlyPrompt bool
	// EarlyPromptWait is the quiet window used to detect an early magic.
	// Defaults to 150ms.
	EarlyPromptWait time.Duration
	// ResetAfterDataPackets injects B.SCS/0/0.WD.URPL\r\n after this many
	// data packets in the first slice, stops ACKing, and re-arms.  Zero
	// disables the reset.
	ResetAfterDataPackets int
	// InvalidRequest asks for an offset/size beyond the FIP.  A correct
	// loader exits without sending a Send-Init packet.
	InvalidRequest bool
	// FinalText is written after the last slice, ending in the U-Boot
	// prompt.  Empty selects a short default U-Boot/Linux console.
	FinalText string
	// ReadyFile, when set, makes Serve wait for the file to exist before
	// emitting the early boot banner.  The loader's --power-cmd can touch it
	// after opening and flushing the tty, so the early prompt is not lost.
	ReadyFile string
	// BaudSwitchMarker emits a 1.5 Mbaud/hand-off marker after the slice at
	// offset 0x1000.  If Slave is set, Serve also polls its termios until
	// the loader changes the tty to B1500000.
	BaudSwitchMarker bool
	// Slave is the PTY slave kept open for independent termios inspection.
	// It is optional, but required for Result.FinalBaudObserved.
	Slave *os.File
	// AckTimeout bounds how long the receiver waits for each loader packet.
	// Defaults to 5s.
	AckTimeout time.Duration
	// ResetWait bounds how long the receiver waits for a new URDL after a
	// mid-transfer reset.  Defaults to 3s.
	ResetWait time.Duration
	// ResetPromptInterval is how often the receiver re-prints a URPL while
	// waiting for the loader to re-arm after a reset.  Defaults to 100ms.
	ResetPromptInterval time.Duration
	// BaudCheckTimeout bounds the termios poll for B1500000.  Defaults to 1s.
	BaudCheckTimeout time.Duration
}

// Slice is one completed EK/... transfer.
type Slice struct {
	Offset int
	Size   int
	Data   []byte
}

// Result is the observable outcome of a simulated boot.
type Result struct {
	// Slices lists only successfully completed transfers, in order.
	Slices []Slice
	// Requests lists every EK/... request sent, including retries.
	Requests []rom.Request
	// MagicCount is the number of URDL sequences seen in the inbound
	// stream, including duplicates skipped while looking for a packet.
	MagicCount int
	// EarlyMagic records a URDL sent in response to the early prompt.
	EarlyMagic bool
	// Resets is the number of mid-transfer reset injections observed.
	Resets int
	// FinalBaudObserved reports that the slave tty reached B1500000.
	FinalBaudObserved bool
	// SendInitCount is the number of valid S packets received.
	SendInitCount int
}

// Simulator runs the BootROM/E-Kermit state machine.
type Simulator struct {
	cfg Config

	mu     sync.Mutex
	result Result
}

// New creates a simulator.  It does not touch the serial stream until Serve.
func New(cfg Config) *Simulator {
	if cfg.SliceSize <= 0 {
		cfg.SliceSize = 4096
	}
	if cfg.EarlyPromptWait == 0 {
		cfg.EarlyPromptWait = 150 * time.Millisecond
	}
	if cfg.AckTimeout == 0 {
		cfg.AckTimeout = 5 * time.Second
	}
	if cfg.ResetWait == 0 {
		cfg.ResetWait = 3 * time.Second
	}
	if cfg.ResetPromptInterval == 0 {
		cfg.ResetPromptInterval = 100 * time.Millisecond
	}
	if cfg.BaudCheckTimeout == 0 {
		cfg.BaudCheckTimeout = time.Second
	}
	if cfg.FinalText == "" {
		cfg.FinalText = defaultFinalText
	}
	return &Simulator{cfg: cfg}
}

// Result returns a snapshot of the simulator's observations.
func (s *Simulator) Result() Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.result
	out.Slices = append([]Slice(nil), s.result.Slices...)
	for i := range out.Slices {
		out.Slices[i].Data = append([]byte(nil), out.Slices[i].Data...)
	}
	out.Requests = append([]rom.Request(nil), s.result.Requests...)
	return out
}

func (s *Simulator) addRequest(r rom.Request) {
	s.mu.Lock()
	s.result.Requests = append(s.result.Requests, r)
	s.mu.Unlock()
}

func (s *Simulator) addSlice(sl Slice) {
	sl.Data = append([]byte(nil), sl.Data...)
	s.mu.Lock()
	s.result.Slices = append(s.result.Slices, sl)
	s.mu.Unlock()
}

func (s *Simulator) incMagic() {
	s.mu.Lock()
	s.result.MagicCount++
	s.mu.Unlock()
}

func (s *Simulator) setEarlyMagic(v bool) {
	if !v {
		return
	}
	s.mu.Lock()
	s.result.EarlyMagic = true
	s.mu.Unlock()
}

func (s *Simulator) incResets() {
	s.mu.Lock()
	s.result.Resets++
	s.mu.Unlock()
}

func (s *Simulator) setFinalBaud(v bool) {
	s.mu.Lock()
	s.result.FinalBaudObserved = v
	s.mu.Unlock()
}

func (s *Simulator) incSendInit() {
	s.mu.Lock()
	s.result.SendInitCount++
	s.mu.Unlock()
}

// EarlyBannerMarker uniquely identifies the early cold-boot banner so tests
// can prove the loader read it before the reset prompt.
const EarlyBannerMarker = "ROM-SIM-EARLY-PROMPT-9c41"

const earlyBanner = "\r\n" + EarlyBannerMarker + "\r\n" +
	"U-Boot SPL 2021.10 (Oct 27 2025)\r\nB.SCS/0/0.WD.URPL.\r\n"

const resetBanner = "\r\n E:Boot failed (8).\r\n" +
	" E:RESET:plat/mars/platform.c:114\r\n" +
	"B.SCS/0/0.WD.URPL.\r\n"

const resetText = "B.SCS/0/0.WD.URPL.\r\n"

const baudMarkerText = "\r\nLoader_2nd loaded.\r\n" +
	"Jump to monitor at 0x80000000.\r\n" +
	"UART: 1500000 baud\r\n"

const defaultFinalText = "\r\nU-Boot 2026.07-rc3 (Oct 27 2025 - 00:00:00 +0000)\r\n" +
	"Loading Linux from UART...\r\n" +
	"Starting kernel...\r\n" +
	"oz64 login: (automatic)\r\noz64=> "

func (s *Simulator) slicePlan() []rom.Request {
	n := len(s.cfg.FIP)
	if n == 0 {
		return nil
	}
	var out []rom.Request
	for off := 0; off < n; off += s.cfg.SliceSize {
		size := s.cfg.SliceSize
		if off+size > n {
			size = n - off
		}
		out = append(out, rom.Request{Offset: off, Size: size})
	}
	return out
}

func waitForReadyFile(ctx context.Context, path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("romsim: timed out waiting for ready file %s", path)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// Serve runs the simulated BootROM against a loader connected to master.  It
// returns when the transcript is complete, the loader exits, or ctx is done.
func (s *Simulator) Serve(ctx context.Context, master *os.File) error {
	if master == nil {
		return errors.New("romsim: nil PTY master")
	}
	c := &conn{f: master, onURDL: s.incMagic}

	if s.cfg.ReadyFile != "" {
		if err := waitForReadyFile(ctx, s.cfg.ReadyFile, 5*time.Second); err != nil {
			return err
		}
	}
	if _, err := c.write([]byte(earlyBanner)); err != nil {
		return err
	}
	if s.cfg.EarlyPrompt {
		ok, err := c.scanURDL(ctx, s.cfg.EarlyPromptWait)
		if err != nil && !errors.Is(err, errTimeout) && !errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		s.setEarlyMagic(ok)
	}

	if _, err := c.write([]byte(resetBanner)); err != nil {
		return err
	}

	ok, err := c.scanURDL(ctx, s.cfg.AckTimeout)
	if err != nil && !errors.Is(err, errTimeout) && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if !ok {
		return fmt.Errorf("timed out waiting for URDL after the boot prompt")
	}

	if s.cfg.InvalidRequest {
		return s.serveInvalid(ctx, c)
	}

	for i, req := range s.slicePlan() {
		resetAfter := 0
		if i == 0 {
			resetAfter = s.cfg.ResetAfterDataPackets
		}
		reset, err := s.serveRequest(ctx, c, req, resetAfter)
		if err != nil {
			return err
		}
		if reset {
			s.incResets()
			if err := s.awaitResetURDL(ctx, c); err != nil {
				return err
			}
			reset, err = s.serveRequest(ctx, c, req, 0)
			if err != nil {
				return err
			}
			if reset {
				return errors.New("romsim: second reset during a slice")
			}
		}
		if req.Offset == 0x1000 && s.cfg.BaudSwitchMarker {
			if _, err := c.write([]byte(baudMarkerText)); err != nil {
				return err
			}
			if s.cfg.Slave != nil {
				observed, err := s.waitForFinalBaud(ctx)
				if err != nil {
					return err
				}
				s.setFinalBaud(observed)
			}
		}
	}

	if _, err := c.write([]byte(s.cfg.FinalText)); err != nil {
		return err
	}
	return nil
}

func (s *Simulator) serveInvalid(ctx context.Context, c *conn) error {
	req := rom.Request{Offset: 0, Size: len(s.cfg.FIP) + 4096}
	s.addRequest(req)
	if _, err := c.write([]byte(fmt.Sprintf("BS/UART.PS.EK/%d/%d/UL_START\r\n", req.Offset, req.Size))); err != nil {
		return err
	}
	deadline := time.Now().Add(s.cfg.AckTimeout)
	for {
		pkt, err := c.readPacket(ctx, deadline)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// The loader exits on the out-of-range request and closes
			// the slave, so a read error is the expected outcome.
			return nil
		}
		if pkt.Type == 'S' {
			s.incSendInit()
			return fmt.Errorf("romsim: loader sent Send-Init for an out-of-range request")
		}
	}
}

// serveRequest requests one slice and receives it.  It returns reset=true
// without completing the slice when a mid-transfer reset is injected.
func (s *Simulator) serveRequest(ctx context.Context, c *conn, req rom.Request, resetAfter int) (bool, error) {
	if req.Offset < 0 || req.Size < 0 || req.Offset > len(s.cfg.FIP) || req.Size > len(s.cfg.FIP)-req.Offset {
		return false, fmt.Errorf("romsim: request %d/%d out of range for FIP of %d bytes", req.Offset, req.Size, len(s.cfg.FIP))
	}
	s.addRequest(req)
	line := fmt.Sprintf("BS/UART.PS.EK/%d/%d/UL_START\r\n", req.Offset, req.Size)
	if _, err := c.write([]byte(line)); err != nil {
		return false, err
	}

	deadline := time.Now().Add(s.cfg.AckTimeout)
	pkt, err := c.readPacket(ctx, deadline)
	if err != nil {
		return false, fmt.Errorf("waiting for Send-Init: %w", err)
	}
	if pkt.Type != 'S' || pkt.Seq != 0 {
		return false, fmt.Errorf("expected Send-Init, got type %c seq=%d", pkt.Type, pkt.Seq)
	}
	got, err := kermit.DecodeParams(pkt.Data)
	if err != nil {
		return false, fmt.Errorf("decode Send-Init: %w", err)
	}
	if err := validateSendInit(got); err != nil {
		return false, err
	}
	s.incSendInit()
	ackParams := romACKParams()
	ackData := kermit.EncodeParams(ackParams)
	if err := s.ack(c, 0, ackData); err != nil {
		return false, err
	}

	expected := byte(1)
	lastSeq := byte(0)
	var file []byte
	dataPackets := 0
	for {
		deadline := time.Now().Add(s.cfg.AckTimeout)
		pkt, err := c.readPacket(ctx, deadline)
		if err != nil {
			return false, fmt.Errorf("waiting for packet at offset %d: %w", len(file), err)
		}
		if pkt.Type == 'S' {
			return false, fmt.Errorf("romsim: second Send-Init packet mid-transfer")
		}
		if pkt.Seq == lastSeq {
			if err := s.ack(c, pkt.Seq, nil); err != nil {
				return false, err
			}
			continue
		}
		if pkt.Seq != expected {
			return false, fmt.Errorf("packet sequence %d, want %d (type %c)", pkt.Seq, expected, pkt.Type)
		}
		switch pkt.Type {
		case 'F':
			name := kermit.DecodeData(pkt.Data, ackParams, false, 0)
			if string(name) != "fip.bin" {
				return false, fmt.Errorf("filename %q, want fip.bin", name)
			}
			if err := s.ack(c, pkt.Seq, nil); err != nil {
				return false, err
			}
		case 'D':
			dataPackets++
			if resetAfter > 0 && dataPackets == resetAfter {
				if _, err := c.write([]byte(resetText)); err != nil {
					return false, err
				}
				return true, nil
			}
			dec := kermit.DecodeData(pkt.Data, ackParams, false, 0)
			file = append(file, dec...)
			if len(file) > req.Size {
				return false, fmt.Errorf("received %d bytes, request is %d", len(file), req.Size)
			}
			if err := s.ack(c, pkt.Seq, nil); err != nil {
				return false, err
			}
		case 'Z':
			if err := s.ack(c, pkt.Seq, nil); err != nil {
				return false, err
			}
		case 'B':
			if err := s.ack(c, pkt.Seq, nil); err != nil {
				return false, err
			}
			if len(file) != req.Size {
				return false, fmt.Errorf("received %d bytes, request is %d", len(file), req.Size)
			}
			want := s.cfg.FIP[req.Offset : req.Offset+req.Size]
			if !bytes.Equal(file, want) {
				return false, fmt.Errorf("slice %d/%d differs from FIP", req.Offset, req.Size)
			}
			s.addSlice(Slice{Offset: req.Offset, Size: req.Size, Data: file})
			return false, nil
		default:
			return false, fmt.Errorf("unexpected packet type %c seq=%d", pkt.Type, pkt.Seq)
		}
		lastSeq = pkt.Seq
		expected = (expected + 1) & 63
	}
}

func (s *Simulator) ack(c *conn, seq byte, data []byte) error {
	raw, err := kermit.EncodePacket(seq, 'Y', data, romACKParams())
	if err != nil {
		return err
	}
	_, err = c.write(raw)
	return err
}

// romACKParams reproduces the observed E-Kermit 1.8 ACK to the loader:
// 01 30 20 59 7e 48 20 40 2d 23 59 31 7e 2a 3f 4b 2b 26 0d.
func romACKParams() kermit.Params {
	return kermit.Params{
		MaxPacket: 94,
		Timeout:   40,
		NPAD:      0,
		PADC:      0x40,
		EOL:       kermit.CR,
		QCTL:      '#',
		QBIN:      'Y',
		CHKT:      '1',
		REPT:      '~',
		CAPAS:     kermit.CAP_LP | kermit.CAP_AT,
		Window:    31,
		LongMax:   4096,
	}
}

func validateSendInit(p kermit.Params) error {
	switch {
	case p.MaxPacket != 94:
		return fmt.Errorf("Send-Init MaxPacket=%d, want 94", p.MaxPacket)
	case p.Timeout != 15:
		return fmt.Errorf("Send-Init Timeout=%d, want 15", p.Timeout)
	case p.NPAD != 0:
		return fmt.Errorf("Send-Init NPAD=%d, want 0", p.NPAD)
	case p.PADC != 0x40:
		return fmt.Errorf("Send-Init PADC=%#x, want 0x40", p.PADC)
	case p.EOL != kermit.CR:
		return fmt.Errorf("Send-Init EOL=%#x, want CR", p.EOL)
	case p.QCTL != '#':
		return fmt.Errorf("Send-Init QCTL=%#x, want '#'", p.QCTL)
	case p.QBIN != 'Y':
		return fmt.Errorf("Send-Init QBIN=%#x, want 'Y'", p.QBIN)
	case p.CHKT != '1':
		return fmt.Errorf("Send-Init CHKT=%#x, want '1'", p.CHKT)
	case p.REPT != '~':
		return fmt.Errorf("Send-Init REPT=%#x, want '~'", p.REPT)
	case p.CAPAS != 58:
		return fmt.Errorf("Send-Init CAPAS=%d, want 58", p.CAPAS)
	case p.Window != 1:
		return fmt.Errorf("Send-Init Window=%d, want 1", p.Window)
	case p.LongMax != 4095:
		return fmt.Errorf("Send-Init LongMax=%d, want 4095", p.LongMax)
	}
	return nil
}

func (s *Simulator) awaitResetURDL(ctx context.Context, c *conn) error {
	deadline := time.Now().Add(s.cfg.ResetWait)
	nextPrompt := time.Now().Add(s.cfg.ResetPromptInterval)
	for {
		now := time.Now()
		if !now.Before(deadline) {
			return fmt.Errorf("timed out waiting for URDL after reset")
		}
		waitUntil := deadline
		if nextPrompt.Before(waitUntil) {
			waitUntil = nextPrompt
		}
		ok, err := c.scanURDL(ctx, time.Until(waitUntil))
		if err != nil && !errors.Is(err, errTimeout) && !errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		if ok {
			return nil
		}
		if !time.Now().Before(nextPrompt) {
			if _, err := c.write([]byte("URPL.\r\n")); err != nil {
				return err
			}
			nextPrompt = time.Now().Add(s.cfg.ResetPromptInterval)
		}
	}
}

func (s *Simulator) waitForFinalBaud(ctx context.Context) (bool, error) {
	deadline := time.Now().Add(s.cfg.BaudCheckTimeout)
	for {
		ok, err := slaveAtBaud(s.cfg.Slave, 1500000)
		if err != nil {
			return false, fmt.Errorf("read slave termios: %w", err)
		}
		if ok {
			return true, nil
		}
		if !time.Now().Before(deadline) {
			return false, fmt.Errorf("loader did not set the slave tty to 1500000 baud")
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}

var errTimeout = errors.New("romsim: read timeout")

type conn struct {
	f      *os.File
	buf    []byte
	hist   [4]byte
	seen   int
	urdl   int
	onURDL func()
}

func (c *conn) write(b []byte) (int, error) {
	total := 0
	for len(b) > 0 {
		n, err := c.f.Write(b)
		total += n
		b = b[n:]
		if err != nil {
			return total, err
		}
		if n == 0 {
			return total, fmt.Errorf("romsim: short write")
		}
	}
	return total, nil
}

// fill reads at least one byte, waking periodically to notice ctx.
func (c *conn) fill(ctx context.Context, deadline time.Time) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		remain := time.Until(deadline)
		if remain <= 0 {
			return errTimeout
		}
		slice := remain
		if slice > 20*time.Millisecond {
			slice = 20 * time.Millisecond
		}
		if err := c.f.SetReadDeadline(time.Now().Add(slice)); err != nil {
			return err
		}
		tmp := make([]byte, 4096)
		n, err := c.f.Read(tmp)
		if n > 0 {
			c.buf = append(c.buf, tmp[:n]...)
			return nil
		}
		if err != nil {
			if os.IsTimeout(err) {
				continue
			}
			return err
		}
	}
}

func (c *conn) readByte(ctx context.Context, deadline time.Time) (byte, error) {
	if len(c.buf) == 0 {
		if err := c.fill(ctx, deadline); err != nil {
			return 0, err
		}
	}
	b := c.buf[0]
	c.buf = c.buf[1:]
	c.hist[0], c.hist[1], c.hist[2], c.hist[3] = c.hist[1], c.hist[2], c.hist[3], b
	c.seen++
	if c.seen >= 4 && c.hist == [4]byte{'U', 'R', 'D', 'L'} {
		c.urdl++
		if c.onURDL != nil {
			c.onURDL()
		}
	}
	return b, nil
}

func (c *conn) readN(ctx context.Context, deadline time.Time, n int) ([]byte, error) {
	out := make([]byte, 0, n)
	for len(out) < n {
		if len(c.buf) == 0 {
			if err := c.fill(ctx, deadline); err != nil {
				return nil, err
			}
		}
		take := n - len(out)
		if take > len(c.buf) {
			take = len(c.buf)
		}
		out = append(out, c.buf[:take]...)
		c.buf = c.buf[take:]
	}
	return out, nil
}

func (c *conn) readPacket(ctx context.Context, deadline time.Time) (kermit.Packet, error) {
	for {
		b, err := c.readByte(ctx, deadline)
		if err != nil {
			return kermit.Packet{}, err
		}
		if b != kermit.SOH {
			continue
		}
		lb, err := c.readByte(ctx, deadline)
		if err != nil {
			return kermit.Packet{}, err
		}
		length := int(lb) - kermit.SP
		if length < 3 || length > 94 {
			return kermit.Packet{}, fmt.Errorf("bad packet length %d", length)
		}
		rest, err := c.readN(ctx, deadline, length+1)
		if err != nil {
			return kermit.Packet{}, err
		}
		raw := append([]byte{kermit.SOH, lb}, rest...)
		pkt, err := kermit.DecodePacketType(raw, '1')
		if err != nil {
			return kermit.Packet{}, fmt.Errorf("decode packet: %w", err)
		}
		return pkt, nil
	}
}

func (c *conn) scanURDL(ctx context.Context, timeout time.Duration) (bool, error) {
	deadline := time.Now().Add(timeout)
	start := c.urdl
	for {
		if _, err := c.readByte(ctx, deadline); err != nil {
			return false, err
		}
		if c.urdl > start {
			return true, nil
		}
	}
}
