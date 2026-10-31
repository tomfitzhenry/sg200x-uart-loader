//go:build linux

// Package romsim simulates the SG200x/CV181x-family BootROM and its E-Kermit
// receiver over a serial stream.  It is used by end-to-end tests of the real
// loader and can also be driven from other local harnesses.
package romsim

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// OpenPTY opens a new pseudo-terminal pair.  The master is non-blocking so it
// can be used with os.File read deadlines; the slave is put into raw mode so
// writes to the master are not echoed back.
func OpenPTY() (master *os.File, slave *os.File, err error) {
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open /dev/ptmx: %w", err)
	}
	closeFD := func() {
		if fd >= 0 {
			unix.Close(fd)
		}
	}
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		closeFD()
		return nil, nil, fmt.Errorf("unlock pty: %w", err)
	}
	n, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		closeFD()
		return nil, nil, fmt.Errorf("get pty number: %w", err)
	}
	path := fmt.Sprintf("/dev/pts/%d", n)
	sfd, err := unix.Open(path, unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		closeFD()
		return nil, nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := setRaw(sfd); err != nil {
		unix.Close(sfd)
		closeFD()
		return nil, nil, fmt.Errorf("configure %s: %w", path, err)
	}
	master = os.NewFile(uintptr(fd), "/dev/ptmx")
	slave = os.NewFile(uintptr(sfd), path)
	return master, slave, nil
}

func setRaw(fd int) error {
	t, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return err
	}
	t.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP |
		unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON | unix.IXOFF | unix.IXANY
	t.Oflag &^= unix.OPOST
	t.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	t.Cflag &^= unix.CSIZE | unix.PARENB | unix.PARODD | unix.CSTOPB | unix.CRTSCTS
	t.Cflag |= unix.CS8 | unix.CREAD | unix.CLOCAL
	t.Cc[unix.VMIN] = 0
	t.Cc[unix.VTIME] = 0
	return unix.IoctlSetTermios(fd, unix.TCSETS, t)
}

// slaveAtBaud reports whether the slave tty is currently configured for baud.
func slaveAtBaud(f *os.File, baud int) (bool, error) {
	if f == nil {
		return false, fmt.Errorf("no slave tty")
	}
	t, err := unix.IoctlGetTermios(int(f.Fd()), unix.TCGETS)
	if err != nil {
		return false, err
	}
	got := termiosBaud(t.Cflag & unix.CBAUD)
	return got == baud, nil
}

func termiosBaud(speed uint32) int {
	switch speed {
	case unix.B9600:
		return 9600
	case unix.B19200:
		return 19200
	case unix.B38400:
		return 38400
	case unix.B57600:
		return 57600
	case unix.B115200:
		return 115200
	case unix.B1500000:
		return 1500000
	case unix.B3000000:
		return 3000000
	default:
		return 0
	}
}
