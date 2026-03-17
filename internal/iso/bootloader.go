// Package iso implements the ISO-to-USB writing pipeline, including bootloader
// detection, MBR bootstrap writing, and ISO content extraction.
package iso

import (
	"embed"
	"fmt"
	"io/fs"
	"strings"

	"golang.org/x/sys/windows"
)

// MBR layout constants from the ImageUSB decompilation.
const (
	mbrBootstrapSize  = 0x1B8 // 440 bytes: bootstrap code region
	mbrPartTableStart = 0x1BE // 446 bytes: start of partition table
	mbrSignatureOff   = 0x1FE // 510 bytes: boot signature (0x55AA)
	sectorSize        = 512
)

//go:embed mbr/*.mbr
var mbrTemplates embed.FS

// BootloaderType represents the detected bootloader in an ISO image.
type BootloaderType int

const (
	BootloaderWindows  BootloaderType = iota // Default fallback
	BootloaderGRUB2                          // /boot/grub/i386-pc/ directory present
	BootloaderSyslinux                       // syslinux.cfg present
	BootloaderGRUB4DOS                       // grub.cfg present (without i386-pc parent)
)

// String returns a human-readable name for the bootloader type.
func (b BootloaderType) String() string {
	switch b {
	case BootloaderGRUB2:
		return "GRUB2"
	case BootloaderSyslinux:
		return "Syslinux"
	case BootloaderGRUB4DOS:
		return "GRUB4DOS"
	default:
		return "Windows"
	}
}

// mbrFileName maps each bootloader type to its embedded MBR template file.
func (b BootloaderType) mbrFileName() string {
	switch b {
	case BootloaderGRUB2:
		return "mbr/grub2.mbr"
	case BootloaderSyslinux:
		return "mbr/syslinux.mbr"
	case BootloaderGRUB4DOS:
		return "mbr/grub4dos.mbr"
	default:
		return "mbr/windows.mbr"
	}
}

// isoScanResult captures what was found while walking the ISO filesystem.
// Used by DetectBootloader and scanISOContents.
type isoScanResult struct {
	HasGRUB2i386PC bool // /boot/grub/i386-pc/ directory exists
	HasSyslinuxCfg bool // syslinux.cfg exists anywhere
	HasGrubCfg     bool // grub.cfg exists (without i386-pc parent)
	HasLargeFile   bool // any file > 4 GB
}

// classifyBootloader determines the bootloader type from scan results.
// The priority order matches the ImageUSB decompilation:
// Syslinux > GRUB2 > GRUB4DOS > Windows.
func (r *isoScanResult) classifyBootloader() BootloaderType {
	if r.HasSyslinuxCfg {
		return BootloaderSyslinux
	}
	if r.HasGRUB2i386PC {
		return BootloaderGRUB2
	}
	if r.HasGrubCfg {
		return BootloaderGRUB4DOS
	}
	return BootloaderWindows
}

// classifyPath updates the scan result based on a single file/directory path
// found in the ISO. Paths are expected with forward slashes and no leading slash.
func (r *isoScanResult) classifyPath(path string, isDir bool, fileSize int64) {
	lower := strings.ToLower(path)

	if isDir && strings.Contains(lower, "boot/grub/i386-pc") {
		r.HasGRUB2i386PC = true
	}

	base := lower
	if idx := strings.LastIndex(lower, "/"); idx >= 0 {
		base = lower[idx+1:]
	}

	if !isDir && base == "syslinux.cfg" {
		r.HasSyslinuxCfg = true
	}

	// grub.cfg without i386-pc parent means GRUB4DOS, not GRUB2.
	if !isDir && base == "grub.cfg" && !strings.Contains(lower, "i386-pc") {
		r.HasGrubCfg = true
	}

	if !isDir && fileSize > 4*1024*1024*1024 {
		r.HasLargeFile = true
	}
}

// WriteMBR writes the appropriate MBR bootstrap code to sector 0 of the disk.
// It preserves the existing partition table (bytes 446-509) and disk signature
// (bytes 440-445), then ensures the boot signature (0x55AA) is set and the
// first partition is marked active (bootable flag 0x80).
func WriteMBR(diskHandle windows.Handle, bootType BootloaderType) error {
	// Read the MBR template from embedded files.
	templateData, err := fs.ReadFile(mbrTemplates, bootType.mbrFileName())
	if err != nil {
		return fmt.Errorf("read MBR template %s: %w", bootType.mbrFileName(), err)
	}

	// Read the current sector 0 from the disk.
	currentMBR := make([]byte, sectorSize)
	if _, err := readSector(diskHandle, 0, currentMBR); err != nil {
		return fmt.Errorf("read current MBR: %w", err)
	}

	// Copy only the bootstrap code (first 440 bytes) from the template.
	// The template files are 423-440 bytes, so copy up to what's available
	// without exceeding mbrBootstrapSize.
	bootstrapLen := len(templateData)
	if bootstrapLen > mbrBootstrapSize {
		bootstrapLen = mbrBootstrapSize
	}
	copy(currentMBR[:bootstrapLen], templateData[:bootstrapLen])

	// Bytes 440-445 (disk signature + reserved) are left unchanged.
	// Bytes 446-509 (partition table) are left unchanged.

	// Set boot signature at bytes 510-511.
	currentMBR[mbrSignatureOff] = 0x55
	currentMBR[mbrSignatureOff+1] = 0xAA

	// Mark the first partition as active (bootable).
	currentMBR[mbrPartTableStart] = 0x80

	// Write the modified sector back to disk.
	if _, err := writeSector(diskHandle, 0, currentMBR); err != nil {
		return fmt.Errorf("write MBR: %w", err)
	}

	return nil
}

// readSector reads one sector from the disk at the given sector number.
func readSector(handle windows.Handle, sectorNum int64, buf []byte) (int, error) {
	offset := sectorNum * sectorSize
	if _, err := windows.Seek(handle, offset, 0); err != nil {
		return 0, fmt.Errorf("seek to sector %d: %w", sectorNum, err)
	}
	var read uint32
	if err := windows.ReadFile(handle, buf, &read, nil); err != nil {
		return int(read), fmt.Errorf("read sector %d: %w", sectorNum, err)
	}
	return int(read), nil
}

// writeSector writes one sector to the disk at the given sector number.
func writeSector(handle windows.Handle, sectorNum int64, buf []byte) (int, error) {
	offset := sectorNum * sectorSize
	if _, err := windows.Seek(handle, offset, 0); err != nil {
		return 0, fmt.Errorf("seek to sector %d: %w", sectorNum, err)
	}
	var written uint32
	if err := windows.WriteFile(handle, buf, &written, nil); err != nil {
		return int(written), fmt.Errorf("write sector %d: %w", sectorNum, err)
	}
	return int(written), nil
}
