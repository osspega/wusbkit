// Package disk provides low-level Windows disk IOCTL wrappers for direct
// disk management without PowerShell. It supports MBR partition table creation,
// partition layout manipulation, volume locking, and geometry queries.
package disk

import (
	"encoding/binary"
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows IOCTL control codes for disk and volume management.
const (
	IOCTL_DISK_GET_DRIVE_GEOMETRY_EX = 0x000700A0
	IOCTL_DISK_GET_DRIVE_LAYOUT_EX   = 0x00070050
	IOCTL_DISK_CREATE_DISK           = 0x0007C058
	IOCTL_DISK_SET_DRIVE_LAYOUT_EX   = 0x0007C054
	IOCTL_DISK_UPDATE_PROPERTIES     = 0x00070140
	IOCTL_DISK_GROW_PARTITION        = 0x0007C0D0

	IOCTL_STORAGE_EJECT_MEDIA = 0x002D4808

	FSCTL_LOCK_VOLUME            = 0x00090018
	FSCTL_DISMOUNT_VOLUME        = 0x00090020
	FSCTL_ALLOW_EXTENDED_DASD_IO = 0x00090083
	FSCTL_EXTEND_VOLUME          = 0x000900A0
)

// Windows partition style constants.
const (
	PARTITION_STYLE_MBR = 0
	PARTITION_STYLE_GPT = 1
	PARTITION_STYLE_RAW = 2
)

// Common MBR partition type identifiers.
const (
	PARTITION_FAT32     = 0x0C // FAT32 with LBA
	PARTITION_NTFS      = 0x07 // NTFS / exFAT / HPFS
	PARTITION_FAT16_LBA = 0x0E
	PARTITION_EXTENDED  = 0x0F
)

// Maximum number of partitions supported in a single IOCTL buffer.
// MBR supports 4 primary partitions; we allocate enough for the IOCTL struct.
const maxPartitions = 128

// ---------------------------------------------------------------------------
// Raw Win32 structures (matching Windows SDK layout for DeviceIoControl)
// ---------------------------------------------------------------------------

// rawDiskGeometry maps to DISK_GEOMETRY.
type rawDiskGeometry struct {
	Cylinders         int64
	MediaType         uint32
	TracksPerCylinder uint32
	SectorsPerTrack   uint32
	BytesPerSector    uint32
}

// rawDiskGeometryEx maps to DISK_GEOMETRY_EX (only the fixed-size header).
// The variable-length detection and partition info are not needed for our use case.
type rawDiskGeometryEx struct {
	Geometry rawDiskGeometry
	DiskSize int64
}

// rawPartitionInformationMBR maps to PARTITION_INFORMATION_MBR.
type rawPartitionInformationMBR struct {
	PartitionType       byte
	BootIndicator       bool
	RecognizedPartition bool
	HiddenSectors       uint32
}

// rawPartitionInformationEx maps to PARTITION_INFORMATION_EX.
// The Mbr/Gpt union occupies 112 bytes at offset 32. We use the MBR variant
// since this package targets MBR disk operations.
type rawPartitionInformationEx struct {
	PartitionStyle   uint32
	StartingOffset   int64
	PartitionLength  int64
	PartitionNumber  uint32
	RewritePartition bool
	IsServicePartition bool
	_                [2]byte // padding to align the union
	Mbr              rawPartitionInformationMBR
	_                [100]byte // remaining union space (GPT variant is larger)
}

// rawDriveLayoutInformationMBR maps to the MBR member of the union in
// DRIVE_LAYOUT_INFORMATION_EX.
type rawDriveLayoutInformationMBR struct {
	Signature uint32
	CheckSum  uint32
}

// rawDriveLayoutInformationEx maps to DRIVE_LAYOUT_INFORMATION_EX.
// It contains a variable-length PartitionEntry array; we define it with
// maxPartitions entries so the buffer is large enough for any real disk.
type rawDriveLayoutInformationEx struct {
	PartitionStyle uint32
	PartitionCount uint32
	Mbr            rawDriveLayoutInformationMBR
	_              [32]byte // remaining union space (GPT variant)
	PartitionEntry [maxPartitions]rawPartitionInformationEx
}

// rawDiskGrowPartition maps to DISK_GROW_PARTITION.
type rawDiskGrowPartition struct {
	PartitionNumber int32
	BytesToGrow     int64
}

// ---------------------------------------------------------------------------
// Public helper types (clean Go representations)
// ---------------------------------------------------------------------------

// DiskGeometry holds the physical geometry and total size of a disk.
type DiskGeometry struct {
	DiskSize          int64
	BytesPerSector    uint32
	SectorsPerTrack   uint32
	TracksPerCylinder uint32
	Cylinders         int64
}

// DriveLayout describes the partition table of a disk.
type DriveLayout struct {
	PartitionStyle int32
	PartitionCount int32
	Partitions     []PartitionInfo
}

// PartitionInfo describes a single partition on a disk.
type PartitionInfo struct {
	PartitionNumber int32
	StartingOffset  int64
	Length          int64
	PartitionType   byte
	IsActive        bool
}

// MBRPartition describes a partition to create when calling SetDriveLayoutMBR.
type MBRPartition struct {
	PartitionType byte
	BootIndicator bool
	StartOffset   int64
	Size          int64
}

// ---------------------------------------------------------------------------
// Disk handle operations
// ---------------------------------------------------------------------------

// OpenPhysicalDisk opens \\.\PhysicalDriveN with read/write access for IOCTL
// operations. The caller is responsible for closing the returned handle.
func OpenPhysicalDisk(diskNumber int) (windows.Handle, error) {
	path := fmt.Sprintf(`\\.\PhysicalDrive%d`, diskNumber)
	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, fmt.Errorf("invalid disk path: %w", err)
	}

	handle, err := windows.CreateFile(
		pathPtr,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		0,
		0,
	)
	if err != nil {
		return windows.InvalidHandle, fmt.Errorf("open PhysicalDrive%d: %w", diskNumber, err)
	}
	return handle, nil
}

