# Evidence

Curated, sanitized excerpts from hardware runs. Full loader and console logs
are not published: captured console logs can be multi-megabyte and can
contain private host, network, and log paths.

## Files

| File | What it shows |
| --- | --- |
| `boot-handoff.txt` | A row-boot U-Boot 2026.07 cold boot on the Pine64 Oz64 (SG2000): prompt, request sequence, 1.5 Mbaud switch, and FSBL to BL31 hand-off. |

The transcript was captured with the pre-rename build, whose console prefix
was `oz64:`; renaming the tool to `sg200x-uart-loader` did not change the wire
protocol. The evidence is therefore still SG2000/Oz64-specific.

## Provenance

The excerpt comes from a row-boot U-Boot 2026.07 FIP built with
`CONFIG_BAUDRATE=1500000` and 1,500,000 in `CFG_SYS_BAUDRATE_TABLE`,
304,640 bytes:

```text
sha256 3dfaad354f9cb87922ff1eb6f84a95be8cbd0e81b1532bd027c6a9a6700b9417
```

The vendor Armbian Duo S fallback FIP used for the `--follow-baud` path is
303,616 bytes:

```text
sha256 382296a757e14070fc956fe348d6f0e4d3efabb07208e8e56117f66dcfd01061
```

Capture: CH347 USB-UART on `/dev/ttyACM0`, 115200 8N1 start, loader in what is
now the default row-boot mode (1.5 Mbaud final; v0.1.0 spelled this
`--final-baud 1500000`, removed in v0.2.0), reset-marker gate enabled. See the
evidence notes for current FIPs, hashes, and full hardware validation:

No vendor FIP images are redistributed.
