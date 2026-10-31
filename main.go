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
		showVersion = flag.Bool("version", false, "print the version and exit")
		port        = flag.String("port", "/dev/ttyACM0", "serial port connected to the Oz64 UART")
		fipPath     = flag.String("fip", "", "FIP image to serve")
		logPath     = flag.String("log", "", "write a detailed hex/protocol log here")
		powerCmd    = flag.String("power-cmd", "", "shell command to run before waiting for URPL")
		verbose     = flag.Bool("verbose", false, "verbose protocol logging")
		dryRun      = flag.Bool("dry-run", false, "validate inputs and print the plan; do not open the port")
		waitTimeout = flag.Duration("wait-timeout", 180*time.Second, "how long to wait for URPL/EK")
		post        = flag.Duration("post", 30*time.Second, "console capture time after the last FIP slice")
		race        = flag.Bool("race", false, "answer the first URPL immediately (valid boot0 race mode)")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("sg200x-uart-loader %s\n", version)
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
		p.CHKT = loader.KermitChecksum
		s, _ := kermit.EncodePacket(0, 'S', kermit.EncodeParams(p), p)
		fmt.Printf("dry-run: fip=%s size=%d sha256=%s\n", *fipPath, len(fip), hex.EncodeToString(sum[:]))
		fmt.Printf("dry-run: port=%s baud=%d fsbl=%d wait=%s post=%s data=%d checksum=%c\n",
			*port, loader.BootROMBaud, loader.FSBLBaud, *waitTimeout, *post, loader.DataSize, loader.KermitChecksum)
		fmt.Printf("dry-run: send-init=%x\n", s)
		fmt.Printf("dry-run: first slice 0/4096 would be %d data packets at %d bytes\n",
			(4096+loader.DataSize-1)/loader.DataSize, loader.DataSize)
		return
	}

	log, err := loader.NewLogger(*logPath, *verbose, os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sg200x-uart-loader: log: %v\n", err)
		os.Exit(1)
	}
	defer log.Close()

	log.Event("# sg200x-uart-loader start fip=%s size=%d sha256=%s port=%s baud=%d",
		*fipPath, len(fip), hex.EncodeToString(sum[:]), *port, loader.BootROMBaud)

	l := loader.New(loader.Options{
		Port:        *port,
		FIP:         fip,
		Log:         log,
		Verbose:     *verbose,
		PowerCmd:    *powerCmd,
		WaitTimeout: *waitTimeout,
		Post:        *post,
		Race:        *race,
	})
	if err := l.Run(); err != nil {
		log.Event("# ERROR: %v", err)
		fmt.Fprintf(os.Stderr, "sg200x-uart-loader: %v\n", err)
		os.Exit(1)
	}
	log.Event("# sg200x-uart-loader done")
}
