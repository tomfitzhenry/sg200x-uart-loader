//go:build linux && e2e

package romsim_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tomfitzhenry/sg200x-uart-loader/internal/rom"
	"github.com/tomfitzhenry/sg200x-uart-loader/internal/romsim"
)

var testBinary string

func TestMain(m *testing.M) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		fmt.Fprintln(os.Stderr, "e2e: cannot locate the repository root")
		os.Exit(1)
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	dir, err := os.MkdirTemp("", "sg200x-uart-loader-e2e-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: temp dir: %v\n", err)
		os.Exit(1)
	}
	testBinary = filepath.Join(dir, "sg200x-uart-loader")

	buildArgs := []string{"build"}
	if os.Getenv("E2E_RACE") == "1" {
		buildArgs = append(buildArgs, "-race")
	}
	buildArgs = append(buildArgs, "-o", testBinary, ".")
	build := exec.Command("go", buildArgs...)
	build.Dir = root
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		os.RemoveAll(dir)
		fmt.Fprintf(os.Stderr, "e2e: build loader: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func testFIP(n int) []byte {
	fip := make([]byte, n)
	copy(fip, "CVBL01")
	for i := 6; i < len(fip); i++ {
		fip[i] = byte(i*37 + 11)
	}
	if bytes.Contains(fip, []byte("URDL")) {
		panic("test FIP contains the URDL magic")
	}
	return fip
}

type scenario struct {
	exitCode int
	stdout   string
	stderr   string
	log      string
	sim      romsim.Result
	serveErr error
	elapsed  time.Duration
}

func runScenario(t *testing.T, fipPath string, fip []byte, cfg romsim.Config) scenario {
	t.Helper()
	return runScenarioArgs(t, fipPath, fip, cfg)
}

func runScenarioArgs(t *testing.T, fipPath string, fip []byte, cfg romsim.Config, extra ...string) scenario {
	t.Helper()

	master, slave, err := romsim.OpenPTY()
	if err != nil {
		t.Fatalf("open pty: %v", err)
	}
	defer master.Close()
	defer slave.Close()

	cfg.FIP = append([]byte(nil), fip...)
	cfg.Slave = slave
	dir := t.TempDir()
	readyFile := filepath.Join(dir, "ready")
	cfg.ReadyFile = readyFile
	sim := romsim.New(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	serveCh := make(chan error, 1)
	go func() { serveCh <- sim.Serve(ctx, master) }()

	logPath := filepath.Join(dir, "loader.log")
	args := []string{
		"--port", slave.Name(),
		"--fip", fipPath,
		"--power-cmd", "touch " + readyFile,
		"--wait-timeout", "3s",
		"--post", "300ms",
		"--log", logPath,
	}
	args = append(args, extra...)
	cmd := exec.Command(testBinary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	if err := cmd.Start(); err != nil {
		cancel()
		master.Close()
		<-serveCh
		t.Fatalf("start loader: %v", err)
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	var waitErr, serveErr error
	processDone, simDone := false, false
	timer := time.NewTimer(20 * time.Second)
	defer timer.Stop()
	for !processDone || !simDone {
		select {
		case waitErr = <-waitCh:
			processDone = true
			if !simDone {
				// Let a simulator waiting for a packet see the slave close
				// instead of waiting for its full timeout.
				slave.Close()
			}
		case serveErr = <-serveCh:
			simDone = true
		case <-timer.C:
			cmd.Process.Kill()
			cancel()
			master.Close()
			if !processDone {
				<-waitCh
			}
			if !simDone {
				<-serveCh
			}
			t.Fatalf("scenario timed out\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
		}
	}
	elapsed := time.Since(start)

	exitCode := 0
	if waitErr != nil {
		exitErr, ok := waitErr.(*exec.ExitError)
		if !ok {
			t.Fatalf("wait loader: %v", waitErr)
		}
		exitCode = exitErr.ExitCode()
	}
	logData, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatalf("read loader log: %v", readErr)
	}
	return scenario{
		exitCode: exitCode,
		stdout:   stdout.String(),
		stderr:   stderr.String(),
		log:      string(logData),
		sim:      sim.Result(),
		serveErr: serveErr,
		elapsed:  elapsed,
	}
}

func checkSlices(t *testing.T, got []romsim.Slice, fip []byte, want []romsim.Slice) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("completed %d slices, want %d: %+v", len(got), len(want), got)
	}
	served := 0
	for i, sl := range got {
		if sl.Offset != want[i].Offset || sl.Size != want[i].Size {
			t.Fatalf("slice %d = %d/%d, want %d/%d", i, sl.Offset, sl.Size, want[i].Offset, want[i].Size)
		}
		if !bytes.Equal(sl.Data, fip[sl.Offset:sl.Offset+sl.Size]) {
			t.Fatalf("slice %d/%d differs from FIP", sl.Offset, sl.Size)
		}
		served += sl.Size
	}
	if served != len(fip) {
		t.Fatalf("served %d bytes across slices, want %d", served, len(fip))
	}
}

func checkRequests(t *testing.T, got []rom.Request, want [][2]int) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("received %d requests, want %d: %+v", len(got), len(want), got)
	}
	for i, req := range got {
		if req.Offset != want[i][0] || req.Size != want[i][1] {
			t.Fatalf("request %d = %d/%d, want %d/%d", i, req.Offset, req.Size, want[i][0], want[i][1])
		}
	}
}

