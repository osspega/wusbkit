package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lazaroagomez/wusbkit/internal/format"
	"github.com/lazaroagomez/wusbkit/internal/lock"
	"github.com/lazaroagomez/wusbkit/internal/output"
	"github.com/lazaroagomez/wusbkit/internal/parallel"
	"github.com/lazaroagomez/wusbkit/internal/usb"
	"github.com/pterm/pterm"
	"github.com/spf13/cobra"
)

var (
	formatYes         bool
	formatFS          string
	formatLabel       string
	formatQuick       bool
	formatParallel    bool
	formatMaxConcurrent int
)

var formatCmd = &cobra.Command{
	Use:   "format <drive>",
	Short: "Format a USB drive",
	Long: `Format a USB storage device with the specified filesystem.

WARNING: This will ERASE ALL DATA on the drive!

The drive can be specified by:
  - Drive letter (e.g., E: or E)
  - Disk number (e.g., 2)
  - Multiple disks (e.g., 2,3,4 or 2-6 or 2,4-6,8)

Supported filesystems: fat32, ntfs, exfat`,
	Example: `  wusbkit format E: --fs fat32 --label MYUSB
  wusbkit format 2 --fs ntfs --yes
  wusbkit format E: --fs exfat --label DATA --quick=false
  wusbkit format 2,3,4,5 --fs exfat --label "USB" --parallel --json --yes
  wusbkit format 2-6 --fs fat32 --parallel --yes
  wusbkit format 2,4-6,8 --fs exfat --parallel --max-concurrent 3 --yes`,
	Args: cobra.ExactArgs(1),
	RunE: runFormat,
}

func init() {
	formatCmd.Flags().BoolVarP(&formatYes, "yes", "y", false, "Skip confirmation prompt")
	formatCmd.Flags().StringVar(&formatFS, "fs", "fat32", "Filesystem type: fat32, ntfs, exfat")
	formatCmd.Flags().StringVar(&formatLabel, "label", "USB", "Volume label")
	formatCmd.Flags().BoolVar(&formatQuick, "quick", true, "Quick format")
	formatCmd.Flags().BoolVar(&formatParallel, "parallel", false, "Format multiple disks in parallel")
	formatCmd.Flags().IntVar(&formatMaxConcurrent, "max-concurrent", 0, "Max concurrent operations (0=unlimited)")
	rootCmd.AddCommand(formatCmd)
}

func runFormat(cmd *cobra.Command, args []string) error {
	identifier := args[0]

	// Check if parallel mode (explicit flag or multi-disk syntax)
	if formatParallel || parallel.IsMultiDiskArg(identifier) {
		return runParallelFormat(cmd, args)
	}

	return runSingleFormat(cmd, args)
}

