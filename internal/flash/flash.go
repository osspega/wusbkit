package flash

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"hash"
	"time"
)

// Stage constants for flash progress
const (
	StageExtracting = "Extracting"
	StageWriting    = "Writing"
	StageVerifying  = "Verifying"
	StageComplete   = "Complete"
)

// defaultBufferSize is the fallback buffer size (4MB) when not specified
const defaultBufferSize = 4 << 20

// Write retry constants (mirrors ImageUSB CopyBlock retry pattern)
const (
	maxWriteRetries = 3
	retryDelay      = 1 * time.Second
)

// speedTestBlockSize is the block size for the pre-write speed test (1MB)
const speedTestBlockSize = 1 << 20

// speedTestMaxBlocks is the maximum number of 1MB blocks written during speed test
const speedTestMaxBlocks = 10

// Status constants
const (
	StatusInProgress = "in_progress"
	StatusComplete   = "complete"
	StatusError      = "error"
)

// Progress represents the current state of a flash operation
type Progress struct {
	Stage        string `json:"stage"`
	Percentage   int    `json:"percentage"`
	BytesWritten int64  `json:"bytes_written"`
	TotalBytes   int64  `json:"total_bytes"`
	Speed        string `json:"speed"`
	Status       string `json:"status"`
	Error        string `json:"error,omitempty"`
	Hash         string `json:"hash,omitempty"`
	BytesSkipped int64  `json:"bytes_skipped,omitempty"`
}

// Options configures the flash operation
type Options struct {
	DiskNumber    int
	ImagePath     string
	Verify        bool
	BufferSize    int    // Buffer size in MB (default: 4)
	CalculateHash bool   // Calculate SHA-256 hash while writing
	SkipUnchanged bool   // Skip writing sectors that haven't changed
	DriveLetter   string // Optional: cached drive letter to avoid WMI lookup
}

// Flasher handles USB drive flashing operations
type Flasher struct {
	progressChan chan Progress
}

// NewFlasher creates a new flasher
func NewFlasher() *Flasher {
	return &Flasher{
		progressChan: make(chan Progress, 10),
	}
}

// Progress returns a channel that receives progress updates
func (f *Flasher) Progress() <-chan Progress {
	return f.progressChan
}

// Flash writes an image to a USB drive
// Returns error or nil on success. Also available via FlashWithStats for hash/skip stats.
func (f *Flasher) Flash(ctx context.Context, opts Options) (string, int64, error) {
	defer close(f.progressChan)

	// Open the image source
	source, err := OpenSource(opts.ImagePath)
	if err != nil {
		f.sendError(opts, err.Error())
		return "", 0, err
	}
	defer source.Close()

	totalSize := source.Size()
	f.sendProgress(opts, StageWriting, 0, 0, totalSize, "")

	// Open the disk for writing (use cached drive letter if available)
	var writer *diskWriter
	if opts.DriveLetter != "" {
		writer = newDiskWriterWithDriveLetter(opts.DiskNumber, opts.DriveLetter)
	} else {
		writer = newDiskWriter(opts.DiskNumber)
	}
	if err := writer.Open(); err != nil {
		f.sendError(opts, err.Error())
		return "", 0, err
	}
	defer writer.Close()

	// Pre-write speed test: verify drive is responsive
	if err := f.speedTest(writer); err != nil {
		f.sendError(opts, err.Error())
		return "", 0, err
	}

	// Write the image and get hash/skip stats
	finalHash, bytesSkipped, err := f.writeImage(ctx, opts, source, writer, totalSize)
	if err != nil {
		return "", 0, err
	}

	// Verify if requested
	if opts.Verify {
		if err := f.verifyImage(ctx, opts, writer, totalSize); err != nil {
			return "", 0, err
		}
	}

	f.sendComplete(opts, totalSize, finalHash, bytesSkipped)
	return finalHash, bytesSkipped, nil
}

// progressUpdateInterval controls how often progress updates are sent
// This reduces CPU overhead from calculating/sending progress on every buffer
const progressUpdateInterval = 100 * time.Millisecond

