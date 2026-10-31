package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/tomfitzhenry/sg200x-uart-loader/internal/kermit"
	"github.com/tomfitzhenry/sg200x-uart-loader/internal/loader"
)

var version = "dev"

func main() {
	var (
		showVersion  = flag.Bool("version", false, "print the version and exit")
		port         = flag.String("port", "/dev/ttyACM0", "serial port connected to the Oz64 UART")
		fipPath      = flag.String("fip", "", "FIP image to serve")
		baud         = flag.Int("baud", 115200, "UART baud rate")
		logPath      = flag.String("log", "", "write a detailed hex/protocol log here")
		powerCmd     = flag.String("power-cmd", "", "shell command to run before waiting for URPL")
		verbose      = flag.Bool("verbose", false, "verbose protocol logging")
		dryRun       = flag.Bool("dry-run", false, "validate inputs and print the plan; do not open the port")
		selfTest     = flag.Bool("selftest", false, "run built-in packet/parser tests and exit")
		waitTimeout  = flag.Duration("wait-timeout", 180*time.Second, "how long to wait for URPL/EK")
		post         = flag.Duration("post", 30*time.Second, "console capture time after the last FIP slice")
		ackTimeout   = flag.Duration("ack-timeout", 5*time.Second, "Kermit ACK timeout per packet")
		retries      = flag.Int("retries", 10, "Kermit retransmissions per packet")
		followBaud   = flag.Bool("follow-baud", false, "legacy: switch to --baud2 at the FSBL, then back to --baud at hand-off")
		baud2        = flag.Int("baud2", 1500000, "second baud rate used by --follow-baud")
		finalBaud    = flag.Int("final-baud", 0, "switch to this baud at the FSBL and never switch back (e.g. 1500000)")
		stayBaud     = flag.Int("stay-baud", 0, "alias for --final-baud")
		resetRetries = flag.Int("reset-retries", 3, "retries if the BootROM resets mid-transfer (cold-boot workaround)")
		packetDelay  = flag.Duration("packet-delay", 2*time.Millisecond, "delay after each Kermit ACK at the initial baud")
		packetDelay2 = flag.Duration("packet-delay2", 0, "delay after each Kermit ACK at the final baud")
		dataSize     = flag.Int("data-size", 88, "maximum Kermit data bytes per packet (16..88)")
		checksum     = flag.String("checksum", "1", "Kermit checksum type: 1 (6-bit, ROM default) or 3 (CRC-16)")
		urplDelay    = flag.Duration("urpl-delay", 100*time.Millisecond, "extra delay after a settled URPL before sending URDL")
		urplMaxWait  = flag.Duration("urpl-max-wait", 4*time.Second, "fallback: answer a prompt this long after the first URPL if no eMMC reset marker was seen")
		startDelay   = flag.Duration("start-delay", 150*time.Millisecond, "delay after UL_START before the first Kermit packet")
		drain        = flag.Duration("drain", 150*time.Millisecond, "drain boot-log bytes before starting a transfer")
		ch347Recover = flag.Bool("ch347-recover", true, "re-authorize/reopen the CH347 if TX wedges (ACK timeouts)")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("sg200x-uart-loader %s\n", version)
		return
	}

	if *stayBaud != 0 {
		if *finalBaud != 0 && *finalBaud != *stayBaud {
			fmt.Fprintln(os.Stderr, "sg200x-uart-loader: --stay-baud and --final-baud disagree")
			os.Exit(2)
		}
		*finalBaud = *stayBaud
	}
	if *followBaud && *finalBaud != 0 {
		fmt.Fprintln(os.Stderr, "sg200x-uart-loader: --follow-baud and --final-baud are mutually exclusive")
		os.Exit(2)
	}
	if *checksum != "1" && *checksum != "3" {
		fmt.Fprintln(os.Stderr, "sg200x-uart-loader: --checksum must be 1 or 3")
		os.Exit(2)
	}
	if *dataSize < 16 || *dataSize > 88 {
		fmt.Fprintln(os.Stderr, "sg200x-uart-loader: --data-size must be between 16 and 88")
		os.Exit(2)
	}

	if *selfTest {
		if err := loader.SelfTest(); err != nil {
			fmt.Fprintf(os.Stderr, "selftest FAIL: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("selftest PASS: packet encoding/checksum/quoting, ACK decoding, ROM request parsing")
		return
	}

	if *fipPath == "" {
		fmt.Fprintln(os.Stderr, "sg200x-uart-loader: --fip is required")
		os.Exit(2)
	}
	fip, err := os.ReadFile(*fipPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sg200x-uart-loader: read FIP: %v\n", err)
		os.Exit(1)
	}
	if len(fip) < 4096 || string(fip[:6]) != "CVBL01" {
		fmt.Fprintf(os.Stderr, "sg200x-uart-loader: %s does not look like a CVITEK FIP (want CVBL01 at offset 0, >=4096 bytes)\n", *fipPath)
		os.Exit(1)
	}
	sum := sha256.Sum256(fip)

	if *dryRun {
		p := kermit.DefaultSendParams()
		p.CHKT = (*checksum)[0]
		s, _ := kermit.EncodePacket(0, 'S', kermit.EncodeParams(p), p)
		fmt.Printf("dry-run: fip=%s size=%d sha256=%s\n", *fipPath, len(fip), hex.EncodeToString(sum[:]))
		fmt.Printf("dry-run: port=%s baud=%d final=%d wait=%s post=%s ack=%s retries=%d\n",
			*port, *baud, *finalBaud, *waitTimeout, *post, *ackTimeout, *retries)
		fmt.Printf("dry-run: send-init=%x\n", s)
		fmt.Printf("dry-run: first slice 0/4096 would be %d data packets at %d bytes\n",
			(4096+*dataSize-1) / *dataSize, *dataSize)
		return
	}

	log, err := loader.NewLogger(*logPath, *verbose, os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sg200x-uart-loader: log: %v\n", err)
		os.Exit(1)
	}
	defer log.Close()

	log.Event("# sg200x-uart-loader start fip=%s size=%d sha256=%s port=%s baud=%d",
		*fipPath, len(fip), hex.EncodeToString(sum[:]), *port, *baud)

	l := loader.New(loader.Options{
		Port:         *port,
		Baud:         *baud,
		FIP:          fip,
		Log:          log,
		Verbose:      *verbose,
		PowerCmd:     *powerCmd,
		AckTimeout:   *ackTimeout,
		Retries:      *retries,
		WaitTimeout:  *waitTimeout,
		Post:         *post,
		FollowBaud:   *followBaud,
		Baud2:        *baud2,
		FinalBaud:    *finalBaud,
		ResetRetries: *resetRetries,
		PacketDelay:  *packetDelay,
		PacketDelay2: *packetDelay2,
		DataSize:     *dataSize,
		Checksum:     (*checksum)[0],
		URPLDelay:    *urplDelay,
		URPLMaxWait:  *urplMaxWait,
		StartDelay:   *startDelay,
		Drain:        *drain,
		CH347Recover: *ch347Recover,
	})
	if err := l.Run(); err != nil {
		log.Event("# ERROR: %v", err)
		fmt.Fprintf(os.Stderr, "sg200x-uart-loader: %v\n", err)
		os.Exit(1)
	}
	log.Event("# sg200x-uart-loader done")
}
