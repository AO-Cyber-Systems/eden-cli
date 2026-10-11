package command

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"go.aocyber.ai/eden-cli/internal/runner"
)

var lintCmd = &cobra.Command{
	Use:   "lint",
	Short: "Run linters and static analysis for all workspace packages",
	Long:  "Run go vet, flutter analyze, or dart analyze for each detected workspace package.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runLint(cmd)
	},
}

func runLint(cmd *cobra.Command) error {
	ctx := cmd.Context()
	pkgs := detectPackages(".")
	if len(pkgs) == 0 {
		fmt.Println("no workspace packages detected")
		return nil
	}

	for _, pkg := range pkgs {
		switch pkg.Type {
		case PackageTypeGo:
			fmt.Printf("==> Linting %s (Go)...\n", pkg.Name)
			if err := runner.Run(ctx, pkg.Dir, "go", "vet", "./..."); err != nil {
				return fmt.Errorf("lint %s: %w", pkg.Name, err)
			}
		case PackageTypeFlutter:
			fmt.Printf("==> Linting %s (Flutter)...\n", pkg.Name)
			analyzeArgs := []string{"analyze"}
			// ui packages: suppress warnings/infos to avoid noise from generated code.
			if strings.Contains(pkg.Name, "ui") {
				analyzeArgs = append(analyzeArgs, "--no-fatal-warnings", "--no-fatal-infos")
			}
			if err := runner.Run(ctx, pkg.Dir, "flutter", analyzeArgs...); err != nil {
				return fmt.Errorf("lint %s: %w", pkg.Name, err)
			}
		case PackageTypeDart:
			fmt.Printf("==> Linting %s (Dart)...\n", pkg.Name)
			if err := runner.Run(ctx, pkg.Dir, "dart", "analyze"); err != nil {
				return fmt.Errorf("lint %s: %w", pkg.Name, err)
			}
		}
	}
	return nil
}
