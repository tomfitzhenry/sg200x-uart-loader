//go:build linux

package serial

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Reauthorize toggles the USB authorized flag of the device backing a tty.
// Some CH347 firmware stops transmitting after a baud-rate switch until the
// USB interface is re-enumerated; writing 0 then 1 to the sysfs authorized
// attribute recovers it without touching the UART data path.
func Reauthorize(tty string) error {
	name := filepath.Base(tty)
	dev, err := filepath.EvalSymlinks(filepath.Join("/sys/class/tty", name, "device"))
	if err != nil {
		return fmt.Errorf("resolve %s sysfs: %w", tty, err)
	}

	// Walk up from the interface (e.g. .../5-1/5-1:1.0) to the USB device
	// directory (.../5-1), which is the one that owns "authorized".
	dir := dev
	for i := 0; i < 5; i++ {
		base := filepath.Base(dir)
		if strings.Contains(base, "-") && !strings.Contains(base, ":") {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	auth := filepath.Join(dir, "authorized")
	if _, err := os.Stat(auth); err != nil {
		return fmt.Errorf("find USB authorized for %s: %w", tty, err)
	}
	if err := os.WriteFile(auth, []byte("0"), 0o644); err != nil {
		return fmt.Errorf("de-authorize %s: %w", dir, err)
	}
	time.Sleep(time.Second)
	if err := os.WriteFile(auth, []byte("1"), 0o644); err != nil {
		return fmt.Errorf("re-authorize %s: %w", dir, err)
	}
	return nil
}
