# wusbkit

A fast, native Windows CLI toolkit for USB drive management. Zero external dependencies — all operations use direct Win32 APIs (WMI, DeviceIoControl, VDS, fmifs).

## Features

- **List** all connected USB storage devices (native WMI, sub-200ms)
- **Flash** disk images to USB drives (.img, .bin, .iso, .raw)
- **Create** disk images from USB drives (ImageUSB-compatible .bin format)
- **Format** USB drives (FAT32, NTFS, exFAT) — FAT32 bypasses Windows 32GB limit
- **Eject** USB drives safely
- **Set volume labels** without reformatting
- **Parallel operations** — flash, format, or label multiple drives simultaneously
- **Streaming decompression** — flash from .gz, .xz, .zst files without extracting
- **Remote flashing** — stream images directly from HTTP/HTTPS URLs
- **Write verification** — read back and compare after flashing
- **SHA-256 hashing** — calculate hash during write
- **Skip-unchanged sectors** — faster partial updates
- **Write retry logic** — 3 retries with 1s delay on failure (matches ImageUSB behavior)
- **Pre-write speed test** — detects fake/unresponsive drives before flashing
- **ImageUSB .bin header support** — auto-detects headers, verifies checksums
- **ISO bootable USB** — detects bootloader (GRUB2, Syslinux, Windows), writes MBR
- **Partition extension** — grow NTFS partition after flashing smaller images
- **BitLocker detection** — warns before operating on encrypted drives
- **JSON output** — all commands support `--json` for programmatic integration
- **Disk locking** — prevents concurrent operations on the same drive
- **Signal handling** — graceful cancellation with Ctrl+C

## Requirements

- Windows 10 or later (64-bit)
- Administrator privileges (for format, flash, and create operations)

> **No PowerShell required.** All operations use native Windows APIs.

## Installation

### From Releases

