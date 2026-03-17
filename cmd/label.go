package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/lazaroagomez/wusbkit/internal/output"
	"github.com/lazaroagomez/wusbkit/internal/parallel"
	"github.com/lazaroagomez/wusbkit/internal/usb"
	"github.com/pterm/pterm"
	"github.com/spf13/cobra"
)

const (
	labelMaxRetries = 3
	labelRetryDelay = 500 * time.Millisecond
)

// setVolumeLabel sets the volume label using the native Windows API.
// Retries up to 3 times with 500ms delay to handle transient USB errors.
func setVolumeLabel(driveLetter, label string) error {
	rootPath := driveLetter + ":\\"
	rootPtr, err := syscall.UTF16PtrFromString(rootPath)
	if err != nil {
		return fmt.Errorf("invalid drive letter: %w", err)
	}
	labelPtr, err := syscall.UTF16PtrFromString(label)
	if err != nil {
		return fmt.Errorf("invalid label: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt < labelMaxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(labelRetryDelay)
		}
		lastErr = setVolumeLabelW(rootPtr, labelPtr)
		if lastErr == nil {
			return nil
		}
	}
	return fmt.Errorf("SetVolumeLabelW failed after %d attempts: %w", labelMaxRetries, lastErr)
}

var procSetVolumeLabelW = syscall.NewLazyDLL("kernel32.dll").NewProc("SetVolumeLabelW")

func setVolumeLabelW(rootPathName *uint16, volumeName *uint16) error {
	r1, _, err := procSetVolumeLabelW.Call(
		uintptr(unsafe.Pointer(rootPathName)),
		uintptr(unsafe.Pointer(volumeName)),
	)
	if r1 == 0 {
		return err
	}
	return nil
}

var (
	labelName          string
	labelParallel      bool
	labelMaxConcurrent int
)

var labelCmd = &cobra.Command{
	Use:   "label <drive>",
	Short: "Set volume label for a USB drive",
	Long: `Changes the volume label of a USB drive without reformatting.

The drive can be specified by:
  - Drive letter (e.g., E: or E)
  - Disk number (e.g., 2)
  - Multiple drives (e.g., E,F,G or 2,3,4 or 2-6)

This operation does not require administrator privileges for USB drives.`,
	Example: `  wusbkit label E: --name "BACKUP_001"
  wusbkit label F --name "USB_DATA" --json
  wusbkit label E,F,G --name "USB_DATA" --parallel
  wusbkit label 2,3,4 --name "BACKUP" --parallel --json
  wusbkit label 2-6 --name "USB" --parallel --max-concurrent 3`,
	Args: cobra.ExactArgs(1),
	RunE: runLabel,
}

func init() {
	labelCmd.Flags().StringVar(&labelName, "name", "", "New volume label (required)")
	labelCmd.Flags().BoolVar(&labelParallel, "parallel", false, "Label multiple drives in parallel")
	labelCmd.Flags().IntVar(&labelMaxConcurrent, "max-concurrent", 0, "Max concurrent operations (0=unlimited)")
	labelCmd.MarkFlagRequired("name")
	rootCmd.AddCommand(labelCmd)
}

func runLabel(cmd *cobra.Command, args []string) error {
	identifier := args[0]

	// Check if parallel mode (explicit flag or multi-drive syntax)
	if labelParallel || parallel.IsMultiDiskArg(identifier) {
		return runParallelLabel(cmd, args)
	}

	return runSingleLabel(cmd, args)
}

func runSingleLabel(cmd *cobra.Command, args []string) error {
	// Parse and validate drive letter
	driveLetter := strings.TrimSuffix(strings.ToUpper(args[0]), ":")
	if len(driveLetter) != 1 || driveLetter[0] < 'A' || driveLetter[0] > 'Z' {
		errMsg := fmt.Sprintf("invalid drive letter: %s", args[0])
		if jsonOutput {
			output.PrintJSONError(errMsg, output.ErrCodeInvalidInput)
		} else {
			PrintError(errMsg, output.ErrCodeInvalidInput)
		}
		return errors.New(errMsg)
	}

	// Validate label is not empty
	if strings.TrimSpace(labelName) == "" {
		errMsg := "label name cannot be empty"
		if jsonOutput {
			output.PrintJSONError(errMsg, output.ErrCodeInvalidInput)
		} else {
			PrintError(errMsg, output.ErrCodeInvalidInput)
		}
		return errors.New(errMsg)
	}

	// Verify it's a USB drive
	enum := usb.NewEnumerator()
	device, err := enum.GetDeviceByDriveLetter(driveLetter)
	if err != nil {
		if jsonOutput {
			output.PrintJSONError(err.Error(), output.ErrCodeUSBNotFound)
		} else {
			PrintError(err.Error(), output.ErrCodeUSBNotFound)
		}
		return err
	}
	if device == nil {
		errMsg := fmt.Sprintf("drive %s: not found or not a USB device", driveLetter)
		if jsonOutput {
			output.PrintJSONError(errMsg, output.ErrCodeUSBNotFound)
		} else {
			PrintError(errMsg, output.ErrCodeUSBNotFound)
		}
		return errors.New(errMsg)
	}

	// Use Windows API to set volume label (no admin required for USB drives)
	if err := setVolumeLabel(driveLetter, labelName); err != nil {
		errMsg := fmt.Sprintf("failed to set label: %v", err)
		if jsonOutput {
			output.PrintJSONError(errMsg, output.ErrCodeInternalError)
		} else {
			PrintError(errMsg, output.ErrCodeInternalError)
		}
		return err
	}

	// Output result
	if jsonOutput {
		return PrintJSON(map[string]interface{}{
			"success":     true,
			"driveLetter": driveLetter + ":",
			"label":       labelName,
		})
	}

	pterm.Success.Printf("Label set to \"%s\" on drive %s:\n", labelName, driveLetter)
	return nil
}

