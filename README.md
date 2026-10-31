# sg200x-uart-loader

A Go host loader for the SG200x-family BootROM UART download path, validated on
the Pine64 Oz64 (SG2000). It serves a CVITEK FIP over the serial console as an
E-Kermit 1.8 receiver, keeping the port open for every BootROM/FSBL slice
request. It uses no USB boot/recovery protocol and writes nothing to eMMC or
SPI; CH347 USB-UART recovery is automatic.

## Compatibility

- **SG2000 (ARM64):** tested on the Pine64 Oz64.
- **SG2002 and CV181x:** share the SG200x/CV181x platform and BootROM protocol
  and are expected to work with a matching board FIP, but are not
  hardware-validated here.
- **CV180x:** **unsupported by v0.2.0.** It uses the same URPL/URDL BootROM
  family, but its vendor FSBL has no 1.5 Mbaud switch. Use v0.1.0 for CV180x
  experiments.
- Protocol compatibility is not FIP/board compatibility. Use a matching FIP,
  DDR configuration, UART wiring, and power sequencing.

> **v0.2.0 baud assumption:** v0.2.0 assumes the FSBL switches to **1.5 Mbaud**
> and has **no 115200-stay mode**. It always moves to 1.5 Mbaud at the FSBL;
> `--follow-baud` only selects whether to switch back to 115200 at hand-off.

## Why

The SG200x-family BootROM can load a FIP over UART when no bootable storage is
available. The ROM prints `URPL`, accepts the four bytes `55 52 44 4c`
(`URDL`), and then asks for ranges of the FIP with `EK/<offset>/<size>/UL_START`.
This loader answers those requests, so a board with a bad or empty boot medium
can still reach U-Boot/Linux. It is a RAM loader, not a flashing tool.

## Requirements

- Linux host with Go 1.26.x (no cgo) or a release binary.
- CH347 USB-UART on `/dev/ttyACM0` (or another tty). 8N1, raw, no flow control.
- Oz64 in ARM/A53 mode (J50) with UART0 wired to the adapter.
- A complete CVITEK FIP (`CVBL01` at offset 0).

## Build

```console
$ go build -o sg200x-uart-loader .
$ make amd64          # dist/sg200x-uart-loader-linux-amd64
$ make arm64          # dist/sg200x-uart-loader-linux-arm64
$ nix build .#default # result/bin/sg200x-uart-loader
$ ./sg200x-uart-loader --version
```

## Run

Start the loader **before** powering the board, then power-cycle it (leave it
off for at least 5 s):

```console
$ ./sg200x-uart-loader \
    --port /dev/ttyACM0 --fip <fip> \
    --post 60s --log <log>
```

The default is row-boot U-Boot 2026.07: the loader switches to 1.5 Mbaud at the
FSBL and stays there for U-Boot/Linux built with `CONFIG_BAUDRATE=1500000`.

For vendor U-Boot 2021.10 (115200 console), add `--follow-baud`; that switches
back to 115200 at hand-off.

## Options

| Flag | Meaning |
| --- | --- |
| `--port` | serial device (default `/dev/ttyACM0`) |
| `--fip` | CVITEK FIP to serve (required) |
| `--log` | timestamped RX/TX and protocol log |
| `--verbose` | extra protocol detail |
| `--dry-run` | validate inputs and print the plan without opening the port |
| `--selftest` | run built-in packet/parser checks and exit |
| `--version` | print the version and exit |
| `--power-cmd` | command run before waiting for `URPL` |
| `--follow-baud` | vendor U-Boot 2021.10: switch back to 115200 at hand-off |
| `--wait-timeout` | maximum wait for `URPL`/first request (default `180s`) |
| `--post` | console capture after the last slice (default `30s`) |
| `--race` | answer the first `URPL` immediately (valid boot0 race mode) |

Protocol limits and timing/recovery defaults are constants: 115200 start,
1500000 at the FSBL, 88 data bytes (94-byte packets), checksum type 1, 5 s ACK
timeout, 10 retries, 3 reset retries, and fixed pacing/drain delays. Only
`--wait-timeout` and `--post` remain timing knobs.

## Migrating from v0.1.0

v0.2.0 removes flags that described fixed protocol invariants or validated
defaults. Remove them from scripts:

| Removed in v0.2.0 | Replacement |
| --- | --- |
| `--baud` | omit; the BootROM/BL2 rate is always 115200 |
| `--baud2`, `--final-baud`, `--stay-baud` | omit; the default switches to 1500000 and stays |
| `--checksum` | omit; checksum type 1 is fixed |
| `--data-size` | omit; 88 data bytes / 94-byte packets are fixed |
| `--ack-timeout`, `--retries`, `--reset-retries` | omit; fixed at 5 s / 10 / 3 |
| `--packet-delay`, `--packet-delay2`, `--start-delay`, `--drain` | omit; fixed pacing |
| `--ch347-recover` | omit; CH347 recovery is always on |
| `--urpl-delay`, `--urpl-max-wait` | omit, or use `--race` for the old immediate-answer race path |

`--race` answers the first `URPL` immediately. It is equivalent to the v0.1.0
race incantation `--urpl-delay 1ns --urpl-max-wait 1ns`.

## How it boots

The ROM/BL2 run at 115200; the vendor FSBL then moves its console to
1,500,000 and requests the remaining slices through the same port. See
[`docs/protocol.md`](docs/protocol.md) for the request grammar, E-Kermit
framing, reset-marker gate, and baud switch.

## Testing

```console
$ go test ./...
$ go run . --selftest
$ go test -tags=e2e ./internal/romsim -count=1   # or: make e2e
```

The end-to-end test runs `internal/romsim`, a simulated SG200x-family BootROM
and E-Kermit receiver over a PTY. It needs no serial hardware.

## Troubleshooting

- **Early `URPL` then reset:** the loader waits for `E:Boot failed` /
  `E:RESET:plat/mars`. Answering the early prompt races the ~2.5 s eMMC reset.
- **Valid boot0, no `URPL`:** the ROM may boot eMMC directly. Retry the race
  path with `--wait-timeout 90s --race`.
- **Silent U-Boot/Linux after the FSBL:** the default is a 1.5 M console. For
  the vendor U-Boot 2021.10 (115200), use `--follow-baud`; switching back can
  wedge the CH347.
- **CH347 TX wedges:** recovery is automatic; the loader re-authorizes and
  reopens the device on ACK timeouts. Re-plug or re-authorize the USB port if
  needed.
- **Failed run:** nothing is written to storage. Power-cycle and retry.

## Status

Hardware validation is on the Pine64 Oz64 (SG2000) with the row-boot U-Boot
2026.07 FIP (1.5 M console). Hardware validation remains a manual step; CI only
tests and cross-builds.

## References

- Project: <https://github.com/tomfitzhenry/sg200x-uart-loader>
- E-Kermit 1.8: <https://github.com/KermitProject/ekermit>
- Kermit protocol: <https://kermitproject.org/kproto.pdf>
- CH347 USB-UART: <https://www.wch-ic.com/products/CH347.html>
- Pine64 Oz64: <https://wiki.pine64.org/wiki/Oz64>
- Evidence: [docs/evidence/README.md](docs/evidence/README.md)

## License

MIT; see [`LICENSE`](LICENSE).
