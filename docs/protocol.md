# SG200x-family BootROM UART protocol

Reference for the wire behaviour implemented by `sg200x-uart-loader`. The
protocol is unchanged from the hardware-validated loader; this document is a
summary, not a replacement for the source.

## Scope and sources

The SG200x-family BootROM (SG2000/SG2002 on the CV181x platform) exposes a UART
download path used by the Pine64 Oz64. The receiver is E-Kermit 1.8. The loader
is the sender.

- E-Kermit: <https://github.com/KermitProject/ekermit>
- Kermit protocol: <https://kermitproject.org/kproto.pdf>
- SG200X technical reference manual:
  <https://github.com/sophgo/sophgo-doc/tree/main/SG200X/TRM>
- CH347 USB-UART: <https://www.wch-ic.com/products/CH347.html>
- Lab bring-up: <https://github.com/tomfitzhenry/labgrid/blob/master/docs/oz64-bringup.md>

## Boot flow and console

The ROM prints a short boot banner and then `URPL.` from its prompt loop. On a
cold boot with a failing eMMC path it may also print `E:PARAM1 magic`,
`E:Boot failed`, and `E:RESET:plat/mars` before resetting the SoC roughly
2.5 seconds later.

### `URDL`

For each prompt, send exactly one `URDL` (hex `55 52 44 4c`). Do not repeat it
for the same prompt; a later prompt gets a new `URDL`. Sending it twice, or
answering the wrong prompt, races the reset and loses the transfer.

### Reset-marker gate

The loader delays `URDL` until it has seen `E:Boot failed` or
`E:RESET:plat/mars`, then waits the fixed 100 ms URPL delay, so it answers the
post-reset prompt rather than the early one. If no marker appears within the
fixed 4 s URPL max wait it answers anyway as a fallback. A mid-transfer reset is
retried up to the fixed 3 reset retries and the gate is re-armed.

`--race` skips the gate, the URPL delay, and the max wait: it answers the first
`URPL` immediately. This is for a valid boot0, where the ROM may never reach
the eMMC reset path.

### Valid boot0 race

With a valid boot0 the ROM can boot eMMC directly and never present a usable
`URPL` to the loader. In that case the lab uses the race path: a long
`--wait-timeout`, `--race`, and several power cycles. This is a boot-order
race, not a protocol change.

## Requests

After `URDL` the peer asks for a slice:

```text
EK/<offset>/<size>/UL_START
```

`<offset>` and `<size>` are decimal byte counts into the FIP. The loader
serves `fip[offset:offset+size]` as one Kermit file, bounds-checks both values
against the FIP length, and aborts on an out-of-range request. Requests are
peer-initiated and may be issued in any order; the port stays open between
slices.

Observed request sequences for the row-boot FIP:

```text
115200:   0/4096, 4096/55296
1500000:  59392/4096, 63488/25088, 88576/512, 88576/216064
```

## E-Kermit framing

The loader sends standard short Kermit packets:

| Packet | Purpose |
| --- | --- |
| `S` | Send-Init |
| `F` | filename (`fip.bin`) |
| `D` | data |
| `Z` | EOF |
| `B` | EOT |

- The 94-byte maximum packet, 88-byte maximum data field, and checksum type 1
  are fixed protocol limits. Type 3 (CRC-16) remains implemented and tested,
  but the SG200x E-Kermit NAKs it (`01 23 20 4e 33 0d`), so the loader always
  sends type 1.
- Sequence numbers are modulo 64; the window is 1, so each `D` is ACKed before
  the next is sent.
- Control bytes, `#`, and `~` are quoted with `#`; high-bit data are sent
  transparently (`QBIN=Y`, `ebq=false`).
- ACK/NAK/`E` packets are parsed; NAKs, timeouts, and stale ACKs trigger a
  retransmission.
- The loader negotiates `QCTL=#`, `QBIN=Y`, `CHKT=1`, `REPT=~`, capability
  bits 58, and window 1. The ROM replies with its own parameters
  (94/40/`#`/`Y`/`1`/window 1).

The Send-Init the loader sends is:

```text
01 30 20 53 7e 2f 20 40 2d 23 59 31 7e 5a 21 4b 2a 5b 0d
```

## Baud switch

The ROM and BL2 run at 115200 8N1. At the FSBL the peer switches its console to
1,500,000 and continues the same request protocol. The loader never closes the
tty; it only changes the line speed.

- The loader switches to 1500000 at the FSBL and stays there. This is row-boot
  U-Boot/Linux built with `CONFIG_BAUDRATE=1500000`.
- Vendor U-Boot 2021.10 (115200 console) is unsupported as of v0.3.0. v0.2.0
  was the last release with `--follow-baud`, which switched back to 115200 at
  hand-off. That historical mode is not a current command.
- CV180x uses the same URPL/URDL BootROM family but its vendor FSBL has no
  1.5 Mbaud switch, so the FSBL baud change is not applicable there.

## Pacing, retries, recovery

These timing and recovery values are fixed constants in v0.3.0:

- 2 ms packet pacing at 115200, 0 at the final baud.
- 150 ms `start-delay` after `UL_START` before Send-Init, and a 150 ms drain of
  stale boot-log bytes.
- 5 s ACK timeout and 10 packet retries bound per-packet recovery.
- 3 reset retries re-arm the reset gate after a mid-transfer reset.
- CH347 recovery is always on: on ACK timeouts the loader re-authorizes the USB
  device and reopens the tty, avoiding a manual unplug.

Only `--wait-timeout` and `--post` remain timing knobs.

A failed run writes nothing to eMMC, SPI, or NAND; power-cycle and retry.

## Evidence and provenance

The hardware evidence is curated and sanitized under
[`evidence/`](evidence/README.md). It records the prompt, request sequence,
baud switch, hand-off, and FIP hashes without private host, network, or log
paths. Multi-thousand-line vendor logs are not published.

The end-to-end test double lives in `internal/romsim`: it simulates the
SG200x-family BootROM and an E-Kermit receiver over a PTY. It exercises the
sender, request parser, framing, and ACK path without hardware. Run it and the
unit tests with plain Go:

```console
$ go test ./...
$ go test -tags=e2e ./internal/romsim -count=1
```