func runSingleFormat(cmd *cobra.Command, args []string) error {
	identifier := args[0]

	// Validate filesystem
	if err := format.ValidateFileSystem(formatFS); err != nil {
		if jsonOutput {
			output.PrintJSONError(err.Error(), output.ErrCodeInvalidInput)
		} else {
			PrintError(err.Error(), output.ErrCodeInvalidInput)
		}
		return err
	}

	// Check for admin privileges
	if !format.IsAdmin() {
		errMsg := "Administrator privileges required for formatting"
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

	// Check if disk is being flashed
	diskLock, err := lock.NewDiskLock(device.DiskNumber)
	if err != nil {
		errMsg := fmt.Sprintf("failed to create disk lock: %v", err)
		if jsonOutput {
			output.PrintJSONError(errMsg, output.ErrCodeInternalError)
		} else {
			PrintError(errMsg, output.ErrCodeInternalError)
		}
		return err
	}

	if err := diskLock.TryLock(cmd.Context(), 1*time.Second); err != nil {
		errMsg := fmt.Sprintf("disk %d is busy (another operation in progress)", device.DiskNumber)
		if jsonOutput {
			output.PrintJSONError(errMsg, output.ErrCodeDiskBusy)
		} else {
			PrintError(errMsg, output.ErrCodeDiskBusy)
		}
		return errors.New(errMsg)
	}
	defer diskLock.Unlock()

	// Confirmation prompt (unless --yes or --json)
	if !formatYes && !jsonOutput {
		pterm.Warning.Printf("This will ERASE ALL DATA on disk %d (%s - %s)\n",
			device.DiskNumber, device.FriendlyName, device.SizeHuman)

		confirmed, _ := pterm.DefaultInteractiveConfirm.
			WithDefaultValue(false).
			Show("Continue with format?")

		if !confirmed {
			pterm.Info.Println("Format cancelled")
			return nil
		}
	}

	// Perform format
	opts := format.Options{
		DiskNumber: device.DiskNumber,
		FileSystem: formatFS,
		Label:      formatLabel,
		Quick:      formatQuick,
	}

	formatter := format.NewFormatter()

	// Start format in background
	ctx := context.Background()
	errChan := make(chan error, 1)

	go func() {
		errChan <- formatter.Format(ctx, opts)
	}()

	// Show progress
	if jsonOutput {
		// Stream JSON progress
		for progress := range formatter.Progress() {
			data, _ := json.Marshal(progress)
			fmt.Println(string(data))
		}
	} else {
		// Show spinner with progress updates
		spinner, _ := pterm.DefaultSpinner.Start("Starting format...")

		for progress := range formatter.Progress() {
			switch progress.Status {
			case "in_progress":
				spinner.UpdateText(fmt.Sprintf("%s (%d%%)", progress.Stage, progress.Percentage))
			case "error":
				spinner.Fail(progress.Error)
			case "complete":
				if progress.Drive != "" {
					spinner.Success(fmt.Sprintf("Format complete! Drive assigned: %s", progress.Drive))
				} else {
					spinner.Success("Format complete!")
				}
			}
		}
	}

	// Wait for format to complete
	if err := <-errChan; err != nil {
		if !jsonOutput {
			PrintError(err.Error(), output.ErrCodeFormatFailed)
		}
		return err
	}

	return nil
}

// runParallelFormat formats multiple disks in parallel
func runParallelFormat(cmd *cobra.Command, args []string) error {
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

	// Validate filesystem
	if err := format.ValidateFileSystem(formatFS); err != nil {
		if jsonOutput {
			output.PrintJSONError(err.Error(), output.ErrCodeInvalidInput)
		} else {
			PrintError(err.Error(), output.ErrCodeInvalidInput)
		}
		return err
	}

	// Check for admin privileges
	if !format.IsAdmin() {
		errMsg := "Administrator privileges required for formatting"
		if jsonOutput {
			output.PrintJSONError(errMsg, output.ErrCodePermDenied)
		} else {
			PrintError(errMsg, output.ErrCodePermDenied)
		}
		return errors.New(errMsg)
	}

	// Validate all disks exist and are USB
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
		deviceNames = append(deviceNames, fmt.Sprintf("%d (%s - %s)", diskNum, device.FriendlyName, device.SizeHuman))
	}

	// Confirmation prompt (unless --yes or --json)
	if !formatYes && !jsonOutput {
		pterm.Warning.Printf("This will ERASE ALL DATA on %d drives:\n", len(disks))
		for _, name := range deviceNames {
			pterm.Info.Printf("  Disk %s\n", name)
		}

		confirmed, _ := pterm.DefaultInteractiveConfirm.
			WithDefaultValue(false).
			Show("Continue with parallel format?")

		if !confirmed {
			pterm.Info.Println("Format cancelled")
			return nil
		}
	}

	// Build options
	opts := format.Options{
		FileSystem: formatFS,
		Label:      formatLabel,
		Quick:      formatQuick,
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

	// Execute parallel format
	executor := parallel.NewExecutor(formatMaxConcurrent, jsonOutput)

	if !jsonOutput {
		pterm.Info.Printf("Formatting %d drives in parallel...\n", len(disks))
	}

	result := executor.FormatAll(ctx, disks, opts)

	// Output result (non-JSON mode - JSON mode streams NDJSON)
	if !jsonOutput {
		parallel.PrintBatchResult(result, "Formatted")
	}

	if result.Failed > 0 {
		return fmt.Errorf("%d drives failed to format", result.Failed)
	}
	return nil
}
