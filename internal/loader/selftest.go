package loader

import (
	"bytes"
	"fmt"

	"github.com/tomfitzhenry/sg200x-uart-loader/internal/kermit"
	"github.com/tomfitzhenry/sg200x-uart-loader/internal/rom"
)

// SelfTest exercises the packet encoder/decoder, quoting, checksum, and ROM
// request parser without touching hardware.
func SelfTest() error {
	p := kermit.DefaultSendParams()

	// The exact Send-Init a C-Kermit-compatible sender emits for the ROM.
	wantS := []byte{
		0x01, 0x30, 0x20, 'S', // SOH, len=16, seq=0, S
		0x7e, 0x2f, 0x20, 0x40, 0x2d, '#', 'Y', '1', '~', 'Z', '!', 'K', '*',
		0x5b, 0x0d, // checksum, CR
	}
	gotS, err := kermit.EncodePacket(0, 'S', kermit.EncodeParams(p), p)
	if err != nil {
		return err
	}
	if !bytes.Equal(gotS, wantS) {
		return fmt.Errorf("Send-Init mismatch:\n got %x\nwant %x", gotS, wantS)
	}

	// Checksum matches E-Kermit's chk1 for the observed NAK packet.
	if got := kermit.Checksum6([]byte{0x23, 0x20, 'N'}); got != 0x13 {
		return fmt.Errorf("checksum6 = %#x, want 0x13", got)
	}
	nak := []byte{0x01, 0x23, 0x20, 'N', 0x33, 0x0d}
	pkt, err := kermit.DecodePacket(nak)
	if err != nil {
		return fmt.Errorf("decode observed NAK: %w", err)
	}
	if pkt.Type != 'N' || pkt.Seq != 0 {
		return fmt.Errorf("decoded NAK as %c seq=%d", pkt.Type, pkt.Seq)
	}

	// Binary data with control characters, '#', '~', high-bit bytes, and NUL.
	src := []byte{0x00, 0x0d, 0x0a, '#', '~', 0x7f, 0x80, 0xff, 'A'}
	enc, used, err := kermit.EncodeData(src, 88, p, false, 0)
	if err != nil || used != len(src) {
		return fmt.Errorf("encode data: %v used=%d", err, used)
	}
	if bytes.IndexByte(enc, 0x00) >= 0 || bytes.IndexByte(enc, 0x0d) >= 0 {
		return fmt.Errorf("encoded data contains a raw control byte: %x", enc)
	}
	dec := kermit.DecodeData(enc, p, false, 0)
	if !bytes.Equal(dec, src) {
		return fmt.Errorf("quote round-trip: got %x want %x", dec, src)
	}

	// Request parsing.
	line := []byte("BS/UART.PS.EK/4096/55296/UL_START\r\n")
	rs := rom.FindRequests(line)
	if len(rs) != 1 || rs[0].Offset != 4096 || rs[0].Size != 55296 {
		return fmt.Errorf("request parse: %+v", rs)
	}
	if err := rs[0].Validate(100000); err != nil {
		return err
	}
	if err := (rom.Request{Offset: 90000, Size: 20000}).Validate(100000); err == nil {
		return fmt.Errorf("bounds check accepted an out-of-range request")
	}
	return nil
}
