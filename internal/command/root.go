package command

import (
	"context"
	"os"

	"github.com/spf13/cobra"
)

// version is set at build time via ldflags:
//
//	go build -ldflags="-X github.com/aocybersystems/eden/cli/internal/command.version=v1.0.0"
var version = "dev"

var rootCmd = &cobra.Command{
	Use:     "eden",
	Short:   "Eden workspace manager",
	Long:    "Eden is a workspace manager for Eden-based consumer product projects.",
	Version: version,
}

func init() {
	rootCmd.AddCommand(devCmd)
	rootCmd.AddCommand(setupCmd)
	rootCmd.AddCommand(generateCmd)
	rootCmd.AddCommand(downCmd)
	rootCmd.AddCommand(testCmd)
	rootCmd.AddCommand(lintCmd)
	rootCmd.AddCommand(checkCmd)
}

// Execute runs the root command with a background context.
// It exits with code 1 if the command returns an error.
func Execute() {
	ctx := context.Background()
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		os.Exit(1)
	}
}
