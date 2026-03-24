package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Pool manages workspace server processes, starting them on demand and stopping
// them after an idle timeout. It is the bridge between the reverse proxy and
// individual WorkspaceProcess instances.
type Pool struct {
	state       *State
	workspaces  map[string]*WorkspaceProcess // name -> process
	mu          sync.Mutex
	nextPort    int
	idleTimeout time.Duration
	logsDir     string
}

// NewPool creates a process pool that tracks workspace servers. Port allocation
// starts at 9300 and increments. The idleTimeout controls how long a workspace
// can be idle before it is automatically stopped.
func NewPool(state *State, logsDir string, idleTimeout time.Duration) *Pool {
	return &Pool{
		state:       state,
		workspaces:  make(map[string]*WorkspaceProcess),
		nextPort:    9300,
		idleTimeout: idleTimeout,
		logsDir:     logsDir,
	}
}

// EnsureRunning guarantees that the named workspace has a running server and
// returns its port. If the server is already running, the last-request timestamp
// is updated and the existing port is returned. If not, the server is started
// and readiness is verified via TCP polling before returning.
//
// This method is designed to be used as the Proxy.OnRequest callback. Concurrent
// calls for the same workspace are serialised by the mutex so only one caller
// triggers the actual start.
func (p *Pool) EnsureRunning(ctx context.Context, name string) (int, error) {
	p.mu.Lock()

	// Fast path: already running.
	if wp, ok := p.workspaces[name]; ok && wp.IsRunning() {
		port := wp.Port
		p.mu.Unlock()
		// Update last request timestamp.
		p.touchLastReq(name)
		return port, nil
	}

	// Remove stale entry if process has exited.
	if wp, ok := p.workspaces[name]; ok && !wp.IsRunning() {
		delete(p.workspaces, name)
		p.state.Remove(name)
		slog.Info("removed stale workspace entry", "workspace", name)
	}

	// Resolve workspace directory.
	dir, err := ResolveWorkspace(name)
	if err != nil {
		p.mu.Unlock()
		return 0, fmt.Errorf("resolving workspace %q: %w", name, err)
	}

	// Allocate port.
	port := p.nextPort
	p.nextPort++

	// Create and register workspace process.
	wp := NewWorkspaceProcess(name, dir, port)

	// Only auto-start Go servers.
	if wp.ServerType != ServerTypeGo {
		p.mu.Unlock()
		return 0, fmt.Errorf("workspace %q is type %q; only Go servers are auto-managed",
			name, wp.ServerType)
	}

	p.workspaces[name] = wp

	// Update state to "starting".
	now := time.Now()
	p.state.Set(name, &WorkspaceState{
		Name:    name,
		Port:    port,
		PID:     0,
		Started: now,
		LastReq: now,
		Status:  "starting",
	})

	// Release the lock before blocking on start + readiness.
	p.mu.Unlock()

	// Start the process.
	if err := wp.Start(ctx); err != nil {
		p.state.Set(name, &WorkspaceState{
			Name:   name,
			Status: "stopped",
		})
		p.mu.Lock()
		delete(p.workspaces, name)
		p.mu.Unlock()
		return 0, fmt.Errorf("starting workspace %q: %w", name, err)
	}

	// Wait for TCP readiness (30 second timeout).
	if err := wp.WaitReady(30 * time.Second); err != nil {
		_ = wp.Stop()
		p.state.Set(name, &WorkspaceState{
			Name:   name,
			Status: "stopped",
		})
		p.mu.Lock()
		delete(p.workspaces, name)
		p.mu.Unlock()
		return 0, fmt.Errorf("workspace %q not ready: %w", name, err)
	}

	// Update state to "running" with PID.
	pid := 0
	if wp.cmd != nil && wp.cmd.Process != nil {
		pid = wp.cmd.Process.Pid
	}
	p.state.Set(name, &WorkspaceState{
		Name:    name,
		Port:    port,
		PID:     pid,
		Started: wp.started,
		LastReq: time.Now(),
		Status:  "running",
	})

	slog.Info("workspace server ready",
		"workspace", name,
		"port", port,
		"pid", pid,
	)

	return port, nil
}

// touchLastReq updates the last-request timestamp for a workspace in the State.
func (p *Pool) touchLastReq(name string) {
	ws := p.state.Get(name)
	if ws == nil {
		return
	}
	ws.LastReq = time.Now()
	p.state.Set(name, ws)
}

// Stop terminates a specific workspace server process and removes it from the pool.
func (p *Pool) Stop(name string) error {
	p.mu.Lock()
	wp, ok := p.workspaces[name]
	if !ok {
		p.mu.Unlock()
		return fmt.Errorf("workspace %q not in pool", name)
	}
	delete(p.workspaces, name)
	p.mu.Unlock()

	err := wp.Stop()
	p.state.Set(name, &WorkspaceState{
		Name:   name,
		Status: "stopped",
	})
	return err
}

// StopAll terminates all running workspace processes. It is called during
// daemon shutdown for clean process cleanup.
func (p *Pool) StopAll() error {
	p.mu.Lock()
	names := make([]string, 0, len(p.workspaces))
	for name := range p.workspaces {
		names = append(names, name)
	}
	p.mu.Unlock()

	var firstErr error
	for _, name := range names {
		if err := p.Stop(name); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	if len(names) > 0 {
		slog.Info("stopped all workspace processes", "count", len(names))
	}
	return firstErr
}

// StartIdleMonitor runs a background goroutine that periodically checks for
// idle workspaces and stops them. A workspace is considered idle when no
// requests have been proxied to it for longer than the pool's idleTimeout.
// The goroutine exits when ctx is cancelled.
func (p *Pool) StartIdleMonitor(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.reapIdle()
		}
	}
}

// reapIdle checks all tracked workspaces and stops any that have been idle
// longer than the configured timeout.
func (p *Pool) reapIdle() {
	all := p.state.All()
	for _, ws := range all {
		if ws.Status != "running" {
			continue
		}
		if ws.LastReq.IsZero() {
			continue
		}
		idle := time.Since(ws.LastReq)
		if idle > p.idleTimeout {
			slog.Info("stopping idle workspace",
				"workspace", ws.Name,
				"idle_for", idle.Truncate(time.Second),
			)
			if err := p.Stop(ws.Name); err != nil {
				slog.Error("failed to stop idle workspace",
					"workspace", ws.Name,
					"error", err,
				)
			}
		}
	}
}
