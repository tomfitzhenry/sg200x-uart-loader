package loader

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// Logger writes a detailed timestamped protocol log and concise progress to
// stdout.  It is safe for the reader goroutine and the sender to share.
type Logger struct {
	mu      sync.Mutex
	w       *bufio.Writer
	file    *os.File
	out     io.Writer
	verbose bool
}

func NewLogger(path string, verbose bool, out io.Writer) (*Logger, error) {
	if out == nil {
		out = os.Stdout
	}
	l := &Logger{out: out, verbose: verbose}
	if path != "" {
		f, err := os.Create(path)
		if err != nil {
			return nil, err
		}
		l.file = f
		l.w = bufio.NewWriter(f)
	}
	return l, nil
}

func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.w != nil {
		l.w.Flush()
	}
	if l.file != nil {
		err := l.file.Close()
		l.file = nil
		l.w = nil
		return err
	}
	return nil
}

func (l *Logger) line(format string, args ...any) {
	if l.w == nil {
		return
	}
	fmt.Fprintf(l.w, format+"\n", args...)
	l.w.Flush()
}

func ts() string { return fmt.Sprintf("%.6f", float64(time.Now().UnixNano())/1e9) }

func hexdump(b []byte) string {
	const max = 512
	if len(b) > max {
		return fmt.Sprintf("%x... (%d bytes)", b[:max], len(b))
	}
	return fmt.Sprintf("%x", b)
}

func printable(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		if c >= 0x20 && c < 0x7f {
			sb.WriteByte(c)
		} else if c == '\r' || c == '\n' || c == '\t' {
			sb.WriteByte(c)
		} else {
			sb.WriteByte('.')
		}
	}
	return sb.String()
}

func (l *Logger) RX(b []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.line("%s RX %d %s |%s|", ts(), len(b), hexdump(b), printable(b))
}

func (l *Logger) TX(b []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.line("%s TX %d %s", ts(), len(b), hexdump(b))
}

func (l *Logger) Event(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.line("%s %s", ts(), fmt.Sprintf(format, args...))
}

func (l *Logger) Console(b []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.line("%s CONSOLE %d %s |%s|", ts(), len(b), hexdump(b), printable(b))
	// Echo board console to the operator.
	if l.out != nil {
		fmt.Fprint(l.out, string(b))
	}
}

func (l *Logger) Progress(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.line("%s PROGRESS %s", ts(), msg)
	if l.out != nil {
		fmt.Fprintf(l.out, "sg200x: %s\n", msg)
	}
}

func (l *Logger) Verbose(format string, args ...any) {
	if !l.verbose {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.line("%s DEBUG %s", ts(), fmt.Sprintf(format, args...))
}

func (l *Logger) Packet(dir string, seq, typ byte, data []byte, raw []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.line("%s KERMIT %s type=%c seq=%d data=%d raw=%s text=%q",
		ts(), dir, typ, seq, len(data), hexdump(raw), printable(data))
}
