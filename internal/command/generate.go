package command

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/aocybersystems/eden/cli/internal/runner"
	"github.com/spf13/cobra"
)

var generateCmd = &cobra.Command{
	Use:   "generate",
	Short: "Run buf generate to regenerate protobuf code",
	Long:  "Find buf.gen.yaml in the workspace and run 'buf generate' in that directory.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runGenerate(cmd)
	},
}

func runGenerate(cmd *cobra.Command) error {
	ctx := cmd.Context()
	bufDir, err := findBufGenDir(".")
	if err != nil {
		fmt.Println("no buf.gen.yaml found — skipping code generation")
		return nil
	}
	fmt.Printf("==> Generating protobuf code in %s...\n", bufDir)
	return runner.Run(ctx, bufDir, "buf", "generate")
}

// findBufGenDir walks immediate subdirectories of root looking for a buf.gen.yaml file.
// It checks root itself first, then each subdirectory.
func findBufGenDir(root string) (string, error) {
	// Check root itself.
	if _, err := os.Stat(filepath.Join(root, "buf.gen.yaml")); err == nil {
		abs, _ := filepath.Abs(root)
		return abs, nil
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if _, err := os.Stat(filepath.Join(dir, "buf.gen.yaml")); err == nil {
			abs, _ := filepath.Abs(dir)
			return abs, nil
		}
	}

	return "", fmt.Errorf("buf.gen.yaml not found")
}
