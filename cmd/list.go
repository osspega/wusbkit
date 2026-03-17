package cmd

import (
	"github.com/lazaroagomez/wusbkit/internal/output"
	"github.com/lazaroagomez/wusbkit/internal/usb"
	"github.com/spf13/cobra"
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List all connected USB drives",
	Long: `List all USB storage devices connected to the system.

By default, shows drive letter, name, size, and status.
Use --verbose to see additional details like serial number, VID/PID, and filesystem.`,
	Example: `  wusbkit list
  wusbkit list --verbose
  wusbkit list --json`,
	RunE: runList,
}

func init() {
	rootCmd.AddCommand(listCmd)
}

func runList(cmd *cobra.Command, args []string) error {
	// Enumerate USB devices
	enum := usb.NewEnumerator()
	devices, err := enum.ListDevices()
	if err != nil {
		if jsonOutput {
			output.PrintJSONError(err.Error(), output.ErrCodeInternalError)
		} else {
			PrintError(err.Error(), output.ErrCodeInternalError)
		}
		return err
	}

	// Output results
	if jsonOutput {
		return output.PrintJSON(devices)
	}

	output.PrintDevicesTable(devices, verbose)
	return nil
}
