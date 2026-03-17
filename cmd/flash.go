package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/lazaroagomez/wusbkit/internal/flash"
	"github.com/lazaroagomez/wusbkit/internal/format"
	"github.com/lazaroagomez/wusbkit/internal/iso"
	"github.com/lazaroagomez/wusbkit/internal/lock"
	"github.com/lazaroagomez/wusbkit/internal/output"
	"github.com/lazaroagomez/wusbkit/internal/parallel"
	"github.com/lazaroagomez/wusbkit/internal/usb"
	"github.com/pterm/pterm"
	"github.com/spf13/cobra"
)

var (
	flashImage          string
	flashVerify         bool
	flashYes            bool
	flashBuffer         string
	flashHash           bool
	flashSkipUnchanged  bool
	flashMaxSize        string
	flashForce          bool
	flashParallel       bool
	flashMaxConcurrent  int
	flashExtract        bool
)

var flashCmd = &cobra.Command{
	Use:   "flash <drive>",
	Short: "Write an image to a USB drive",
	Long: `Write a disk image directly to a USB drive (raw write).

WARNING: This will COMPLETELY OVERWRITE the target drive!

The drive can be specified by:
  - Drive letter (e.g., E: or E)
  - Disk number (e.g., 2)
  - Multiple disks (e.g., 2,3,4 or 2-6 or 2,4-6,8)

Supported image sources:
  - Local files: .img, .iso, .bin, .raw
  - Compressed: .gz, .xz, .zst/.zstd (streaming decompression)
  - Archives: .zip (streams first image file inside)
  - Remote URLs: HTTP/HTTPS URLs (streams directly without downloading)

Windows ISO mode (--extract or auto-detected):
  When the image is a Windows ISO, the drive is partitioned, formatted,
  and the ISO contents are extracted (file copy). This produces a bootable
  Windows USB, unlike raw DD mode.`,
	Example: `  wusbkit flash 2 --image ubuntu.img
  wusbkit flash E: --image raspios.img.xz --verify
  wusbkit flash 2 --image debian.iso --yes --json
  wusbkit flash E: --image https://example.com/image.img --hash
  wusbkit flash 2,3,4 --image ubuntu.img --parallel --json --yes
  wusbkit flash 2-6 --image raspios.img --parallel --yes
  wusbkit flash 2,4-6,8 --image debian.iso --parallel --max-concurrent 3 --yes`,
	Args: cobra.ExactArgs(1),
	RunE: runFlash,
}

func init() {
	flashCmd.Flags().StringVarP(&flashImage, "image", "i", "", "Path to image file or URL (required)")
	flashCmd.Flags().BoolVar(&flashVerify, "verify", false, "Verify write by reading back and comparing")
	flashCmd.Flags().BoolVarP(&flashYes, "yes", "y", false, "Skip confirmation prompt")
	flashCmd.Flags().StringVarP(&flashBuffer, "buffer", "b", "4M", "Buffer size (e.g., 4M, 8MB)")
	flashCmd.Flags().BoolVar(&flashHash, "hash", false, "Calculate and display SHA-256 hash")
	flashCmd.Flags().BoolVar(&flashSkipUnchanged, "skip-unchanged", false, "Skip writing sectors that haven't changed")
	flashCmd.Flags().StringVar(&flashMaxSize, "max-size", "", "Maximum device size to allow (e.g., 64G, 256G)")
	flashCmd.Flags().BoolVar(&flashForce, "force", false, "Override safety protections (system disk, size limits)")
	flashCmd.Flags().BoolVar(&flashParallel, "parallel", false, "Flash same image to multiple disks in parallel")
	flashCmd.Flags().IntVar(&flashMaxConcurrent, "max-concurrent", 0, "Max concurrent operations (0=unlimited)")
	flashCmd.Flags().BoolVar(&flashExtract, "extract", false, "Extract ISO contents instead of raw write (auto-detected for Windows ISOs)")
	flashCmd.MarkFlagRequired("image")
	rootCmd.AddCommand(flashCmd)
}

