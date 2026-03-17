package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/pterm/pterm"
	"github.com/spf13/cobra"
)

var (
	// Global flags
	jsonOutput bool
	verbose    bool
	noColor    bool

	// Version info (set via ldflags)
	Version   = "dev"
	BuildDate = "unknown"
)

var rootCmd = &cobra.Command{
	Use:   "wusbkit",
	Short: "USB device management toolkit for Windows",
	Long: `wusbkit is a CLI toolkit for USB device management on Windows.

It provides commands to list, inspect, and format USB drives using
native Windows APIs for all disk operations.`,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		if noColor {
			pterm.DisableColor()
		}
	},
}

func Execute() error {
	return rootCmd.Execute()
}

func init() {
	rootCmd.PersistentFlags().BoolVarP(&jsonOutput, "json", "j", false, "Output in JSON format")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Show verbose output")
	rootCmd.PersistentFlags().BoolVar(&noColor, "no-color", false, "Disable colored output")
}

// IsJSON returns true if JSON output mode is enabled
func IsJSON() bool {
	return jsonOutput
}

// IsVerbose returns true if verbose mode is enabled
func IsVerbose() bool {
	return verbose
}

// PrintError prints an error message, formatted as JSON if in JSON mode
func PrintError(message string, code string) {
	if jsonOutput {
		errObj := map[string]string{
			"error": message,
			"code":  code,
		}
		data, _ := json.Marshal(errObj)
		fmt.Fprintln(os.Stderr, string(data))
	} else {
		pterm.Error.Println(message)
	}
}

// PrintJSON outputs data as formatted JSON
func PrintJSON(data interface{}) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(data)
}
