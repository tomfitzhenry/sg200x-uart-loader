# sg200x-uart-loader

A Go host loader for the SG200x-family BootROM UART download path, validated on
the Pine64 Oz64 (SG2000). It serves a CVITEK FIP over the serial console as an
E-Kermit 1.8 receiver, keeping the port open for every BootROM/FSBL slice
request. It uses no USB boot/recovery protocol and writes nothing to eMMC or
SPI; `--ch347-recover` only re-authorizes the host-side CH347 USB-UART.

## Compatibility

- **SG2000 (ARM64):** tested on the Pine64 Oz64.
- **SG2002 and CV181x:** share the SG200x/CV181x platform and BootROM protocol
  and are expected to work with a matching board FIP, but are not
  hardware-validated here.
- **CV180x:** untested. It uses the same URPL/URDL BootROM family, but the
  vendor FSBL has no 1.5 Mbaud switch, so do not assume
  `--final-baud 1500000`.
- Protocol compatibility is not FIP/board compatibility. Use a matching FIP,
  DDR configuration, UART wiring, and power sequencing.

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
    --final-baud 1500000 --post 60s --log <log>
```

`--final-baud 1500000` follows the FSBL to 1.5 Mbaud and never switches back;
use it for row-boot U-Boot/Linux built with `CONFIG_BAUDRATE=1500000`.

## Options

| Primary | Meaning |
| --- | --- |
| `--port` | serial device (default `/dev/ttyACM0`) |
| `--fip` | CVITEK FIP to serve (required) |
| `--final-baud` | switch to this baud at the FSBL and stay there (e.g. `1500000`; `--stay-baud` is an alias) |
| `--post` | console capture after the last slice (default `30s`) |
| `--log` | timestamped RX/TX and protocol log |

| Advanced / legacy | Meaning |
| --- | --- |
| `--baud` | initial BootROM baud (default `115200`) |
| `--baud2` | FSBL baud for `--follow-baud` (default `1500000`) |
| `--follow-baud` | legacy vendor mode: switch to `--baud2` then back; vendor 115200 FIP only |
| `--wait-timeout` | maximum wait for `URPL`/first request (default `180s`) |
| `--packet-delay`, `--packet-delay2` | pacing after each ACK, initial/final baud (default `2ms`/`0`) |
| `--start-delay`, `--drain` | delay after `UL_START` / drain boot log (default `150ms` each) |
| `--urpl-delay`, `--urpl-max-wait` | extra delay after a settled prompt / answer without a reset marker (default `100ms`/`4s`) |
| `--ack-timeout`, `--retries`, `--reset-retries` | Kermit ACK timeout, packet retries, mid-transfer reset retries (default `5s`/`10`/`3`) |
| `--data-size` | max data bytes per packet, 16..88 (default `88`) |
| `--checksum` | `1` = 6-bit sum (default), `3` = CRC-16 (ROM NAKs it) |
| `--ch347-recover` | re-authorize/reopen the CH347 on ACK timeouts (default `true`) |
| `--power-cmd` | command run before waiting for `URPL` |
| `--dry-run`, `--verbose` | validate inputs and print the plan without serial / extra protocol detail |
| `--selftest` / `--version` | run built-in checks / print version |

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

- **Early `URPL` then reset:** answer only after `E:Boot failed` /
  `E:RESET:plat/mars`. Answering the early prompt races the ~2.5 s eMMC reset.
- **Valid boot0, no `URPL`:** the ROM may boot eMMC directly. Retry the race
  path with `--wait-timeout 90s --urpl-max-wait 1ns --urpl-delay 1ns`.
- **Silent U-Boot/Linux after the FSBL:** use `--final-baud 1500000` for a
  1.5 M console. For the vendor U-Boot 2021.10 (115200), use `--follow-baud`;
  switching back can wedge the CH347.
- **CH347 TX wedges:** `--ch347-recover` (default) re-authorizes and reopens
  the device; re-plug or re-authorize the USB port if needed.
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
- Lab bring-up notes: <https://github.com/tomfitzhenry/labgrid/blob/master/docs/oz64-bringup.md>

## License

MIT; see [`LICENSE`](LICENSE).