// parseSize converts size strings like "64G", "256M", "1T" to bytes.
func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" {
		return 0, nil
	}
	s = strings.TrimSuffix(s, "B") // Remove trailing B if present

	var multiplier int64 = 1
	switch {
	case strings.HasSuffix(s, "T"):
		multiplier = 1024 * 1024 * 1024 * 1024
		s = s[:len(s)-1]
	case strings.HasSuffix(s, "G"):
		multiplier = 1024 * 1024 * 1024
		s = s[:len(s)-1]
	case strings.HasSuffix(s, "M"):
		multiplier = 1024 * 1024
		s = s[:len(s)-1]
	case strings.HasSuffix(s, "K"):
		multiplier = 1024
		s = s[:len(s)-1]
	}

	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size: %s", s)
	}
	return n * multiplier, nil
}

// parseBufferSize converts buffer size strings like "4M", "8MB", "16m" to megabytes.
// Returns the size in MB or an error if the format is invalid.
func parseBufferSize(s string) (int, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	s = strings.TrimSuffix(s, "B") // Remove trailing B if present (8MB -> 8M)

	if strings.HasSuffix(s, "M") {
		val, err := strconv.Atoi(strings.TrimSuffix(s, "M"))
		if err != nil {
			return 0, fmt.Errorf("invalid buffer size: %s", s)
		}
		return val, nil
	}

	// Try plain number (assume MB)
	val, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("invalid buffer size: %s (use format like 4M or 8MB)", s)
	}
	return val, nil
}

func runFlash(cmd *cobra.Command, args []string) error {
	identifier := args[0]

	// Check if parallel mode (explicit flag or multi-disk syntax)
	if flashParallel || parallel.IsMultiDiskArg(identifier) {
		return runParallelFlash(cmd, args)
	}

	return runSingleFlash(cmd, args)
}

