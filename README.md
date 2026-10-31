# sg200x-uart-loader

A Go host loader for the SG200x-family BootROM UART download path, validated on
the Pine64 Oz64 (SG2000). It serves a CVITEK FIP over the serial console as an
E-Kermit 1.8 receiver, keeping the port open for every BootROM/FSBL slice
request. It uses no USB boot protocol and writes nothing to eMMC or SPI.

## Compatibility

- **SG2000 (ARM64):** tested on the Pine64 Oz64.
- **SG2002 and CV181x:** share the SG200x/CV181x platform and BootROM protocol
  and are expected to work with a matching board FIP, but are not
  hardware-validated here.
- **CV180x:** **unsupported by v0.4.0.** It uses the same URPL/URDL BootROM
  family, but its vendor FSBL has no 1.5 Mbaud switch. Use v0.1.0 for CV180x
  experiments.
- Protocol compatibility is not FIP/board compatibility. Use a matching FIP,
  DDR configuration, UART wiring, and power sequencing.

> **v0.4.0 baud behaviour:** the loader starts at 115200 and switches to
> **1.5 Mbaud** at the FSBL, then stays there. Vendor U-Boot 2021.10 (115200
> console) is not supported; v0.2.0 was the last release with `--follow-baud`
> for that vendor mode.

## Why

The SG200x-family BootROM can load a FIP over UART when no bootable storage is
available. The ROM prints `URPL`, accepts the four bytes `55 52 44 4c`
(`URDL`), and then asks for ranges of the FIP with `EK/<offset>/<size>/UL_START`.
This loader answers those requests, so a board with a bad or empty boot medium
can still reach U-Boot/Linux. It is a RAM loader, not a flashing tool.

## Requirements

- Linux host with Go 1.26.x (no cgo) or a release binary.
- A Linux tty (e.g. `/dev/ttyACM0` or `/dev/ttyUSB0`) whose driver supports
  115200 and 1,500,000 baud, raw 8N1, no flow control. No VID/PID/driver
  requirement.
- Oz64 in ARM/A53 mode (J50) with UART0 wired to the adapter.
- A complete CVITEK FIP (`CVBL01` at offset 0).

## Build

```console
$ go build -o sg200x-uart-loader .
$ go build -trimpath -ldflags "-s -w -X main.version=v0.4.0" -o sg200x-uart-loader .
$ CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
    -ldflags "-s -w -X main.version=v0.4.0" \
    -o dist/sg200x-uart-loader-linux-amd64 .
$ CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath \
    -ldflags "-s -w -X main.version=v0.4.0" \
    -o dist/sg200x-uart-loader-linux-arm64 .
$ nix build .#default # optional; result/bin/sg200x-uart-loader
$ ./sg200x-uart-loader --version
```

`flake.nix` and `flake.lock` are an optional Nix build and dev shell; plain Go
needs no Makefile.

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

## Options

| Flag | Meaning |
| --- | --- |
| `--port` | serial device (default `/dev/ttyACM0`) |
| `--fip` | CVITEK FIP to serve (required) |
| `--log` | timestamped RX/TX and protocol log |
| `--verbose` | extra protocol detail |
| `--dry-run` | validate inputs and print the plan without opening the port |
| `--version` | print the version and exit |
| `--power-cmd` | command run before waiting for `URPL` |
| `--wait-timeout` | maximum wait for `URPL`/first request (default `180s`) |
| `--post` | console capture after the last slice (default `30s`) |
| `--race` | answer the first `URPL` immediately (valid boot0 race mode) |

Protocol limits and timing/retry defaults are constants: 115200 start,
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
| `--urpl-delay`, `--urpl-max-wait` | omit, or use `--race` for the old immediate-answer race path |

`--race` answers the first `URPL` immediately. It is equivalent to the v0.1.0
race incantation `--urpl-delay 1ns --urpl-max-wait 1ns`.

## Migrating from v0.2.0

v0.3.0 is a breaking CLI change that removes diagnostics, the vendor baud mode,
and the Makefile. No flags were renamed.

| Removed in v0.3.0 | Replacement |
| --- | --- |
| `--selftest` | `go test ./...` (the same packet, checksum, quoting, and parser cases are unit tests) |
| `--follow-baud` | omit; the loader always switches to 1.5 Mbaud at the FSBL and stays there. Vendor U-Boot 2021.10 is unsupported; stay on v0.2.0 if you need it. |
| `Makefile` targets | plain `go build`, `go test ./...`, and `go test -tags=e2e ./internal/romsim` commands |

## Migrating from v0.3.0

v0.4.0 removes the automatic USB device reset on Kermit ACK timeout. No flags
changed; on an ACK timeout the loader now reports the error and exits after the
fixed retries instead of reopening the tty and retrying the file.

## How it boots

The ROM/BL2 run at 115200; the vendor FSBL then moves its console to
1,500,000 and requests the remaining slices through the same port. See
[`docs/protocol.md`](docs/protocol.md) for the request grammar, E-Kermit
framing, reset-marker gate, and baud switch.

## Testing

```console
$ go test ./...
$ go test -tags=e2e ./internal/romsim -count=1
$ go test -race -tags=e2e ./internal/romsim -count=1
$ E2E_RACE=1 go test -race -tags=e2e ./internal/romsim -count=1
```

The end-to-end test runs `internal/romsim`, a simulated SG200x-family BootROM
and E-Kermit receiver over a PTY. It needs no serial hardware.

## Troubleshooting

- **Early `URPL` then reset:** the loader waits for `E:Boot failed` /
  `E:RESET:plat/mars`. Answering the early prompt races the ~2.5 s eMMC reset.
- **Valid boot0, no `URPL`:** the ROM may boot eMMC directly. Retry the race
  path with `--wait-timeout 90s --race`.
- **Silent U-Boot/Linux after the FSBL:** the loader leaves the console at
  1.5 Mbaud. Build U-Boot/Linux with `CONFIG_BAUDRATE=1500000`. Vendor U-Boot
  2021.10 (115200 console) is unsupported; use v0.2.0 for that mode.
- **Kermit ACK timeout:** after the fixed retries the loader reports the
  timeout and exits. Check the wiring and baud rate, power-cycle the board, and
  retry. If a USB-UART adapter is wedged, re-seat or re-plug it.
- **Failed run:** nothing is written to storage. Power-cycle and retry.

## Status

Hardware validation is on the Pine64 Oz64 (SG2000) with the row-boot U-Boot
2026.07 FIP (1.5 M console). Hardware validation remains a manual step; CI only
tests and cross-builds.

## References

- Project: <https://github.com/tomfitzhenry/sg200x-uart-loader>
- E-Kermit 1.8: <https://github.com/KermitProject/ekermit>
- Kermit protocol: <https://kermitproject.org/kproto.pdf>
- Pine64 Oz64: <https://wiki.pine64.org/wiki/Oz64>
- Evidence: [docs/evidence/README.md](docs/evidence/README.md)

## License

MIT; see [`LICENSE`](LICENSE).
