package daemon

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ServerType identifies the kind of dev server in a workspace.
type ServerType string

const (
	// ServerTypeGo indicates a Go backend server (auto-managed by the pool).
	ServerTypeGo ServerType = "go"
	// ServerTypeFlutter indicates a Flutter frontend (tracked but not auto-started).
	ServerTypeFlutter ServerType = "flutter"
	// ServerTypeUnknown indicates no recognized server type was detected.
	ServerTypeUnknown ServerType = "unknown"
)

// WorkspaceProcess manages the lifecycle of a single workspace dev server process.
type WorkspaceProcess struct {
	Name       string
	Dir        string
	ServerType ServerType
	Port       int

	cmd     *exec.Cmd
	cancel  context.CancelFunc
	stdout  *limitedBuffer
	stderr  *limitedBuffer
	started time.Time
	exited  chan struct{} // closed when the process exits
	mu      sync.Mutex
}

// DetectServerType inspects a workspace directory and returns the type of
// server it contains. It checks for cmd/*/main.go (Go) and pubspec.yaml
// (Flutter). Go takes precedence since that is what the pool auto-starts.
func DetectServerType(dir string) ServerType {
	// Check for Go server: cmd/*/main.go pattern.
	if findGoEntrypoint(dir) != "" {
		return ServerTypeGo
	}
	// Also check eden-platform-go subdirectory (eden-libs workspace).
	platformGo := filepath.Join(dir, "eden-platform-go")
	if info, err := os.Stat(platformGo); err == nil && info.IsDir() {
		if findGoEntrypoint(platformGo) != "" {
			return ServerTypeGo
		}
	}
	// Check for Flutter.
	if fileExists(filepath.Join(dir, "pubspec.yaml")) {
		return ServerTypeFlutter
	}
	return ServerTypeUnknown
}

// findGoEntrypoint searches for the first cmd/*/main.go under base and
// returns the relative package path (e.g. "./cmd/server"), or "" if none found.
func findGoEntrypoint(base string) string {
	cmdDir := filepath.Join(base, "cmd")
	entries, err := os.ReadDir(cmdDir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		mainFile := filepath.Join(cmdDir, e.Name(), "main.go")
		if fileExists(mainFile) {
			return "./cmd/" + e.Name()
		}
	}
	return ""
}

// findGoWorkDir returns the directory containing the Go server and the
// entrypoint package path. It handles both direct workspaces and the
// eden-libs layout where the Go code lives in eden-platform-go/.
func findGoWorkDir(dir string) (workDir string, entrypoint string, err error) {
	// Direct workspace: cmd/*/main.go under dir.
	if ep := findGoEntrypoint(dir); ep != "" {
		return dir, ep, nil
	}
	// Eden-libs layout: eden-platform-go/cmd/*/main.go.
	platformGo := filepath.Join(dir, "eden-platform-go")
	if info, statErr := os.Stat(platformGo); statErr == nil && info.IsDir() {
		if ep := findGoEntrypoint(platformGo); ep != "" {
			return platformGo, ep, nil
		}
	}
	return "", "", fmt.Errorf("no cmd/*/main.go found under %s", dir)
}

// NewWorkspaceProcess creates a new WorkspaceProcess for the named workspace.
func NewWorkspaceProcess(name, dir string, port int) *WorkspaceProcess {
	return &WorkspaceProcess{
		Name:       name,
		Dir:        dir,
		ServerType: DetectServerType(dir),
		Port:       port,
		stdout:     newLimitedBuffer(1000),
		stderr:     newLimitedBuffer(1000),
		exited:     make(chan struct{}),
	}
}

// Start launches the workspace dev server process. Only Go servers are
// supported for auto-start; calling Start on a non-Go workspace returns an error.
func (wp *WorkspaceProcess) Start(ctx context.Context) error {
	wp.mu.Lock()
	defer wp.mu.Unlock()

	if wp.ServerType != ServerTypeGo {
		return fmt.Errorf("workspace %q has server type %q; only %q can be auto-started",
			wp.Name, wp.ServerType, ServerTypeGo)
	}

	workDir, entrypoint, err := findGoWorkDir(wp.Dir)
	if err != nil {
		return fmt.Errorf("workspace %q: %w", wp.Name, err)
	}

	cmdCtx, cancel := context.WithCancel(ctx)
	wp.cancel = cancel

	wp.cmd = exec.CommandContext(cmdCtx, "go", "run", entrypoint)
	wp.cmd.Dir = workDir
	wp.cmd.Env = append(os.Environ(), fmt.Sprintf("PORT=%d", wp.Port))
	wp.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	wp.cmd.Stdout = wp.stdout
	wp.cmd.Stderr = wp.stderr
	wp.started = time.Now()

	if err := wp.cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("starting workspace %q: %w", wp.Name, err)
	}

	slog.Info("workspace process started",
		"workspace", wp.Name,
		"pid", wp.cmd.Process.Pid,
		"port", wp.Port,
		"dir", workDir,
		"entrypoint", entrypoint,
	)

	// Wait for exit in background and signal via channel.
	go func() {
		err := wp.cmd.Wait()
		if err != nil && cmdCtx.Err() == nil {
			slog.Error("workspace process exited with error",
				"workspace", wp.Name,
				"error", err,
			)
		}
		close(wp.exited)
	}()

	return nil
}