func runSingleFlash(cmd *cobra.Command, args []string) error {
	identifier := args[0]

	// Check if image is a URL (skip file existence check for URLs)
	isURL := flash.IsURL(flashImage)

	// Validate local image file exists (skip for URLs)
	if !isURL {
		if _, err := os.Stat(flashImage); os.IsNotExist(err) {
			errMsg := fmt.Sprintf("Image file not found: %s", flashImage)
			if jsonOutput {
				output.PrintJSONError(errMsg, output.ErrCodeInvalidInput)
			} else {
				PrintError(errMsg, output.ErrCodeInvalidInput)
			}
			return errors.New(errMsg)
		}
	}

	// Check for admin privileges
	if !format.IsAdmin() {
		errMsg := "Administrator privileges required for flashing"
		if jsonOutput {
			output.PrintJSONError(errMsg, output.ErrCodePermDenied)
		} else {
			PrintError(errMsg, output.ErrCodePermDenied)
		}
		return errors.New(errMsg)
	}

	// Find the device
	enum := usb.NewEnumerator()
	device, err := enum.GetDevice(identifier)
	if err != nil {
		if jsonOutput {
			output.PrintJSONError(err.Error(), output.ErrCodeUSBNotFound)
		} else {
			PrintError(err.Error(), output.ErrCodeUSBNotFound)
		}
		return err
	}

	// Safety checks (can be overridden with --force)
	if !flashForce {
		// Check max size limit
		if flashMaxSize != "" {
			maxSize, err := parseSize(flashMaxSize)
			if err != nil {
				if jsonOutput {
					output.PrintJSONError(err.Error(), output.ErrCodeInvalidInput)
				} else {
					PrintError(err.Error(), output.ErrCodeInvalidInput)
				}
				return err
			}
			if maxSize > 0 && device.Size > maxSize {
				errMsg := fmt.Sprintf("Device size (%s) exceeds maximum allowed (%s). Use --force to override.",
					device.SizeHuman, flashMaxSize)
				if jsonOutput {
					output.PrintJSONError(errMsg, output.ErrCodeInvalidInput)
				} else {
					PrintError(errMsg, output.ErrCodeInvalidInput)
				}
				return errors.New(errMsg)
			}
		}

		// Check if this is a system disk
		isSystem, _ := enum.IsSystemDisk(device.DiskNumber)
		if isSystem {
			errMsg := fmt.Sprintf("Disk %d appears to be a system disk. Use --force to override.", device.DiskNumber)
			if jsonOutput {
				output.PrintJSONError(errMsg, output.ErrCodeInvalidInput)
			} else {
				PrintError(errMsg, output.ErrCodeInvalidInput)
			}
			return errors.New(errMsg)
		}
	}

	// Acquire exclusive lock on the disk
	diskLock, err := lock.NewDiskLock(device.DiskNumber)
	if err != nil {
		errMsg := fmt.Sprintf("Failed to create disk lock: %v", err)
		if jsonOutput {
			output.PrintJSONError(errMsg, output.ErrCodeInvalidInput)
		} else {
			PrintError(errMsg, output.ErrCodeInvalidInput)
		}
		return errors.New(errMsg)
	}

	if err := diskLock.TryLock(context.Background(), 2*time.Second); err != nil {
		if jsonOutput {
			output.PrintJSONError(err.Error(), output.ErrCodeInvalidInput)
		} else {
			PrintError(err.Error(), output.ErrCodeInvalidInput)
		}
		return err
	}
	defer diskLock.Unlock()

	// Get image info for display
	source, err := flash.OpenSource(flashImage)
	if err != nil {
		if jsonOutput {
			output.PrintJSONError(err.Error(), output.ErrCodeInvalidInput)
		} else {
			PrintError(err.Error(), output.ErrCodeInvalidInput)
		}
		return err
	}
	imageSize := source.Size()
	imageName := source.Name()
	source.Close()

	// Validate image fits on device
	if imageSize > device.Size {
		errMsg := fmt.Sprintf("Image (%s) is larger than device (%s)",
			flash.FormatBytes(imageSize), device.SizeHuman)
		if jsonOutput {
			output.PrintJSONError(errMsg, output.ErrCodeInvalidInput)
		} else {
			PrintError(errMsg, output.ErrCodeInvalidInput)
		}
		return errors.New(errMsg)
	}

	// Confirmation prompt (unless --yes or --json)
	if !flashYes && !jsonOutput {
		pterm.Warning.Printf("This will COMPLETELY OVERWRITE disk %d (%s - %s)\n",
			device.DiskNumber, device.FriendlyName, device.SizeHuman)
		pterm.Info.Printf("Image: %s (%s)\n", imageName, flash.FormatBytes(imageSize))

		if flashVerify {
			pterm.Info.Println("Verification: enabled")
		}

		confirmed, _ := pterm.DefaultInteractiveConfirm.
			WithDefaultValue(false).
			Show("Continue with flash?")

		if !confirmed {
			pterm.Info.Println("Flash cancelled")
			return nil
		}
	}

	// Setup context with cancellation for Ctrl+C
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		if !jsonOutput {
			pterm.Warning.Println("\nCancelling... (waiting for current operation)")
		}
		cancel()
	}()

	// Parse and validate buffer size
	bufferMB, err := parseBufferSize(flashBuffer)
	if err != nil {
		if jsonOutput {
			output.PrintJSONError(err.Error(), output.ErrCodeInvalidInput)
		} else {
			PrintError(err.Error(), output.ErrCodeInvalidInput)
		}
		return err
	}
	if bufferMB < 1 || bufferMB > 64 {
		errMsg := fmt.Sprintf("buffer size must be between 1M and 64M (got %dM)", bufferMB)
		if jsonOutput {
			output.PrintJSONError(errMsg, output.ErrCodeInvalidInput)
		} else {
			PrintError(errMsg, output.ErrCodeInvalidInput)
		}
		return errors.New(errMsg)
	}

	// Auto-detect extract mode for Windows ISOs
	useExtract := flashExtract
	if !useExtract && !isURL && strings.HasSuffix(strings.ToLower(flashImage), ".iso") {
		if iso.IsWindowsISO(flashImage) {
			useExtract = true
			if !jsonOutput {
				pterm.Info.Println("Windows ISO detected — using extract mode (partition + format + file copy)")
			}
		}
	}

	// Extract mode: use ISO pipeline instead of raw flasher
	if useExtract {
		return runExtractFlash(ctx, device.DiskNumber, flashImage)
	}

	// Prepare flash options
	opts := flash.Options{
		DiskNumber:    device.DiskNumber,
		ImagePath:     flashImage,
		Verify:        flashVerify,
		BufferSize:    bufferMB,
		CalculateHash: flashHash,
		SkipUnchanged: flashSkipUnchanged,
	}

	flasher := flash.NewFlasher()

	// Start flash in background
	errChan := make(chan error, 1)
	go func() {
		_, _, err := flasher.Flash(ctx, opts)
		errChan <- err
	}()

	// Show progress
	if jsonOutput {
		// Stream JSON progress
		for progress := range flasher.Progress() {
			data, _ := json.Marshal(progress)
			fmt.Println(string(data))
		}
	} else {
		// Show spinner with progress updates
		spinner, _ := pterm.DefaultSpinner.Start("Preparing to write...")

		for progress := range flasher.Progress() {
			switch progress.Status {
			case flash.StatusInProgress:
				text := fmt.Sprintf("%s %d%% | %s / %s",
					progress.Stage,
					progress.Percentage,
					flash.FormatBytes(progress.BytesWritten),
					flash.FormatBytes(progress.TotalBytes))
				if progress.Speed != "" {
					text += fmt.Sprintf(" | %s", progress.Speed)
				}
				spinner.UpdateText(text)

			case flash.StatusError:
				spinner.Fail(progress.Error)

			case flash.StatusComplete:
				msg := "Flash complete!"
				if flashVerify {
					msg += " (verified)"
				}
				spinner.Success(msg)
				if progress.Hash != "" {
					pterm.Info.Printf("SHA-256: %s\n", progress.Hash)
				}
				if progress.BytesSkipped > 0 {
					pterm.Info.Printf("Skipped: %s (unchanged)\n", flash.FormatBytes(progress.BytesSkipped))
				}
			}
		}
	}

	// Wait for flash to complete
	if err := <-errChan; err != nil {
		if !jsonOutput && err != context.Canceled {
			PrintError(err.Error(), output.ErrCodeFlashFailed)
		}
		return err
	}

	return nil
}

