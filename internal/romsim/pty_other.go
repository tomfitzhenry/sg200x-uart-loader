//go:build !linux

package romsim

import (
	"errors"
	"os"
)

var errPTYUnsupported = errors.New("romsim: PTY simulation is only implemented on linux")

// OpenPTY is unavailable outside linux.
func OpenPTY() (*os.File, *os.File, error) {
	return nil, nil, errPTYUnsupported
}

func slaveAtBaud(*os.File, int) (bool, error) {
	return false, errPTYUnsupported
}
