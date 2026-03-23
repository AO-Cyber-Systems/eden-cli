package command

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(doctorCmd)
}

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check system prerequisites and workspace health",
	Long:  "Verify that required tools, AI tooling, and workspace configuration are present and correctly versioned.",
	RunE:  runDoctor,
}

type checkResult struct {
	name    string
	status  string // "ok", "warn", "fail", "skip"
	detail  string
	version string
}

func runDoctor(cmd *cobra.Command, args []string) error {
	fmt.Println("Eden Doctor")
	fmt.Println("===========")
	fmt.Println()

	var results []checkResult
	var failures int

	// --- Dev toolchain ---
	fmt.Println("Dev Toolchain")
	fmt.Println("─────────────")

	// Required: core build tools
	results = append(results, checkBinary("go", []string{"version"}, "go version go(\\d+\\.\\d+\\.\\d+)", 1, 26, 0, true))
	results = append(results, checkBinary("flutter", []string{"--version"}, "Flutter (\\d+\\.\\d+\\.\\d+)", 3, 11, 0, true))
	results = append(results, checkBinary("dart", []string{"--version"}, "Dart SDK version: (\\d+\\.\\d+\\.\\d+)", 3, 11, 0, true))
	// Optional: needed for specific commands
	results = append(results, checkBinary("buf", []string{"--version"}, "(\\d+\\.\\d+\\.\\d+)", 0, 0, 0, false))
	results = append(results, checkBinary("docker", []string{"--version"}, "Docker version (\\d+\\.\\d+\\.\\d+)", 0, 0, 0, false))
	results = append(results, checkDockerCompose())

	for _, r := range results {
		printResult(r)
		if r.status == "fail" {
			failures++
		}
	}

	// --- AI Tooling ---
	fmt.Println()
	fmt.Println("AI Tooling")
	fmt.Println("──────────")

	var aiResults []checkResult
	aiResults = append(aiResults, checkClaude())
	aiResults = append(aiResults, checkDevFlow())
	aiResults = append(aiResults, checkClaudeMD())
	aiResults = append(aiResults, checkMCPServers())

	for _, r := range aiResults {
		printResult(r)
		if r.status == "fail" {
			failures++
		}
	}

	// --- Workspace ---
	fmt.Println()
	fmt.Println("Workspace")
	fmt.Println("─────────")

	var wsResults []checkResult
	wsResults = append(wsResults, checkWorkspaceStructure())
	wsResults = append(wsResults, checkEdenLibsRef())

	for _, r := range wsResults {
		printResult(r)
		if r.status == "fail" {
			failures++
		}
	}

	// --- Summary ---
	total := len(results) + len(aiResults) + len(wsResults)
	passed := 0
	warned := 0
	for _, lists := range [][]checkResult{results, aiResults, wsResults} {
		for _, r := range lists {
			switch r.status {
			case "ok":
				passed++
			case "warn":
				warned++
			}
		}
	}

	fmt.Println()
	if failures == 0 {
		fmt.Printf("All clear: %d/%d checks passed", passed, total)
		if warned > 0 {
			fmt.Printf(" (%d warnings)", warned)
		}
		fmt.Println()
		return nil
	}
	fmt.Printf("%d issue(s) found out of %d checks.\n", failures, total)
	return fmt.Errorf("%d prerequisite(s) missing", failures)
}

// --- Dev tool checks ---

func checkBinary(name string, versionArgs []string, versionRegex string, minMajor, minMinor, minPatch int, required bool) checkResult {
	path, err := exec.LookPath(name)
	if err != nil {
		status := "fail"
		if !required {
			status = "warn"
		}
		detail := "not found in PATH"
		if !required {
			detail += " (optional — needed for eden " + toolUsage(name) + ")"
		}
		return checkResult{name: name, status: status, detail: detail}
	}

	out, err := exec.Command(path, versionArgs...).CombinedOutput()
	if err != nil {
		return checkResult{name: name, status: "warn", detail: fmt.Sprintf("found at %s but version check failed", path)}
	}

	version := "unknown"
	if versionRegex != "" {
		re := regexp.MustCompile(versionRegex)
		if m := re.FindSubmatch(out); len(m) > 1 {
			version = string(m[1])
		}
	}

	if minMajor > 0 && version != "unknown" {
		if !versionAtLeast(version, minMajor, minMinor, minPatch) {
			return checkResult{
				name:    name,
				status:  "warn",
				version: version,
				detail:  fmt.Sprintf("found %s, want >= %d.%d.%d", version, minMajor, minMinor, minPatch),
			}
		}
	}

	return checkResult{name: name, status: "ok", version: version, detail: path}
}