// ---------------------------------------------------------------------------
// Disk geometry
// ---------------------------------------------------------------------------

// GetDiskGeometry queries the physical geometry and total size of the disk.
func GetDiskGeometry(handle windows.Handle) (*DiskGeometry, error) {
	var geo rawDiskGeometryEx
	var bytesReturned uint32

	err := windows.DeviceIoControl(
		handle,
		IOCTL_DISK_GET_DRIVE_GEOMETRY_EX,
		nil, 0,
		(*byte)(unsafe.Pointer(&geo)),
		uint32(unsafe.Sizeof(geo)),
		&bytesReturned,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("IOCTL_DISK_GET_DRIVE_GEOMETRY_EX: %w", err)
	}

	return &DiskGeometry{
		DiskSize:          geo.DiskSize,
		BytesPerSector:    geo.Geometry.BytesPerSector,
		SectorsPerTrack:   geo.Geometry.SectorsPerTrack,
		TracksPerCylinder: geo.Geometry.TracksPerCylinder,
		Cylinders:         geo.Geometry.Cylinders,
	}, nil
}

// ---------------------------------------------------------------------------
// Drive layout (read)
// ---------------------------------------------------------------------------

// GetDriveLayout reads the partition table of the disk.
func GetDriveLayout(handle windows.Handle) (*DriveLayout, error) {
	var layout rawDriveLayoutInformationEx
	var bytesReturned uint32

	err := windows.DeviceIoControl(
		handle,
		IOCTL_DISK_GET_DRIVE_LAYOUT_EX,
		nil, 0,
		(*byte)(unsafe.Pointer(&layout)),
		uint32(unsafe.Sizeof(layout)),
		&bytesReturned,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("IOCTL_DISK_GET_DRIVE_LAYOUT_EX: %w", err)
	}

	count := int(layout.PartitionCount)
	if count > maxPartitions {
		count = maxPartitions
	}

	partitions := make([]PartitionInfo, 0, count)
	for i := 0; i < count; i++ {
		entry := &layout.PartitionEntry[i]
		// Windows may return "empty" entries with PartitionLength == 0; skip them.
		if entry.PartitionLength == 0 {
			continue
		}
		partitions = append(partitions, PartitionInfo{
			PartitionNumber: int32(entry.PartitionNumber),
			StartingOffset:  entry.StartingOffset,
			Length:          entry.PartitionLength,
			PartitionType:   entry.Mbr.PartitionType,
			IsActive:        entry.Mbr.BootIndicator,
		})
	}

	return &DriveLayout{
		PartitionStyle: int32(layout.PartitionStyle),
		PartitionCount: int32(len(partitions)),
		Partitions:     partitions,
	}, nil
}

// ---------------------------------------------------------------------------
// Disk initialization
// ---------------------------------------------------------------------------

