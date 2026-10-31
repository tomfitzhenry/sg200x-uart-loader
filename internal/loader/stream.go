package loader

import (
	"bytes"
	"sync/atomic"

	"github.com/tomfitzhenry/sg200x-uart-loader/internal/kermit"
)

type EventKind int

const (
	EventText EventKind = iota
	EventPacket
)

// Event is either a chunk of console text or one decoded Kermit packet.
type Event struct {
	Kind   EventKind
	Text   []byte
	Packet kermit.Packet
	Raw    []byte
}

// StreamParser splits the serial byte stream into console text and Kermit
// packets.  It tolerates text interleaved with binary packets.
type StreamParser struct {
	buf  []byte
	chkt atomic.Int32 // kermit checksum type: '1' or '3'
}

// SetChecksumType selects the checksum type used to decode incoming packets.
// The parser still tries the other type once, so an ACK to our Send-Init that
// refuses CRC-16 is decoded rather than discarded.
func (p *StreamParser) SetChecksumType(chkt byte) {
	if chkt != '3' {
		chkt = '1'
	}
	p.chkt.Store(int32(chkt))
}

func (p *StreamParser) checksumType() byte {
	c := byte(p.chkt.Load())
	if c != '3' {
		return '1'
	}
	return c
}

func (p *StreamParser) decode(raw []byte) (kermit.Packet, byte, error) {
	chkt := p.checksumType()
	pkt, err := kermit.DecodePacketType(raw, chkt)
	if err == nil {
		return pkt, chkt, nil
	}
	other := byte('1')
	if chkt == '1' {
		other = '3'
	}
	pkt2, err2 := kermit.DecodePacketType(raw, other)
	if err2 != nil {
		return kermit.Packet{}, chkt, err
	}
	p.SetChecksumType(other)
	return pkt2, other, nil
}

func (p *StreamParser) Feed(data []byte) []Event {
	p.buf = append(p.buf, data...)
	var events []Event
	for len(p.buf) > 0 {
		if p.buf[0] != kermit.SOH {
			i := bytes.IndexByte(p.buf, kermit.SOH)
			if i < 0 {
				events = append(events, Event{Kind: EventText, Text: append([]byte(nil), p.buf...)})
				p.buf = nil
				break
			}
			if i > 0 {
				events = append(events, Event{Kind: EventText, Text: append([]byte(nil), p.buf[:i]...)})
				p.buf = p.buf[i:]
			}
			continue
		}
		if len(p.buf) < 2 {
			break
		}
		lenVal := int(p.buf[1]) - kermit.SP
		if lenVal == 0 {
			// Long packet.  E-Kermit's ACKs to us are short; if we ever
			// see a long packet, drop the SOH and resynchronise rather
			// than mis-parsing it as console text.
			events = append(events, Event{Kind: EventText, Text: []byte{kermit.SOH}})
			p.buf = p.buf[1:]
			continue
		}
		if lenVal < 3 || lenVal > 94 {
			events = append(events, Event{Kind: EventText, Text: []byte{kermit.SOH}})
			p.buf = p.buf[1:]
			continue
		}
		total := 2 + lenVal + 1
		if len(p.buf) < total {
			break
		}
		if p.buf[total-1] != kermit.CR && p.buf[total-1] != '\n' {
			events = append(events, Event{Kind: EventText, Text: []byte{kermit.SOH}})
			p.buf = p.buf[1:]
			continue
		}
		pkt, _, err := p.decode(p.buf[:total])
		if err != nil {
			events = append(events, Event{Kind: EventText, Text: []byte{kermit.SOH}})
			p.buf = p.buf[1:]
			continue
		}
		events = append(events, Event{Kind: EventPacket, Packet: pkt, Raw: pkt.Raw})
		p.buf = p.buf[total:]
	}
	return events
}
