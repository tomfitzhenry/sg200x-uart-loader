package kermit

import (
	"bytes"
	"testing"
)

func TestChecksum6MatchesObservedNAK(t *testing.T) {
	// The ROM's NAK observed on hardware: 01 23 20 4e 33 0d.
	if got := Checksum6([]byte{0x23, 0x20, 'N'}); got != 0x13 {
		t.Fatalf("checksum6 = %#x, want 0x13", got)
	}
	pkt, err := DecodePacket([]byte{0x01, 0x23, 0x20, 'N', 0x33, 0x0d})
	if err != nil {
		t.Fatal(err)
	}
	if pkt.Type != 'N' || pkt.Seq != 0 || len(pkt.Data) != 0 {
		t.Fatalf("decoded %c seq=%d data=%x", pkt.Type, pkt.Seq, pkt.Data)
	}
}

func TestSendInitPacket(t *testing.T) {
	p := DefaultSendParams()
	got, err := EncodePacket(0, 'S', EncodeParams(p), p)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{
		0x01, 0x30, 0x20, 'S',
		0x7e, 0x2f, 0x20, 0x40, 0x2d, '#', 'Y', '1', '~', 'Z', '!', 'K', '*',
		0x5b, 0x0d,
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got  %x\nwant %x", got, want)
	}
}

func TestParamsRoundTrip(t *testing.T) {
	p := DefaultSendParams()
	q, err := DecodeParams(EncodeParams(p))
	if err != nil {
		t.Fatal(err)
	}
	if q.MaxPacket != p.MaxPacket || q.EOL != p.EOL || q.QCTL != p.QCTL ||
		q.QBIN != p.QBIN || q.CHKT != p.CHKT || q.Window != p.Window ||
		q.CAPAS != p.CAPAS || q.LongMax != p.LongMax {
		t.Fatalf("round trip mismatch: %+v != %+v", q, p)
	}
}

func TestQuoteRoundTripBinary(t *testing.T) {
	p := DefaultSendParams()
	src := []byte{0x00, 0x0d, 0x0a, 0x01, '#', '~', 0x7f, 0x80, 0xff, 'A'}
	enc, used, err := EncodeData(src, 88, p, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if used != len(src) {
		t.Fatalf("used %d of %d", used, len(src))
	}
	for _, bad := range []byte{0x00, 0x0d, 0x0a, 0x01, 0x7f} {
		if bytes.IndexByte(enc, bad) >= 0 {
			t.Fatalf("encoded field contains raw control byte %#x: %x", bad, enc)
		}
	}
	if dec := DecodeData(enc, p, false, 0); !bytes.Equal(dec, src) {
		t.Fatalf("got %x want %x", dec, src)
	}
}

func TestQuoteEighthBit(t *testing.T) {
	p := DefaultSendParams()
	src := []byte{0x80, 0xff, 0x00}
	enc, used, err := EncodeData(src, 88, p, true, '&')
	if err != nil || used != len(src) {
		t.Fatalf("err=%v used=%d", err, used)
	}
	if dec := DecodeData(enc, p, true, '&'); !bytes.Equal(dec, src) {
		t.Fatalf("got %x want %x (encoded %x)", dec, src, enc)
	}
}

func TestEncodeDataHonoursLimit(t *testing.T) {
	p := DefaultSendParams()
	src := make([]byte, 100)
	for i := range src {
		src[i] = 0x00 // each expands to two bytes
	}
	enc, used, err := EncodeData(src, 88, p, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(enc) > 88 {
		t.Fatalf("encoded %d bytes > 88", len(enc))
	}
	if used <= 0 || used >= len(src) {
		t.Fatalf("used=%d", used)
	}
}

func TestCRC16KnownVector(t *testing.T) {
	if got := CRC16CCITT([]byte("123456789")); got != 0x31c3 {
		t.Fatalf("CRC16CCITT = %#04x, want 0x31c3", got)
	}
}

func TestType3PacketRoundTrip(t *testing.T) {
	p := DefaultSendParams()
	p.CHKT = '3'
	raw, err := EncodePacket(7, 'D', []byte("hello\x00\xff"), p)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 4+7+3+1 {
		t.Fatalf("raw length %d: %x", len(raw), raw)
	}
	pkt, err := DecodePacketType(raw, '3')
	if err != nil {
		t.Fatal(err)
	}
	if pkt.Seq != 7 || pkt.Type != 'D' || !bytes.Equal(pkt.Data, []byte("hello\x00\xff")) {
		t.Fatalf("decode = %+v", pkt)
	}
	// A type-1 decoder must reject it rather than accept the CRC field.
	if _, err := DecodePacketType(raw, '1'); err == nil {
		t.Fatal("type-1 decode accepted a type-3 packet")
	}
}