Download `wusbkit.exe` from the [latest release](https://github.com/lazaroagomez/wusbkit/releases).

### From Source

```bash
git clone https://github.com/lazaroagomez/wusbkit.git
cd wusbkit
go build -o dist/wusbkit.exe .
```

## Quick Start

```bash
# List USB drives
wusbkit list

# Flash an image (ChromeOS, Ubuntu, Raspberry Pi, etc.)
wusbkit flash 2 --image recovery.bin --yes --verify --hash

# Flash to multiple drives at once
wusbkit flash 2,3,4 --image ubuntu.img --parallel --yes

# Create a backup image from USB
wusbkit create E: --output backup.bin

# Format as FAT32 (works on drives > 32GB)
wusbkit format 2 --fs fat32 --label "USB" --yes

# Eject safely
wusbkit eject E:
```

## Commands

### `list` — List USB Drives

```bash
wusbkit list              # Table output
wusbkit list -v           # Verbose (serial, VID/PID, filesystem, hub port)
wusbkit list --json       # JSON array
```

### `info` — Drive Details

```bash
wusbkit info E:           # By drive letter
wusbkit info 2            # By disk number
wusbkit info E: --json    # JSON output
```

### `flash` — Write Image to USB

```bash
# Local files
wusbkit flash 2 --image ubuntu.img --yes
wusbkit flash E: --image recovery.bin --verify --hash

# Compressed (streaming decompression)
wusbkit flash 2 --image ubuntu.img.xz --yes
wusbkit flash 2 --image raspios.img.gz --yes
wusbkit flash 2 --image arch.img.zst --yes

# From URL (streams without downloading)
wusbkit flash 2 --image https://example.com/image.img --yes

# Parallel flash (same image to multiple drives)
wusbkit flash 2,3,4,5 --image ubuntu.img --parallel --yes
wusbkit flash 2-6 --image recovery.bin --parallel --max-concurrent 3 --yes

# All options
wusbkit flash 2 --image file.img --yes --verify --hash --skip-unchanged --buffer 8M
```

**Supported sources:** `.img`, `.bin`, `.iso`, `.raw`, `.gz`, `.xz`, `.zst`, `.zip`, HTTP/HTTPS URLs

### `create` — Create Image from USB

```bash
wusbkit create E: --output backup.bin --yes
wusbkit create 2 --output D:\images\chromeos.bin --yes --json
```

Creates an ImageUSB-compatible `.bin` file with a 512-byte header containing MD5 and SHA1 checksums. A companion `.log` file is generated alongside the image.

### `format` — Format USB Drive

```bash
wusbkit format 2 --fs fat32 --yes                        # FAT32 (no 32GB limit)
wusbkit format E: --fs ntfs --label "DATA" --yes          # NTFS
wusbkit format 2 --fs exfat --yes                         # exFAT
wusbkit format 2,3,4 --fs fat32 --parallel --yes          # Parallel
```

| Filesystem | Max File Size | Cross-Platform | Notes |
|------------|--------------|----------------|-------|
| FAT32 | 4 GB | Excellent | Custom formatter bypasses Windows 32GB limit |
| NTFS | 16 EB | Windows | Full permissions support |
| exFAT | 16 EB | Good | Large files + cross-platform |

### `eject` — Safely Eject

```bash
wusbkit eject E:          # By drive letter
wusbkit eject 2           # By disk number
wusbkit eject E: --yes    # Skip confirmation
```

### `label` — Set Volume Label

```bash
wusbkit label E: --name "BACKUP"
wusbkit label E,F,G --name "USB" --parallel     # Multiple drives
```

> Does not require administrator privileges for USB drives.

### `version` — Show Version

```bash
wusbkit version
wusbkit version --json
```

## Global Flags

| Flag | Short | Description |
|------|-------|-------------|
| `--json` | `-j` | JSON output for programmatic use |
| `--verbose` | `-v` | Verbose output |
| `--no-color` | | Disable colored output |

## Multi-Disk Syntax

All parallel commands accept flexible disk specifications:

| Syntax | Example | Meaning |
|--------|---------|---------|
| Single | `2` or `E:` | One drive |
| List | `2,3,4` | Specific drives |
| Range | `2-6` | Drives 2 through 6 |
| Mixed | `2,4-6,8` | Drives 2, 4, 5, 6, 8 |

## JSON API

All commands support `--json` for integration with external tools.

- **stdout**: JSON data and NDJSON progress streams
- **stderr**: JSON error objects `{"error": "...", "code": "..."}`
- **Exit 0**: Success, **Exit 1**: Error

### Error Codes

| Code | Description |
|------|-------------|
| `USB_NOT_FOUND` | Device not found |
| `FORMAT_FAILED` | Format operation failed |
| `FLASH_FAILED` | Flash operation failed |
| `PERMISSION_DENIED` | Admin privileges required |
| `INVALID_INPUT` | Invalid arguments |
| `DISK_BUSY` | Another operation in progress |
| `INTERNAL_ERROR` | Unexpected error |

### Progress Streaming (NDJSON)

Flash and format operations emit line-delimited JSON progress:

```json
{"stage":"Writing","percentage":45,"bytes_written":2348810240,"total_bytes":5170026496,"speed":"48.2 MB/s","status":"in_progress"}
{"stage":"Verifying","percentage":90,"bytes_written":4653023846,"total_bytes":5170026496,"speed":"52.1 MB/s","status":"in_progress"}
{"stage":"Complete","percentage":100,"status":"complete","hash":"c7425a15..."}
```

Parallel operations emit per-disk events:

```json
{"type":"start","diskNumber":2,"operation":"flash"}
{"type":"complete","diskNumber":2,"success":true,"duration":"1m45s"}
{"type":"summary","total":4,"succeeded":4,"failed":0}
```

## Architecture

```
wusbkit/
├── cmd/                    # CLI commands (Cobra)
│   ├── create.go           # create command
│   ├── eject.go            # eject command (IOCTL_STORAGE_EJECT_MEDIA)
│   ├── flash.go            # flash command
│   ├── format.go           # format command
│   ├── label.go            # label command (SetVolumeLabelW)
│   ├── list.go             # list command
│   ├── info.go             # info command
│   └── version.go          # version command
├── internal/
│   ├── disk/               # Native Win32 disk operations
│   │   ├── ioctl.go        # DeviceIoControl wrappers
│   │   ├── format_fat32.go # Custom FAT32 formatter (BPB + FAT tables)
│   │   ├── format_vds.go   # NTFS/exFAT via fmifs.dll + VDS COM
│   │   ├── extend.go       # Partition extension and creation
│   │   ├── bitlocker.go    # BitLocker detection (WMI)
│   │   └── volume.go       # Volume label operations
│   ├── flash/              # Image flashing
│   │   ├── flash.go        # Flash orchestration + retry + speed test
│   │   ├── source.go       # Image sources (file, zip, URL, compressed, .bin)
│   │   └── writer.go       # Raw disk writer + buffer pooling
│   ├── format/             # Format orchestration
│   │   └── format.go       # High-level format pipeline
│   ├── image/              # ImageUSB .bin format
│   │   ├── header.go       # 512-byte header codec
│   │   └── create.go       # USB-to-image creation
│   ├── iso/                # ISO bootable USB pipeline
│   │   ├── pipeline.go     # ISO write orchestrator
│   │   ├── bootloader.go   # Bootloader detection + MBR writing
│   │   └── mbr/            # Embedded MBR templates (GRUB2, Syslinux, Windows)
│   ├── encoding/           # Shared encoding utilities
│   │   └── utf16le.go      # UTF-16LE codec
│   ├── usb/                # USB device enumeration
│   │   ├── device.go       # Device data models
│   │   ├── enumerate.go    # Enumeration with caching
│   │   ├── enumerate_native.go  # Native WMI (parallel queries)
│   │   └── location_windows.go  # USB hub port via cfgmgr32
│   ├── parallel/           # Parallel operations
│   │   └── executor.go     # Batch format/flash/label with NDJSON
│   ├── lock/               # Disk locking
│   │   └── disklock.go     # File-based cross-process locks
│   └── output/             # Display helpers
│       ├── json.go         # JSON output + error codes
│       └── table.go        # pterm table formatters
└── main.go                 # Entry point
```

## How It Works

All disk operations use **native Windows APIs** — no PowerShell, no diskpart, no external processes:

| Operation | API Used |
|-----------|----------|
| Device enumeration | WMI (Win32_DiskDrive, MSFT_Partition) |
| Raw disk I/O | CreateFileW + ReadFile/WriteFile (unbuffered, 4KB aligned) |
| Volume locking | FSCTL_LOCK_VOLUME + FSCTL_DISMOUNT_VOLUME |
| Partition creation | IOCTL_DISK_CREATE_DISK + IOCTL_DISK_SET_DRIVE_LAYOUT_EX |
| FAT32 formatting | Custom sector writer (BPB, FSInfo, FAT tables) |
| NTFS/exFAT formatting | fmifs.dll FormatEx (VDS COM fallback) |
| Partition extension | IOCTL_DISK_GROW_PARTITION + FSCTL_EXTEND_VOLUME |
| Eject | IOCTL_STORAGE_EJECT_MEDIA |
| Volume label | SetVolumeLabelW |
| BitLocker detection | WMI (Win32_EncryptableVolume) |
| Hub port location | cfgmgr32.dll (DEVPKEY_Device_LocationInfo) |

## License

Apache 2.0
