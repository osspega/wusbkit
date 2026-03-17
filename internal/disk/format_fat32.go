package disk

import (
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

// FormatFAT32Options configures the custom FAT32 format operation.
// This formatter writes directly to disk sectors, bypassing the Windows
// 32GB FAT32 size limit by constructing the BPB, FAT tables, and root
// directory manually.
type FormatFAT32Options struct {
	DiskHandle        windows.Handle // Already-opened physical disk handle
	PartitionOffset   int64          // Start offset of the partition in bytes
	PartitionSize     int64          // Size of the partition in bytes
	VolumeLabel       string         // Volume label (max 11 chars, uppercased)
	BytesPerSector    uint32         // From disk geometry (usually 512)
	SectorsPerTrack   uint32         // From disk geometry
	TracksPerCylinder uint32         // From disk geometry (heads)
	HiddenSectors     uint32         // Partition start in sectors
}

const (
	fat32ReservedSectors   = 32
	fat32NumFATs           = 2
	fat32RootCluster       = 2
	fat32FSInfoSector      = 1
	fat32BackupBootSector  = 6
	fat32MediaType         = 0xF8
	fat32DriveNumber       = 0x80
	fat32BootSignatureByte = 0x29
	fat32MinClusters       = 65526
	fat32MaxClusters       = 0x0FFFFFF5
)

// FormatFAT32 formats a partition as FAT32 by writing the BPB, FSInfo,
// backup boot sector, both FAT copies, and an empty root directory cluster
// directly to disk sectors. This bypasses the Windows 32GB FAT32 limitation.
func FormatFAT32(opts FormatFAT32Options) error {
	if err := validateFAT32Options(opts); err != nil {
		return fmt.Errorf("invalid format options: %w", err)
	}

	bps := opts.BytesPerSector
	totalSectors := uint32(opts.PartitionSize / int64(bps))
	sectorsPerCluster := calculateSectorsPerCluster(opts.PartitionSize, bps)

	fatSizeSectors := calculateFATSize(totalSectors, fat32ReservedSectors, sectorsPerCluster, bps)
	dataStartSector := fat32ReservedSectors + (fatSizeSectors * fat32NumFATs)
	totalClusters := (totalSectors - dataStartSector) / sectorsPerCluster

	if totalClusters < fat32MinClusters {
		return fmt.Errorf("partition too small for FAT32: %d clusters (minimum %d)", totalClusters, fat32MinClusters)
	}
	if totalClusters > fat32MaxClusters {
		return fmt.Errorf("partition too large for FAT32: %d clusters (maximum %d)", totalClusters, fat32MaxClusters)
	}

	label := formatVolumeLabel(opts.VolumeLabel)
	serialNumber := generateVolumeSerial()

	params := &fat32Params{
		bytesPerSector:    bps,
		sectorsPerCluster: sectorsPerCluster,
		totalSectors:      totalSectors,
		fatSizeSectors:    fatSizeSectors,
		dataStartSector:   dataStartSector,
		totalClusters:     totalClusters,
		serialNumber:      serialNumber,
		label:             label,
		sectorsPerTrack:   opts.SectorsPerTrack,
		tracksPerCylinder: opts.TracksPerCylinder,
		hiddenSectors:     opts.HiddenSectors,
	}

	w := &sectorWriter{
		handle:          opts.DiskHandle,
		partitionOffset: opts.PartitionOffset,
		bytesPerSector:  bps,
	}

	if err := writeBPB(w, params); err != nil {
		return fmt.Errorf("failed to write boot sector: %w", err)
	}

	if err := writeBackupBPB(w, params); err != nil {
		return fmt.Errorf("failed to write backup boot sector: %w", err)
	}

	if err := writeFSInfo(w, params); err != nil {
		return fmt.Errorf("failed to write FSInfo sector: %w", err)
	}

	if err := writeFATTables(w, params); err != nil {
		return fmt.Errorf("failed to write FAT tables: %w", err)
	}

	if err := writeRootDirectory(w, params); err != nil {
		return fmt.Errorf("failed to write root directory: %w", err)
	}

	return nil
}

// fat32Params holds the computed FAT32 filesystem parameters.
type fat32Params struct {
	bytesPerSector    uint32
	sectorsPerCluster uint32
	totalSectors      uint32
	fatSizeSectors    uint32
	dataStartSector   uint32
	totalClusters     uint32
	serialNumber      uint32
	label             [11]byte
	sectorsPerTrack   uint32
	tracksPerCylinder uint32
	hiddenSectors     uint32
}

// sectorWriter writes aligned sector data to a disk handle at a given
// partition offset.
type sectorWriter struct {
	handle          windows.Handle
	partitionOffset int64
	bytesPerSector  uint32
}

// writeSector writes a single sector-sized buffer at the given sector number
// relative to the partition start.
func (w *sectorWriter) writeSector(sectorNum uint32, data []byte) error {
	offset := w.partitionOffset + int64(sectorNum)*int64(w.bytesPerSector)
	if _, err := windows.Seek(w.handle, offset, 0); err != nil {
		return fmt.Errorf("seek to sector %d (offset %d): %w", sectorNum, offset, err)
	}
	var written uint32
	if err := windows.WriteFile(w.handle, data, &written, nil); err != nil {
		return fmt.Errorf("write sector %d: %w", sectorNum, err)
	}
	if written != uint32(len(data)) {
		return fmt.Errorf("short write at sector %d: wrote %d of %d bytes", sectorNum, written, len(data))
	}
	return nil
}

// writeSectors writes multiple contiguous sectors starting at sectorNum.
func (w *sectorWriter) writeSectors(sectorNum uint32, data []byte) error {
	offset := w.partitionOffset + int64(sectorNum)*int64(w.bytesPerSector)
	if _, err := windows.Seek(w.handle, offset, 0); err != nil {
		return fmt.Errorf("seek to sector %d (offset %d): %w", sectorNum, offset, err)
	}
	var written uint32
	if err := windows.WriteFile(w.handle, data, &written, nil); err != nil {
		return fmt.Errorf("write at sector %d: %w", sectorNum, err)
	}
	if written != uint32(len(data)) {
		return fmt.Errorf("short write at sector %d: wrote %d of %d bytes", sectorNum, written, len(data))
	}
	return nil
}

// validateFAT32Options checks that all required fields are set and sensible.
func validateFAT32Options(opts FormatFAT32Options) error {
	if opts.DiskHandle == windows.InvalidHandle {
		return fmt.Errorf("invalid disk handle")
	}
	if opts.PartitionSize <= 0 {
		return fmt.Errorf("partition size must be positive")
	}
	if opts.BytesPerSector == 0 || opts.BytesPerSector&(opts.BytesPerSector-1) != 0 {
		return fmt.Errorf("bytes per sector must be a power of 2, got %d", opts.BytesPerSector)
	}
	if opts.PartitionOffset < 0 {
		return fmt.Errorf("partition offset must be non-negative")
	}
	return nil
}

// calculateSectorsPerCluster determines the cluster size based on partition
// capacity, following the ImageUSB algorithm thresholds.
func calculateSectorsPerCluster(partitionSize int64, bytesPerSector uint32) uint32 {
	const (
		mb = 1 << 20
		gb = 1 << 30
	)

	var clusterSize uint32
	switch {
	case partitionSize <= 64*mb:
		clusterSize = 512
	case partitionSize <= 128*mb:
		clusterSize = 1024
	case partitionSize <= 256*mb:
		clusterSize = 2048
	case partitionSize <= 8*gb:
		clusterSize = 4096
	case partitionSize <= 16*gb:
		clusterSize = 8192
	case partitionSize <= 32*gb:
		clusterSize = 16384
	default:
		clusterSize = 32768
	}

	spc := clusterSize / bytesPerSector
	if spc < 1 {
		spc = 1
	}
	return spc
}

// calculateFATSize computes the number of sectors needed for one FAT table.
// Uses the standard FAT32 calculation from the Microsoft FAT specification:
//
//	fatSize = ceil((totalSectors - reservedSectors) / (sectorsPerCluster * 128 + 1))
//
// where 128 = bytesPerSector / 4 (each FAT32 entry is 4 bytes, so one sector
// holds bytesPerSector/4 entries). The result is rounded up to a whole number
// of sectors.
func calculateFATSize(totalSectors, reservedSectors, sectorsPerCluster, bytesPerSector uint32) uint32 {
	// Number of FAT entries that fit in one sector
	entriesPerSector := bytesPerSector / 4

	// Numerator: total data sectors that need FAT coverage
	numerator := uint64(totalSectors - reservedSectors)

	// Denominator: how many data sectors one FAT sector can cover
	// Each FAT sector maps entriesPerSector clusters, each cluster is
	// sectorsPerCluster data sectors. Add 1 for the FAT sector itself
	// (accounts for both FAT copies via the iterative approximation).
	denominator := uint64(sectorsPerCluster)*uint64(entriesPerSector) + uint64(fat32NumFATs)

	// Ceiling division
	fatSize := (numerator + denominator - 1) / denominator

	return uint32(fatSize)
}

// buildBPBSector constructs a 512-byte FAT32 boot sector (BPB).
func buildBPBSector(p *fat32Params) []byte {
	bpb := make([]byte, p.bytesPerSector)

	// Jump instruction
	bpb[0] = 0xEB
	bpb[1] = 0x58
	bpb[2] = 0x90

	// OEM Name (bytes 3-10)
	copy(bpb[3:11], []byte("MSDOS5.0"))

	// BPB fields
	binary.LittleEndian.PutUint16(bpb[11:13], uint16(p.bytesPerSector))
	bpb[13] = byte(p.sectorsPerCluster)
	binary.LittleEndian.PutUint16(bpb[14:16], fat32ReservedSectors)
	bpb[16] = fat32NumFATs
	binary.LittleEndian.PutUint16(bpb[17:19], 0) // RootEntryCount (0 for FAT32)
	binary.LittleEndian.PutUint16(bpb[19:21], 0) // TotalSectors16 (0 for FAT32)
	bpb[21] = fat32MediaType
	binary.LittleEndian.PutUint16(bpb[22:24], 0) // FATSize16 (0 for FAT32)
	binary.LittleEndian.PutUint16(bpb[24:26], uint16(p.sectorsPerTrack))
	binary.LittleEndian.PutUint16(bpb[26:28], uint16(p.tracksPerCylinder))
	binary.LittleEndian.PutUint32(bpb[28:32], p.hiddenSectors)
	binary.LittleEndian.PutUint32(bpb[32:36], p.totalSectors)

	// FAT32-specific extended BPB
	binary.LittleEndian.PutUint32(bpb[36:40], p.fatSizeSectors)
	binary.LittleEndian.PutUint16(bpb[40:42], 0) // ExtFlags
	binary.LittleEndian.PutUint16(bpb[42:44], 0) // FSVersion
	binary.LittleEndian.PutUint32(bpb[44:48], fat32RootCluster)
	binary.LittleEndian.PutUint16(bpb[48:50], fat32FSInfoSector)
	binary.LittleEndian.PutUint16(bpb[50:52], fat32BackupBootSector)
	// Bytes 52-63: Reserved (already zero)

	bpb[64] = fat32DriveNumber // DriveNumber (0x80 for hard disk)
	bpb[65] = 0                // Reserved1
	bpb[66] = fat32BootSignatureByte

	binary.LittleEndian.PutUint32(bpb[67:71], p.serialNumber)
	copy(bpb[71:82], p.label[:])
	copy(bpb[82:90], []byte("FAT32   "))

	// Boot sector signature at bytes 510-511
	bpb[510] = 0x55
	bpb[511] = 0xAA

	return bpb
}

// writeBPB writes the primary boot sector (BPB) to sector 0 of the partition.
func writeBPB(w *sectorWriter, p *fat32Params) error {
	bpb := buildBPBSector(p)
	return w.writeSector(0, bpb)
}

// writeBackupBPB writes a backup copy of the boot sector to sector 6.
func writeBackupBPB(w *sectorWriter, p *fat32Params) error {
	bpb := buildBPBSector(p)
	return w.writeSector(fat32BackupBootSector, bpb)
}

// buildFSInfoSector constructs the FAT32 FSInfo sector (sector 1).
func buildFSInfoSector(p *fat32Params) []byte {
	fsinfo := make([]byte, p.bytesPerSector)

	// Lead signature
	binary.LittleEndian.PutUint32(fsinfo[0:4], 0x41615252)

	// Struct signature (offset 484)
	binary.LittleEndian.PutUint32(fsinfo[484:488], 0x61417272)

	// Free cluster count: total clusters minus 1 (root directory uses cluster 2)
	binary.LittleEndian.PutUint32(fsinfo[488:492], p.totalClusters-1)

	// Next free cluster hint (cluster 3, since cluster 2 is root directory)
	binary.LittleEndian.PutUint32(fsinfo[492:496], 3)

	// Trail signature at bytes 510-511 (bytes 508-509 are reserved/zero)
	fsinfo[510] = 0x55
	fsinfo[511] = 0xAA

	return fsinfo
}

// writeFSInfo writes the FSInfo structure to sector 1 of the partition.
func writeFSInfo(w *sectorWriter, p *fat32Params) error {
	fsinfo := buildFSInfoSector(p)
	return w.writeSector(fat32FSInfoSector, fsinfo)
}

// writeFATTables writes both FAT table copies. Each FAT has three initialized
// entries: the media byte marker, the end-of-chain marker, and the root
// directory end-of-chain marker. All remaining entries are zero (free).
func writeFATTables(w *sectorWriter, p *fat32Params) error {
	// Build a single FAT table in memory. For very large partitions the FAT
	// can be many megabytes, so we write it in chunks. However, only the
	// first 12 bytes (3 uint32 entries) are non-zero, so we only need to
	// construct the first sector specially and zero-fill the rest.
	firstSector := make([]byte, p.bytesPerSector)

	// Entry 0: media byte in low 8 bits, rest 0xFF (0x0FFFFFF8)
	binary.LittleEndian.PutUint32(firstSector[0:4], 0x0FFFFFF8)
	// Entry 1: end-of-chain marker (0x0FFFFFFF)
	binary.LittleEndian.PutUint32(firstSector[4:8], 0x0FFFFFFF)
	// Entry 2: root directory cluster end-of-chain (0x0FFFFFFF)
	binary.LittleEndian.PutUint32(firstSector[8:12], 0x0FFFFFFF)

	// Write FAT1 and FAT2
	for fatIndex := uint32(0); fatIndex < fat32NumFATs; fatIndex++ {
		fatStartSector := fat32ReservedSectors + (fatIndex * p.fatSizeSectors)

		// Write the first sector with the initialized entries
		if err := w.writeSector(fatStartSector, firstSector); err != nil {
			return fmt.Errorf("FAT%d first sector: %w", fatIndex+1, err)
		}

		// Zero-fill remaining FAT sectors in chunks for efficiency
		remainingSectors := p.fatSizeSectors - 1
		if remainingSectors > 0 {
			if err := writeZeroSectors(w, fatStartSector+1, remainingSectors, p.bytesPerSector); err != nil {
				return fmt.Errorf("FAT%d zero-fill: %w", fatIndex+1, err)
			}
		}
	}

	return nil
}

// writeZeroSectors writes count sectors of zeros starting at startSector.
// Uses a chunked approach (up to 1 MB at a time) to balance memory usage
// and I/O efficiency.
func writeZeroSectors(w *sectorWriter, startSector, count, bytesPerSector uint32) error {
	const maxChunkBytes = 1 << 20 // 1 MB

	maxSectorsPerChunk := maxChunkBytes / bytesPerSector
	if maxSectorsPerChunk == 0 {
		maxSectorsPerChunk = 1
	}

	zeroBuf := make([]byte, maxSectorsPerChunk*bytesPerSector)

	sector := startSector
	remaining := count
	for remaining > 0 {
		batch := remaining
		if batch > maxSectorsPerChunk {
			batch = maxSectorsPerChunk
		}

		writeLen := batch * bytesPerSector
		if err := w.writeSectors(sector, zeroBuf[:writeLen]); err != nil {
			return err
		}

		sector += batch
		remaining -= batch
	}

	return nil
}

// writeRootDirectory zeroes out the first cluster of the data area, which
// serves as the empty root directory for the new filesystem.
func writeRootDirectory(w *sectorWriter, p *fat32Params) error {
	clusterBytes := p.sectorsPerCluster * p.bytesPerSector
	zeroBuf := make([]byte, clusterBytes)
	return w.writeSectors(p.dataStartSector, zeroBuf)
}

// formatVolumeLabel converts a string into an 11-byte FAT volume label,
// uppercased and padded with spaces.
func formatVolumeLabel(label string) [11]byte {
	var result [11]byte
	for i := range result {
		result[i] = ' '
	}

	upper := strings.ToUpper(label)
	n := len(upper)
	if n > 11 {
		n = 11
	}
	copy(result[:n], upper[:n])

	return result
}

// generateVolumeSerial creates a pseudo-random volume serial number derived
// from the current system time, matching the ImageUSB approach of using
// date/time components.
func generateVolumeSerial() uint32 {
	now := time.Now()

	// Combine date and time components into a 32-bit serial number.
	// The low word uses month+day combined with seconds+centiseconds.
	// The high word uses hours+minutes combined with year.
	low := uint16(now.Month())<<8 | uint16(now.Day())
	low += uint16(now.Second())*100 + uint16(now.Nanosecond()/10_000_000)

	high := uint16(now.Hour())<<8 | uint16(now.Minute())
	high += uint16(now.Year())

	// Mix in a random component for uniqueness when formatting multiple
	// drives within the same second.
	serial := uint32(high)<<16 | uint32(low)
	serial ^= rand.Uint32()

	return serial
}
