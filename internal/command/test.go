package command

import (
	"fmt"

	"github.com/spf13/cobra"
	"go.aocyber.ai/eden-cli/internal/runner"
)

var testCmd = &cobra.Command{
	Use:   "test",
	Short: "Run tests for all workspace packages",
	Long:  "Run go test, flutter test, or dart test for each detected workspace package.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTest(cmd)
	},
}

func runTest(cmd *cobra.Command) error {
	ctx := cmd.Context()
	pkgs := detectPackages(".")
	if len(pkgs) == 0 {
		fmt.Println("no workspace packages detected")
		return nil
	}

	for _, pkg := range pkgs {
		switch pkg.Type {
		case PackageTypeGo:
			fmt.Printf("==> Running tests in %s (Go)...\n", pkg.Name)
			if err := runner.Run(ctx, pkg.Dir, "go", "test", "./..."); err != nil {
				return fmt.Errorf("test %s: %w", pkg.Name, err)
			}
		case PackageTypeFlutter:
			fmt.Printf("==> Running tests in %s (Flutter)...\n", pkg.Name)
			if err := runner.Run(ctx, pkg.Dir, "flutter", "test"); err != nil {
				return fmt.Errorf("test %s: %w", pkg.Name, err)
			}
		case PackageTypeDart:
			fmt.Printf("==> Running tests in %s (Dart)...\n", pkg.Name)
			if err := runner.Run(ctx, pkg.Dir, "dart", "test"); err != nil {
				return fmt.Errorf("test %s: %w", pkg.Name, err)
			}
		}
	}
	return nil
}
