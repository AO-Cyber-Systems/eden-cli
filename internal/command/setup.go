package command

import (
	"fmt"

	"github.com/aocybersystems/eden/cli/internal/runner"
	"github.com/spf13/cobra"
)

var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Install dependencies for all workspace packages",
	Long:  "Install dependencies for all detected Go and Dart/Flutter packages in the workspace.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSetup(cmd)
	},
}

func runSetup(cmd *cobra.Command) error {
	ctx := cmd.Context()
	pkgs := detectPackages(".")
	if len(pkgs) == 0 {
		fmt.Println("no workspace packages detected")
		return nil
	}

	for _, pkg := range pkgs {
		switch pkg.Type {
		case PackageTypeGo:
			fmt.Printf("==> Setting up %s (Go)...\n", pkg.Name)
			if err := runner.Run(ctx, pkg.Dir, "go", "mod", "download"); err != nil {
				return fmt.Errorf("setup %s: %w", pkg.Name, err)
			}
		case PackageTypeFlutter:
			fmt.Printf("==> Setting up %s (Flutter)...\n", pkg.Name)
			if err := runner.Run(ctx, pkg.Dir, "flutter", "pub", "get"); err != nil {
				return fmt.Errorf("setup %s: %w", pkg.Name, err)
			}
		case PackageTypeDart:
			fmt.Printf("==> Setting up %s (Dart)...\n", pkg.Name)
			if err := runner.Run(ctx, pkg.Dir, "dart", "pub", "get"); err != nil {
				return fmt.Errorf("setup %s: %w", pkg.Name, err)
			}
		}
	}
	return nil
}
