package command

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/aocybersystems/eden/cli/internal/daemon"
	"github.com/spf13/cobra"
)

var teardownForce bool

func init() {
	devCmd.AddCommand(devSetupCmd)
	devCmd.AddCommand(devTeardownCmd)
	devCmd.AddCommand(devLinkCmd)
	devCmd.AddCommand(devUnlinkCmd)
	devCmd.AddCommand(devStatusCmd)
	devCmd.AddCommand(devDaemonCmd)
	devCmd.AddCommand(devLogsCmd)

	devTeardownCmd.Flags().BoolVar(&teardownForce, "force", false, "Remove CA certificates without prompting")
}

// --- eden dev setup ---

var devSetupCmd = &cobra.Command{
	Use:   "setup",
	Short: "One-time system setup for the Eden dev server",
	Long: `Creates the ~/.eden/ directory structure, installs the macOS LaunchAgent,
and configures DNS resolution for *.eden.test domains.

This command requires sudo for /etc/resolver/test.`,
	RunE: runDevSetup,
}

func runDevSetup(cmd *cobra.Command, _ []string) error {
	fmt.Println("[eden] Setting up dev server...")

	// 1. Create config directories.
	if err := daemon.EnsureConfigDirs(); err != nil {
		return fmt.Errorf("creating config directories: %w", err)
	}
	fmt.Println("  Created ~/.eden/ directory structure")

	// 2. Write /etc/resolver/test (requires sudo).
	resolverContent := "nameserver 127.0.0.1\nport 9253\n"
	sudoTee := exec.CommandContext(cmd.Context(), "sudo", "tee", "/etc/resolver/test")
	sudoTee.Stdin = strings.NewReader(resolverContent)
	sudoTee.Stdout = nil // suppress tee's stdout echo
	sudoTee.Stderr = os.Stderr
	if err := sudoTee.Run(); err != nil {
		return fmt.Errorf("writing /etc/resolver/test (sudo required): %w", err)
	}
	fmt.Println("  Configured DNS resolver for *.eden.test")

	// 3. Write LaunchAgent plist.
	if err := writeLaunchAgentPlist(); err != nil {
		return fmt.Errorf("writing LaunchAgent plist: %w", err)
	}
	fmt.Println("  Installed LaunchAgent plist")

	// 4. Load LaunchAgent.
	plistPath, _ := launchAgentPlistPath()
	loadCmd := exec.CommandContext(cmd.Context(), "launchctl", "load", plistPath)
	loadCmd.Stdout = os.Stdout
	loadCmd.Stderr = os.Stderr
	if err := loadCmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "  Warning: launchctl load failed (daemon may already be loaded): %v\n", err)
	} else {
		fmt.Println("  Loaded LaunchAgent")
	}

	fmt.Println("\n[eden] Dev server setup complete!")
	fmt.Println("  Link a workspace:  eden dev link myapp /path/to/app")
	fmt.Println("  Check status:      eden dev status")
	return nil
}

// --- eden dev teardown ---

var devTeardownCmd = &cobra.Command{
	Use:   "teardown",
	Short: "Remove the Eden dev server system configuration",
	Long: `Unloads the LaunchAgent, removes the plist, and cleans up DNS resolver config.
Use --force to also remove CA certificates.`,
	RunE: runDevTeardown,
}

func runDevTeardown(cmd *cobra.Command, _ []string) error {
	fmt.Println("[eden] Tearing down dev server...")

	// 1. Unload LaunchAgent.
	plistPath, err := launchAgentPlistPath()
	if err == nil {
		unloadCmd := exec.CommandContext(cmd.Context(), "launchctl", "unload", plistPath)
		unloadCmd.Stdout = os.Stdout
		unloadCmd.Stderr = os.Stderr
		if err := unloadCmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "  Warning: launchctl unload failed: %v\n", err)
		} else {
			fmt.Println("  Unloaded LaunchAgent")
		}

		// 2. Remove plist.
		if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "  Warning: removing plist: %v\n", err)
		} else {
			fmt.Println("  Removed LaunchAgent plist")
		}
	}

	// 3. Remove /etc/resolver/test.
	sudoRm := exec.CommandContext(cmd.Context(), "sudo", "rm", "-f", "/etc/resolver/test")
	sudoRm.Stdout = os.Stdout
	sudoRm.Stderr = os.Stderr
	if err := sudoRm.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "  Warning: removing /etc/resolver/test: %v\n", err)
	} else {
		fmt.Println("  Removed DNS resolver config")
	}

	// 4. Optionally remove CA.
	if teardownForce {
		caDir, err := daemon.CADir()
		if err == nil {
			if err := os.RemoveAll(caDir); err != nil {
				fmt.Fprintf(os.Stderr, "  Warning: removing CA directory: %v\n", err)
			} else {
				fmt.Println("  Removed CA certificates")
			}
		}
	}

	fmt.Println("\n[eden] Teardown complete.")
	return nil
}

// --- eden dev link ---

var devLinkCmd = &cobra.Command{
	Use:   "link [name] [dir]",
	Short: "Register a workspace with the Eden dev server",
	Long: `Creates a symlink in ~/.eden/workspaces/ pointing to the given directory.
If name is omitted, the directory basename is used. If dir is omitted, the
current working directory is used.

After linking, the workspace will be accessible at https://<name>.eden.test.`,
	Args: cobra.MaximumNArgs(2),
	RunE: runDevLink,
}