func checkEarlyPrompt(t *testing.T, sc scenario) {
	t.Helper()
	if !strings.Contains(sc.log, romsim.EarlyBannerMarker) {
		t.Fatalf("loader log is missing the early-banner marker %q\nlog:\n%s", romsim.EarlyBannerMarker, sc.log)
	}
	if sc.sim.EarlyMagic {
		t.Fatal("loader answered the early URPL")
	}
}

func wantSlices() []romsim.Slice {
	return []romsim.Slice{
		{Offset: 0, Size: 4096},
		{Offset: 4096, Size: 4096},
		{Offset: 8192, Size: 1808},
	}
}

func TestLoaderAgainstSimulatedROM(t *testing.T) {
	fip := testFIP(10000)
	fipPath := filepath.Join(t.TempDir(), "fip.bin")
	if err := os.WriteFile(fipPath, fip, 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("normal", func(t *testing.T) {
		sc := runScenario(t, fipPath, fip, romsim.Config{
			EarlyPrompt:      true,
			BaudSwitchMarker: true,
		})
		if sc.serveErr != nil {
			t.Fatalf("simulator: %v\nloader stderr:\n%s\nloader log:\n%s", sc.serveErr, sc.stderr, sc.log)
		}
		if sc.exitCode != 0 {
			t.Fatalf("loader exit %d\nstdout:\n%s\nstderr:\n%s\nlog:\n%s", sc.exitCode, sc.stdout, sc.stderr, sc.log)
		}
		checkEarlyPrompt(t, sc)
		if sc.sim.MagicCount != 1 {
			t.Fatalf("URDL count = %d, want 1", sc.sim.MagicCount)
		}
		checkRequests(t, sc.sim.Requests, [][2]int{
			{0, 4096},
			{4096, 4096},
			{8192, 1808},
		})
		checkSlices(t, sc.sim.Slices, fip, wantSlices())
		if sc.sim.SendInitCount != 3 {
			t.Fatalf("Send-Init count = %d, want 3", sc.sim.SendInitCount)
		}
		if !sc.sim.FinalBaudObserved {
			t.Fatal("slave tty never reached 1500000 baud")
		}
		for _, want := range []string{
			"FIP complete: 10000/10000 bytes served",
			"HANDOFF",
		} {
			if !strings.Contains(sc.log, want) {
				t.Fatalf("loader log is missing %q\nlog:\n%s", want, sc.log)
			}
		}
		t.Logf("normal scenario completed in %s", sc.elapsed)
	})

	t.Run("race-valid-boot0", func(t *testing.T) {
		sc := runScenarioArgs(t, fipPath, fip, romsim.Config{
			Race:             true,
			BaudSwitchMarker: true,
		}, "--race")
		if sc.serveErr != nil {
			t.Fatalf("simulator: %v\nloader stderr:\n%s\nloader log:\n%s", sc.serveErr, sc.stderr, sc.log)
		}
		if sc.exitCode != 0 {
			t.Fatalf("loader exit %d\nstdout:\n%s\nstderr:\n%s\nlog:\n%s", sc.exitCode, sc.stdout, sc.stderr, sc.log)
		}
		if sc.sim.MagicCount != 1 {
			t.Fatalf("URDL count = %d, want 1", sc.sim.MagicCount)
		}
		checkRequests(t, sc.sim.Requests, [][2]int{
			{0, 4096},
			{4096, 4096},
			{8192, 1808},
		})
		checkSlices(t, sc.sim.Slices, fip, wantSlices())
		if !sc.sim.FinalBaudObserved {
			t.Fatal("slave tty never reached 1500000 baud")
		}
		if !strings.Contains(sc.log, "FIP complete: 10000/10000 bytes served") {
			t.Fatalf("loader log is missing the completion line\nlog:\n%s", sc.log)
		}
		t.Logf("race scenario completed in %s", sc.elapsed)
	})

	t.Run("mid-transfer-reset", func(t *testing.T) {
		sc := runScenario(t, fipPath, fip, romsim.Config{
			EarlyPrompt:           true,
			ResetAfterDataPackets: 3,
			BaudSwitchMarker:      true,
			// Keep re-prompts slower than the loader's fixed 100 ms URPL
			// delay so the test observes one URDL per simulated prompt.
			ResetPromptInterval: 300 * time.Millisecond,
		})
		if sc.serveErr != nil {
			t.Fatalf("simulator: %v\nloader stderr:\n%s\nloader log:\n%s", sc.serveErr, sc.stderr, sc.log)
		}
		if sc.exitCode != 0 {
			t.Fatalf("loader exit %d\nstdout:\n%s\nstderr:\n%s\nlog:\n%s", sc.exitCode, sc.stdout, sc.stderr, sc.log)
		}
		checkEarlyPrompt(t, sc)
		if sc.sim.Resets != 1 {
			t.Fatalf("reset count = %d, want 1", sc.sim.Resets)
		}
		if sc.sim.MagicCount != 2 {
			t.Fatalf("URDL count = %d, want 2 (one per prompt)", sc.sim.MagicCount)
		}
		checkRequests(t, sc.sim.Requests, [][2]int{
			{0, 4096},
			{0, 4096},
			{4096, 4096},
			{8192, 1808},
		})
		checkSlices(t, sc.sim.Slices, fip, wantSlices())
		if sc.sim.SendInitCount != 4 {
			t.Fatalf("Send-Init count = %d, want 4 (three slices plus the reset retry)", sc.sim.SendInitCount)
		}
		if !sc.sim.FinalBaudObserved {
			t.Fatal("slave tty never reached 1500000 baud")
		}
		if !strings.Contains(sc.log, "FIP complete: 10000/10000 bytes served") {
			t.Fatalf("loader log is missing the completion line\nlog:\n%s", sc.log)
		}
		t.Logf("reset scenario completed in %s", sc.elapsed)
	})

	t.Run("invalid-request", func(t *testing.T) {
		sc := runScenario(t, fipPath, fip, romsim.Config{
			EarlyPrompt:    true,
			InvalidRequest: true,
		})
		if sc.serveErr != nil {
			t.Fatalf("simulator: %v\nloader stderr:\n%s\nloader log:\n%s", sc.serveErr, sc.stderr, sc.log)
		}
		if sc.exitCode != 1 {
			t.Fatalf("loader exit %d, want 1\nstdout:\n%s\nstderr:\n%s\nlog:\n%s", sc.exitCode, sc.stdout, sc.stderr, sc.log)
		}
		checkEarlyPrompt(t, sc)
		if sc.sim.SendInitCount != 0 {
			t.Fatalf("loader sent %d Send-Init packets for an invalid request", sc.sim.SendInitCount)
		}
		if len(sc.sim.Slices) != 0 {
			t.Fatalf("loader completed %d slices for an invalid request", len(sc.sim.Slices))
		}
		for _, want := range []string{"bad ROM request", "exceeds FIP size"} {
			if !strings.Contains(sc.stderr, want) {
				t.Fatalf("loader stderr is missing %q\nstderr:\n%s\nstdout:\n%s\nlog:\n%s\nsim: %+v", want, sc.stderr, sc.stdout, sc.log, sc.sim)
			}
		}
		t.Logf("invalid-request scenario completed in %s", sc.elapsed)
	})
}
