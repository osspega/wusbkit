package format

import (
	"context"
	"fmt"
	"math/rand"
	"os/exec"
	"strings"
	"time"

	"github.com/lazaroagomez/wusbkit/internal/disk"
	"golang.org/x/sys/windows"
)

// Options configures the format operation
type Options struct {
	DiskNumber int
	FileSystem string // fat32, ntfs, exfat
	Label      string
	Quick      bool
}

// ValidateFileSystem checks if the filesystem is supported
func ValidateFileSystem(fs string) error {
	fs = strings.ToLower(fs)
	switch fs {
	case "fat32", "ntfs", "exfat":
		return nil
	default:
		return fmt.Errorf("unsupported filesystem: %s (supported: fat32, ntfs, exfat)", fs)
	}
}

// Progress represents the current state of a format operation
type Progress struct {
	Drive      string `json:"drive"`
	DiskNumber int    `json:"diskNumber"`
	Stage      string `json:"stage"`
	Percentage int    `json:"percentage"`
	Status     string `json:"status"` // in_progress, complete, error
	Error      string `json:"error,omitempty"`
}

// Stage constants for format progress
const (
	StageCleaning          = "Cleaning disk"
	StageCreatingPartition = "Creating partition"
	StageFormatting        = "Formatting"
	StageAssigningLetter   = "Assigning drive letter"
	StageComplete          = "Complete"
)

// FormatResult represents the result of a format operation
type FormatResult struct {
	Success     bool   `json:"Success"`
	DriveLetter string `json:"DriveLetter"`
	Message     string `json:"Message"`
}

// Formatter handles USB drive formatting operations using native Win32 APIs.
// No PowerShell dependency — all operations use direct DeviceIoControl calls,
// a custom FAT32 formatter, and fmifs.dll/VDS for NTFS/exFAT.
type Formatter struct {
	progressChan chan Progress
}

// NewFormatter creates a new formatter
func NewFormatter() *Formatter {
	return &Formatter{
		progressChan: make(chan Progress, 10),
	}
}

// Progress returns a channel that receives progress updates
func (f *Formatter) Progress() <-chan Progress {
	return f.progressChan
}

// Format formats a USB drive using native Windows APIs.
func (f *Formatter) Format(ctx context.Context, opts Options) error {
	defer close(f.progressChan)

	label := opts.Label
	if label == "" {
		label = "USB"
	}
	fs := strings.ToLower(opts.FileSystem)

	// Step 1: Open physical disk
	f.sendProgress(opts, StageCleaning, 5)

	handle, err := disk.OpenPhysicalDisk(opts.DiskNumber)
	if err != nil {
		f.sendError(opts, "Failed to open disk: "+err.Error())
		return fmt.Errorf("open disk %d: %w", opts.DiskNumber, err)
	}
	defer windows.CloseHandle(handle)

	// Step 2: Get disk geometry
	geom, err := disk.GetDiskGeometry(handle)
	if err != nil {
		f.sendError(opts, "Failed to get disk geometry: "+err.Error())
		return fmt.Errorf("get geometry disk %d: %w", opts.DiskNumber, err)
	}

	// Step 3: Create MBR partition table (clears existing partitions)
	f.sendProgress(opts, StageCleaning, 15)

	mbrSignature := rand.Uint32()
	if err := disk.CreateMBRDisk(handle, mbrSignature); err != nil {
		f.sendError(opts, "Failed to create MBR: "+err.Error())
		return fmt.Errorf("create MBR disk %d: %w", opts.DiskNumber, err)
	}

	if err := disk.UpdateDiskProperties(handle); err != nil {
		// Non-fatal, continue
		_ = err
	}

	// Step 4: Create single partition spanning entire disk
	f.sendProgress(opts, StageCreatingPartition, 25)

	// Partition starts at sector offset (typically 1MB alignment = 2048 sectors for 512-byte sectors)
	alignmentOffset := int64(1048576) // 1 MB
	if alignmentOffset > geom.DiskSize/2 {
		alignmentOffset = int64(geom.BytesPerSector) // Tiny disk: start at sector 1
	}

	partitionSize := geom.DiskSize - alignmentOffset

	// Determine partition type
	partType := byte(0x0C) // FAT32 LBA (default)
	switch fs {
	case "ntfs":
		partType = 0x07 // NTFS/HPFS/exFAT
	case "exfat":
		partType = 0x07 // Same type ID as NTFS
	case "fat32":
		if geom.DiskSize > 4*1024*1024*1024 { // > 4GB
			partType = 0x0C // FAT32 LBA
		} else {
			partType = 0x0B // FAT32 CHS
		}
	}

	partition := disk.MBRPartition{
		PartitionType: partType,
		BootIndicator: true,
		StartOffset:   alignmentOffset,
		Size:          partitionSize,
	}

	if err := disk.SetDriveLayoutMBR(handle, []disk.MBRPartition{partition}); err != nil {
		f.sendError(opts, "Failed to create partition: "+err.Error())
		return fmt.Errorf("set drive layout disk %d: %w", opts.DiskNumber, err)
	}

	if err := disk.UpdateDiskProperties(handle); err != nil {
		_ = err
	}

	// Close disk handle before formatting — Windows needs exclusive access to the volume
	windows.CloseHandle(handle)
	handle = windows.InvalidHandle

	// Step 5: Wait for Windows to recognize the new volume
	f.sendProgress(opts, StageFormatting, 40)

	// Re-open disk briefly to trigger volume detection
	tmpHandle, err := disk.OpenPhysicalDisk(opts.DiskNumber)
	if err == nil {
		disk.UpdateDiskProperties(tmpHandle)
		windows.CloseHandle(tmpHandle)
	}

	volumePath, err := disk.WaitForVolumeReady(windows.InvalidHandle, opts.DiskNumber, 15*time.Second)
	if err != nil {
		f.sendError(opts, "Volume not detected after partitioning: "+err.Error())
		return fmt.Errorf("wait for volume disk %d: %w", opts.DiskNumber, err)
	}

	// Step 6: Format the volume
	f.sendProgress(opts, StageFormatting, 50)

	switch fs {
	case "fat32":
		// Use custom FAT32 formatter for speed and to bypass 32GB limit
		err = f.formatFAT32Native(opts.DiskNumber, volumePath, label, geom, alignmentOffset, partitionSize)
	case "ntfs", "exfat":
		// Lock and dismount volume before fmifs format
		if vh, vhErr := disk.OpenVolumeHandle(volumePath); vhErr == nil {
			_ = disk.LockVolume(vh)
			_ = disk.DismountVolume(vh)
			windows.CloseHandle(vh)
		}
		// Use fmifs.dll/VDS for NTFS and exFAT
		err = disk.FormatVolume(disk.FormatVolumeOptions{
			VolumePath:  volumePath,
			FileSystem:  strings.ToUpper(fs),
			Label:       label,
			QuickFormat: opts.Quick || fs == "exfat", // exFAT always quick
			ClusterSize: 0,                           // Default
		})
	}

	if err != nil {
		f.sendError(opts, "Format failed: "+err.Error())
		return fmt.Errorf("format disk %d as %s: %w", opts.DiskNumber, fs, err)
	}

	// Step 7: Assign a drive letter if one isn't already assigned
	f.sendProgress(opts, StageAssigningLetter, 90)

	driveLetter, _ := disk.GetVolumeDriveLetter(volumePath)
	if driveLetter == "" {
		driveLetter, err = disk.AssignDriveLetter(volumePath)
		if err != nil {
			// Non-fatal — format succeeded even without a letter
			driveLetter = ""
		}
	}

	f.sendComplete(opts, driveLetter)
	return nil
}