// writeImage writes the source to the disk with progress updates
// Returns: finalHash (empty if not calculated), bytesSkipped, error
func (f *Flasher) writeImage(ctx context.Context, opts Options, source Source, writer *diskWriter, totalSize int64) (string, int64, error) {
	// Calculate buffer size in bytes (with fallback to 4MB)
	bufSize := opts.BufferSize << 20
	if bufSize <= 0 {
		bufSize = defaultBufferSize
	}

	// Get buffer from pool (or allocate if needed)
	buffer := GetBuffer(bufSize)
	defer PutBuffer(bufSize, buffer)

	var bytesWritten int64
	var bytesSkipped int64
	startTime := time.Now()
	lastProgressUpdate := startTime

	// Initialize hash if requested
	var hasher hash.Hash
	if opts.CalculateHash {
		hasher = sha256.New()
	}

	// Buffer for skip-write comparison
	var diskBuffer []byte
	if opts.SkipUnchanged {
		diskBuffer = GetBuffer(bufSize)
		defer PutBuffer(bufSize, diskBuffer)
	}

	for {
		select {
		case <-ctx.Done():
			f.sendError(opts, "operation cancelled")
			return "", 0, ctx.Err()
		default:
		}

		// Read from source
		n, err := source.Read(buffer)
		if n == 0 && err != nil {
			if err.Error() == "EOF" {
				break
			}
			f.sendError(opts, fmt.Sprintf("read error: %v", err))
			return "", 0, err
		}

		if n == 0 {
			break
		}

		// Update hash with actual data (before padding)
		if hasher != nil {
			hasher.Write(buffer[:n])
		}

		// Align write size for unbuffered I/O
		writeSize := alignSize(n)
		writeBuffer := buffer[:writeSize]

		// Zero-pad if needed (optimized: use clear instead of byte-by-byte loop)
		if writeSize > n {
			clear(writeBuffer[n:writeSize])
		}

		// Skip-write: check if data on disk is already identical
		shouldWrite := true
		if opts.SkipUnchanged {
			_, readErr := writer.ReadAt(diskBuffer[:writeSize], bytesWritten)
			if readErr == nil && bytes.Equal(buffer[:n], diskBuffer[:n]) {
				shouldWrite = false
				bytesSkipped += int64(n)
			}
		}

		// Write to disk only if needed (with retry on failure)
		if shouldWrite {
			written, err := f.writeWithRetry(writer, writeBuffer, bytesWritten)
			if err != nil {
				f.sendError(opts, fmt.Sprintf("write error at offset %d: %v", bytesWritten, err))
				return "", 0, err
			}
			if written < writeSize {
				f.sendError(opts, fmt.Sprintf("incomplete write at offset %d: wrote %d of %d bytes", bytesWritten, written, writeSize))
				return "", 0, fmt.Errorf("incomplete write at offset %d: wrote %d of %d bytes", bytesWritten, written, writeSize)
			}
		}

		bytesWritten += int64(n)

		// Throttle progress updates to reduce CPU overhead
		now := time.Now()
		if now.Sub(lastProgressUpdate) >= progressUpdateInterval {
			lastProgressUpdate = now

			// Calculate speed and send progress
			elapsed := now.Sub(startTime).Seconds()
			speed := ""
			if elapsed > 0 {
				bytesPerSec := float64(bytesWritten) / elapsed
				speed = formatSpeed(bytesPerSec)
			}

			percentage := int(float64(bytesWritten) / float64(totalSize) * 100)
			if percentage > 100 {
				percentage = 100 // Cap at 100% (can exceed if size was estimated)
			}
			f.sendProgress(opts, StageWriting, percentage, bytesWritten, totalSize, speed)
		}
	}

	// Calculate final hash
	finalHash := ""
	if hasher != nil {
		finalHash = fmt.Sprintf("%x", hasher.Sum(nil))
	}

	return finalHash, bytesSkipped, nil
}

// verifyImage reads back the written data and compares with source
func (f *Flasher) verifyImage(ctx context.Context, opts Options, writer *diskWriter, totalSize int64) error {
	// Reopen the source for verification
	source, err := OpenSource(opts.ImagePath)
	if err != nil {
		f.sendError(opts, fmt.Sprintf("verify: failed to reopen source: %v", err))
		return err
	}
	defer source.Close()

	// Calculate buffer size in bytes (with fallback to 4MB)
	bufSize := opts.BufferSize << 20
	if bufSize <= 0 {
		bufSize = defaultBufferSize
	}

	// Get buffers from pool
	sourceBuffer := GetBuffer(bufSize)
	defer PutBuffer(bufSize, sourceBuffer)
	diskBuffer := GetBuffer(bufSize)
	defer PutBuffer(bufSize, diskBuffer)

	var bytesVerified int64
	startTime := time.Now()
	lastProgressUpdate := startTime

	f.sendProgress(opts, StageVerifying, 0, 0, totalSize, "")

	for {
		select {
		case <-ctx.Done():
			f.sendError(opts, "verification cancelled")
			return ctx.Err()
		default:
		}

		// Read from source
		n, err := source.Read(sourceBuffer)
		if n == 0 && err != nil {
			if err.Error() == "EOF" {
				break
			}
			f.sendError(opts, fmt.Sprintf("verify: read source error: %v", err))
			return err
		}

		if n == 0 {
			break
		}

		// Read from disk (aligned)
		readSize := alignSize(n)
		_, err = writer.ReadAt(diskBuffer[:readSize], bytesVerified)
		if err != nil {
			f.sendError(opts, fmt.Sprintf("verify: read disk error at offset %d: %v", bytesVerified, err))
			return err
		}

		// Compare only the actual data bytes (not padding)
		if !bytes.Equal(sourceBuffer[:n], diskBuffer[:n]) {
			f.sendError(opts, fmt.Sprintf("verify: data mismatch at offset %d", bytesVerified))
			return fmt.Errorf("verification failed: data mismatch at offset %d", bytesVerified)
		}

		bytesVerified += int64(n)

		// Throttle progress updates to reduce CPU overhead
		now := time.Now()
		if now.Sub(lastProgressUpdate) >= progressUpdateInterval {
			lastProgressUpdate = now

			// Calculate speed and send progress
			elapsed := now.Sub(startTime).Seconds()
			speed := ""
			if elapsed > 0 {
				bytesPerSec := float64(bytesVerified) / elapsed
				speed = formatSpeed(bytesPerSec)
			}

			percentage := int(float64(bytesVerified) / float64(totalSize) * 100)
			if percentage > 100 {
				percentage = 100 // Cap at 100% (can exceed if size was estimated)
			}
			f.sendProgress(opts, StageVerifying, percentage, bytesVerified, totalSize, speed)
		}
	}

	return nil
}

