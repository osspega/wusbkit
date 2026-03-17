package image

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"fmt"
	"hash"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/windows"

	"github.com/lazaroagomez/wusbkit/internal/disk"
)

// Version stamp written into .bin headers. These package-level variables can
// be overridden by the build system (e.g. an init function or build-time
// code generation) to inject the release version.
var (
	VersionMajor    uint32 = 1
	VersionMinor    uint32 = 0
	VersionBuild    uint32 = 0
	VersionRevision uint32 = 1
)

const (
	defaultBufferSize = 1 << 20 // 1 MB

	// FAT32 maximum file size (4 GB - 1 byte)
	fat32MaxFileSize = (4 << 30) - 1

	// Progress update interval to avoid flooding the channel
	progressInterval = 250 * time.Millisecond
)

// CreateOptions configures the image creation operation.
type CreateOptions struct {
	DiskNumber int
	OutputPath string // Path for the .bin file
	Verify     bool   // Post-creation verification (reserved for future use)
	BufferSize int    // Buffer size in bytes (default 1 MB)
}

// CreateProgress reports the current state of an image creation operation.
type CreateProgress struct {
	Stage      string `json:"stage"`
	Percentage int    `json:"percentage"`
	BytesRead  int64  `json:"bytes_read"`
	TotalBytes int64  `json:"total_bytes"`
	Speed      string `json:"speed"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
}

// Creator handles USB drive image creation operations.
type Creator struct {
	progressChan chan CreateProgress
}

// NewCreator creates a new image creator.
func NewCreator() *Creator {
	return &Creator{
		progressChan: make(chan CreateProgress, 10),
	}
}

// Progress returns a channel that receives progress updates.
func (c *Creator) Progress() <-chan CreateProgress {
	return c.progressChan
}

// Create reads a USB drive and writes it as an ImageUSB .bin file.
// The operation can be cancelled via the context.
func (c *Creator) Create(ctx context.Context, opts CreateOptions) error {
	defer close(c.progressChan)

	bufSize := opts.BufferSize
	if bufSize <= 0 {
		bufSize = defaultBufferSize
	}

	// Step 1: Open physical disk for reading
	c.sendProgress("Preparing", 0, 0, 0, "")

	diskHandle, err := openPhysicalDiskReadOnly(opts.DiskNumber)
	if err != nil {
		c.sendError(fmt.Sprintf("open disk: %v", err))
		return fmt.Errorf("open PhysicalDrive%d for reading: %w", opts.DiskNumber, err)
	}
	defer windows.CloseHandle(diskHandle)

	// Step 2: Get disk geometry for total size
	geom, err := disk.GetDiskGeometry(diskHandle)
	if err != nil {
		c.sendError(fmt.Sprintf("get disk geometry: %v", err))
		return fmt.Errorf("get disk geometry: %w", err)
	}
	diskSize := geom.DiskSize

	// Step 3: Check destination constraints
	if err := checkDestination(opts.OutputPath, diskSize); err != nil {
		c.sendError(err.Error())
		return err
	}

	// Step 4: Create output file
	outFile, err := os.Create(opts.OutputPath)
	if err != nil {
		c.sendError(fmt.Sprintf("create output file: %v", err))
		return fmt.Errorf("create output file: %w", err)
	}
	defer outFile.Close()

	// Step 5: Write initial header with zeroed checksums
	header := &Header{
		VersionMajor:    VersionMajor,
		VersionMinor:    VersionMinor,
		VersionBuild:    VersionBuild,
		VersionRevision: VersionRevision,
		ImageLength:     uint64(diskSize),
	}
	if err := WriteHeader(outFile, header); err != nil {
		c.sendError(fmt.Sprintf("write header: %v", err))
		return fmt.Errorf("write initial header: %w", err)
	}

	// Step 6: Read disk and write to file while computing checksums
	md5Hash := md5.New()
	sha1Hash := sha1.New()

	bytesRead, err := c.copyDiskToFile(ctx, diskHandle, outFile, md5Hash, sha1Hash, diskSize, bufSize)
	if err != nil {
		c.sendError(err.Error())
		return err
	}

	// Step 7: Finalize checksums and update header
	header.MD5 = fmt.Sprintf("%x", md5Hash.Sum(nil))
	header.SHA1 = fmt.Sprintf("%x", sha1Hash.Sum(nil))

	if err := WriteHeader(outFile, header); err != nil {
		c.sendError(fmt.Sprintf("update header checksums: %v", err))
		return fmt.Errorf("update header checksums: %w", err)
	}

	// Step 8: Write log file
	if err := writeLogFile(opts.OutputPath, header, bytesRead); err != nil {
		// Non-fatal: log the issue but don't fail the whole operation
		c.sendError(fmt.Sprintf("write log file (non-fatal): %v", err))
	}

	c.sendComplete(bytesRead, diskSize)
	return nil
}

// copyDiskToFile reads the disk in blocks, writes to the output file, and
// feeds data to the hash writers. Returns total bytes read.
func (c *Creator) copyDiskToFile(
	ctx context.Context,
	diskHandle windows.Handle,
	outFile *os.File,
	md5Hash, sha1Hash hash.Hash,
	diskSize int64,
	bufSize int,
) (int64, error) {
	buf := make([]byte, bufSize)
	var totalRead int64
	startTime := time.Now()
	lastUpdate := time.Time{}

	for totalRead < diskSize {
		// Check for cancellation
		select {
		case <-ctx.Done():
			return totalRead, fmt.Errorf("operation cancelled: %w", ctx.Err())
		default:
		}

		// Calculate how much to read this iteration
		remaining := diskSize - totalRead
		readSize := int64(bufSize)
		if remaining < readSize {
			readSize = remaining
		}

		// Read from disk
		var bytesReturned uint32
		err := windows.ReadFile(diskHandle, buf[:readSize], &bytesReturned, nil)
		if err != nil {
			return totalRead, fmt.Errorf("read disk at offset %d: %w", totalRead, err)
		}
		if bytesReturned == 0 {
			break
		}

		chunk := buf[:bytesReturned]

		// Write to output file
		if _, err := outFile.Write(chunk); err != nil {
			return totalRead, fmt.Errorf("write to output at offset %d: %w", totalRead, err)
		}

		// Feed to hashers
		md5Hash.Write(chunk)
		sha1Hash.Write(chunk)

		totalRead += int64(bytesReturned)

		// Send progress update (throttled)
		now := time.Now()
		if now.Sub(lastUpdate) >= progressInterval {
			elapsed := now.Sub(startTime).Seconds()
			speed := formatSpeed(float64(totalRead) / elapsed)
			pct := int(totalRead * 100 / diskSize)
			c.sendProgress("Reading", pct, totalRead, diskSize, speed)
			lastUpdate = now
		}
	}

	return totalRead, nil
}

// openPhysicalDiskReadOnly opens a physical disk with read-only access.
func openPhysicalDiskReadOnly(diskNumber int) (windows.Handle, error) {
	path := fmt.Sprintf(`\\.\PhysicalDrive%d`, diskNumber)
	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, fmt.Errorf("invalid disk path: %w", err)
	}

	handle, err := windows.CreateFile(
		pathPtr,
		windows.GENERIC_READ,
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

// checkDestination verifies the output path has enough free space and
// that the filesystem supports the required file size.
func checkDestination(outputPath string, diskSize int64) error {
	dir := filepath.Dir(outputPath)

	// Ensure the parent directory exists
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}

	// Query free disk space on the destination volume
	dirPtr, err := syscall.UTF16PtrFromString(dir)
	if err != nil {
		return fmt.Errorf("invalid output path: %w", err)
	}

	var freeBytesAvailable, totalBytes, totalFreeBytes uint64
	err = windows.GetDiskFreeSpaceEx(
		dirPtr,
		&freeBytesAvailable,
		&totalBytes,
		&totalFreeBytes,
	)
	if err != nil {
		return fmt.Errorf("query free space on %s: %w", dir, err)
	}

	// Total file size = header + disk image
	requiredBytes := uint64(HeaderSize) + uint64(diskSize)
	if freeBytesAvailable < requiredBytes {
		return fmt.Errorf(
			"insufficient disk space: need %d bytes, available %d bytes on %s",
			requiredBytes, freeBytesAvailable, dir,
		)
	}

	// Check FAT32 4GB file size limit by examining the volume's filesystem
	if requiredBytes > fat32MaxFileSize && isFAT32Volume(dir) {
		return fmt.Errorf(
			"output file would be %d bytes, which exceeds the FAT32 4GB file size limit",
			requiredBytes,
		)
	}

	return nil
}

// isFAT32Volume checks if the volume containing dir is formatted as FAT32.
func isFAT32Volume(dir string) bool {
	volumeRoot := filepath.VolumeName(dir) + `\`
	rootPtr, err := syscall.UTF16PtrFromString(volumeRoot)
	if err != nil {
		return false
	}

	var fsNameBuf [256]uint16
	err = windows.GetVolumeInformation(
		rootPtr,
		nil, 0, // volume name (not needed)
		nil,    // serial number
		nil,    // max component length
		nil,    // filesystem flags
		&fsNameBuf[0],
		uint32(len(fsNameBuf)),
	)
	if err != nil {
		return false
	}

	fsName := syscall.UTF16ToString(fsNameBuf[:])
	return fsName == "FAT32"
}

// writeLogFile creates a .log companion file with checksums and timestamp.
func writeLogFile(binPath string, h *Header, bytesRead int64) error {
	logPath := binPath + ".log"
	f, err := os.Create(logPath)
	if err != nil {
		return fmt.Errorf("create log file: %w", err)
	}
	defer f.Close()

	_, err = fmt.Fprintf(f,
		"ImageUSB Log\n"+
			"Created: %s\n"+
			"Version: %d.%d.%d.%d\n"+
			"Image Size: %d bytes\n"+
			"Bytes Read: %d\n"+
			"MD5:  %s\n"+
			"SHA1: %s\n",
		time.Now().Format(time.RFC3339),
		h.VersionMajor, h.VersionMinor, h.VersionBuild, h.VersionRevision,
		h.ImageLength,
		bytesRead,
		h.MD5,
		h.SHA1,
	)
	return err
}

// sendProgress sends a progress update on the channel without blocking.
func (c *Creator) sendProgress(stage string, pct int, bytesRead, totalBytes int64, speed string) {
	select {
	case c.progressChan <- CreateProgress{
		Stage:      stage,
		Percentage: pct,
		BytesRead:  bytesRead,
		TotalBytes: totalBytes,
		Speed:      speed,
		Status:     "running",
	}:
	default:
	}
}

// sendError sends an error progress update without blocking.
func (c *Creator) sendError(errMsg string) {
	select {
	case c.progressChan <- CreateProgress{
		Stage:  "Error",
		Status: "error",
		Error:  errMsg,
	}:
	default:
	}
}

// sendComplete sends the final completion progress update.
func (c *Creator) sendComplete(bytesRead, totalBytes int64) {
	select {
	case c.progressChan <- CreateProgress{
		Stage:      "Complete",
		Percentage: 100,
		BytesRead:  bytesRead,
		TotalBytes: totalBytes,
		Status:     "complete",
	}:
	default:
	}
}

// formatSpeed formats a bytes-per-second value into a human-readable string.
func formatSpeed(bytesPerSec float64) string {
	const (
		kb = 1024
		mb = kb * 1024
		gb = mb * 1024
	)

	switch {
	case bytesPerSec >= gb:
		return fmt.Sprintf("%.1f GB/s", bytesPerSec/gb)
	case bytesPerSec >= mb:
		return fmt.Sprintf("%.1f MB/s", bytesPerSec/mb)
	case bytesPerSec >= kb:
		return fmt.Sprintf("%.1f KB/s", bytesPerSec/kb)
	default:
		return fmt.Sprintf("%.0f B/s", bytesPerSec)
	}
}
