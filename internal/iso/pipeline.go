package iso

import (
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kdomanski/iso9660"
	"golang.org/x/sys/windows"

	"github.com/lazaroagomez/wusbkit/internal/disk"
)

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
//  1. Scan ISO to detect bootloader type and large files
//  2. Partition the disk (open, create MBR, wait for volume)
//  3. Format volume (FAT32 or NTFS)
//  4. Write bootloader MBR to sector 0
//  5. Assign drive letter and extract ISO contents
func (w *Writer) Write(ctx context.Context, opts WriteOptions) error {
	defer close(w.progressChan)

	// Step 1: Scan ISO contents.
	w.report("scanning", 0, "Scanning ISO contents")
	scanResult, err := w.scanISO(opts.ISOPath)
	if err != nil {
		return w.fail("scanning", fmt.Errorf("scan ISO: %w", err))
	}

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

	// Step 4: Write bootloader MBR.
	w.report("bootloader", 35, fmt.Sprintf("Writing %s bootloader MBR", bootType))
	if err := w.writeBootloader(opts.DiskNumber, bootType); err != nil {
		return err // writeBootloader already calls w.fail
	}

	if err := ctx.Err(); err != nil {
		return w.fail("extracting", err)
	}

	// Step 5: Extract ISO contents.
	w.report("extracting", 45, "Extracting ISO contents")
	if err := w.extractContents(ctx, opts.ISOPath, partResult.VolumePath); err != nil {
		return err // extractContents already calls w.fail
	}

	w.report("complete", 100, "ISO written successfully")
	return nil
}

// scanISO opens the ISO and walks its filesystem to detect bootloader
// indicators and large files.
func (w *Writer) scanISO(isoPath string) (*isoScanResult, error) {
	return scanISO(isoPath)
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

// extractContents assigns a drive letter to the volume and extracts the ISO
// contents to the mounted filesystem.
func (w *Writer) extractContents(ctx context.Context, isoPath, volumePath string) error {
	w.report("mounting", 40, "Assigning drive letter")
	driveLetter, err := ensureDriveLetter(volumePath)
	if err != nil {
		return w.fail("mounting", fmt.Errorf("assign drive letter: %w", err))
	}

	if err := ctx.Err(); err != nil {
		return w.fail("mounting", err)
	}

	if err := w.extractISO(ctx, isoPath, driveLetter); err != nil {
		return w.fail("extracting", fmt.Errorf("extract ISO: %w", err))
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

// scanISO opens the ISO and walks its filesystem to detect bootloader
// indicators and large files.
func scanISO(isoPath string) (*isoScanResult, error) {
	f, err := os.Open(isoPath)
	if err != nil {
		return nil, fmt.Errorf("open ISO file: %w", err)
	}
	defer f.Close()

	img, err := iso9660.OpenImage(f)
	if err != nil {
		return nil, fmt.Errorf("parse ISO image: %w", err)
	}

	root, err := img.RootDir()
	if err != nil {
		return nil, fmt.Errorf("read ISO root directory: %w", err)
	}

	result := &isoScanResult{}
	walkISODir(root, "", result)
	return result, nil
}

// walkISODir recursively walks an ISO directory tree and updates the scan result.
func walkISODir(dir *iso9660.File, prefix string, result *isoScanResult) {
	children, err := dir.GetChildren()
	if err != nil {
		return
	}

	for _, child := range children {
		name := child.Name()
		if name == "\x00" || name == "\x01" {
			// Current directory (.) and parent (..) entries in ISO 9660.
			continue
		}

		fullPath := prefix + name
		result.classifyPath(fullPath, child.IsDir(), int64(child.Size()))

		if child.IsDir() {
			walkISODir(child, fullPath+"/", result)
		}
	}
}

// extractISO extracts all files from the ISO image to the target directory.
// It uses the pure Go iso9660 library to read the ISO filesystem.
func (w *Writer) extractISO(ctx context.Context, isoPath, targetDir string) error {
	f, err := os.Open(isoPath)
	if err != nil {
		return fmt.Errorf("open ISO: %w", err)
	}
	defer f.Close()

	img, err := iso9660.OpenImage(f)
	if err != nil {
		return fmt.Errorf("parse ISO: %w", err)
	}

	root, err := img.RootDir()
	if err != nil {
		return fmt.Errorf("read root directory: %w", err)
	}

	// Count total files for progress reporting.
	totalFiles := countISOFiles(root)
	extracted := 0

	return w.extractDir(ctx, root, targetDir, &extracted, totalFiles)
}

// extractDir recursively extracts an ISO directory to the filesystem.
func (w *Writer) extractDir(ctx context.Context, dir *iso9660.File, targetDir string, extracted *int, total int) error {
	children, err := dir.GetChildren()
	if err != nil {
		return fmt.Errorf("list directory: %w", err)
	}

	for _, child := range children {
		if err := ctx.Err(); err != nil {
			return err
		}

		name := child.Name()
		if name == "\x00" || name == "\x01" {
			continue
		}

		// Remove ISO 9660 version suffix (";1") if present.
		if idx := strings.Index(name, ";"); idx >= 0 {
			name = name[:idx]
		}

		targetPath := filepath.Join(targetDir, name)

		if child.IsDir() {
			if err := os.MkdirAll(targetPath, 0o755); err != nil {
				return fmt.Errorf("create directory %s: %w", targetPath, err)
			}
			if err := w.extractDir(ctx, child, targetPath, extracted, total); err != nil {
				return err
			}
			continue
		}

		if err := extractFile(child, targetPath); err != nil {
			return fmt.Errorf("extract %s: %w", targetPath, err)
		}

		*extracted++
		if total > 0 {
			// Map extraction progress to 45%-99% of overall progress.
			pct := 45 + (*extracted*54)/total
			if pct > 99 {
				pct = 99
			}
			w.report("extracting", pct, fmt.Sprintf("Extracting files (%d/%d)", *extracted, total))
		}
	}

	return nil
}

// extractFile writes a single ISO file entry to the target filesystem path.
func extractFile(isoFile *iso9660.File, targetPath string) error {
	dir := filepath.Dir(targetPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create parent directory: %w", err)
	}

	outFile, err := os.Create(targetPath)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	defer outFile.Close()

	reader := isoFile.Reader()
	if _, err := io.Copy(outFile, reader); err != nil {
		return fmt.Errorf("write file contents: %w", err)
	}

	return nil
}

// countISOFiles counts the total number of files (non-directories) in the ISO.
func countISOFiles(dir *iso9660.File) int {
	count := 0
	children, err := dir.GetChildren()
	if err != nil {
		return 0
	}

	for _, child := range children {
		name := child.Name()
		if name == "\x00" || name == "\x01" {
			continue
		}
		if child.IsDir() {
			count += countISOFiles(child)
		} else {
			count++
		}
	}
	return count
}