// writeWithRetry writes data to disk, retrying on failure or partial writes.
// On write error: retries up to maxWriteRetries times with retryDelay between attempts.
// On partial write: retries the remaining bytes up to maxWriteRetries times.
func (f *Flasher) writeWithRetry(writer *diskWriter, data []byte, offset int64) (int, error) {
	totalWritten := 0
	remaining := data
	currentOffset := offset

	for attempt := 0; attempt <= maxWriteRetries; attempt++ {
		written, err := writer.WriteAt(remaining, currentOffset)
		totalWritten += written

		if err != nil {
			if attempt < maxWriteRetries {
				time.Sleep(retryDelay)
				continue
			}
			return totalWritten, fmt.Errorf("write failed after %d retries: %w", maxWriteRetries, err)
		}

		if written >= len(remaining) {
			return totalWritten, nil
		}

		// Partial write: advance past what was written and retry the rest
		remaining = remaining[written:]
		currentOffset += int64(written)

		if attempt < maxWriteRetries {
			time.Sleep(retryDelay)
		}
	}

	return totalWritten, fmt.Errorf("partial write after %d retries: wrote %d of %d bytes", maxWriteRetries, totalWritten, len(data))
}

// speedTest writes up to 10MB of zeroes to verify the drive is responsive.
// Stops early if 1 second has elapsed. Returns an error if zero blocks
// were written (likely a fake or unresponsive drive).
// The data is written at offset 0 and will be overwritten by the actual image.
func (f *Flasher) speedTest(writer *diskWriter) error {
	buf := make([]byte, speedTestBlockSize)

	deadline := time.Now().Add(1 * time.Second)
	blocksWritten := 0

	for i := 0; i < speedTestMaxBlocks; i++ {
		if time.Now().After(deadline) {
			break
		}

		offset := int64(i) * int64(speedTestBlockSize)
		_, err := writer.WriteAt(buf, offset)
		if err != nil {
			break
		}
		blocksWritten++
	}

	if blocksWritten == 0 {
		return fmt.Errorf("drive unresponsive (possible fake drive)")
	}

	return nil
}

func (f *Flasher) sendProgress(opts Options, stage string, percentage int, bytesWritten, totalBytes int64, speed string) {
	select {
	case f.progressChan <- Progress{
		Stage:        stage,
		Percentage:   percentage,
		BytesWritten: bytesWritten,
		TotalBytes:   totalBytes,
		Speed:        speed,
		Status:       StatusInProgress,
	}:
	default:
	}
}

func (f *Flasher) sendError(opts Options, errMsg string) {
	select {
	case f.progressChan <- Progress{
		Stage:  "Error",
		Status: StatusError,
		Error:  errMsg,
	}:
	default:
	}
}

func (f *Flasher) sendComplete(opts Options, totalBytes int64, hash string, bytesSkipped int64) {
	select {
	case f.progressChan <- Progress{
		Stage:        StageComplete,
		Percentage:   100,
		BytesWritten: totalBytes,
		TotalBytes:   totalBytes,
		Status:       StatusComplete,
		Hash:         hash,
		BytesSkipped: bytesSkipped,
	}:
	default:
	}
}

// formatSpeed formats bytes per second into human readable string
func formatSpeed(bytesPerSec float64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)

	switch {
	case bytesPerSec >= GB:
		return fmt.Sprintf("%.1f GB/s", bytesPerSec/GB)
	case bytesPerSec >= MB:
		return fmt.Sprintf("%.1f MB/s", bytesPerSec/MB)
	case bytesPerSec >= KB:
		return fmt.Sprintf("%.1f KB/s", bytesPerSec/KB)
	default:
		return fmt.Sprintf("%.0f B/s", bytesPerSec)
	}
}

// FormatBytes formats bytes into human readable string
func FormatBytes(bytes int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)

	switch {
	case bytes >= GB:
		return fmt.Sprintf("%.1f GB", float64(bytes)/GB)
	case bytes >= MB:
		return fmt.Sprintf("%.1f MB", float64(bytes)/MB)
	case bytes >= KB:
		return fmt.Sprintf("%.1f KB", float64(bytes)/KB)
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}
