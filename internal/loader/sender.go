package loader

import (
	"errors"
	"fmt"
	"time"

	"github.com/tomfitzhenry/sg200x-uart-loader/internal/kermit"
)

var errACKTimeout = errors.New("timeout waiting for Kermit ACK")
var errBoardReset = errors.New("board reset during Kermit transfer")

type packetWriter interface {
	Write([]byte) error
}

type sender struct {
	port        packetWriter
	events      <-chan Event
	log         *Logger
	onText      func([]byte)
	abort       <-chan struct{}
	timeout     time.Duration
	retries     int
	delay       time.Duration
	dataSize    int
	checksum    byte
	setChecksum func(byte)
}

type negotiated struct {
	params  kermit.Params
	maxData int
	ebq     bool
	ebqCh   byte
}

func newNegotiated(p kermit.Params, wantData int) negotiated {
	n := negotiated{params: p}
	n.maxData = 88
	if wantData > 0 && wantData < n.maxData {
		n.maxData = wantData
	}
	if lim := p.MaxPacket - 3; lim < n.maxData {
		n.maxData = lim
	}
	if n.maxData < 16 {
		n.maxData = 16
	}
	// Our advertised QBIN is 'Y', so high-bit data is sent transparently
	// and the receiver does not use 8th-bit prefixing.  Keep ebq off unless
	// a future change advertises a parity-quoting character.
	return n
}

func (s *sender) readPacket(timeout time.Duration) (kermit.Packet, error) {
	deadline := time.Now().Add(timeout)
	for {
		remain := time.Until(deadline)
		if remain <= 0 {
			return kermit.Packet{}, errACKTimeout
		}
		select {
		case <-s.abort:
			return kermit.Packet{}, errBoardReset
		case ev := <-s.events:
			if ev.Kind == EventText {
				if s.onText != nil {
					s.onText(ev.Text)
				}
				continue
			}
			return ev.Packet, nil
		case <-time.After(remain):
			return kermit.Packet{}, errACKTimeout
		}
	}
}

// exchange sends one packet and waits for its ACK, retransmitting on NAK or
// timeout.  data must already be escaped (except for the Send-Init packet).
func (s *sender) exchange(seq byte, typ byte, data []byte, n negotiated) (kermit.Packet, error) {
	raw, err := kermit.EncodePacket(seq, typ, data, n.params)
	if err != nil {
		return kermit.Packet{}, err
	}
	var last string
	for attempt := 0; attempt <= s.retries; attempt++ {
		if err := s.port.Write(raw); err != nil {
			return kermit.Packet{}, fmt.Errorf("serial write: %w", err)
		}
		s.log.TX(raw)
		s.log.Packet("TX", seq, typ, data, raw)
		if attempt == 0 {
			s.log.Verbose("sent %c seq=%d, waiting up to %s", typ, seq, s.timeout)
		} else {
			s.log.Verbose("retransmit %c seq=%d (attempt %d/%d)", typ, seq, attempt, s.retries)
		}
		pkt, err := s.readPacket(s.timeout)
		if err != nil {
			if errors.Is(err, errBoardReset) {
				return kermit.Packet{}, errBoardReset
			}
			last = "timeout"
			s.log.Event("KERMIT retransmit %c seq=%d: %s", typ, seq, last)
			continue
		}
		s.log.Packet("RX", pkt.Seq, pkt.Type, pkt.Data, pkt.Raw)
		switch pkt.Type {
		case 'Y':
			if pkt.Seq == seq {
				if s.delay > 0 {
					time.Sleep(s.delay)
				}
				return pkt, nil
			}
			// Stale or duplicate ACK; keep waiting for ours.
			s.log.Verbose("ignoring ACK seq=%d while waiting for %d", pkt.Seq, seq)
			continue
		case 'N':
			last = fmt.Sprintf("NAK seq=%d raw=%x", pkt.Seq, pkt.Raw)
			s.log.Event("KERMIT NAK for %c seq=%d (raw=%x)", typ, pkt.Seq, pkt.Raw)
			continue
		case 'E':
			return kermit.Packet{}, fmt.Errorf("Kermit error packet seq=%d: %q (raw=%x)", pkt.Seq, string(pkt.Data), pkt.Raw)
		case 'S':
			// Some receivers answer our S with their own S.  Treat its
			// parameters as the negotiation and ACK it.  E-Kermit 1.8
			// answers with Y, so this is only a fallback.
			if typ == 'S' && pkt.Seq == 0 {
				return pkt, nil
			}
			s.log.Verbose("ignoring unexpected %c seq=%d", pkt.Type, pkt.Seq)
			continue
		default:
			s.log.Verbose("ignoring unexpected packet type %c seq=%d", pkt.Type, pkt.Seq)
			continue
		}
	}
	return kermit.Packet{}, fmt.Errorf("%w for %c seq=%d after %d tries (%s)", errACKTimeout, typ, seq, s.retries+1, last)
}

