// Package rom parses the SG200x-family BootROM's UART download prompts.
package rom

import (
	"fmt"
	"regexp"
	"strconv"
)

var requestRE = regexp.MustCompile(`EK/([0-9]+)/([0-9]+)/UL_START`)

// Request is one ROM slice request: the ROM wants fip[offset:offset+size].
type Request struct {
	Offset int
	Size   int
}

// FindRequests returns every complete EK/<offset>/<size>/UL_START request in
// text, in order of appearance.
func FindRequests(text []byte) []Request {
	matches := requestRE.FindAllSubmatch(text, -1)
	out := make([]Request, 0, len(matches))
	for _, m := range matches {
		off, err1 := strconv.Atoi(string(m[1]))
		size, err2 := strconv.Atoi(string(m[2]))
		if err1 != nil || err2 != nil {
			continue
		}
		out = append(out, Request{Offset: off, Size: size})
	}
	return out
}

// LastRequest returns the final request in text, if any.
func LastRequest(text []byte) (Request, bool) {
	rs := FindRequests(text)
	if len(rs) == 0 {
		return Request{}, false
	}
	return rs[len(rs)-1], true
}

// Validate checks a request against a FIP of length n.
func (r Request) Validate(n int) error {
	if r.Offset < 0 {
		return fmt.Errorf("negative offset %d", r.Offset)
	}
	if r.Size <= 0 {
		return fmt.Errorf("non-positive size %d", r.Size)
	}
	if r.Offset > n || r.Offset+r.Size > n {
		return fmt.Errorf("request %d+%d exceeds FIP size %d", r.Offset, r.Size, n)
	}
	return nil
}
