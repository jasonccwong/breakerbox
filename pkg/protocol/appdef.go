package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// AppDefSchemaVersion is the current breakerbox.app.json schema version.
// Bump only with a migration path; importers accept older versions.
const AppDefSchemaVersion = 1

// AppDefinition describes how to run, monitor, and stop one app. It is the
// payload of breakerbox.app.json (the AI-generated import format) and the
// `definition` field of the hub's apps collection.
type AppDefinition struct {
	SchemaVersion int               `json:"schema_version"`
	Name          string            `json:"name"`
	Kind          AppKind           `json:"kind"` // defaults to "process"
	Cmd           string            `json:"cmd,omitempty"`
	Args          []string          `json:"args,omitempty"`
	Cwd           string            `json:"cwd,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
	Ports         []int             `json:"ports,omitempty"` // expected listen ports (informational)
	HealthCheck   *HealthCheck      `json:"health_check,omitempty"`
	Stop          *StopSpec         `json:"stop,omitempty"`
	RestartPolicy *RestartPolicy    `json:"restart_policy,omitempty"`

	// Docker/Compose kinds.
	ContainerID    string `json:"container_id,omitempty"`
	ComposeProject string `json:"compose_project,omitempty"`
	ComposeFile    string `json:"compose_file,omitempty"`
}

// AppKind selects the lifecycle backend.
type AppKind string

const (
	KindProcess AppKind = "process"
	KindDocker  AppKind = "docker"
	KindCompose AppKind = "compose"
)

// HealthCheck is an optional HTTP liveness probe.
type HealthCheck struct {
	URL      string `json:"url"`
	TimeoutS int    `json:"timeout_s,omitempty"` // default 5
}

// StopSpec describes graceful shutdown. If Command is set it runs first; then
// the supervisor signals the process group (Signal, default SIGTERM) and
// escalates to SIGKILL after TimeoutS.
type StopSpec struct {
	Command  string `json:"command,omitempty"`
	Signal   string `json:"signal,omitempty"`    // "SIGTERM" (default) | "SIGINT" | ...
	TimeoutS int    `json:"timeout_s,omitempty"` // default 10
}

// RestartPolicy mirrors PM2 semantics.
type RestartPolicy struct {
	MaxRestarts int `json:"max_restarts,omitempty"` // default 15, 0 = never restart
	MinUptimeS  int `json:"min_uptime_s,omitempty"` // default 1; exits faster than this count as crashes
	BackoffMaxS int `json:"backoff_max_s,omitempty"`// default 60; exponential backoff cap
}

// Validate checks an imported definition. It returns every problem found so
// the UI/CLI can show them all at once.
func (d *AppDefinition) Validate() error {
	var errs []string
	if d.SchemaVersion < 1 || d.SchemaVersion > AppDefSchemaVersion {
		errs = append(errs, fmt.Sprintf("schema_version must be 1..%d, got %d", AppDefSchemaVersion, d.SchemaVersion))
	}
	if strings.TrimSpace(d.Name) == "" {
		errs = append(errs, "name is required")
	}
	kind := d.Kind
	if kind == "" {
		kind = KindProcess
	}
	switch kind {
	case KindProcess:
		if strings.TrimSpace(d.Cmd) == "" {
			errs = append(errs, "cmd is required for kind=process")
		}
	case KindDocker:
		if d.ContainerID == "" {
			errs = append(errs, "container_id is required for kind=docker")
		}
	case KindCompose:
		if d.ComposeProject == "" {
			errs = append(errs, "compose_project is required for kind=compose")
		}
	default:
		errs = append(errs, fmt.Sprintf("unknown kind %q", d.Kind))
	}
	for _, p := range d.Ports {
		if p < 1 || p > 65535 {
			errs = append(errs, fmt.Sprintf("port %d out of range", p))
		}
	}
	if hc := d.HealthCheck; hc != nil && !strings.HasPrefix(hc.URL, "http") {
		errs = append(errs, "health_check.url must be http(s)")
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// Hash returns the canonical definition hash used for host-side approval
// pinning. It hashes the canonical JSON encoding (Go maps marshal with sorted
// keys, giving a stable byte stream).
func (d *AppDefinition) Hash() string {
	b, _ := json.Marshal(d)
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}
