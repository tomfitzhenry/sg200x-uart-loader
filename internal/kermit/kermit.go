// Package kermit implements the subset of the Kermit protocol needed to act
// as a sender to the SG200x-family BootROM's E-Kermit 1.8 receiver.
package kermit

import (
	"errors"
	"fmt"
)

const (
	SOH = 0x01
	CR  = 0x0d
	SP  = 0x20
)

// Capability bits from E-Kermit's kermit.h.
const (
	CAP_LP = 2
	CAP_SW = 4
	CAP_AT = 8
	CAP_RS = 16
	CAP_LS = 32
)

// Params is a Kermit Send-Init parameter string.  Fields that are numeric in
// the protocol are stored decoded; EncodeParams applies tochar() where the
// protocol requires it.
type Params struct {
	MaxPacket int
	Timeout   int
	NPAD      int
	PADC      byte
	EOL       byte
	QCTL      byte
	QBIN      byte
	CHKT      byte
	REPT      byte
	CAPAS     int
	Window    int
	LongMax   int
}

// DefaultSendParams matches the C-Kermit settings that transferred 4 KiB from
// the SG200x ROM: 94-byte packets, one-byte checksum, no parity, window 1.
func DefaultSendParams() Params {
	return Params{
		MaxPacket: 94,
		Timeout:   15,
		NPAD:      0,
		PADC:      0x40, // ctl(0), as E-Kermit sends it
		EOL:       CR,
		QCTL:      '#',
		QBIN:      'Y', // C-Kermit's value when parity is none
		CHKT:      '1',
		REPT:      '~', // C-Kermit enables run-length encoding
		CAPAS:     CAP_LP | CAP_AT | CAP_RS | CAP_LS,
		Window:    1,
		LongMax:   4095,
	}
}

func xunchar(b byte) int { return int(b) - SP }
func tochar(n int) byte  { return byte(n) + SP }

// EncodeParams produces the data field of an S (Send-Init) packet.
func EncodeParams(p Params) []byte {
	return []byte{
		tochar(p.MaxPacket),
		tochar(p.Timeout),
		tochar(p.NPAD),
		p.PADC,
		tochar(int(p.EOL)),
		p.QCTL,
		p.QBIN,
		p.CHKT,
		p.REPT,
		tochar(p.CAPAS),
		tochar(p.Window),
		tochar(p.LongMax / 95),
		tochar(p.LongMax % 95),
	}
}

// DecodeParams parses the data field of a Send-Init ACK (or S packet).
func DecodeParams(data []byte) (Params, error) {
	if len(data) < 10 {
		return Params{}, fmt.Errorf("send-init data too short: %d", len(data))
	}
	p := Params{
		MaxPacket: xunchar(data[0]),
		Timeout:   xunchar(data[1]),
		NPAD:      xunchar(data[2]),
		PADC:      data[3],
		EOL:       byte(xunchar(data[4])),
		QCTL:      data[5],
		QBIN:      data[6],
		CHKT:      data[7],
		REPT:      data[8],
		CAPAS:     xunchar(data[9]),
		Window:    1,
		LongMax:   94,
	}
	if p.CAPAS&CAP_SW != 0 && len(data) >= 11 {
		p.Window = xunchar(data[10])
		if p.Window < 1 {
			p.Window = 1
		}
	}
	if p.CAPAS&CAP_LP != 0 && len(data) >= 13 {
		p.LongMax = xunchar(data[11])*95 + xunchar(data[12])
	}
	if p.MaxPacket < 10 {
		return Params{}, fmt.Errorf("negotiated max packet too small: %d", p.MaxPacket)
	}
	if p.CHKT != '1' && p.CHKT != '2' && p.CHKT != '3' && p.CHKT != '5' {
		// E-Kermit falls back to type 1 for anything it does not know.
		p.CHKT = '1'
	}
	return p, nil
}

// Packet is a decoded Kermit packet.
type Packet struct {
	Seq  byte
	Type byte
	Data []byte
	Raw  []byte
}

func (p Packet) String() string {
	return fmt.Sprintf("%c seq=%d data=%d bytes", p.Type, p.Seq, len(p.Data))
}

// Checksum6 computes the Kermit type-1 (6-bit) checksum over the bytes that
// follow the length field (length, sequence, type, and data).
func Checksum6(b []byte) byte {
	var sum int
	for _, c := range b {
		sum += int(c)
	}
	sum = (sum & 0x3f) + ((sum & 0xc0) >> 6)
	return byte(sum & 0x3f)
}

// checksumChars is the number of on-wire checksum bytes for a Kermit
// checksum type.  Type 1 is one 6-bit character; type 3 is a 16-bit CRC
// split into three 6-bit characters.
func checksumChars(chkt byte) int {
	if chkt == '3' {
		return 3
	}
	return 1
}

