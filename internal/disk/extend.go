package disk

import (
	"fmt"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// ExtendPartitionOptions configures the partition extension.
type ExtendPartitionOptions struct {
	DiskNumber int
}

// AddNewPartitionOptions configures new partition creation.
type AddNewPartitionOptions struct {
	DiskNumber int
	FileSystem string // "NTFS" (default), could support "FAT32" too
	Label      string
}

// minExtendBytes is the minimum unallocated space required to bother
// extending a partition (1 MB).
const minExtendBytes = 1 << 20

// minNewPartitionBytes is the minimum unallocated space required to create
// a useful new partition (10 MB).
const minNewPartitionBytes = 10 << 20

// ExtendPartition extends the last partition on a disk to fill remaining
// unallocated space. Only works for NTFS partitions. Returns the number of
// bytes the partition grew by, or 0 if there was nothing to extend.
func ExtendPartition(opts ExtendPartitionOptions) (int64, error) {
	diskHandle, err := OpenPhysicalDisk(opts.DiskNumber)
	if err != nil {
		return 0, fmt.Errorf("extend partition: %w", err)
	}
	defer windows.CloseHandle(diskHandle)

	geo, err := GetDiskGeometry(diskHandle)
	if err != nil {
		return 0, fmt.Errorf("extend partition: %w", err)
	}

	layout, err := GetDriveLayout(diskHandle)
	if err != nil {
		return 0, fmt.Errorf("extend partition: %w", err)
	}
	if len(layout.Partitions) == 0 {
		return 0, fmt.Errorf("extend partition: no partitions found on disk %d", opts.DiskNumber)
	}

	last := findLastPartition(layout.Partitions)
	currentEnd := last.StartingOffset + last.Length
	remaining := geo.DiskSize - currentEnd

	if remaining < minExtendBytes {
		return 0, nil
	}

	// Grow the partition table entry.
	if err := GrowPartition(diskHandle, last.PartitionNumber, remaining); err != nil {
		return 0, fmt.Errorf("extend partition: grow partition %d: %w", last.PartitionNumber, err)
	}

	if err := UpdateDiskProperties(diskHandle); err != nil {
		return 0, fmt.Errorf("extend partition: update disk properties: %w", err)
	}

	// Close the physical disk before working with the volume. Windows needs
	// a moment to recognize the partition change.
	windows.CloseHandle(diskHandle)
	time.Sleep(500 * time.Millisecond)

	// Extend the NTFS filesystem to fill the grown partition.
	if err := extendFilesystem(opts.DiskNumber, remaining, geo.BytesPerSector); err != nil {
		return 0, fmt.Errorf("extend partition: %w", err)
	}

	return remaining, nil
}

// extendFilesystem finds the volume on the given disk and issues
// FSCTL_EXTEND_VOLUME to grow the NTFS filesystem.
func extendFilesystem(diskNumber int, additionalBytes int64, bytesPerSector uint32) error {
	volumePath, err := FindVolumeByDiskNumber(diskNumber)
	if err != nil {
		return fmt.Errorf("find volume on disk %d: %w", diskNumber, err)
	}

	volumeHandle, err := openVolumeHandle(volumePath)
	if err != nil {
		return fmt.Errorf("open volume for extend: %w", err)
	}
	defer windows.CloseHandle(volumeHandle)

	// FSCTL_EXTEND_VOLUME expects the number of additional sectors.
	additionalSectors := additionalBytes / int64(bytesPerSector)
	if additionalSectors <= 0 {
		return nil
	}

	return ExtendVolume(volumeHandle, additionalSectors)
}

// AddNewPartition creates a new partition in the remaining unallocated space
// after the last existing partition and formats it. Returns the drive letter
// assigned to the new partition (e.g. "G:\").
func AddNewPartition(opts AddNewPartitionOptions) (string, error) {
	fs := normalizeFileSystem(opts.FileSystem)
	label := opts.Label
	if label == "" {
		label = "USB"
	}

	diskHandle, err := OpenPhysicalDisk(opts.DiskNumber)
	if err != nil {
		return "", fmt.Errorf("add new partition: %w", err)
	}
	defer windows.CloseHandle(diskHandle)

	geo, err := GetDiskGeometry(diskHandle)
	if err != nil {
		return "", fmt.Errorf("add new partition: %w", err)
	}

	layout, err := GetDriveLayout(diskHandle)
	if err != nil {
		return "", fmt.Errorf("add new partition: %w", err)
	}
	if len(layout.Partitions) == 0 {
		return "", fmt.Errorf("add new partition: no existing partitions on disk %d", opts.DiskNumber)
	}

	last := findLastPartition(layout.Partitions)
	newStart := last.StartingOffset + last.Length
	newSize := geo.DiskSize - newStart

	if newSize < minNewPartitionBytes {
		return "", fmt.Errorf("add new partition: remaining space %d bytes is too small (minimum %d)",
			newSize, minNewPartitionBytes)
	}

	// Build a new partition list: copy existing entries and append the new one.
	partitions := buildPartitionListWithNew(layout.Partitions, newStart, newSize)
	if len(partitions) > 4 {
		return "", fmt.Errorf("add new partition: MBR supports at most 4 primary partitions, would have %d", len(partitions))
	}

	if err := SetDriveLayoutMBR(diskHandle, partitions); err != nil {
		return "", fmt.Errorf("add new partition: set drive layout: %w", err)
	}

	if err := UpdateDiskProperties(diskHandle); err != nil {
		return "", fmt.Errorf("add new partition: update disk properties: %w", err)
	}

	// Close disk handle before waiting for the volume to appear.
	windows.CloseHandle(diskHandle)

	// Wait for Windows to detect the new volume.
	volumePath, err := waitForNewVolume(opts.DiskNumber, 15*time.Second)
	if err != nil {
		return "", fmt.Errorf("add new partition: %w", err)
	}

	// Format the new volume.
	if err := FormatVolume(FormatVolumeOptions{
		VolumePath:  volumePath,
		FileSystem:  fs,
		Label:       label,
		QuickFormat: true,
	}); err != nil {
		return "", fmt.Errorf("add new partition: format: %w", err)
	}

	// Assign a drive letter.
	driveLetter, err := AssignDriveLetter(volumePath)
	if err != nil {
		return "", fmt.Errorf("add new partition: assign drive letter: %w", err)
	}

	return driveLetter, nil
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// findLastPartition returns the partition with the highest starting offset.
func findLastPartition(partitions []PartitionInfo) PartitionInfo {
	last := partitions[0]
	for _, p := range partitions[1:] {
		if p.StartingOffset > last.StartingOffset {
			last = p
		}
	}
	return last
}

// openVolumeHandle opens a volume GUID path (e.g. \\?\Volume{GUID}\) for
// read/write IOCTL access. The trailing backslash is removed so that we
// open the device itself rather than the root directory.
func openVolumeHandle(volumeGUIDPath string) (windows.Handle, error) {
	devPath := strings.TrimRight(volumeGUIDPath, `\`)
	pathPtr, err := syscall.UTF16PtrFromString(devPath)
	if err != nil {
		return windows.InvalidHandle, fmt.Errorf("invalid volume path: %w", err)
	}

	h, err := windows.CreateFile(
		pathPtr,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		0,
		0,
	)
	if err != nil {
		return windows.InvalidHandle, fmt.Errorf("open volume %s: %w", devPath, err)
	}
	return h, nil
}

// buildPartitionListWithNew copies existing partitions into MBRPartition
// form and appends a new NTFS partition at the given offset and size.
func buildPartitionListWithNew(existing []PartitionInfo, startOffset, size int64) []MBRPartition {
	parts := make([]MBRPartition, 0, len(existing)+1)
	for _, p := range existing {
		parts = append(parts, MBRPartition{
			PartitionType: p.PartitionType,
			BootIndicator: p.IsActive,
			StartOffset:   p.StartingOffset,
			Size:          p.Length,
		})
	}
	parts = append(parts, MBRPartition{
		PartitionType: PARTITION_NTFS,
		BootIndicator: false,
		StartOffset:   startOffset,
		Size:          size,
	})
	return parts
}

// waitForNewVolume polls for a new volume to appear on the given disk.
// This is used after creating a partition, giving Windows time to enumerate
// the new volume.
func waitForNewVolume(diskNumber int, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		volumePath, err := FindVolumeByDiskNumber(diskNumber)
		if err == nil && volumePath != "" {
			return volumePath, nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return "", fmt.Errorf("timed out waiting for new volume on PhysicalDrive%d after %v", diskNumber, timeout)
}

// normalizeFileSystem returns a canonical filesystem name, defaulting to
// "NTFS" when empty.
func normalizeFileSystem(fs string) string {
	if fs == "" {
		return "NTFS"
	}
	return strings.ToUpper(fs)
}
