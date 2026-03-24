package command

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(upgradeCmd)

	upgradeCmd.Flags().String("eden-libs", "", "Path to eden-libs (auto-detected from go.mod/pubspec.yaml if omitted)")
	upgradeCmd.Flags().Bool("dry-run", false, "Show what would change without applying updates")
	upgradeCmd.Flags().String("model", "", "Claude model to use (e.g., sonnet, opus) — defaults to Claude CLI default")
}

var upgradeCmd = &cobra.Command{
	Use:   "upgrade",
	Short: "Upgrade this workspace to the current eden-libs version",
	Long: `Analyze the current workspace against the latest eden-libs and apply
necessary updates using the Claude CLI.

The command detects the eden-libs path from go.mod replace directives or
pubspec.yaml path dependencies, generates a changelog of framework changes,
and invokes Claude to review and update the consumer codebase.`,
	RunE: runUpgrade,
}

// upgradeContext holds the detected workspace state for the upgrade.
type upgradeContext struct {
	WorkspaceDir string
	EdenLibsPath string
	HasGo        bool
	HasFlutter   bool
	HasDart      bool
	GoModPath    string
	PubspecPath  string
	Changelog    string
	EdenVersion  string
}

func runUpgrade(cmd *cobra.Command, args []string) error {
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	edenLibsOverride, _ := cmd.Flags().GetString("eden-libs")
	model, _ := cmd.Flags().GetString("model")

	fmt.Println("Eden Upgrade")
	fmt.Println("============")
	fmt.Println()

	// Step 1: Detect workspace and eden-libs path.
	uctx, err := detectUpgradeContext(edenLibsOverride)
	if err != nil {
		return err
	}

	fmt.Printf("  Workspace:  %s\n", uctx.WorkspaceDir)
	fmt.Printf("  eden-libs:  %s\n", uctx.EdenLibsPath)
	fmt.Printf("  Go:         %v\n", uctx.HasGo)
	fmt.Printf("  Flutter:    %v\n", uctx.HasFlutter)
	fmt.Printf("  Dart:       %v\n", uctx.HasDart)
	fmt.Println()

	// Step 2: Resolve eden-libs to absolute path and validate it exists.
	absEdenLibs, err := filepath.Abs(uctx.EdenLibsPath)
	if err != nil {
		return fmt.Errorf("resolving eden-libs path: %w", err)
	}
	if _, err := os.Stat(absEdenLibs); os.IsNotExist(err) {
		return fmt.Errorf("eden-libs not found at %s — use --eden-libs to specify the correct path", absEdenLibs)
	}

	// Step 3: Check that claude CLI is available.
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		return fmt.Errorf("claude CLI not found in PATH — install from https://claude.ai/download")
	}

	// Step 4: Generate changelog from eden-libs.
	fmt.Println("Generating eden-libs changelog...")
	changelog, err := generateEdenChangelog(absEdenLibs)
	if err != nil {
		fmt.Printf("  Warning: could not generate changelog: %v\n", err)
		changelog = "(changelog unavailable)"
	}
	uctx.Changelog = changelog

	// Step 5: Read eden-libs version (latest git tag or short SHA).
	uctx.EdenVersion = getEdenVersion(absEdenLibs)
	fmt.Printf("  eden-libs version: %s\n", uctx.EdenVersion)
	fmt.Println()

	// Step 6: Build the Claude prompt.
	prompt := buildUpgradePrompt(uctx, dryRun)

	if dryRun {
		fmt.Println("DRY RUN — Claude will analyze but not modify files.")
		fmt.Println()
	}

	// Step 7: Invoke Claude with streaming output.
	fmt.Println("Starting Claude analysis...")
	fmt.Println("─────────────────────────────────────────")
	fmt.Println()

	claudeArgs := []string{
		"--print",
		"--verbose",
		"--output-format", "stream-json",
		"--add-dir", absEdenLibs,
	}

	if model != "" {
		claudeArgs = append(claudeArgs, "--model", model)
	}

	// acceptEdits auto-approves reads and file edits without interactive prompts.
	claudeArgs = append(claudeArgs, "--permission-mode", "acceptEdits")

	if dryRun {
		claudeArgs = append(claudeArgs, "--allowedTools", "Read,Glob,Grep")
	} else {
		claudeArgs = append(claudeArgs, "--allowedTools", buildAllowedTools(uctx))
	}

	// Pass prompt via stdin — it can exceed OS arg length limits.
	c := exec.Command(claudePath, claudeArgs...)
	c.Dir = uctx.WorkspaceDir
	c.Stderr = os.Stderr
	c.Stdin = strings.NewReader(prompt)

	stdout, err := c.StdoutPipe()
	if err != nil {
		return fmt.Errorf("creating stdout pipe: %w", err)
	}

	if err := c.Start(); err != nil {
		return fmt.Errorf("starting claude: %w", err)
	}

	result, err := streamClaude(stdout)
	if err != nil {
		// Still wait for process to finish.
		_ = c.Wait()
		return fmt.Errorf("streaming claude output: %w", err)
	}

	if err := c.Wait(); err != nil {
		return fmt.Errorf("claude exited with error: %w", err)
	}

	// Print the final report.
	fmt.Println()
	fmt.Println("─────────────────────────────────────────")
	fmt.Println()
	fmt.Println(result)
	fmt.Println()
	fmt.Println("─────────────────────────────────────────")
	fmt.Println("Upgrade analysis complete.")
	if dryRun {
		fmt.Println("Re-run without --dry-run to apply changes.")
	} else {
		manifest := upgradeManifest{
			EdenLibsVersion: uctx.EdenVersion,
			EdenLibsPath:    absEdenLibs,
			UpgradedAt:      time.Now().UTC().Format(time.RFC3339),
			DryRun:          false,
			Model:           model,
		}
		if err := writeUpgradeManifest(uctx.WorkspaceDir, manifest); err != nil {
			fmt.Printf("  Warning: could not write upgrade manifest: %v\n", err)
		} else {
			fmt.Println("  Wrote .eden-upgrade.json")
		}
		fmt.Println("Run 'eden check' to verify the upgrade.")
	}
	return nil
}