func checkDockerCompose() checkResult {
	out, err := exec.Command("docker", "compose", "version").CombinedOutput()
	if err != nil {
		return checkResult{name: "docker compose", status: "warn", detail: "docker compose plugin not available"}
	}
	re := regexp.MustCompile(`v?(\d+\.\d+\.\d+)`)
	version := "unknown"
	if m := re.FindSubmatch(out); len(m) > 1 {
		version = string(m[1])
	}
	return checkResult{name: "docker compose", status: "ok", version: version}
}

// --- AI tooling checks ---

func checkClaude() checkResult {
	path, err := exec.LookPath("claude")
	if err != nil {
		return checkResult{name: "claude", status: "fail", detail: "Claude Code CLI not found — install from https://claude.ai/download"}
	}
	out, err := exec.Command(path, "--version").CombinedOutput()
	if err != nil {
		return checkResult{name: "claude", status: "warn", detail: "found but version check failed"}
	}
	version := strings.TrimSpace(string(out))
	// Claude version output: "2.1.81 (Claude Code)"
	re := regexp.MustCompile(`(\d+\.\d+\.\d+)`)
	if m := re.FindStringSubmatch(version); len(m) > 1 {
		version = m[1]
	}
	return checkResult{name: "claude", status: "ok", version: version, detail: path}
}

func checkDevFlow() checkResult {
	home, err := os.UserHomeDir()
	if err != nil {
		return checkResult{name: "devflow", status: "fail", detail: "cannot determine home directory"}
	}

	dfDir := filepath.Join(home, ".claude", "devflow")
	if _, err := os.Stat(dfDir); os.IsNotExist(err) {
		return checkResult{name: "devflow", status: "warn", detail: "~/.claude/devflow/ not found — DevFlow plugin not installed"}
	}

	// Check for key files
	binDir := filepath.Join(dfDir, "bin", "df-tools.cjs")
	if _, err := os.Stat(binDir); os.IsNotExist(err) {
		return checkResult{name: "devflow", status: "warn", detail: "~/.claude/devflow/ exists but df-tools.cjs missing"}
	}

	// Check version
	versionFile := filepath.Join(dfDir, "VERSION")
	if data, err := os.ReadFile(versionFile); err == nil {
		version := strings.TrimSpace(string(data))
		return checkResult{name: "devflow", status: "ok", version: version, detail: dfDir}
	}

	return checkResult{name: "devflow", status: "ok", detail: dfDir}
}

func checkClaudeMD() checkResult {
	// Check for CLAUDE.md in current directory
	if _, err := os.Stat("CLAUDE.md"); err == nil {
		return checkResult{name: "CLAUDE.md", status: "ok", detail: "found in workspace root"}
	}
	return checkResult{name: "CLAUDE.md", status: "warn", detail: "no CLAUDE.md in current directory — AI agents lack project context"}
}