// CreateMBRDisk initializes the disk with a fresh MBR partition table.
// The signature is a unique 32-bit value identifying the disk.
func CreateMBRDisk(handle windows.Handle, signature uint32) error {
	// CREATE_DISK: PartitionStyle (4) + union (20) = 24 bytes minimum.
	// Use raw buffer to guarantee correct size regardless of Go struct layout.
	var buf [24]byte
	binary.LittleEndian.PutUint32(buf[0:4], PARTITION_STYLE_MBR)
	binary.LittleEndian.PutUint32(buf[4:8], signature)

	var bytesReturned uint32
	err := windows.DeviceIoControl(
		handle,
		IOCTL_DISK_CREATE_DISK,
		&buf[0],
		uint32(len(buf)),
		nil, 0,
		&bytesReturned,
		nil,
	)
	if err != nil {
		return fmt.Errorf("IOCTL_DISK_CREATE_DISK: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Partition layout (write)
// ---------------------------------------------------------------------------

// SetDriveLayoutMBR writes an MBR partition table with the given partitions.
// The disk must have been initialized with CreateMBRDisk first.
func SetDriveLayoutMBR(handle windows.Handle, partitions []MBRPartition) error {
	if len(partitions) == 0 {
		return fmt.Errorf("at least one partition is required")
	}
	if len(partitions) > 4 {
		return fmt.Errorf("MBR supports at most 4 primary partitions, got %d", len(partitions))
	}

	// Build the raw layout structure. The buffer must contain exactly
	// the header + PartitionCount entries (padded to 4 for MBR).
	var layout rawDriveLayoutInformationEx
	layout.PartitionStyle = PARTITION_STYLE_MBR
	layout.PartitionCount = uint32(len(partitions))

	for i, p := range partitions {
		entry := &layout.PartitionEntry[i]
		entry.PartitionStyle = PARTITION_STYLE_MBR
		entry.StartingOffset = p.StartOffset
		entry.PartitionLength = p.Size
		entry.PartitionNumber = uint32(i + 1)
		entry.RewritePartition = true
		entry.Mbr.PartitionType = p.PartitionType
		entry.Mbr.BootIndicator = p.BootIndicator
		entry.Mbr.RecognizedPartition = true
	}

	// Calculate the actual size to send: header + N partition entries.
	// headerSize = offset of PartitionEntry field within the struct.
	headerSize := unsafe.Offsetof(layout.PartitionEntry)
	entrySize := unsafe.Sizeof(layout.PartitionEntry[0])
	totalSize := headerSize + entrySize*uintptr(len(partitions))

	var bytesReturned uint32
	err := windows.DeviceIoControl(
		handle,
		IOCTL_DISK_SET_DRIVE_LAYOUT_EX,
		(*byte)(unsafe.Pointer(&layout)),
		uint32(totalSize),
		nil, 0,
		&bytesReturned,
		nil,
	)
	if err != nil {
		return fmt.Errorf("IOCTL_DISK_SET_DRIVE_LAYOUT_EX: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Disk property refresh
// ---------------------------------------------------------------------------

// UpdateDiskProperties tells the kernel to re-read the disk's partition table.
// Call this after modifying the partition layout so that volumes are re-enumerated.
func UpdateDiskProperties(handle windows.Handle) error {
	var bytesReturned uint32
	err := windows.DeviceIoControl(
		handle,
		IOCTL_DISK_UPDATE_PROPERTIES,
		nil, 0, nil, 0,
		&bytesReturned,
		nil,
	)
	if err != nil {
		return fmt.Errorf("IOCTL_DISK_UPDATE_PROPERTIES: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Partition growth
// ---------------------------------------------------------------------------

// GrowPartition extends a partition by the specified number of bytes.
// The extra space must already be available (unallocated) immediately after
// the partition on disk.
func GrowPartition(handle windows.Handle, partitionNumber int32, bytesToGrow int64) error {
	grow := rawDiskGrowPartition{
		PartitionNumber: partitionNumber,
		BytesToGrow:     bytesToGrow,
	}
	var bytesReturned uint32

	err := windows.DeviceIoControl(
		handle,
		IOCTL_DISK_GROW_PARTITION,
		(*byte)(unsafe.Pointer(&grow)),
		uint32(unsafe.Sizeof(grow)),
		nil, 0,
		&bytesReturned,
		nil,
	)
	if err != nil {
		return fmt.Errorf("IOCTL_DISK_GROW_PARTITION: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Volume FSCTL operations
// ---------------------------------------------------------------------------

// ExtendVolume extends a file system volume to fill additional sectors.
// The volumeHandle must be an open handle to the volume (e.g., \\.\E:).
// newSectors is the total number of additional sectors to add to the volume.
func ExtendVolume(volumeHandle windows.Handle, newSectors int64) error {
	var bytesReturned uint32
	err := windows.DeviceIoControl(
		volumeHandle,
		FSCTL_EXTEND_VOLUME,
		(*byte)(unsafe.Pointer(&newSectors)),
		uint32(unsafe.Sizeof(newSectors)),
		nil, 0,
		&bytesReturned,
		nil,
	)
	if err != nil {
		return fmt.Errorf("FSCTL_EXTEND_VOLUME: %w", err)
	}
	return nil
}

// LockVolume acquires an exclusive lock on a volume, preventing other
// processes from accessing it. The handle must be a volume handle
// (e.g., \\.\E:).
func LockVolume(handle windows.Handle) error {
	var bytesReturned uint32
	err := windows.DeviceIoControl(
		handle,
		FSCTL_LOCK_VOLUME,
		nil, 0, nil, 0,
		&bytesReturned,
		nil,
	)
	if err != nil {
		return fmt.Errorf("FSCTL_LOCK_VOLUME: %w", err)
	}
	return nil
}

// DismountVolume dismounts the file system on a volume, invalidating all
// open file handles. The volume should be locked first with LockVolume.
func DismountVolume(handle windows.Handle) error {
	var bytesReturned uint32
	err := windows.DeviceIoControl(
		handle,
		FSCTL_DISMOUNT_VOLUME,
		nil, 0, nil, 0,
		&bytesReturned,
		nil,
	)
	if err != nil {
		return fmt.Errorf("FSCTL_DISMOUNT_VOLUME: %w", err)
	}
	return nil
}