// detectUpgradeContext inspects the current directory to find eden-libs references.
func detectUpgradeContext(override string) (*upgradeContext, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("getting working directory: %w", err)
	}

	uctx := &upgradeContext{
		WorkspaceDir: wd,
	}

	if override != "" {
		uctx.EdenLibsPath = override
	}

	// Check root and immediate subdirs for go.mod / pubspec.yaml.
	goModPath, edenLibsFromGo := findEdenLibsInGoMod(wd)
	if goModPath != "" {
		uctx.HasGo = true
		uctx.GoModPath = goModPath
		if uctx.EdenLibsPath == "" && edenLibsFromGo != "" {
			uctx.EdenLibsPath = edenLibsFromGo
		}
	}

	pubspecPath, edenLibsFromPubspec := findEdenLibsInPubspec(wd)
	if pubspecPath != "" {
		uctx.PubspecPath = pubspecPath
		if uctx.EdenLibsPath == "" && edenLibsFromPubspec != "" {
			uctx.EdenLibsPath = edenLibsFromPubspec
		}
		// Determine Flutter vs plain Dart.
		if pubspecType(pubspecPath) == PackageTypeFlutter {
			uctx.HasFlutter = true
		} else {
			uctx.HasDart = true
		}
	}

	if uctx.EdenLibsPath == "" {
		return nil, fmt.Errorf("could not detect eden-libs path — use --eden-libs to specify it")
	}

	return uctx, nil
}

// findEdenLibsInGoMod searches for a go.mod with an eden-platform-go replace directive.
// Returns the go.mod path and the resolved eden-libs root directory.
func findEdenLibsInGoMod(root string) (string, string) {
	// Check root go.mod first.
	if result, edenPath := parseGoModForEden(filepath.Join(root, "go.mod"), root); result != "" {
		return result, edenPath
	}

	// Check immediate subdirectories.
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		subdir := filepath.Join(root, e.Name())
		gomod := filepath.Join(subdir, "go.mod")
		if result, edenPath := parseGoModForEden(gomod, subdir); result != "" {
			return result, edenPath
		}
	}
	return "", ""
}

// parseGoModForEden reads a go.mod and extracts the eden-platform-go replace path.
func parseGoModForEden(gomodPath, dir string) (string, string) {
	data, err := os.ReadFile(gomodPath)
	if err != nil {
		return "", ""
	}

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Look for: replace github.com/aocybersystems/eden-platform-go => ../eden-libs/eden-platform-go
		if strings.Contains(trimmed, "eden-platform-go") && strings.Contains(trimmed, "=>") {
			parts := strings.Split(trimmed, "=>")
			if len(parts) == 2 {
				replacePath := strings.TrimSpace(parts[1])
				// Strip trailing version if present.
				if fields := strings.Fields(replacePath); len(fields) > 0 {
					replacePath = fields[0]
				}
				// replacePath is like ../eden-libs/eden-platform-go — go up one level.
				absReplace := replacePath
				if !filepath.IsAbs(absReplace) {
					absReplace = filepath.Join(dir, absReplace)
				}
				edenLibsRoot := filepath.Dir(absReplace) // parent of eden-platform-go
				return gomodPath, edenLibsRoot
			}
		}
	}

	// Has go.mod but no eden replace directive.
	if strings.Contains(string(data), "eden-platform-go") {
		return gomodPath, ""
	}
	return "", ""
}

