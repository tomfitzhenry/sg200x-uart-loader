package loader

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"github.com/tomfitzhenry/sg200x-uart-loader/internal/kermit"
)

// fakeReceiver implements enough of E-Kermit 1.8's receive state machine to
// validate the sender end-to-end without hardware.
type fakeReceiver struct {
	t      *testing.T
	events chan Event
	log    *Logger

	out    kermit.Params
	next   byte
	file   bytes.Buffer
	done   chan struct{}
	failed chan error
}

func newFakeReceiver(t *testing.T) *fakeReceiver {
	log, err := NewLogger("", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := kermit.DefaultSendParams()
	out.QBIN = 'Y' // E-Kermit's default when parity is none
	out.REPT = '~'
	out.LongMax = 4096
	return &fakeReceiver{
		t:      t,
		events: make(chan Event, 16),
		log:    log,
		out:    out,
		next:   0,
		done:   make(chan struct{}),
		failed: make(chan error, 1),
	}
}

func (r *fakeReceiver) fail(err error) {
	select {
	case r.failed <- err:
	default:
	}
}

func (r *fakeReceiver) ack(seq byte, data []byte) {
	raw, err := kermit.EncodePacket(seq, 'Y', data, r.out)
	if err != nil {
		r.fail(err)
		return
	}
	r.events <- Event{Kind: EventPacket, Packet: kermit.Packet{Seq: seq, Type: 'Y', Data: data, Raw: raw}}
}

func (r *fakeReceiver) Write(p []byte) error {
	pkt, err := kermit.DecodePacket(p)
	if err != nil {
		r.fail(err)
		return nil
	}
	if pkt.Seq != r.next {
		r.fail(fmt.Errorf("seq %d, want %d", pkt.Seq, r.next))
		return nil
	}
	switch pkt.Type {
	case 'S':
		got, err := kermit.DecodeParams(pkt.Data)
		if err != nil {
			r.fail(err)
			return nil
		}
		if got.MaxPacket != 94 || got.CHKT != '1' || got.QCTL != '#' || got.QBIN != 'Y' || got.REPT != '~' || got.Window != 1 {
			r.fail(fmt.Errorf("bad send-init: %+v", got))
			return nil
		}
		r.ack(0, kermit.EncodeParams(r.out))
	case 'F':
		r.ack(pkt.Seq, nil)
	case 'D':
		dec := kermit.DecodeData(pkt.Data, r.out, false, 0)
		r.file.Write(dec)
		r.ack(pkt.Seq, nil)
	case 'Z':
		r.ack(pkt.Seq, nil)
	case 'B':
		r.ack(pkt.Seq, nil)
		close(r.done)
	default:
		r.fail(fmt.Errorf("unexpected type %c", pkt.Type))
		return nil
	}
	r.next = (r.next + 1) & 63
	return nil
}

func TestSenderAgainstFakeReceiver(t *testing.T) {
	r := newFakeReceiver(t)
	s := &sender{
		port:    r,
		events:  r.events,
		log:     r.log,
		timeout: 2 * time.Second,
		retries: 3,
	}
	payload := make([]byte, 4096)
	for i := range payload {
		payload[i] = byte(i * 7)
	}
	payload[0] = 0
	payload[1] = '\r'
	payload[2] = '#'

	errCh := make(chan error, 1)
	go func() { errCh <- s.sendFile("fip.bin", payload) }()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case err := <-r.failed:
		t.Fatal(err)
	case <-time.After(10 * time.Second):
		t.Fatal("timeout")
	}
	select {
	case <-r.done:
	default:
		t.Fatal("receiver did not see EOT")
	}
	if !bytes.Equal(r.file.Bytes(), payload) {
		t.Fatalf("received %d bytes, want %d", r.file.Len(), len(payload))
	}
}
