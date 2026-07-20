// Package appconfig persists the agent's local state: hub binding, app
// definitions, and — critically — which definition hashes the host has
// approved. It also implements the spool through which CLI subcommands hand
// operations to the running daemon (two processes, one state dir, no IPC).
package appconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/breakerbox/breakerbox/pkg/protocol"
)

// DefaultStateDir returns the platform state directory for the agent.
func DefaultStateDir() string {
	if custom := os.Getenv("BREAKERBOX_STATE_DIR"); custom != "" {
		return custom
	}
	switch runtime.GOOS {
	case "darwin":
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "Library", "Application Support", "breakerbox-agent")
	case "windows":
		return filepath.Join(os.Getenv("ProgramData"), "breakerbox-agent")
	default:
		if os.Geteuid() == 0 {
			return "/var/lib/breakerbox-agent"
		}
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".local", "share", "breakerbox-agent")
	}
}

// AppState is the agent's local view of one app.
type AppState struct {
	Definition   protocol.AppDefinition `json:"definition"`
	Hash         string                 `json:"hash"`
	Approval     protocol.Approval      `json:"approval"`
	DesiredState protocol.DesiredState  `json:"desired_state"`
}

// State is everything the agent persists.
type State struct {
	HubURL   string              `json:"hub_url"`
	SystemID string              `json:"system_id"`
	Apps     map[string]AppState `json:"apps"` // hub app ID -> state
}

// Store loads/saves State with atomic writes.
type Store struct {
	Dir string
}

func (s *Store) statePath() string { return filepath.Join(s.Dir, "state.json") }
func (s *Store) spoolDir() string  { return filepath.Join(s.Dir, "spool") }

// Load reads state; a missing file yields an empty state.
func (s *Store) Load() (*State, error) {
	st := &State{Apps: map[string]AppState{}}
	b, err := os.ReadFile(s.statePath())
	if os.IsNotExist(err) {
		return st, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, st); err != nil {
		return nil, fmt.Errorf("corrupt state file %s: %w", s.statePath(), err)
	}
	if st.Apps == nil {
		st.Apps = map[string]AppState{}
	}
	return st, nil
}

// Save writes state atomically (tmp + rename).
func (s *Store) Save(st *State) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.statePath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.statePath())
}

// SpoolOp is one operation handed from a CLI invocation to the daemon.
type SpoolOp struct {
	Op         string                  `json:"op"` // "import" | "approve" | "reject"
	AppID      string                  `json:"app_id,omitempty"`
	Definition *protocol.AppDefinition `json:"definition,omitempty"`
	QueuedAt   time.Time               `json:"queued_at"`
}

// Enqueue writes a spool op for the daemon to pick up.
func (s *Store) Enqueue(op SpoolOp) error {
	if err := os.MkdirAll(s.spoolDir(), 0o700); err != nil {
		return err
	}
	op.QueuedAt = time.Now().UTC()
	b, err := json.Marshal(op)
	if err != nil {
		return err
	}
	name := fmt.Sprintf("%d-%s.json", time.Now().UnixNano(), op.Op)
	tmp := filepath.Join(s.spoolDir(), "."+name)
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.spoolDir(), name))
}

// Drain reads and removes all queued spool ops in FIFO order. Hidden files
// (in-progress writes) are skipped.
func (s *Store) Drain() ([]SpoolOp, error) {
	entries, err := os.ReadDir(s.spoolDir())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names) // nanosecond prefix gives FIFO order
	var ops []SpoolOp
	for _, name := range names {
		path := filepath.Join(s.spoolDir(), name)
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var op SpoolOp
		if json.Unmarshal(b, &op) == nil {
			ops = append(ops, op)
		}
		_ = os.Remove(path)
	}
	return ops, nil
}
