package command

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"go.aocyber.ai/eden-cli/internal/scaffold"
)

func init() {
	rootCmd.AddCommand(newCmd)

	newCmd.Flags().String("template", "", `Template to use: "fullstack", "backend-only", or "flutter-only" (required)`)
	_ = newCmd.MarkFlagRequired("template")

	newCmd.Flags().String("eden-libs", "../eden-libs", "Path to eden-libs relative to the new workspace directory")
	newCmd.Flags().String("module", "", "Go module path (e.g., github.com/acme/my-app). Omit for flutter-only.")
}

var newCmd = &cobra.Command{
	Use:   "new <name>",
	Short: "Create a new Eden workspace from a template",
	Args:  cobra.ExactArgs(1),
	RunE:  runNew,
}

func runNew(cmd *cobra.Command, args []string) error {
	name := args[0]

	templateName, _ := cmd.Flags().GetString("template")
	edenLibsPath, _ := cmd.Flags().GetString("eden-libs")
	modulePath, _ := cmd.Flags().GetString("module")

	// Validate template name.
	valid := scaffold.ListTemplates()
	if !containsString(valid, templateName) {
		return fmt.Errorf("invalid template %q — valid options: %v", templateName, valid)
	}

	// Destination directory is a subdirectory of the current working directory.
	destDir := filepath.Join(".", name)

	// Check that destDir does not exist or is empty.
	if err := checkDestDir(destDir); err != nil {
		return err
	}

	// Derive module path for Go-bearing templates if not provided.
	hasGo := templateName == "fullstack" || templateName == "backend-only"
	if hasGo && modulePath == "" {
		modulePath = name
	}

	data := scaffold.TemplateData{
		AppName:      name,
		ModuleName:   modulePath,
		EdenLibsPath: edenLibsPath,
	}

	// Scaffold template files into the destination.
	if err := scaffold.Scaffold(templateName, destDir, data); err != nil {
		// Clean up partial output so the user can retry.
		_ = os.RemoveAll(destDir)
		return fmt.Errorf("scaffolding workspace: %w", err)
	}

	// For templates with Go, overwrite the placeholder go.mod with a real one.
	if hasGo {
		if err := scaffold.WriteGoMod(destDir, modulePath, edenLibsPath); err != nil {
			return fmt.Errorf("writing go.mod: %w", err)
		}
	}

	fmt.Printf("Created workspace %q from template %q\nNext: cd %s && eden setup\n", name, templateName, name)
	return nil
}

// checkDestDir returns an error when destDir exists and is non-empty.
func checkDestDir(destDir string) error {
	entries, err := os.ReadDir(destDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("checking destination %s: %w", destDir, err)
	}
	if len(entries) > 0 {
		return fmt.Errorf("destination %q already exists and is not empty", destDir)
	}
	return nil
}

// containsString reports whether slice contains s.
func containsString(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}
