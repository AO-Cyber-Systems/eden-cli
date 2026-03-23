package command

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/aocybersystems/eden/cli/internal/runner"
	"github.com/spf13/cobra"
)

var downCmd = &cobra.Command{
	Use:   "down",
	Short: "Stop docker compose services",
	Long:  "Find compose.yaml in the workspace and run 'docker compose down -v'.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runDown(cmd)
	},
}

func runDown(cmd *cobra.Command) error {
	ctx := cmd.Context()
	composePath, err := findComposeFile(".")
	if err != nil {
		fmt.Println("no compose.yaml found — nothing to stop")
		return nil
	}
	absPath, _ := filepath.Abs(composePath)
	fmt.Printf("==> Stopping services defined in %s...\n", absPath)
	return runner.Run(ctx, ".", "docker", "compose", "-f", absPath, "down", "-v")
}

// findComposeFile looks for compose.yaml in root and immediate subdirectories.
func findComposeFile(root string) (string, error) {
	// Check root itself.
	candidate := filepath.Join(root, "compose.yaml")
	if _, err := os.Stat(candidate); err == nil {
		return candidate, nil
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		candidate := filepath.Join(root, e.Name(), "compose.yaml")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("compose.yaml not found")
}