// formatFAT32Native formats a partition as FAT32 using direct sector writes.
func (f *Formatter) formatFAT32Native(diskNumber int, volumePath, label string, geom *disk.DiskGeometry, partOffset, partSize int64) error {
	// Lock and dismount the volume first (requires a volume handle, not disk handle).
	volumeHandle, err := disk.OpenVolumeHandle(volumePath)
	if err == nil {
		_ = disk.LockVolume(volumeHandle)
		_ = disk.DismountVolume(volumeHandle)
		defer windows.CloseHandle(volumeHandle)
	}

	// Open the physical disk for raw sector writes
	handle, err := disk.OpenPhysicalDisk(diskNumber)
	if err != nil {
		return fmt.Errorf("open disk for FAT32 format: %w", err)
	}
	defer windows.CloseHandle(handle)

	hiddenSectors := uint32(partOffset / int64(geom.BytesPerSector))

	return disk.FormatFAT32(disk.FormatFAT32Options{
		DiskHandle:        handle,
		PartitionOffset:   partOffset,
		PartitionSize:     partSize,
		VolumeLabel:       label,
		BytesPerSector:    geom.BytesPerSector,
		SectorsPerTrack:   geom.SectorsPerTrack,
		TracksPerCylinder: geom.TracksPerCylinder,
		HiddenSectors:     hiddenSectors,
	})
}

func (f *Formatter) sendProgress(opts Options, stage string, percentage int) {
	select {
	case f.progressChan <- Progress{
		DiskNumber: opts.DiskNumber,
		Stage:      stage,
		Percentage: percentage,
		Status:     "in_progress",
	}:
	default:
	}
}

func (f *Formatter) sendError(opts Options, errMsg string) {
	select {
	case f.progressChan <- Progress{
		DiskNumber: opts.DiskNumber,
		Stage:      "Error",
		Percentage: 0,
		Status:     "error",
		Error:      errMsg,
	}:
	default:
	}
}

func (f *Formatter) sendComplete(opts Options, driveLetter string) {
	select {
	case f.progressChan <- Progress{
		Drive:      driveLetter,
		DiskNumber: opts.DiskNumber,
		Stage:      StageComplete,
		Percentage: 100,
		Status:     "complete",
	}:
	default:
	}
}

// IsAdmin checks if the current process has administrator privileges
func IsAdmin() bool {
	cmd := exec.Command("net", "session")
	err := cmd.Run()
	return err == nil
}