// findEdenLibsInPubspec searches for a pubspec.yaml with eden path dependencies.
func findEdenLibsInPubspec(root string) (string, string) {
	// Check root pubspec first.
	if result, edenPath := parsePubspecForEden(filepath.Join(root, "pubspec.yaml"), root); result != "" {
		return result, edenPath
	}

	// Check immediate subdirectories.
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		subdir := filepath.Join(root, e.Name())
		pubspec := filepath.Join(subdir, "pubspec.yaml")
		if result, edenPath := parsePubspecForEden(pubspec, subdir); result != "" {
			return result, edenPath
		}
	}
	return "", ""
}

// parsePubspecForEden reads a pubspec.yaml and extracts eden path dependency roots.
func parsePubspecForEden(pubspecPath, dir string) (string, string) {
	f, err := os.Open(pubspecPath)
	if err != nil {
		return "", ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	foundEdenDep := false
	lookingForPath := false

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "eden_platform_flutter:") ||
			strings.HasPrefix(trimmed, "eden_ui_flutter:") ||
			strings.HasPrefix(trimmed, "eden_platform_api_dart:") {
			foundEdenDep = true
			lookingForPath = true
			continue
		}

		if lookingForPath && strings.HasPrefix(trimmed, "path:") {
			pathVal := strings.TrimSpace(strings.TrimPrefix(trimmed, "path:"))
			// Path is like ../eden-libs/eden-platform-flutter — go up to eden-libs root.
			absPath := pathVal
			if !filepath.IsAbs(absPath) {
				absPath = filepath.Join(dir, absPath)
			}
			edenLibsRoot := filepath.Dir(absPath) // parent of the specific package dir
			return pubspecPath, edenLibsRoot
		}

		// If we hit a non-indented line and were looking for path, stop.
		if lookingForPath && len(line) > 0 && line[0] != ' ' && line[0] != '\t' {
			lookingForPath = false
		}
	}

	if foundEdenDep {
		return pubspecPath, ""
	}
	return "", ""
}

// generateEdenChangelog runs the changelog script in the eden-libs directory.
func generateEdenChangelog(edenLibsPath string) (string, error) {
	scriptPath := filepath.Join(edenLibsPath, "scripts", "changelog.sh")
	if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
		// Fall back to git log.
		return generateGitChangelog(edenLibsPath)
	}

	out, err := exec.Command("bash", scriptPath).CombinedOutput()
	if err != nil {
		return generateGitChangelog(edenLibsPath)
	}
	return string(out), nil
}

// --- Streaming Claude output ---

// streamEvent represents a single JSON line from claude --output-format stream-json.
type streamEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Message *struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	} `json:"message"`
	Result string `json:"result"`
}

// toolInput is used to extract file paths from tool_use inputs.
type toolInput struct {
	FilePath string `json:"file_path"`
	Pattern  string `json:"pattern"`
	Command  string `json:"command"`
}

// streamClaude reads the streaming JSON from Claude, prints live progress
// updates, and returns the final result text.
func streamClaude(r io.Reader) (string, error) {
	scanner := bufio.NewScanner(r)
	// Increase buffer for large JSON lines.
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)

	var (
		result    string
		toolCount int
		start     = time.Now()
		lastTool  string
	)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var ev streamEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue // skip malformed lines
		}

		switch ev.Type {
		case "assistant":
			if ev.Message == nil {
				continue
			}
			for _, c := range ev.Message.Content {
				switch c.Type {
				case "tool_use":
					toolCount++
					desc := formatToolProgress(c.Name, c.Input)
					if desc != lastTool {
						elapsed := time.Since(start).Truncate(time.Second)
						fmt.Printf("  [%s] %s\n", elapsed, desc)
						lastTool = desc
					}
				}
			}

		case "result":
			result = ev.Result
		}
	}

	if err := scanner.Err(); err != nil {
		return result, fmt.Errorf("reading stream: %w", err)
	}

	elapsed := time.Since(start).Truncate(time.Second)
	fmt.Printf("\n  Completed in %s (%d tool calls)\n", elapsed, toolCount)

	return result, nil
}

