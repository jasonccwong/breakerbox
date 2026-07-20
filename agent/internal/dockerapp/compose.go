package dockerapp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Compose lifecycle goes through the docker CLI plugin because the Compose
// spec has no engine-level API. BreakerBox only manages projects that already
// exist (created by the user with `docker compose up` at least once); it
// never authors compose files (non-goal).

const composeTimeout = 90 * time.Second

// composeCmd runs `docker compose -p <project> args...`.
func composeCmd(project string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), composeTimeout)
	defer cancel()
	full := append([]string{"compose", "-p", project}, args...)
	cmd := exec.CommandContext(ctx, "docker", full...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("docker compose %s: %s", strings.Join(args, " "), msg)
	}
	return out.Bytes(), nil
}

// ComposeStart starts all services of an existing project.
func ComposeStart(project string) error {
	_, err := composeCmd(project, "start")
	if err != nil && strings.Contains(err.Error(), "no such service") {
		// Project exists but containers were removed: recreate from its
		// stored config is out of scope; surface a clear message.
		return fmt.Errorf("compose project %q has no containers; run `docker compose up -d` in its directory once, then BreakerBox can manage it", project)
	}
	return err
}

// ComposeStop stops all services of a project.
func ComposeStop(project string) error {
	_, err := composeCmd(project, "stop")
	return err
}

// ComposeRestart bounces all services of a project.
func ComposeRestart(project string) error {
	_, err := composeCmd(project, "restart")
	return err
}

// ComposeContainerIDs lists the container IDs of a project (running or not).
func ComposeContainerIDs(project string) ([]string, error) {
	out, err := composeCmd(project, "ps", "-a", "-q")
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			ids = append(ids, l)
		}
	}
	return ids, nil
}

// ComposeStatus aggregates a project's container states: running if any
// service runs.
type ComposeState struct {
	Running      int
	Total        int
	ContainerIDs []string
}

// Status inspects each project container through the engine API.
func (c *Client) ComposeStatus(project string) (ComposeState, error) {
	ids, err := ComposeContainerIDs(project)
	if err != nil {
		return ComposeState{}, err
	}
	st := ComposeState{Total: len(ids), ContainerIDs: ids}
	for _, id := range ids {
		cs, err := c.Inspect(id)
		if err != nil {
			continue
		}
		if cs.Running {
			st.Running++
		}
	}
	return st, nil
}

// ComposeAvailable reports whether the docker compose CLI plugin works.
func ComposeAvailable() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "docker", "compose", "version").Run() == nil
}

// ComposePS returns service name -> state for UI display.
func ComposePS(project string) (map[string]string, error) {
	out, err := composeCmd(project, "ps", "-a", "--format", "json")
	if err != nil {
		return nil, err
	}
	// Depending on version this is a JSON array or NDJSON lines.
	services := map[string]string{}
	trimmed := bytes.TrimSpace(out)
	if len(trimmed) == 0 {
		return services, nil
	}
	type row struct {
		Service string `json:"Service"`
		State   string `json:"State"`
	}
	if trimmed[0] == '[' {
		var rows []row
		if err := json.Unmarshal(trimmed, &rows); err != nil {
			return nil, err
		}
		for _, r := range rows {
			services[r.Service] = r.State
		}
		return services, nil
	}
	for _, line := range bytes.Split(trimmed, []byte("\n")) {
		var r row
		if json.Unmarshal(bytes.TrimSpace(line), &r) == nil && r.Service != "" {
			services[r.Service] = r.State
		}
	}
	return services, nil
}
