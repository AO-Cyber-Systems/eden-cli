package command

import (
	"context"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "eden",
	Short: "Eden workspace manager",
	Long:  "Eden is a workspace manager for Eden-based consumer product projects.",
}

func init() {
	rootCmd.AddCommand(devCmd)
}

// Execute runs the root command with a background context.
// It exits with code 1 if the command returns an error.
func Execute() {
	ctx := context.Background()
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		os.Exit(1)
	}
}
