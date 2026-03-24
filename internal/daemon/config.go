// Package daemon implements the Eden dev server daemon, including configuration
// directory management, workspace registry, and daemon lifecycle.
package daemon

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	// EdenDirName is the name of the Eden config directory under $HOME.
	EdenDirName = ".eden"
	// CADirName holds TLS certificate authority files.
	CADirName = "ca"
	// WorkspacesDirName holds workspace symlinks.
	WorkspacesDirName = "workspaces"
	// LogsDirName holds daemon log files.
	LogsDirName = "logs"
	// StateDirName holds runtime state files.
	StateDirName = "state"
	// LaunchAgentLabel is the macOS LaunchAgent identifier.
	LaunchAgentLabel = "com.eden.dev"
)

// Workspace describes a registered workspace and its detected capabilities.
type Workspace struct {
	Name       string
	Dir        string
	HasGo      bool
	HasFlutter bool
}

// EdenDir returns the path to ~/.eden/.
func EdenDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("getting home directory: %w", err)
	}
	return filepath.Join(home, EdenDirName), nil
}

// WorkspacesDir returns the path to ~/.eden/workspaces/.
func WorkspacesDir() (string, error) {
	base, err := EdenDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, WorkspacesDirName), nil
}

// CADir returns the path to ~/.eden/ca/.
func CADir() (string, error) {
	base, err := EdenDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, CADirName), nil
}

// LogsDir returns the path to ~/.eden/logs/.
func LogsDir() (string, error) {
	base, err := EdenDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, LogsDirName), nil
}

// StateDir returns the path to ~/.eden/state/.
func StateDir() (string, error) {
	base, err := EdenDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, StateDirName), nil
}

// EnsureConfigDirs creates the ~/.eden/ directory and all required subdirectories.
// It is idempotent: existing directories are left untouched.
func EnsureConfigDirs() error {
	base, err := EdenDir()
	if err != nil {
		return err
	}
	dirs := []string{
		base,
		filepath.Join(base, CADirName),
		filepath.Join(base, WorkspacesDirName),
		filepath.Join(base, LogsDirName),
		filepath.Join(base, StateDirName),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("creating directory %s: %w", d, err)
		}
	}
	return nil
}

// Link registers a workspace by creating a symlink at ~/.eden/workspaces/<name>
// pointing to dir. If a symlink with the same name already exists it is replaced.
func Link(name, dir string) error {
	wsDir, err := WorkspacesDir()
	if err != nil {
		return err
	}
	link := filepath.Join(wsDir, name)

	// Remove existing symlink if present.
	if _, statErr := os.Lstat(link); statErr == nil {
		if err := os.Remove(link); err != nil {
			return fmt.Errorf("removing existing link %s: %w", link, err)
		}
	}

	if err := os.Symlink(dir, link); err != nil {
		return fmt.Errorf("creating symlink %s -> %s: %w", link, dir, err)
	}
	return nil
}

// Unlink removes a workspace symlink from ~/.eden/workspaces/.
func Unlink(name string) error {
	wsDir, err := WorkspacesDir()
	if err != nil {
		return err
	}
	link := filepath.Join(wsDir, name)
	if err := os.Remove(link); err != nil {
		return fmt.Errorf("removing workspace %q: %w", name, err)
	}
	return nil
}

// ListWorkspaces reads all symlinks in ~/.eden/workspaces/ and returns their
// resolved targets along with detected project types.
func ListWorkspaces() ([]Workspace, error) {
	wsDir, err := WorkspacesDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(wsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading workspaces directory: %w", err)
	}

	var workspaces []Workspace
	for _, e := range entries {
		link := filepath.Join(wsDir, e.Name())
		target, err := os.Readlink(link)
		if err != nil {
			continue // skip non-symlink entries
		}
		// Resolve relative symlinks.
		if !filepath.IsAbs(target) {
			target = filepath.Join(wsDir, target)
		}
		ws := Workspace{
			Name:       e.Name(),
			Dir:        target,
			HasGo:      fileExists(filepath.Join(target, "go.mod")),
			HasFlutter: fileExists(filepath.Join(target, "pubspec.yaml")),
		}
		workspaces = append(workspaces, ws)
	}
	return workspaces, nil
}

// ResolveWorkspace resolves a single workspace symlink to its target directory.
func ResolveWorkspace(name string) (string, error) {
	wsDir, err := WorkspacesDir()
	if err != nil {
		return "", err
	}
	link := filepath.Join(wsDir, name)
	target, err := os.Readlink(link)
	if err != nil {
		return "", fmt.Errorf("workspace %q not found: %w", name, err)
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(wsDir, target)
	}
	return target, nil
}

// fileExists reports whether the named file exists.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