// runParallelLabel labels multiple drives in parallel
func runParallelLabel(cmd *cobra.Command, args []string) error {
	identifier := args[0]

	// Validate label is not empty
	if strings.TrimSpace(labelName) == "" {
		errMsg := "label name cannot be empty"
		if jsonOutput {
			output.PrintJSONError(errMsg, output.ErrCodeInvalidInput)
		} else {
			PrintError(errMsg, output.ErrCodeInvalidInput)
		}
		return errors.New(errMsg)
	}

	// Parse the identifier - could be drive letters (E,F,G) or disk numbers (2,3,4)
	driveLetters, err := parseDriversOrDisks(identifier)
	if err != nil {
		if jsonOutput {
			output.PrintJSONError(err.Error(), output.ErrCodeInvalidInput)
		} else {
			PrintError(err.Error(), output.ErrCodeInvalidInput)
		}
		return err
	}

	if len(driveLetters) == 0 {
		errMsg := "no valid drives provided"
		if jsonOutput {
			output.PrintJSONError(errMsg, output.ErrCodeInvalidInput)
		} else {
			PrintError(errMsg, output.ErrCodeInvalidInput)
		}
		return errors.New(errMsg)
	}

	// Validate all drives are USB devices
	enum := usb.NewEnumerator()
	var driveNames []string
	for _, dl := range driveLetters {
		device, err := enum.GetDeviceByDriveLetter(dl)
		if err != nil {
			errMsg := fmt.Sprintf("drive %s: %v", dl, err)
			if jsonOutput {
				output.PrintJSONError(errMsg, output.ErrCodeUSBNotFound)
			} else {
				PrintError(errMsg, output.ErrCodeUSBNotFound)
			}
			return fmt.Errorf("drive %s: %w", dl, err)
		}
		if device == nil {
			errMsg := fmt.Sprintf("drive %s: not found or not a USB device", dl)
			if jsonOutput {
				output.PrintJSONError(errMsg, output.ErrCodeUSBNotFound)
			} else {
				PrintError(errMsg, output.ErrCodeUSBNotFound)
			}
			return fmt.Errorf("drive %s: not found or not a USB device", dl)
		}
		driveNames = append(driveNames, fmt.Sprintf("%s: (%s - %s)", dl, device.FriendlyName, device.SizeHuman))
	}

	// Show info in non-JSON mode
	if !jsonOutput {
		pterm.Info.Printf("Setting label \"%s\" on %d drives:\n", labelName, len(driveLetters))
		for _, name := range driveNames {
			pterm.Info.Printf("  Drive %s\n", name)
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
			pterm.Warning.Println("\nCancelling...")
		}
		cancel()
	}()

	// Execute parallel label
	opts := parallel.LabelOptions{
		Label: labelName,
	}

	executor := parallel.NewExecutor(labelMaxConcurrent, jsonOutput)
	result := executor.LabelAll(ctx, driveLetters, opts)

	// Output result (non-JSON mode - JSON mode streams NDJSON)
	if !jsonOutput {
		parallel.PrintBatchResult(result, "Labeled")
	}

	if result.Failed > 0 {
		return fmt.Errorf("%d drives failed to label", result.Failed)
	}
	return nil
}

// parseDriversOrDisks parses an identifier that could be drive letters (E,F,G) or disk numbers (2,3,4)
// and returns a list of drive letters
func parseDriversOrDisks(identifier string) ([]string, error) {
	// First, try to parse as disk numbers
	if isNumericIdentifier(identifier) {
		disks, err := parallel.ParseDisks(identifier)
		if err != nil {
			return nil, err
		}

		// Convert disk numbers to drive letters
		enum := usb.NewEnumerator()
		var letters []string
		for _, diskNum := range disks {
			device, err := enum.GetDeviceByDiskNumber(diskNum)
			if err != nil {
				return nil, fmt.Errorf("disk %d: %v", diskNum, err)
			}
			if device == nil {
				return nil, fmt.Errorf("disk %d: not found", diskNum)
			}
			if device.DriveLetter == "" {
				return nil, fmt.Errorf("disk %d: no drive letter assigned", diskNum)
			}
			// Extract just the letter from "E:" format
			letters = append(letters, strings.TrimSuffix(device.DriveLetter, ":"))
		}
		return letters, nil
	}

	// Parse as drive letters (E,F,G or E:,F:,G:)
	parts := strings.Split(identifier, ",")
	var letters []string
	for _, part := range parts {
		part = strings.TrimSpace(part)
		part = strings.TrimSuffix(strings.ToUpper(part), ":")
		if len(part) != 1 || part[0] < 'A' || part[0] > 'Z' {
			return nil, fmt.Errorf("invalid drive letter: %s", part)
		}
		letters = append(letters, part)
	}
	return uniqueStrings(letters), nil
}

// isNumericIdentifier checks if the identifier contains only numbers, commas, and dashes
func isNumericIdentifier(s string) bool {
	for _, c := range s {
		if c != ',' && c != '-' && (c < '0' || c > '9') {
			return false
		}
	}
	// Must contain at least one digit
	for _, c := range s {
		if c >= '0' && c <= '9' {
			return true
		}
	}
	return false
}

// uniqueStrings removes duplicates from a string slice
func uniqueStrings(strs []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(strs))
	for _, s := range strs {
		if !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}
	return result
}
