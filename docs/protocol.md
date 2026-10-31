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
- Evidence: [docs/evidence/README.md](docs/evidence/README.md)

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
`E:RESET:plat/mars` (plus `--urpl-delay`), so it answers the post-reset prompt
rather than the early one. If no marker appears within `--urpl-max-wait`
(default 4 s) it answers anyway as a fallback. A mid-transfer reset is retried
up to `--reset-retries` times and the gate is re-armed.

### Valid boot0 race

With a valid boot0 the ROM can boot eMMC directly and never present a usable
`URPL` to the loader. In that case the lab uses the race path: a long
`--wait-timeout`, near-zero `--urpl-max-wait`/`--urpl-delay`, and several
power cycles. This is a boot-order race, not a protocol change.

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

- Maximum packet 94 bytes, maximum data field 88 bytes (`--data-size` 16..88).
- Sequence numbers are modulo 64; the window is 1, so each `D` is ACKed before
  the next is sent.
- Checksum type 1 (6-bit sum) is the default. Type 3 (CRC-16) is implemented
  but the SG200x E-Kermit NAKs it (`01 23 20 4e 33 0d`).
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

- `--final-baud 1500000` switches at the FSBL and stays there. Use this for
  row-boot U-Boot/Linux built with `CONFIG_BAUDRATE=1500000`.
- `--follow-baud` switches to `--baud2` and then back to `--baud` at hand-off.
  This is legacy vendor-U-Boot-2021.10 (115200) behaviour. Switching back can
  wedge the CH347 TX path, so do not use it with row-boot 2026.07.
- CV180x uses the same URPL/URDL BootROM family but its vendor FSBL has no
  1.5 Mbaud switch, so `--final-baud 1500000` is not applicable there.

## Pacing, retries, recovery

- `--packet-delay` (default 2 ms) paces ACKed packets at 115200;
  `--packet-delay2` does the same at the final baud (default 0).
- `--start-delay` (150 ms) waits after `UL_START` before Send-Init;
  `--drain` (150 ms) discards stale boot-log bytes.
- `--ack-timeout` (5 s) and `--retries` (10) bound per-packet recovery.
- `--reset-retries` (3) re-arms the reset gate and retries a mid-transfer
  reset.
- `--ch347-recover` (default) re-authorizes the USB device and reopens the tty
  when ACKs stop, avoiding a manual unplug.

A failed run writes nothing to eMMC, SPI, or NAND; power-cycle and retry.

## Evidence and provenance

The hardware evidence is curated and sanitized under
[`evidence/`](evidence/README.md). It records the prompt, request sequence,
baud switch, hand-off, and FIP hashes without private host, network, or log
paths. Multi-thousand-line vendor logs are not published.

The end-to-end test double lives in `internal/romsim`: it simulates the
SG200x-family BootROM and an E-Kermit receiver over a PTY. It exercises the
sender, request parser, framing, and ACK path without hardware.
