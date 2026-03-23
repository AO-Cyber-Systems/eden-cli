package command

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/aocybersystems/eden/cli/internal/runner"
	"github.com/spf13/cobra"
)

var devCmd = &cobra.Command{
	Use:   "dev",
	Short: "Start development servers",
	Long: `Start Go and Flutter development servers concurrently.

By default, all detected servers are started. Use --go-only or --flutter-only
to start a single server type.`,
	RunE: runDev,
}

var (
	devGoOnly      bool
	devFlutterOnly bool
)

func init() {
	devCmd.Flags().BoolVar(&devGoOnly, "go-only", false, "Start only the Go server")
	devCmd.Flags().BoolVar(&devFlutterOnly, "flutter-only", false, "Start only the Flutter server")
}

func runDev(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}

	var procs []runner.Proc

	if !devFlutterOnly {
		goProc, err := detectGoProc(cwd)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[eden] warning: no Go entrypoint found: %v\n", err)
		} else {
			procs = append(procs, goProc)
		}
	}

	if !devGoOnly {
		flutterProc, err := detectFlutterProc(cwd)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[eden] warning: no Flutter entrypoint found: %v\n", err)
		} else {
			procs = append(procs, flutterProc)
		}
	}

	if len(procs) == 0 {
		return fmt.Errorf("no dev servers detected in workspace")
	}

	for _, p := range procs {
		fmt.Printf("[eden] Starting %s server in %s...\n", p.Name, p.Dir)
	}

	return runner.RunConcurrent(cmd.Context(), procs)
}

// detectGoProc searches for a Go server entrypoint starting from the given
// directory. It checks:
//  1. A direct cmd/ subdirectory containing main.go entries (consumer workspace)
//  2. An eden-platform-go subdirectory with its own cmd/ entries (eden-libs workspace)
func detectGoProc(root string) (runner.Proc, error) {
	// Check for direct cmd/*/main.go under root (consumer workspace pattern).
	goDir, goArgs, err := findGoCmdArgs(root)
	if err == nil {
		return runner.Proc{
			Name: "go",
			Dir:  goDir,
			Cmd:  "go",
			Args: goArgs,
		}, nil
	}

	// Check for eden-platform-go subdirectory (eden-libs workspace pattern).
	edenPlatformGo := filepath.Join(root, "eden-platform-go")
	if info, statErr := os.Stat(edenPlatformGo); statErr == nil && info.IsDir() {
		goDir, goArgs, err2 := findGoCmdArgs(edenPlatformGo)
		if err2 == nil {
			return runner.Proc{
				Name: "go",
				Dir:  goDir,
				Cmd:  "go",
				Args: goArgs,
			}, nil
		}
	}

	return runner.Proc{}, fmt.Errorf("no cmd/*/main.go found under %s", root)
}

// findGoCmdArgs looks for a cmd/ directory under base and returns the
// directory and run arguments for the first entrypoint it finds.
func findGoCmdArgs(base string) (dir string, args []string, err error) {
	cmdDir := filepath.Join(base, "cmd")
	entries, readErr := os.ReadDir(cmdDir)
	if readErr != nil {
		return "", nil, fmt.Errorf("no cmd/ directory at %s", base)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		mainFile := filepath.Join(cmdDir, e.Name(), "main.go")
		if _, statErr := os.Stat(mainFile); statErr == nil {
			// Use the package path relative to base.
			relPkg := filepath.Join("./cmd", e.Name())
			return base, []string{"run", relPkg}, nil
		}
	}
	return "", nil, fmt.Errorf("no cmd/*/main.go found under %s", base)
}

// detectFlutterProc searches for a Flutter entrypoint starting from root. It
// checks:
//  1. An example/ subdirectory containing pubspec.yaml (library workspace pattern)
//  2. A Flutter package directory named like *flutter* with an example/ dir
//  3. A lib/main.dart in the root (consumer app workspace pattern)
func detectFlutterProc(root string) (runner.Proc, error) {
	// Check for direct example/ directory at root.
	exampleDir := filepath.Join(root, "example")
	if hasFile(exampleDir, "pubspec.yaml") {
		return flutterRunProc(exampleDir), nil
	}

	// Look for *flutter* subdirectory with an example/ directory (eden-libs
	// workspace pattern, e.g. eden-platform-flutter/example).
	entries, err := os.ReadDir(root)
	if err != nil {
		return runner.Proc{}, fmt.Errorf("reading workspace root: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sub := filepath.Join(root, e.Name())
		subExample := filepath.Join(sub, "example")
		if hasFile(subExample, "pubspec.yaml") {
			return flutterRunProc(subExample), nil
		}
	}

	// Fallback: lib/main.dart in root (consumer app pattern).
	if hasFile(filepath.Join(root, "lib"), "main.dart") {
		return flutterRunProc(root), nil
	}

	return runner.Proc{}, fmt.Errorf("no Flutter entrypoint found under %s", root)
}

// flutterRunProc builds a Proc for `flutter run` in the given directory.
func flutterRunProc(dir string) runner.Proc {
	return runner.Proc{
		Name: "flutter",
		Dir:  dir,
		Cmd:  "flutter",
		Args: []string{"run", "--dart-define=API_BASE_URL=http://localhost:8080"},
	}
}

// hasFile reports whether the given directory contains a file with the given name.
func hasFile(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}
