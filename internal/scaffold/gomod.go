package scaffold

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/mod/modfile"
)

// WriteGoMod generates a go.mod file at destDir/go.mod for the given module.
// It adds standard eden-platform-go dependencies and a replace directive
// pointing to the local eden-libs checkout.
func WriteGoMod(destDir, modulePath, edenLibsPath string) error {
	base := fmt.Sprintf("module %s\n\ngo 1.26.1\n", modulePath)

	f, err := modfile.Parse("go.mod", []byte(base), nil)
	if err != nil {
		return fmt.Errorf("parsing base go.mod: %w", err)
	}

	if err := f.AddRequire("github.com/aocybersystems/eden-platform-go", "v0.0.0"); err != nil {
		return fmt.Errorf("adding eden-platform-go require: %w", err)
	}

	if err := f.AddRequire("connectrpc.com/connect", "v1.19.1"); err != nil {
		return fmt.Errorf("adding connect require: %w", err)
	}

	platformGoPath := filepath.Join(edenLibsPath, "eden-platform-go")
	if err := f.AddReplace("github.com/aocybersystems/eden-platform-go", "", platformGoPath, ""); err != nil {
		return fmt.Errorf("adding replace directive: %w", err)
	}

	f.Cleanup()

	out, err := f.Format()
	if err != nil {
		return fmt.Errorf("formatting go.mod: %w", err)
	}

	dest := filepath.Join(destDir, "go.mod")
	if err := os.WriteFile(dest, out, 0644); err != nil {
		return fmt.Errorf("writing go.mod to %s: %w", dest, err)
	}

	return nil
}
