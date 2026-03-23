package scaffold

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

//go:embed all:templates
var templateFS embed.FS

// TemplateData holds variables available in .tmpl files.
type TemplateData struct {
	AppName      string // e.g., "my-app"
	ModuleName   string // e.g., "github.com/acme/my-app"
	EdenLibsPath string // e.g., "../eden-libs"
}

// ListTemplates returns the names of available template directories.
func ListTemplates() []string {
	entries, err := templateFS.ReadDir("templates")
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}

// Scaffold renders the named template into destDir using the provided data.
// Files with a .tmpl suffix are rendered as Go templates (with [[ ]] delimiters).
// Files without .tmpl are copied verbatim. The .tmpl suffix is stripped from
// the destination filename.
func Scaffold(templateName, destDir string, data TemplateData) error {
	root := "templates/" + templateName

	// Validate the template directory exists.
	if _, err := templateFS.ReadDir(root); err != nil {
		return fmt.Errorf("unknown template %q: %w", templateName, err)
	}

	return fs.WalkDir(templateFS, root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("walking template %s: %w", path, err)
		}

		// Compute destination relative path by stripping the root prefix.
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("computing relative path for %s: %w", path, err)
		}

		// Skip the root directory itself.
		if rel == "." {
			return nil
		}

		dest := filepath.Join(destDir, rel)

		if d.IsDir() {
			return os.MkdirAll(dest, 0755)
		}

		// Ensure parent directory exists.
		if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
			return fmt.Errorf("creating directory for %s: %w", dest, err)
		}

		content, err := templateFS.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading template file %s: %w", path, err)
		}

		if strings.HasSuffix(path, ".tmpl") {
			// Strip .tmpl suffix from destination filename.
			dest = strings.TrimSuffix(dest, ".tmpl")
			return renderTemplate(path, dest, string(content), data)
		}

		// Copy verbatim.
		return os.WriteFile(dest, content, 0644)
	})
}

// renderTemplate parses and executes a single template file with [[ ]] delimiters.
func renderTemplate(name, dest, content string, data TemplateData) error {
	tmpl, err := template.New(filepath.Base(name)).Delims("[[", "]]").Parse(content)
	if err != nil {
		return fmt.Errorf("parsing template %s: %w", name, err)
	}

	f, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("creating output file %s: %w", dest, err)
	}
	defer f.Close()

	if err := tmpl.Execute(f, data); err != nil {
		return fmt.Errorf("executing template %s: %w", name, err)
	}
	return nil
}
