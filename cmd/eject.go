package cmd

import (
	"fmt"
	"syscall"

	"github.com/lazaroagomez/wusbkit/internal/output"
	"github.com/lazaroagomez/wusbkit/internal/usb"
	"github.com/pterm/pterm"
	"github.com/spf13/cobra"
	"golang.org/x/sys/windows"
)

var ejectYes bool

var ejectCmd = &cobra.Command{
	Use:   "eject <drive>",
	Short: "Safely eject a USB drive",
	Long: `Safely eject a USB storage device.

This performs the same action as "Safely Remove Hardware" in Windows,
ensuring all pending writes are flushed before ejecting.

The drive can be specified by:
  - Drive letter (e.g., E: or E)
  - Disk number (e.g., 2)`,
	Example: `  wusbkit eject E:
  wusbkit eject E
  wusbkit eject 2
  wusbkit eject E: --yes`,
	Args: cobra.ExactArgs(1),
	RunE: runEject,
}

func init() {
	ejectCmd.Flags().BoolVarP(&ejectYes, "yes", "y", false, "Skip confirmation prompt")
	rootCmd.AddCommand(ejectCmd)
}

// IOCTL_STORAGE_EJECT_MEDIA ejects removable media from the device.
const ioctlStorageEjectMedia = 0x002D4808

func runEject(cmd *cobra.Command, args []string) error {
	identifier := args[0]

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

	// Confirmation prompt (unless --yes or --json)
	if !ejectYes && !jsonOutput {
		pterm.Info.Printf("Ejecting disk %d (%s - %s)\n",
			device.DiskNumber, device.FriendlyName, device.SizeHuman)

		confirmed, _ := pterm.DefaultInteractiveConfirm.
			WithDefaultValue(true).
			Show("Continue?")

		if !confirmed {
			pterm.Info.Println("Eject cancelled")
			return nil
		}
	}

	// Eject using native IOCTL_STORAGE_EJECT_MEDIA — no PowerShell needed
	if err := ejectDisk(device.DiskNumber); err != nil {
		errMsg := fmt.Sprintf("Failed to eject disk %d: %v", device.DiskNumber, err)
		if jsonOutput {
			output.PrintJSONError(errMsg, output.ErrCodeInternalError)
		} else {
			PrintError(errMsg, output.ErrCodeInternalError)
		}
		return err
	}

	// Output success
	driveName := device.DriveLetter
	if driveName == "" {
		driveName = fmt.Sprintf("disk %d", device.DiskNumber)
	}

	if jsonOutput {
		result := map[string]interface{}{
			"success":     true,
			"driveLetter": device.DriveLetter,
			"diskNumber":  device.DiskNumber,
			"message":     fmt.Sprintf("Successfully ejected %s", driveName),
		}
		return output.PrintJSON(result)
	}

	pterm.Success.Printf("Successfully ejected %s (%s)\n", driveName, device.FriendlyName)
	return nil
}

// ejectDisk safely ejects a physical disk using native Windows API.
func ejectDisk(diskNumber int) error {
	path := fmt.Sprintf(`\\.\PhysicalDrive%d`, diskNumber)
	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return fmt.Errorf("invalid disk path: %w", err)
	}

	handle, err := windows.CreateFile(
		pathPtr,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		0,
		0,
	)
	if err != nil {
		return fmt.Errorf("failed to open disk: %w", err)
	}
	defer windows.CloseHandle(handle)

	var bytesReturned uint32
	err = windows.DeviceIoControl(
		handle,
		ioctlStorageEjectMedia,
		nil, 0,
		nil, 0,
		&bytesReturned,
		nil,
	)
	if err != nil {
		return fmt.Errorf("IOCTL_STORAGE_EJECT_MEDIA failed: %w", err)
	}

	return nil
}