// formatToolProgress returns a human-readable description of a tool call.
func formatToolProgress(name string, rawInput json.RawMessage) string {
	var input toolInput
	_ = json.Unmarshal(rawInput, &input)

	switch name {
	case "Read":
		if input.FilePath != "" {
			return fmt.Sprintf("Reading %s", shortenPath(input.FilePath))
		}
		return "Reading file"
	case "Glob":
		if input.Pattern != "" {
			return fmt.Sprintf("Searching for %s", input.Pattern)
		}
		return "Searching files"
	case "Grep":
		if input.Pattern != "" {
			return fmt.Sprintf("Grep: %s", truncate(input.Pattern, 60))
		}
		return "Searching content"
	case "Edit":
		if input.FilePath != "" {
			return fmt.Sprintf("Editing %s", shortenPath(input.FilePath))
		}
		return "Editing file"
	case "Write":
		if input.FilePath != "" {
			return fmt.Sprintf("Writing %s", shortenPath(input.FilePath))
		}
		return "Writing file"
	case "Bash":
		if input.Command != "" {
			return fmt.Sprintf("Running: %s", truncate(input.Command, 60))
		}
		return "Running command"
	default:
		return fmt.Sprintf("Tool: %s", name)
	}
}

// shortenPath trims a file path to the last 3 components for readability.
func shortenPath(p string) string {
	parts := strings.Split(filepath.ToSlash(p), "/")
	if len(parts) <= 3 {
		return p
	}
	return ".../" + strings.Join(parts[len(parts)-3:], "/")
}

// truncate shortens a string to maxLen, appending "..." if truncated.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// generateGitChangelog produces a simple git log from eden-libs.
func generateGitChangelog(edenLibsPath string) (string, error) {
	cmd := exec.Command("git", "log", "--oneline", "-50", "--no-merges")
	cmd.Dir = edenLibsPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git log: %w", err)
	}
	return string(out), nil
}