// sendFile sends data as a Kermit file named name.
func (s *sender) sendFile(name string, data []byte) error {
	send := kermit.DefaultSendParams()
	if s.checksum == '3' {
		send.CHKT = '3'
	}
	n := newNegotiated(send, s.dataSize)
	if s.setChecksum != nil {
		s.setChecksum(send.CHKT)
	}

	// Send-Init.
	spkt, err := s.exchange(0, 'S', kermit.EncodeParams(send), n)
	if err != nil {
		return fmt.Errorf("send-init: %w", err)
	}
	if spkt.Type == 'S' {
		// Receiver sent its own Send-Init; acknowledge with ours.
		ack, err := kermit.EncodePacket(0, 'Y', kermit.EncodeParams(send), n.params)
		if err != nil {
			return err
		}
		if err := s.port.Write(ack); err != nil {
			return err
		}
		s.log.TX(ack)
		s.log.Packet("TX", 0, 'Y', kermit.EncodeParams(send), ack)
	}
	rp, err := kermit.DecodeParams(spkt.Data)
	if err != nil && spkt.Type == 'Y' {
		s.log.Event("KERMIT warning: unusable Send-Init ACK (%v); keeping defaults", err)
	} else if err == nil {
		send.MaxPacket = rp.MaxPacket
		send.EOL = rp.EOL
		send.CHKT = rp.CHKT
		if rp.Window > 0 {
			send.Window = 1
		}
		n = newNegotiated(send, s.dataSize)
		if s.setChecksum != nil {
			s.setChecksum(send.CHKT)
		}
		s.log.Event("KERMIT negotiated max_packet=%d chkt=%c eol=0x%02x window=1 max_data=%d",
			send.MaxPacket, send.CHKT, send.EOL, n.maxData)
	}

	seq := byte(1)
	encName, used, err := kermit.EncodeData([]byte(name), n.maxData, n.params, n.ebq, n.ebqCh)
	if err != nil || used != len(name) {
		return fmt.Errorf("encode filename %q: %v (used %d)", name, err, used)
	}
	if _, err := s.exchange(seq, 'F', encName, n); err != nil {
		return fmt.Errorf("filename packet: %w", err)
	}
	seq = (seq + 1) & 63

	pos := 0
	for pos < len(data) {
		enc, used, err := kermit.EncodeData(data[pos:], n.maxData, n.params, n.ebq, n.ebqCh)
		if err != nil {
			return err
		}
		if used == 0 {
			return fmt.Errorf("encoder made no progress at offset %d", pos)
		}
		if _, err := s.exchange(seq, 'D', enc, n); err != nil {
			return fmt.Errorf("data packet at offset %d: %w", pos, err)
		}
		pos += used
		seq = (seq + 1) & 63
		s.log.Verbose("data %d/%d bytes (%d%%)", pos, len(data), pos*100/len(data))
	}

	if _, err := s.exchange(seq, 'Z', nil, n); err != nil {
		return fmt.Errorf("EOF packet: %w", err)
	}
	seq = (seq + 1) & 63
	if _, err := s.exchange(seq, 'B', nil, n); err != nil {
		return fmt.Errorf("EOT packet: %w", err)
	}
	return nil
}