// runExtractFlash uses the ISO pipeline to partition, format, and extract ISO contents.
func runExtractFlash(ctx context.Context, diskNumber int, isoPath string) error {
	writer := iso.NewWriter()

	errChan := make(chan error, 1)
	go func() {
		errChan <- writer.Write(ctx, iso.WriteOptions{
			DiskNumber: diskNumber,
			ISOPath:    isoPath,
		})
	}()

	if jsonOutput {
		for p := range writer.Progress() {
			data, _ := json.Marshal(p)
			fmt.Println(string(data))
		}
	} else {
		spinner, _ := pterm.DefaultSpinner.Start("Preparing ISO extract...")

		for p := range writer.Progress() {
			if p.Error != "" {
				spinner.Fail(p.Error)
			} else if p.Percentage >= 100 {
				spinner.Success("ISO written successfully!")
			} else {
				spinner.UpdateText(fmt.Sprintf("[%s] %d%% — %s", p.Stage, p.Percentage, p.Status))
			}
		}
	}

	if err := <-errChan; err != nil {
		if !jsonOutput && err != context.Canceled {
			PrintError(err.Error(), output.ErrCodeFlashFailed)
		}
		return err
	}

	return nil
}

// runParallelFlash flashes the same image to multiple disks in parallel
func runParallelFlash(cmd *cobra.Command, args []string) error {
	identifier := args[0]

	// Parse disk numbers
	disks, err := parallel.ParseDisks(identifier)
	if err != nil {
		if jsonOutput {
			output.PrintJSONError(err.Error(), output.ErrCodeInvalidInput)
		} else {
			PrintError(err.Error(), output.ErrCodeInvalidInput)
		}
		return err
	}

	if len(disks) == 0 {
		errMsg := "no valid disk numbers provided"
		if jsonOutput {
			output.PrintJSONError(errMsg, output.ErrCodeInvalidInput)
		} else {
			PrintError(errMsg, output.ErrCodeInvalidInput)
		}
		return errors.New(errMsg)
	}

	// Check if image is a URL (skip file existence check for URLs)
	isURL := flash.IsURL(flashImage)

	// Validate local image file exists (skip for URLs)
	if !isURL {
		if _, err := os.Stat(flashImage); os.IsNotExist(err) {
			errMsg := fmt.Sprintf("Image file not found: %s", flashImage)
			if jsonOutput {
				output.PrintJSONError(errMsg, output.ErrCodeInvalidInput)
			} else {
				PrintError(errMsg, output.ErrCodeInvalidInput)
			}
			return errors.New(errMsg)
		}
	}

	// Check for admin privileges
	if !format.IsAdmin() {
		errMsg := "Administrator privileges required for flashing"
		if jsonOutput {
			output.PrintJSONError(errMsg, output.ErrCodePermDenied)
		} else {
			PrintError(errMsg, output.ErrCodePermDenied)
		}
		return errors.New(errMsg)
	}

	// Get image info for display
	source, err := flash.OpenSource(flashImage)
	if err != nil {
		if jsonOutput {
			output.PrintJSONError(err.Error(), output.ErrCodeInvalidInput)
		} else {
			PrintError(err.Error(), output.ErrCodeInvalidInput)
		}
		return err
	}
	imageSize := source.Size()
	imageName := source.Name()
	source.Close()

	// Validate all disks exist, are USB, and can hold the image
	enum := usb.NewEnumerator()
	var deviceNames []string
	for _, diskNum := range disks {
		device, err := enum.GetDeviceByDiskNumber(diskNum)
		if err != nil {
			errMsg := fmt.Sprintf("disk %d: %v", diskNum, err)
			if jsonOutput {
				output.PrintJSONError(errMsg, output.ErrCodeUSBNotFound)
			} else {
				PrintError(errMsg, output.ErrCodeUSBNotFound)
			}
			return fmt.Errorf("disk %d: %w", diskNum, err)
		}
		if device == nil {
			errMsg := fmt.Sprintf("disk %d: not found or not a USB device", diskNum)
			if jsonOutput {
				output.PrintJSONError(errMsg, output.ErrCodeUSBNotFound)
			} else {
				PrintError(errMsg, output.ErrCodeUSBNotFound)
			}
			return fmt.Errorf("disk %d: not found or not a USB device", diskNum)
		}

		// Validate image fits on device
		if imageSize > device.Size {
			errMsg := fmt.Sprintf("disk %d: image (%s) is larger than device (%s)",
				diskNum, flash.FormatBytes(imageSize), device.SizeHuman)
			if jsonOutput {
				output.PrintJSONError(errMsg, output.ErrCodeInvalidInput)
			} else {
				PrintError(errMsg, output.ErrCodeInvalidInput)
			}
			return errors.New(errMsg)
		}

		// Safety checks (unless --force)
		if !flashForce {
			// Check max size limit
			if flashMaxSize != "" {
				maxSize, err := parseSize(flashMaxSize)
				if err != nil {
					if jsonOutput {
						output.PrintJSONError(err.Error(), output.ErrCodeInvalidInput)
					} else {
						PrintError(err.Error(), output.ErrCodeInvalidInput)
					}
					return err
				}
				if maxSize > 0 && device.Size > maxSize {
					errMsg := fmt.Sprintf("disk %d: size (%s) exceeds maximum allowed (%s)",
						diskNum, device.SizeHuman, flashMaxSize)
					if jsonOutput {
						output.PrintJSONError(errMsg, output.ErrCodeInvalidInput)
					} else {
						PrintError(errMsg, output.ErrCodeInvalidInput)
					}
					return errors.New(errMsg)
				}
			}

			// Check if this is a system disk
			isSystem, _ := enum.IsSystemDisk(device.DiskNumber)
			if isSystem {
				errMsg := fmt.Sprintf("disk %d appears to be a system disk", diskNum)
				if jsonOutput {
					output.PrintJSONError(errMsg, output.ErrCodeInvalidInput)
				} else {
					PrintError(errMsg, output.ErrCodeInvalidInput)
				}
				return errors.New(errMsg)
			}
		}

		deviceNames = append(deviceNames, fmt.Sprintf("%d (%s - %s)", diskNum, device.FriendlyName, device.SizeHuman))
	}

	// Confirmation prompt (unless --yes or --json)
	if !flashYes && !jsonOutput {
		pterm.Warning.Printf("This will COMPLETELY OVERWRITE %d drives:\n", len(disks))
		for _, name := range deviceNames {
			pterm.Info.Printf("  Disk %s\n", name)
		}
		pterm.Info.Printf("Image: %s (%s)\n", imageName, flash.FormatBytes(imageSize))

		if flashVerify {
			pterm.Info.Println("Verification: enabled")
		}

		confirmed, _ := pterm.DefaultInteractiveConfirm.
			WithDefaultValue(false).
			Show("Continue with parallel flash?")

		if !confirmed {
			pterm.Info.Println("Flash cancelled")
			return nil
		}
	}

	// Parse and validate buffer size
	bufferMB, err := parseBufferSize(flashBuffer)
	if err != nil {
		if jsonOutput {
			output.PrintJSONError(err.Error(), output.ErrCodeInvalidInput)
		} else {
			PrintError(err.Error(), output.ErrCodeInvalidInput)
		}
		return err
	}
	if bufferMB < 1 || bufferMB > 64 {
		errMsg := fmt.Sprintf("buffer size must be between 1M and 64M (got %dM)", bufferMB)
		if jsonOutput {
			output.PrintJSONError(errMsg, output.ErrCodeInvalidInput)
		} else {
			PrintError(errMsg, output.ErrCodeInvalidInput)
		}
		return errors.New(errMsg)
	}

	// Build options
	opts := flash.Options{
		ImagePath:     flashImage,
		Verify:        flashVerify,
		BufferSize:    bufferMB,
		CalculateHash: flashHash,
		SkipUnchanged: flashSkipUnchanged,
	}

	// Setup context with cancellation for Ctrl+C
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		if !jsonOutput {
			pterm.Warning.Println("\nCancelling... (waiting for current operations)")
		}
		cancel()
	}()

	// Execute parallel flash
	executor := parallel.NewExecutor(flashMaxConcurrent, jsonOutput)

	if !jsonOutput {
		pterm.Info.Printf("Flashing %d drives in parallel...\n", len(disks))
	}

	result := executor.FlashAll(ctx, disks, opts)

	// Output result (non-JSON mode - JSON mode streams NDJSON)
	if !jsonOutput {
		parallel.PrintBatchResult(result, "Flashed")
	}

	if result.Failed > 0 {
		return fmt.Errorf("%d drives failed to flash", result.Failed)
	}
	return nil
}
