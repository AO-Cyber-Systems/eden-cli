package command

import (
	"fmt"

	"github.com/spf13/cobra"
	"go.aocyber.ai/eden-cli/internal/runner"
)

var fmtCmd = &cobra.Command{
	Use:   "fmt",
	Short: "Run formatters for all workspace packages",
	Long:  "Run gofmt for Go packages and dart format for Dart/Flutter packages.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runFmt(cmd)
	},
}

func runFmt(cmd *cobra.Command) error {
	ctx := cmd.Context()
	pkgs := detectPackages(".")
	if len(pkgs) == 0 {
		fmt.Println("no workspace packages detected")
		return nil
	}

	for _, pkg := range pkgs {
		switch pkg.Type {
		case PackageTypeGo:
			fmt.Printf("==> Formatting %s (Go)...\n", pkg.Name)
			if err := runner.Run(ctx, pkg.Dir, "gofmt", "-w", "."); err != nil {
				return fmt.Errorf("fmt %s: %w", pkg.Name, err)
			}
		case PackageTypeFlutter, PackageTypeDart:
			fmt.Printf("==> Formatting %s (%s)...\n", pkg.Name, pkg.Type)
			if err := runner.Run(ctx, pkg.Dir, "dart", "format", "."); err != nil {
				return fmt.Errorf("fmt %s: %w", pkg.Name, err)
			}
		}
	}
	return nil
}
