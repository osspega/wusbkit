package iso

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"

	"github.com/lazaroagomez/wusbkit/internal/disk"
)

// IsWindowsISO mounts the ISO, scans its contents, and returns true if the
// detected bootloader is Windows (i.e., no Linux bootloader markers found).
// Returns false with a logged warning if the ISO cannot be mounted.
func IsWindowsISO(isoPath string) bool {
	drive, cleanup, err := disk.MountISO(isoPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not mount ISO for detection: %v\n", err)
		return false
	}
	defer cleanup()
	return scanMountedDir(drive).classifyBootloader() == BootloaderWindows
}

// WriteOptions configures how an ISO image is written to a USB drive.
type WriteOptions struct {
	DiskNumber int    // Physical disk number (e.g., 1 for \\.\PhysicalDrive1)
	ISOPath    string // Path to the ISO file
	FileSystem string // "FAT32" or "NTFS" (auto-detected if empty: NTFS if any file > 4GB)
	Label      string // Volume label for the formatted partition
}

// WriteProgress reports the current stage and progress of an ISO write operation.
type WriteProgress struct {
	Stage      string `json:"stage"`
	Percentage int    `json:"percentage"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
}

// Writer orchestrates writing an ISO image to a USB drive. It creates a
// partition table, formats the drive, writes the bootloader MBR, and
// extracts ISO contents.
type Writer struct {
	progressChan chan WriteProgress
}

// NewWriter creates a new ISO Writer with a buffered progress channel.
func NewWriter() *Writer {
	return &Writer{
		progressChan: make(chan WriteProgress, 64),
	}
}

// Progress returns a read-only channel that receives progress updates
// during the Write operation.
func (w *Writer) Progress() <-chan WriteProgress {
	return w.progressChan
}

// partitionResult holds the output of the partitionDisk step, needed by
// subsequent pipeline stages.
type partitionResult struct {
	Geo         *disk.DiskGeometry
	VolumePath  string
	StartOffset int64
	PartSize    int64
}

// Write executes the full ISO-to-USB pipeline:
//  1. Mount ISO and scan contents to detect bootloader type and large files
//  2. Partition the disk (open, create MBR, wait for volume)
//  3. Format volume (FAT32 or NTFS)
//  4. Write bootloader MBR to sector 0 (skip for Windows UEFI)
//  5. Assign drive letter and copy ISO contents to USB
func (w *Writer) Write(ctx context.Context, opts WriteOptions) error {
	defer close(w.progressChan)

	// Mount ISO once for the entire pipeline (scan + extract).
	w.report("scanning", 0, "Mounting ISO image")
	srcDrive, unmount, err := disk.MountISO(opts.ISOPath)
	if err != nil {
		return w.fail("scanning", fmt.Errorf("mount ISO: %w", err))
	}
	defer unmount()

	// Step 1: Scan mounted ISO contents.
	w.report("scanning", 2, "Scanning ISO contents")
	scanResult := scanMountedDir(srcDrive)

	bootType := scanResult.classifyBootloader()

	// Auto-detect filesystem if not specified.
	fsType := strings.ToUpper(opts.FileSystem)
	if fsType == "" {
		if scanResult.HasLargeFile {
			fsType = "NTFS"
		} else {
			fsType = "FAT32"
		}
	}
	if scanResult.HasLargeFile && fsType == "FAT32" {
		fsType = "NTFS" // Force NTFS when files exceed 4 GB.
	}

	label := opts.Label
	if label == "" {
		label = "USB"
	}

	// Step 2: Partition the disk.
	w.report("partitioning", 5, "Opening disk and reading geometry")
	partResult, err := w.partitionDisk(ctx, opts.DiskNumber, fsType)
	if err != nil {
		return err // partitionDisk already calls w.fail
	}

	if err := ctx.Err(); err != nil {
		return w.fail("formatting", err)
	}

	// Step 3: Format the volume.
	w.report("formatting", 20, fmt.Sprintf("Formatting as %s", fsType))
	if err := w.formatVolume(opts.DiskNumber, partResult, fsType, label); err != nil {
		return err // formatVolume already calls w.fail
	}

	if err := ctx.Err(); err != nil {
		return w.fail("bootloader", err)
	}

	// Step 4: Write bootloader MBR (skip for Windows — UEFI boots from EFI/ directory).
	if bootType != BootloaderWindows {
		w.report("bootloader", 35, fmt.Sprintf("Writing %s bootloader MBR", bootType))
		if err := w.writeBootloader(opts.DiskNumber, bootType); err != nil {
			return err // writeBootloader already calls w.fail
		}
	} else {
		w.report("bootloader", 35, "Skipping MBR bootstrap (UEFI boot from EFI directory)")
	}

	if err := ctx.Err(); err != nil {
		return w.fail("extracting", err)
	}

	// Step 5: Copy ISO contents from mounted drive to USB.
	w.report("extracting", 40, "Preparing to copy files")
	if err := w.copyToUSB(ctx, srcDrive, partResult.VolumePath); err != nil {
		return err
	}

	w.report("complete", 100, "ISO written successfully")
	return nil
}

// partitionDisk opens the physical disk, creates an MBR partition table, and
// waits for Windows to recognize the new volume. It returns the geometry and
// volume path needed by later stages. The disk handle is closed before
// returning so that formatting can proceed.
func (w *Writer) partitionDisk(ctx context.Context, diskNumber int, fsType string) (*partitionResult, error) {
	diskHandle, err := disk.OpenPhysicalDisk(diskNumber)
	if err != nil {
		return nil, w.fail("partitioning", fmt.Errorf("open disk: %w", err))
	}
	defer func() {
		if diskHandle != windows.InvalidHandle {
			windows.CloseHandle(diskHandle)
		}
	}()

	if err := ctx.Err(); err != nil {
		return nil, w.fail("partitioning", err)
	}

	geo, err := disk.GetDiskGeometry(diskHandle)
	if err != nil {
		return nil, w.fail("partitioning", fmt.Errorf("get geometry: %w", err))
	}

	w.report("partitioning", 10, "Creating MBR partition table")
	signature := rand.Uint32()
	if err := disk.CreateMBRDisk(diskHandle, signature); err != nil {
		return nil, w.fail("partitioning", fmt.Errorf("create MBR: %w", err))
	}

	// Determine partition type based on target filesystem.
	partType := byte(disk.PARTITION_FAT32) // 0x0C
	if fsType == "NTFS" {
		partType = byte(disk.PARTITION_NTFS) // 0x07
	}

	// Single partition spanning the full disk, starting at one track offset.
	startOffset := int64(geo.SectorsPerTrack) * int64(geo.BytesPerSector)
	partSize := geo.DiskSize - startOffset

	err = disk.SetDriveLayoutMBR(diskHandle, []disk.MBRPartition{
		{
			PartitionType: partType,
			BootIndicator: true,
			StartOffset:   startOffset,
			Size:          partSize,
		},
	})
	if err != nil {
		return nil, w.fail("partitioning", fmt.Errorf("set drive layout: %w", err))
	}

	if err := disk.UpdateDiskProperties(diskHandle); err != nil {
		return nil, w.fail("partitioning", fmt.Errorf("update properties: %w", err))
	}

	w.report("partitioning", 15, "Waiting for volume to appear")
	volumePath, err := disk.WaitForVolumeReady(diskHandle, diskNumber, 30*time.Second)
	if err != nil {
		return nil, w.fail("partitioning", fmt.Errorf("volume not ready: %w", err))
	}

	if err := ctx.Err(); err != nil {
		return nil, w.fail("partitioning", err)
	}

	// Close the disk handle before formatting (Windows requires this).
	windows.CloseHandle(diskHandle)
	diskHandle = windows.InvalidHandle
	time.Sleep(1 * time.Second)

	return &partitionResult{
		Geo:         geo,
		VolumePath:  volumePath,
		StartOffset: startOffset,
		PartSize:    partSize,
	}, nil
}

// formatVolume formats the newly created volume with the appropriate
// filesystem (FAT32 or NTFS).
func (w *Writer) formatVolume(diskNumber int, pr *partitionResult, fsType, label string) error {
	err := formatVolume(pr.VolumePath, fsType, label, pr.Geo, pr.StartOffset, pr.PartSize, diskNumber)
	if err != nil {
		return w.fail("formatting", fmt.Errorf("format volume: %w", err))
	}
	time.Sleep(1 * time.Second)
	return nil
}

// writeBootloader opens the physical disk and writes the appropriate MBR
// bootstrap code to sector 0.
func (w *Writer) writeBootloader(diskNumber int, bootType BootloaderType) error {
	diskHandle, err := disk.OpenPhysicalDisk(diskNumber)
	if err != nil {
		return w.fail("bootloader", fmt.Errorf("reopen disk for bootloader: %w", err))
	}
	defer windows.CloseHandle(diskHandle)

	if err := WriteMBR(diskHandle, bootType); err != nil {
		return w.fail("bootloader", fmt.Errorf("write bootloader: %w", err))
	}
	return nil
}

// copyToUSB assigns a drive letter to the USB volume and copies all files
// from the mounted ISO drive to it.
func (w *Writer) copyToUSB(ctx context.Context, srcDrive, volumePath string) error {
	w.report("mounting", 40, "Assigning drive letter to USB")
	dstDrive, err := ensureDriveLetter(volumePath)
	if err != nil {
		return w.fail("mounting", fmt.Errorf("assign drive letter: %w", err))
	}

	if err := ctx.Err(); err != nil {
		return w.fail("extracting", err)
	}

	// Count files for progress.
	total := countFiles(srcDrive)
	copied := 0

	w.report("extracting", 45, "Copying files")
	err = filepath.WalkDir(srcDrive, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		// Build relative path (forward slashes stripped of drive prefix).
		relPath, err := filepath.Rel(srcDrive, path)
		if err != nil {
			return err
		}
		if relPath == "." {
			return nil
		}

		dstPath := filepath.Join(dstDrive, relPath)

		if d.IsDir() {
			return os.MkdirAll(dstPath, 0o755)
		}

		if err := copyFile(path, dstPath); err != nil {
			return fmt.Errorf("copy %s: %w", relPath, err)
		}

		copied++
		if total > 0 {
			pct := 45 + (copied*54)/total
			if pct > 99 {
				pct = 99
			}
			w.report("extracting", pct, fmt.Sprintf("Copying files (%d/%d)", copied, total))
		}
		return nil
	})
	if err != nil {
		return w.fail("extracting", fmt.Errorf("copy files: %w", err))
	}

	return nil
}

// report sends a progress update to the progress channel.
func (w *Writer) report(stage string, pct int, status string) {
	select {
	case w.progressChan <- WriteProgress{
		Stage:      stage,
		Percentage: pct,
		Status:     status,
	}:
	default:
		// Drop update if channel is full (non-blocking).
	}
}

// fail sends an error progress update and returns the error.
func (w *Writer) fail(stage string, err error) error {
	select {
	case w.progressChan <- WriteProgress{
		Stage:  stage,
		Status: "Error",
		Error:  err.Error(),
	}:
	default:
	}
	return err
}

// formatVolume formats the volume with the appropriate filesystem.
func formatVolume(volumePath, fsType, label string, geo *disk.DiskGeometry, partOffset, partSize int64, diskNumber int) error {
	// Lock and dismount volume before formatting to avoid "volume in use" errors.
	// Keep the handle open so the lock persists during the entire format.
	if vh, err := disk.OpenVolumeHandle(volumePath); err == nil {
		_ = disk.LockVolume(vh)
		_ = disk.DismountVolume(vh)
		defer windows.CloseHandle(vh)
	}

	if fsType == "FAT32" {
		// Use the custom FAT32 formatter which bypasses the Windows 32 GB limit.
		diskHandle, err := disk.OpenPhysicalDisk(diskNumber)
		if err != nil {
			return fmt.Errorf("open disk for FAT32 format: %w", err)
		}
		defer windows.CloseHandle(diskHandle)

		return disk.FormatFAT32(disk.FormatFAT32Options{
			DiskHandle:        diskHandle,
			PartitionOffset:   partOffset,
			PartitionSize:     partSize,
			VolumeLabel:       label,
			BytesPerSector:    geo.BytesPerSector,
			SectorsPerTrack:   geo.SectorsPerTrack,
			TracksPerCylinder: geo.TracksPerCylinder,
			HiddenSectors:     uint32(partOffset / int64(geo.BytesPerSector)),
		})
	}

	// NTFS: use the VDS/fmifs formatter.
	return disk.FormatVolume(disk.FormatVolumeOptions{
		VolumePath:  volumePath,
		FileSystem:  "NTFS",
		Label:       label,
		QuickFormat: true,
	})
}

// ensureDriveLetter checks if the volume already has a drive letter assigned,
// and assigns one if not. Returns the drive root path (e.g., "G:\").
func ensureDriveLetter(volumePath string) (string, error) {
	letter, err := disk.GetVolumeDriveLetter(volumePath)
	if err == nil && letter != "" {
		return letter, nil
	}
	return disk.AssignDriveLetter(volumePath)
}

// scanMountedDir walks a mounted directory and returns scan results for
// bootloader detection and large file checks.
func scanMountedDir(root string) *isoScanResult {
	result := &isoScanResult{}
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip inaccessible entries
		}
		relPath, relErr := filepath.Rel(root, path)
		if relErr != nil || relPath == "." {
			return nil
		}
		// Convert to forward slashes for classifyPath compatibility.
		relPath = strings.ReplaceAll(relPath, `\`, "/")

		var fileSize int64
		if !d.IsDir() {
			if info, infoErr := d.Info(); infoErr == nil {
				fileSize = info.Size()
			}
		}
		result.classifyPath(relPath, d.IsDir(), fileSize)
		return nil
	})
	return result
}

// countFiles counts non-directory entries under root.
func countFiles(root string) int {
	count := 0
	filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			count++
		}
		return nil
	})
	return count
}

// copyFile copies a single file from src to dst using a 1 MB buffer.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	buf := make([]byte, 1<<20) // 1 MB buffer
	_, err = io.CopyBuffer(out, in, buf)
	return err
}
