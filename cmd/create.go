package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/lazaroagomez/wusbkit/internal/format"
	"github.com/lazaroagomez/wusbkit/internal/image"
	"github.com/lazaroagomez/wusbkit/internal/lock"
	"github.com/lazaroagomez/wusbkit/internal/output"
	"github.com/lazaroagomez/wusbkit/internal/usb"
	"github.com/pterm/pterm"
	"github.com/spf13/cobra"
)

var (
	createOutput string
	createYes    bool
	createVerify bool
)

var createCmd = &cobra.Command{
	Use:   "create <drive>",
	Short: "Create a .bin image from a USB drive",
	Long: `Read an entire USB drive and save it as an ImageUSB-compatible .bin file.

The output file includes a 512-byte header with MD5 and SHA1 checksums,
compatible with ImageUSB's .bin format. The image can later be written
back using the flash command.

The drive can be specified by:
  - Drive letter (e.g., E: or E)
  - Disk number (e.g., 2)`,
	Example: `  wusbkit create E: --output backup.bin
  wusbkit create 2 --output D:\images\usb_backup.bin --yes
  wusbkit create E: --output backup.bin --json`,
	Args: cobra.ExactArgs(1),
	RunE: runCreate,
}

func init() {
	createCmd.Flags().StringVarP(&createOutput, "output", "o", "", "Output .bin file path (required)")
	createCmd.Flags().BoolVarP(&createYes, "yes", "y", false, "Skip confirmation prompt")
	createCmd.Flags().BoolVar(&createVerify, "verify", false, "Verify image after creation")
	createCmd.MarkFlagRequired("output")
	rootCmd.AddCommand(createCmd)
}

func runCreate(cmd *cobra.Command, args []string) error {
	identifier := args[0]

	// Check admin privileges
	if !format.IsAdmin() {
		errMsg := "administrator privileges required for raw disk access"
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

	// Ensure output has .bin extension
	outputPath := createOutput
	if strings.ToLower(filepath.Ext(outputPath)) != ".bin" {
		outputPath += ".bin"
	}

	// Make absolute path
	absPath, err := filepath.Abs(outputPath)
	if err == nil {
		outputPath = absPath
	}

	// Confirmation prompt (unless --yes or --json)
	if !createYes && !jsonOutput {
		pterm.Info.Printf("Creating image from disk %d (%s - %s)\n",
			device.DiskNumber, device.FriendlyName, device.SizeHuman)
		pterm.Info.Printf("Output: %s\n", outputPath)

		confirmed, _ := pterm.DefaultInteractiveConfirm.
			WithDefaultValue(true).
			Show("Continue?")

		if !confirmed {
			pterm.Info.Println("Create cancelled")
			return nil
		}
	}

	// Acquire disk lock
	diskLock, err := lock.NewDiskLock(device.DiskNumber)
	if err != nil {
		errMsg := fmt.Sprintf("failed to create disk lock: %v", err)
		if jsonOutput {
			output.PrintJSONError(errMsg, output.ErrCodeDiskBusy)
		} else {
			PrintError(errMsg, output.ErrCodeDiskBusy)
		}
		return err
	}

	lockCtx, lockCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer lockCancel()
	if err := diskLock.TryLock(lockCtx, 5*time.Second); err != nil {
		errMsg := "disk is busy (another operation in progress)"
		if jsonOutput {
			output.PrintJSONError(errMsg, output.ErrCodeDiskBusy)
		} else {
			PrintError(errMsg, output.ErrCodeDiskBusy)
		}
		return errors.New(errMsg)
	}
	defer diskLock.Unlock()

	// Setup context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		if !jsonOutput {
			pterm.Warning.Println("\nCancelling...")
		}
		cancel()
	}()

	// Create the image
	creator := image.NewCreator()
	opts := image.CreateOptions{
		DiskNumber: device.DiskNumber,
		OutputPath: outputPath,
		Verify:     createVerify,
		BufferSize:  1 << 20, // 1 MB
	}

	// Progress reporting
	startTime := time.Now()
	var spinner *pterm.SpinnerPrinter

	if jsonOutput {
		go func() {
			for p := range creator.Progress() {
				data, _ := json.Marshal(p)
				fmt.Println(string(data))
			}
		}()
	} else {
		spinner, _ = pterm.DefaultSpinner.
			WithRemoveWhenDone(true).
			Start("Creating image...")

		go func() {
			for p := range creator.Progress() {
				if spinner != nil {
					spinner.UpdateText(fmt.Sprintf("Creating image... %d%% %s", p.Percentage, p.Speed))
				}
			}
		}()
	}

	err = creator.Create(ctx, opts)

	if spinner != nil {
		spinner.Stop()
	}

	if err != nil {
		errMsg := fmt.Sprintf("create failed: %v", err)
		if jsonOutput {
			output.PrintJSONError(errMsg, output.ErrCodeInternalError)
		} else {
			PrintError(errMsg, output.ErrCodeInternalError)
		}
		return err
	}

	elapsed := time.Since(startTime)

	if jsonOutput {
		result := map[string]interface{}{
			"success":    true,
			"diskNumber": device.DiskNumber,
			"output":     outputPath,
			"duration":   elapsed.String(),
		}
		return PrintJSON(result)
	}

	pterm.Success.Printf("Image created: %s (%s)\n", outputPath, elapsed.Round(time.Second))
	return nil
}
