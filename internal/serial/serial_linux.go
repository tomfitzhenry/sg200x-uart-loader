//go:build linux

// Package serial provides a small raw-termios serial port with poll-based
// read timeouts.  It deliberately uses no cgo so the loader cross-compiles to
// a static linux/arm64 binary.
package serial

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

var ErrTimeout = errors.New("serial read timeout")

type Port struct {
	mu   sync.Mutex
	fd   int
	path string
}

func (p *Port) getFD() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.fd
}

func baudConst(baud int) (uint32, error) {
	switch baud {
	case 9600:
		return unix.B9600, nil
	case 19200:
		return unix.B19200, nil
	case 38400:
		return unix.B38400, nil
	case 57600:
		return unix.B57600, nil
	case 115200:
		return unix.B115200, nil
	case 1500000:
		return unix.B1500000, nil
	case 3000000:
		return unix.B3000000, nil
	default:
		return 0, fmt.Errorf("unsupported baud rate %d", baud)
	}
}

// Open opens path and configures it for raw 8N1, no flow control, no echo.
func Open(path string, baud int) (*Port, error) {
	speed, err := baudConst(baud)
	if err != nil {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NOCTTY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	t, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("tcgetattr %s: %w", path, err)
	}

	t.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP |
		unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON | unix.IXOFF | unix.IXANY
	t.Oflag &^= unix.OPOST
	t.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	t.Cflag &^= unix.CSIZE | unix.PARENB | unix.PARODD | unix.CSTOPB | unix.CRTSCTS
	t.Cflag |= unix.CS8 | unix.CREAD | unix.CLOCAL
	t.Cflag = (t.Cflag &^ unix.CBAUD) | speed
	t.Ispeed = speed
	t.Ospeed = speed
	t.Cc[unix.VMIN] = 0
	t.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, t); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("tcsetattr %s: %w", path, err)
	}
	if err := unix.IoctlSetInt(fd, unix.TCFLSH, unix.TCIOFLUSH); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("tcflush %s: %w", path, err)
	}
	return &Port{fd: fd, path: path}, nil
}

func (p *Port) Close() error {
	p.mu.Lock()
	fd := p.fd
	p.fd = -1
	p.mu.Unlock()
	if fd < 0 {
		return nil
	}
	return unix.Close(fd)
}

// SetBaud reconfigures the line speed, keeping the raw settings.
func (p *Port) SetBaud(baud int) error {
	speed, err := baudConst(baud)
	if err != nil {
		return err
	}
	fd := p.getFD()
	if fd < 0 {
		return fmt.Errorf("serial: port is closed")
	}
	t, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return err
	}
	t.Cflag = (t.Cflag &^ unix.CBAUD) | speed
	t.Ispeed = speed
	t.Ospeed = speed
	return unix.IoctlSetTermios(fd, unix.TCSETS, t)
}

func (p *Port) wait(events int16, timeout time.Duration) error {
	fd := p.getFD()
	if fd < 0 {
		return fmt.Errorf("serial: port is closed")
	}
	deadline := time.Now().Add(timeout)
	for {
		remain := time.Until(deadline)
		if remain <= 0 {
			return ErrTimeout
		}
		ms := int(remain / time.Millisecond)
		if ms < 1 {
			ms = 1
		}
		fds := []unix.PollFd{{Fd: int32(fd), Events: events}}
		n, err := unix.Poll(fds, ms)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrTimeout
		}
		if fds[0].Revents&(events|unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
			return nil
		}
	}
}

// Read reads up to len(buf) bytes, returning ErrTimeout if none arrive.
func (p *Port) Read(buf []byte, timeout time.Duration) (int, error) {
	for {
		if err := p.wait(unix.POLLIN, timeout); err != nil {
			return 0, err
		}
		fd := p.getFD()
		if fd < 0 {
			return 0, fmt.Errorf("serial: port is closed")
		}
		n, err := unix.Read(fd, buf)
		if err == unix.EAGAIN || err == unix.EWOULDBLOCK {
			continue
		}
		if err == unix.EINTR {
			continue
		}
		return n, err
	}
}

// Write writes all of b, waiting for the port to become writable if needed.
func (p *Port) Write(b []byte) error {
	fd := p.getFD()
	if fd < 0 {
		return fmt.Errorf("serial: port is closed")
	}
	for len(b) > 0 {
		n, err := unix.Write(fd, b)
		if err == unix.EAGAIN || err == unix.EWOULDBLOCK {
			if err := p.wait(unix.POLLOUT, 5*time.Second); err != nil {
				return err
			}
			continue
		}
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		b = b[n:]
	}
	return nil
}