func checkMCPServers() checkResult {
	home, err := os.UserHomeDir()
	if err != nil {
		return checkResult{name: "MCP servers", status: "skip", detail: "cannot determine home directory"}
	}

	// Check project-level .mcp.json first, then global
	projectMCP := ".mcp.json"
	globalMCP := filepath.Join(home, ".claude", "settings.json")

	hasProject := false
	if _, err := os.Stat(projectMCP); err == nil {
		hasProject = true
	}

	hasGlobal := false
	if data, err := os.ReadFile(globalMCP); err == nil {
		if strings.Contains(string(data), "mcpServers") {
			hasGlobal = true
		}
	}

	if hasProject && hasGlobal {
		return checkResult{name: "MCP servers", status: "ok", detail: "project + global configs found"}
	}
	if hasProject {
		return checkResult{name: "MCP servers", status: "ok", detail: "project .mcp.json found"}
	}
	if hasGlobal {
		return checkResult{name: "MCP servers", status: "ok", detail: "global settings has MCP config"}
	}
	return checkResult{name: "MCP servers", status: "skip", detail: "no MCP configuration found (optional)"}
}

// --- Workspace checks ---

func checkWorkspaceStructure() checkResult {
	hasGoMod := fileExists("go.mod")
	hasPubspec := fileExists("pubspec.yaml")

	// Check for monorepo pattern (subdirs with go.mod/pubspec.yaml)
	isMonorepo := false
	entries, _ := os.ReadDir(".")
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if fileExists(filepath.Join(e.Name(), "go.mod")) || fileExists(filepath.Join(e.Name(), "pubspec.yaml")) {
			isMonorepo = true
			break
		}
	}

	if hasGoMod || hasPubspec {
		return checkResult{name: "workspace", status: "ok", detail: "consumer workspace detected (root-level packages)"}
	}
	if isMonorepo {
		return checkResult{name: "workspace", status: "ok", detail: "monorepo workspace detected (sub-packages)"}
	}
	return checkResult{name: "workspace", status: "warn", detail: "no Go or Flutter packages detected in current directory"}
}

func checkEdenLibsRef() checkResult {
	// Check go.mod for eden-platform-go replace directive
	if data, err := os.ReadFile("go.mod"); err == nil {
		if strings.Contains(string(data), "eden-platform-go") {
			return checkResult{name: "eden-libs ref", status: "ok", detail: "go.mod references eden-platform-go"}
		}
	}

	// Check subdirs for go.mod with replace
	entries, _ := os.ReadDir(".")
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		gomod := filepath.Join(e.Name(), "go.mod")
		if data, err := os.ReadFile(gomod); err == nil {
			if strings.Contains(string(data), "eden-platform-go") {
				return checkResult{name: "eden-libs ref", status: "ok", detail: fmt.Sprintf("%s references eden-platform-go", gomod)}
			}
		}
	}

	// Check pubspec.yaml for eden deps
	if data, err := os.ReadFile("pubspec.yaml"); err == nil {
		if strings.Contains(string(data), "eden_platform_flutter") {
			return checkResult{name: "eden-libs ref", status: "ok", detail: "pubspec.yaml references eden packages"}
		}
	}

	return checkResult{name: "eden-libs ref", status: "skip", detail: "no eden-libs references found (run from an Eden workspace)"}
}

// --- Helpers ---

func toolUsage(name string) string {
	switch name {
	case "buf":
		return "generate"
	case "docker":
		return "dev/down"
	default:
		return name
	}
}

func printResult(r checkResult) {
	var icon string
	switch r.status {
	case "ok":
		icon = "  ✓"
	case "warn":
		icon = "  !"
	case "fail":
		icon = "  ✗"
	case "skip":
		icon = "  -"
	}

	if r.version != "" {
		fmt.Printf("%s %-22s %s (%s)\n", icon, r.name, r.version, r.detail)
	} else {
		fmt.Printf("%s %-22s %s\n", icon, r.name, r.detail)
	}
}

func versionAtLeast(version string, minMajor, minMinor, minPatch int) bool {
	parts := strings.SplitN(version, ".", 3)
	if len(parts) < 2 {
		return false
	}
	major, _ := strconv.Atoi(parts[0])
	minor, _ := strconv.Atoi(parts[1])
	patch := 0
	if len(parts) > 2 {
		patch, _ = strconv.Atoi(parts[2])
	}

	if major != minMajor {
		return major > minMajor
	}
	if minor != minMinor {
		return minor > minMinor
	}
	return patch >= minPatch
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
