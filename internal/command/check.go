package command

import (
	"fmt"

	"github.com/spf13/cobra"
)

var checkCmd = &cobra.Command{
	Use:   "check",
	Short: "Run generate + lint + test in sequence",
	Long:  "Run the full check pipeline: generate protobuf code, lint all packages, then run all tests.",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println("==> Generating...")
		if err := runGenerate(cmd); err != nil {
			return fmt.Errorf("generate: %w", err)
		}

		fmt.Println("==> Linting...")
		if err := runLint(cmd); err != nil {
			return fmt.Errorf("lint: %w", err)
		}

		fmt.Println("==> Testing...")
		if err := runTest(cmd); err != nil {
			return fmt.Errorf("test: %w", err)
		}

		fmt.Println("==> All checks passed.")
		return nil
	},
}