func runDevLink(_ *cobra.Command, args []string) error {
	var name, dir string

	switch len(args) {
	case 2:
		name = args[0]
		dir = args[1]
	case 1:
		name = args[0]
	}

	if dir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getting working directory: %w", err)
		}
		dir = cwd
	}

	// Resolve to absolute path.
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolving absolute path: %w", err)
	}

	if name == "" {
		name = filepath.Base(absDir)
	}

	if err := daemon.Link(name, absDir); err != nil {
		return err
	}

	fmt.Printf("[eden] Linked %s -> %s\n", name, absDir)
	fmt.Printf("  Visit https://%s.eden.test\n", name)
	return nil
}

// --- eden dev unlink ---

var devUnlinkCmd = &cobra.Command{
	Use:   "unlink <name>",
	Short: "Unregister a workspace from the Eden dev server",
	Args:  cobra.ExactArgs(1),
	RunE:  runDevUnlink,
}

func runDevUnlink(_ *cobra.Command, args []string) error {
	name := args[0]
	if err := daemon.Unlink(name); err != nil {
		return err
	}
	fmt.Printf("[eden] Unlinked %s\n", name)
	return nil
}

// --- eden dev status ---

var devStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show registered workspaces and daemon status",
	RunE:  runDevStatus,
}

func runDevStatus(cmd *cobra.Command, _ []string) error {
	// Check daemon status via launchctl.
	listCmd := exec.CommandContext(cmd.Context(), "launchctl", "list", daemon.LaunchAgentLabel)
	daemonRunning := listCmd.Run() == nil

	if daemonRunning {
		fmt.Println("[eden] Daemon: running")
	} else {
		fmt.Println("[eden] Daemon: stopped")
	}

	workspaces, err := daemon.ListWorkspaces()
	if err != nil {
		return fmt.Errorf("listing workspaces: %w", err)
	}

	if len(workspaces) == 0 {
		fmt.Println("\nNo workspaces registered. Use 'eden dev link' to register one.")
		return nil
	}

	fmt.Println()
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tDIRECTORY\tGO\tFLUTTER")
	for _, ws := range workspaces {
		goStr := boolMark(ws.HasGo)
		flutterStr := boolMark(ws.HasFlutter)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", ws.Name, ws.Dir, goStr, flutterStr)
	}
	tw.Flush()

	return nil
}

func boolMark(v bool) string {
	if v {
		return "yes"
	}
	return "-"
}

// --- eden dev daemon ---

var devDaemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Run the Eden dev server daemon (foreground)",
	Long: `Starts the Eden dev server daemon in the foreground. This is normally
invoked by the LaunchAgent, but can be run directly for debugging.

Press Ctrl-C to stop.`,
	RunE: runDevDaemon,
}

func runDevDaemon(cmd *cobra.Command, _ []string) error {
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	d := &daemon.Daemon{
		Config: daemon.DefaultConfig(),
		State:  daemon.NewState(),
	}
	return d.Run(ctx)
}

// --- eden dev logs ---

var devLogsCmd = &cobra.Command{
	Use:   "logs",
	Short: "Tail the Eden dev server logs",
	RunE:  runDevLogs,
}

func runDevLogs(cmd *cobra.Command, _ []string) error {
	logsDir, err := daemon.LogsDir()
	if err != nil {
		return err
	}

	// Tail all log files in the logs directory.
	tailCmd := exec.CommandContext(cmd.Context(), "tail", "-f", filepath.Join(logsDir, "*.log"))
	tailCmd.Stdout = os.Stdout
	tailCmd.Stderr = os.Stderr
	return tailCmd.Run()
}

// --- helpers ---

// launchAgentPlistPath returns the path to the LaunchAgent plist file.
func launchAgentPlistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", daemon.LaunchAgentLabel+".plist"), nil
}

// writeLaunchAgentPlist writes the com.eden.dev.plist to ~/Library/LaunchAgents/.
func writeLaunchAgentPlist() error {
	plistPath, err := launchAgentPlistPath()
	if err != nil {
		return err
	}

	// Ensure the LaunchAgents directory exists.
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		return fmt.Errorf("creating LaunchAgents directory: %w", err)
	}

	edenBin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("finding eden binary path: %w", err)
	}

	logsDir, err := daemon.LogsDir()
	if err != nil {
		return err
	}

	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>dev</string>
		<string>daemon</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>StandardOutPath</key>
	<string>%s/daemon.log</string>
	<key>StandardErrorPath</key>
	<string>%s/daemon.err.log</string>
	<key>Sockets</key>
	<dict>
		<key>http</key>
		<dict>
			<key>SockServiceName</key>
			<string>http</string>
			<key>SockType</key>
			<string>stream</string>
			<key>SockFamily</key>
			<string>IPv4</string>
		</dict>
		<key>https</key>
		<dict>
			<key>SockServiceName</key>
			<string>https</string>
			<key>SockType</key>
			<string>stream</string>
			<key>SockFamily</key>
			<string>IPv4</string>
		</dict>
	</dict>
</dict>
</plist>
`, daemon.LaunchAgentLabel, edenBin, logsDir, logsDir)

	return os.WriteFile(plistPath, []byte(plist), 0o644)
}
