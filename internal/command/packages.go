package command

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// PackageType identifies the kind of package detected in a directory.
type PackageType string

const (
	PackageTypeGo      PackageType = "go"
	PackageTypeFlutter PackageType = "flutter"
	PackageTypeDart    PackageType = "dart"
)

// Package describes a detected workspace package.
type Package struct {
	Dir  string
	Name string
	Type PackageType
}

// detectPackages walks immediate subdirectories of root (including root itself)
// and returns all packages found by inspecting go.mod and pubspec.yaml files.
func detectPackages(root string) []Package {
	var pkgs []Package

	entries, err := os.ReadDir(root)
	if err != nil {
		return pkgs
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// Skip hidden directories.
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if pkg, ok := classifyDir(dir, e.Name()); ok {
			pkgs = append(pkgs, pkg)
		}
	}

	return pkgs
}

// classifyDir returns a Package if the directory is a Go, Flutter, or Dart package.
func classifyDir(dir, name string) (Package, bool) {
	// Go package: has go.mod
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
		return Package{Dir: dir, Name: name, Type: PackageTypeGo}, true
	}

	// Dart/Flutter package: has pubspec.yaml
	pubspecPath := filepath.Join(dir, "pubspec.yaml")
	if _, err := os.Stat(pubspecPath); err == nil {
		pkgType := pubspecType(pubspecPath)
		return Package{Dir: dir, Name: name, Type: pkgType}, true
	}

	return Package{}, false
}

// pubspecType determines whether a pubspec.yaml is a Flutter or plain Dart package
// by checking for a "flutter:" or "sdk: flutter" dependency entry.
func pubspecType(pubspecPath string) PackageType {
	f, err := os.Open(pubspecPath)
	if err != nil {
		return PackageTypeDart
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, "sdk: flutter") || strings.HasPrefix(trimmed, "flutter:") {
			return PackageTypeFlutter
		}
	}
	return PackageTypeDart
}
