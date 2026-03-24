package daemon

import (
	"sync"
	"time"
)

// WorkspaceState tracks the runtime state of a single workspace process.
type WorkspaceState struct {
	Name    string
	Port    int
	PID     int
	Started time.Time
	LastReq time.Time
	Status  string // "starting", "running", "idle", "stopped"
}

// State holds the runtime state for all managed workspaces. It is safe for
// concurrent use.
type State struct {
	mu         sync.RWMutex
	workspaces map[string]*WorkspaceState
}

// NewState returns an initialized State.
func NewState() *State {
	return &State{
		workspaces: make(map[string]*WorkspaceState),
	}
}

// Get returns the state for the named workspace, or nil if not tracked.
func (s *State) Get(name string) *WorkspaceState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ws := s.workspaces[name]
	if ws == nil {
		return nil
	}
	// Return a copy to avoid races on the caller side.
	cp := *ws
	return &cp
}

// Set stores or replaces the state for the named workspace.
func (s *State) Set(name string, ws *WorkspaceState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workspaces[name] = ws
}

// Remove deletes the state for the named workspace.
func (s *State) Remove(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.workspaces, name)
}

// All returns a snapshot of all workspace states.
func (s *State) All() []*WorkspaceState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*WorkspaceState, 0, len(s.workspaces))
	for _, ws := range s.workspaces {
		cp := *ws
		result = append(result, &cp)
	}
	return result
}