// CRC16CCITT computes the Kermit type-3 CRC-16 (CCITT/XMODEM polynomial
// 0x1021, initial value 0) over the bytes that follow the length field.
func CRC16CCITT(b []byte) uint16 {
	var crc uint16
	for _, c := range b {
		crc ^= uint16(c) << 8
		for i := 0; i < 8; i++ {
			if crc&0x8000 != 0 {
				crc = (crc << 1) ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

// EncodePacket encodes an already-escaped data field.  Only short packets
// (length byte != 0) are emitted; the ROM negotiates a 94-byte maximum.  The
// checksum type comes from p.CHKT: '1' (6-bit sum, the ROM default) or '3'
// (CRC-16/CCITT).
func EncodePacket(seq byte, typ byte, data []byte, p Params) ([]byte, error) {
	chkt := p.CHKT
	if chkt != '3' {
		chkt = '1'
	}
	bodyLen := len(data) + checksumChars(chkt) + 2 // data + checksum + seq/type
	if bodyLen > 94 {
		return nil, fmt.Errorf("packet body %d exceeds short-packet limit", bodyLen)
	}
	out := make([]byte, 0, 1+1+bodyLen+1)
	out = append(out, SOH, tochar(bodyLen), tochar(int(seq&63)), typ)
	out = append(out, data...)
	if chkt == '3' {
		crc := CRC16CCITT(out[1:]) // length, seq, type, data
		out = append(out, tochar(int(crc>>12)), tochar(int((crc>>6)&0x3f)), tochar(int(crc&0x3f)))
	} else {
		chk := Checksum6(out[1:]) // length, seq, type, data
		out = append(out, tochar(int(chk)))
	}
	out = append(out, p.EOL)
	return out, nil
}

var ErrIncomplete = errors.New("incomplete packet")

// DecodePacket parses one short Kermit type-1 packet from raw.  It returns
// ErrIncomplete if more bytes are needed.
func DecodePacket(raw []byte) (Packet, error) {
	return DecodePacketType(raw, '1')
}

// DecodePacketType parses one short Kermit packet with the given checksum
// type ('1' or '3').  It returns ErrIncomplete if more bytes are needed.
func DecodePacketType(raw []byte, chkt byte) (Packet, error) {
	if chkt != '3' {
		chkt = '1'
	}
	if len(raw) < 2 || raw[0] != SOH {
		return Packet{}, fmt.Errorf("not a packet")
	}
	lenVal := xunchar(raw[1])
	if lenVal == 0 {
		return Packet{}, fmt.Errorf("long packets not supported")
	}
	minLen := 2 + checksumChars(chkt)
	if lenVal < minLen || lenVal > 94 {
		return Packet{}, fmt.Errorf("bad packet length %d", lenVal)
	}
	total := 2 + lenVal + 1
	if len(raw) < total {
		return Packet{}, ErrIncomplete
	}
	if raw[total-1] != CR && raw[total-1] != '\n' {
		return Packet{}, fmt.Errorf("bad packet terminator %#x", raw[total-1])
	}
	seq := raw[2] - SP
	typ := raw[3]
	dataLen := lenVal - 2 - checksumChars(chkt)
	data := append([]byte(nil), raw[4:4+dataLen]...)
	checkPos := 4 + dataLen
	if chkt == '3' {
		want := CRC16CCITT(raw[1:checkPos])
		got := uint16(raw[checkPos]-SP)<<12 |
			uint16(raw[checkPos+1]-SP)<<6 |
			uint16(raw[checkPos+2]-SP)
		if got != want {
			return Packet{}, fmt.Errorf("bad CRC-16: got %#04x want %#04x", got, want)
		}
	} else {
		want := Checksum6(raw[1:checkPos])
		got := raw[checkPos] - SP
		if got != want {
			return Packet{}, fmt.Errorf("bad checksum: got %#x want %#x", got, want)
		}
	}
	return Packet{Seq: seq & 63, Type: typ, Data: data, Raw: append([]byte(nil), raw[:total]...)}, nil
}

// EncodeData escapes src bytes for a Kermit data field, stopping before the
// escaped field would exceed maxLen bytes.  It returns the escaped bytes and
// the number of source bytes consumed (always >= 1 unless maxLen is too
// small).
func EncodeData(src []byte, maxLen int, p Params, ebq bool, ebqCh byte) ([]byte, int, error) {
	if maxLen < 2 {
		return nil, 0, fmt.Errorf("max data field too small: %d", maxLen)
	}
	out := make([]byte, 0, maxLen)
	for i, b := range src {
		start := len(out)
		a := b
		if ebq && (a&0x80) != 0 {
			out = append(out, ebqCh)
			a &= 0x7f
		}
		switch {
		case a < 32 || a == 127:
			out = append(out, p.QCTL, a^0x40)
		case a == p.QCTL:
			out = append(out, p.QCTL, a)
		case ebq && a == ebqCh:
			out = append(out, p.QCTL, a)
		case p.REPT > 32 && a == p.REPT:
			out = append(out, p.QCTL, a)
		default:
			out = append(out, a)
		}
		if len(out) > maxLen {
			return out[:start], i, nil
		}
	}
	return out, len(src), nil
}

// DecodeData reverses EncodeData.  It is used by tests and by any receiver we
// implement locally.
func DecodeData(src []byte, p Params, ebq bool, ebqCh byte) []byte {
	out := make([]byte, 0, len(src))
	for i := 0; i < len(src); i++ {
		a := src[i]
		var b8 byte
		if ebq && a == ebqCh && i+1 < len(src) {
			b8 = 0x80
			i++
			a = src[i] & 0x7f
		}
		if a == p.QCTL && i+1 < len(src) {
			i++
			a = src[i]
			a7 := a & 0x7f
			if (a7 >= 0x40 && a7 <= 0x5f) || a7 == '?' {
				a ^= 0x40
			}
		}
		out = append(out, a|b8)
	}
	return out
}