// getEdenVersion returns the latest tag or short SHA from eden-libs.
func getEdenVersion(edenLibsPath string) string {
	// Try tag first.
	cmd := exec.Command("git", "describe", "--tags", "--always")
	cmd.Dir = edenLibsPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

// buildAllowedTools returns the comma-separated list of tools Claude should be allowed to use.
func buildAllowedTools(uctx *upgradeContext) string {
	tools := []string{
		"Read",
		"Glob",
		"Grep",
		"Edit",
		"Write",
		"Bash(go mod tidy*)",
		"Bash(flutter pub get*)",
		"Bash(dart pub get*)",
		"Bash(go build*)",
		"Bash(dart analyze*)",
		"Bash(flutter analyze*)",
	}
	return strings.Join(tools, ",")
}

// buildUpgradePrompt constructs the system prompt for Claude to perform the upgrade.
func buildUpgradePrompt(uctx *upgradeContext, dryRun bool) string {
	absEdenLibs, _ := filepath.Abs(uctx.EdenLibsPath)

	var b strings.Builder

	b.WriteString("You are upgrading an Eden consumer workspace to the latest eden-libs framework version.\n\n")

	b.WriteString("## Workspace Info\n\n")
	b.WriteString(fmt.Sprintf("- Consumer working directory: %s\n", uctx.WorkspaceDir))
	b.WriteString(fmt.Sprintf("- eden-libs absolute path: %s\n", absEdenLibs))
	b.WriteString(fmt.Sprintf("- eden-libs version: %s\n", uctx.EdenVersion))
	b.WriteString(fmt.Sprintf("- Has Go packages: %v\n", uctx.HasGo))
	b.WriteString(fmt.Sprintf("- Has Flutter packages: %v\n", uctx.HasFlutter))
	b.WriteString(fmt.Sprintf("- Has Dart packages: %v\n", uctx.HasDart))
	b.WriteString("\n")

	b.WriteString("## Access\n\n")
	b.WriteString(fmt.Sprintf("You have read access to eden-libs at `%s` via --add-dir.\n", absEdenLibs))
	b.WriteString("Read source files directly from eden-libs when you need to verify API signatures.\n\n")

	b.WriteString("## Recent eden-libs Changes\n\n```\n")
	b.WriteString(uctx.Changelog)
	b.WriteString("```\n\n")

	b.WriteString("## Task\n\n")
	b.WriteString("Perform a comprehensive upgrade analysis in three phases.\n\n")

	// Phase 1: Gather
	b.WriteString("### Phase 1: Gather (read in parallel where possible)\n\n")

	if uctx.HasFlutter || uctx.HasDart {
		b.WriteString("**eden-libs API surface** — read these files:\n")
		b.WriteString(fmt.Sprintf("- `%s/eden-ui-flutter/lib/eden_ui.dart` (barrel exports)\n", absEdenLibs))
		b.WriteString(fmt.Sprintf("- `%s/eden-ui-flutter/pubspec.yaml`\n", absEdenLibs))
		b.WriteString(fmt.Sprintf("- `%s/eden-platform-flutter/lib/eden_platform.dart` (barrel exports)\n", absEdenLibs))
		b.WriteString(fmt.Sprintf("- `%s/eden-platform-flutter/pubspec.yaml`\n", absEdenLibs))
		b.WriteString(fmt.Sprintf("- `%s/eden-platform-api-dart/lib/eden_platform_api_dart.dart` (barrel exports)\n", absEdenLibs))
		b.WriteString(fmt.Sprintf("- `%s/eden-platform-api-dart/pubspec.yaml`\n", absEdenLibs))
		b.WriteString("\n")
	}
	if uctx.HasGo {
		b.WriteString(fmt.Sprintf("- `%s/eden-platform-go/go.mod`\n", absEdenLibs))
		b.WriteString("\n")
	}

	b.WriteString("**Consumer usage** — grep this workspace for all eden imports, then extract the list of Eden symbols actually used (widgets, classes, enums, providers, functions).\n\n")

	// Phase 2: Verify
	b.WriteString("### Phase 2: Verify Compatibility\n\n")
	b.WriteString("For every Eden symbol the consumer uses:\n")
	b.WriteString("1. Confirm it is still present in the barrel exports\n")
	b.WriteString("2. Read the eden-libs source file for that symbol to verify:\n")
	b.WriteString("   - Constructor/function signature (required params, optional params, types)\n")
	b.WriteString("   - Enum values the consumer references still exist\n")
	b.WriteString("   - Static methods the consumer calls still exist with matching signatures\n")
	b.WriteString("   - Token access patterns work (e.g., indexed color access like `EdenColors.blue[500]`)\n")
	b.WriteString("3. Check dependency version constraints are compatible (SDK versions, package versions)\n\n")
	b.WriteString("**Efficiency tip:** batch-read source files for multiple symbols from the same file. ")
	b.WriteString("If the consumer uses 5 symbols from 3 source files, read those 3 files — not 20+.\n\n")

	// Phase 3: Act
	if dryRun {
		b.WriteString("### Phase 3: Report (DRY RUN — do not modify files)\n\n")
		b.WriteString("Produce a comprehensive report:\n\n")
		b.WriteString("1. **Compatibility Matrix** — every Eden symbol used, whether it exists, whether its signature matches consumer usage\n")
		b.WriteString("2. **Breaking Changes** — exact details of what broke (added required param, renamed symbol, etc.)\n")
		b.WriteString("3. **Files Requiring Changes** — consumer files that need updates, with what to change\n")
		b.WriteString("4. **Dependency Compatibility** — SDK version and package version conflicts\n")
		b.WriteString("5. **New Features Available** — brief list of unused Eden widgets the consumer could adopt\n")
		b.WriteString("6. **Verification Commands** — commands to run to confirm compatibility\n")
		b.WriteString("7. **Overall Risk** — low/medium/high with reasoning\n")
	} else {
		b.WriteString("### Phase 3: Apply Changes\n\n")
		b.WriteString("1. Update import paths and renamed symbols\n")
		b.WriteString("2. Fix changed function signatures and constructors\n")
		b.WriteString("3. Update pubspec.yaml / go.mod version constraints and replace directives\n")
		b.WriteString("4. Run `flutter pub get` / `dart pub get` / `go mod tidy` as needed\n")
		b.WriteString("5. Run `flutter analyze` / `go build ./...` to verify\n")
		b.WriteString("6. If verification finds issues, fix them and re-run (max 2 fix iterations)\n\n")

		b.WriteString("**Guidelines:**\n")
		b.WriteString("- Only modify files that actually need changes\n")
		b.WriteString("- Preserve existing code style\n")
		b.WriteString("- Do NOT modify files inside eden-libs\n")
		b.WriteString("- Do NOT create new files unless the upgrade requires it\n")
		b.WriteString("- Do NOT refactor unrelated code\n")
	}

	return b.String()
}

// upgradeManifest is written after a successful upgrade for tracking.
type upgradeManifest struct {
	EdenLibsVersion string `json:"eden_libs_version"`
	EdenLibsPath    string `json:"eden_libs_path"`
	UpgradedAt      string `json:"upgraded_at"`
	DryRun          bool   `json:"dry_run"`
	Model           string `json:"model,omitempty"`
}

// writeUpgradeManifest saves the upgrade state for future reference.
func writeUpgradeManifest(dir string, manifest upgradeManifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, ".eden-upgrade.json"), data, 0644)
}