// WaitReady polls the workspace's TCP port until it accepts connections or the
// timeout elapses. It returns an error if the server does not become ready in
// time or if the process exits before becoming ready.
func (wp *WorkspaceProcess) WaitReady(timeout time.Duration) error {
	deadline := time.After(timeout)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	addr := fmt.Sprintf("127.0.0.1:%d", wp.Port)

	for {
		select {
		case <-wp.exited:
			return fmt.Errorf("workspace %q process exited before becoming ready: %s",
				wp.Name, wp.RecentOutput())
		case <-deadline:
			return fmt.Errorf("workspace %q did not become ready within %s (port %d): %s",
				wp.Name, timeout, wp.Port, wp.RecentOutput())
		case <-ticker.C:
			conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
			if err == nil {
				conn.Close()
				slog.Info("workspace ready",
					"workspace", wp.Name,
					"port", wp.Port,
					"startup_time", time.Since(wp.started).Truncate(time.Millisecond),
				)
				return nil
			}
		}
	}
}

// Stop gracefully terminates the workspace process. It sends SIGTERM to the
// process group, waits up to 5 seconds, then sends SIGKILL if still running.
func (wp *WorkspaceProcess) Stop() error {
	wp.mu.Lock()
	defer wp.mu.Unlock()

	if wp.cmd == nil || wp.cmd.Process == nil {
		return nil
	}

	pid := wp.cmd.Process.Pid

	// Send SIGTERM to the process group.
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
		// Process may already be dead.
		slog.Debug("SIGTERM failed (process may have exited)", "workspace", wp.Name, "error", err)
	}

	// Wait up to 5 seconds for graceful exit.
	select {
	case <-wp.exited:
		slog.Info("workspace stopped gracefully", "workspace", wp.Name)
		return nil
	case <-time.After(5 * time.Second):
	}

	// Force kill the process group.
	slog.Warn("workspace did not stop gracefully, sending SIGKILL", "workspace", wp.Name)
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		slog.Debug("SIGKILL failed", "workspace", wp.Name, "error", err)
	}

	// Wait for process to fully exit.
	select {
	case <-wp.exited:
	case <-time.After(2 * time.Second):
		return fmt.Errorf("workspace %q process (pid %d) did not exit after SIGKILL", wp.Name, pid)
	}

	if wp.cancel != nil {
		wp.cancel()
	}

	slog.Info("workspace force-stopped", "workspace", wp.Name)
	return nil
}

// IsRunning reports whether the workspace process is still alive.
func (wp *WorkspaceProcess) IsRunning() bool {
	select {
	case <-wp.exited:
		return false
	default:
		return wp.cmd != nil && wp.cmd.Process != nil
	}
}

// RecentOutput returns the last ~50 lines of stderr output for error diagnosis.
func (wp *WorkspaceProcess) RecentOutput() string {
	output := wp.stderr.String()
	lines := strings.Split(output, "\n")
	if len(lines) > 50 {
		lines = lines[len(lines)-50:]
	}
	return strings.Join(lines, "\n")
}

// limitedBuffer is a bytes.Buffer that keeps only the most recent maxLines
// lines of output. It is safe for concurrent use.
type limitedBuffer struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	maxLines int
}

func newLimitedBuffer(maxLines int) *limitedBuffer {
	return &limitedBuffer{maxLines: maxLines}
}

func (lb *limitedBuffer) Write(p []byte) (int, error) {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	n, err := lb.buf.Write(p)
	lb.trim()
	return n, err
}

func (lb *limitedBuffer) String() string {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	return lb.buf.String()
}

func (lb *limitedBuffer) trim() {
	lines := strings.Split(lb.buf.String(), "\n")
	if len(lines) > lb.maxLines {
		lines = lines[len(lines)-lb.maxLines:]
		lb.buf.Reset()
		lb.buf.WriteString(strings.Join(lines, "\n"))
	}
}
