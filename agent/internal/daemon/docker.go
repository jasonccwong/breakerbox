package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/breakerbox/breakerbox/agent/internal/dockerapp"
	"github.com/breakerbox/breakerbox/pkg/protocol"
)

const dockerPollInterval = 15 * time.Second

// isDockerKind reports whether an app is container-backed.
func isDockerKind(k protocol.AppKind) bool {
	return k == protocol.KindDocker || k == protocol.KindCompose
}

// stopTimeoutS resolves the graceful-stop timeout for container kinds.
func stopTimeoutS(def protocol.AppDefinition) int {
	if def.Stop != nil && def.Stop.TimeoutS > 0 {
		return def.Stop.TimeoutS
	}
	return 10
}

// startDockerApp handles VerbStart for container kinds.
func (d *Daemon) startDockerApp(appID string, def protocol.AppDefinition) error {
	if d.docker == nil {
		return fmt.Errorf("docker is not available on this host")
	}
	switch def.Kind {
	case protocol.KindDocker:
		if err := d.docker.Start(def.ContainerID); err != nil {
			d.sendEvent(appID, protocol.StatusErrored, 0, nil)
			return err
		}
		cs, err := d.docker.Inspect(def.ContainerID)
		pid := 0
		if err == nil {
			pid = cs.PID
		}
		d.setDockerStatus(appID, protocol.StatusRunning)
		d.sendEvent(appID, protocol.StatusRunning, pid, nil)
	case protocol.KindCompose:
		if err := dockerapp.ComposeStart(def.ComposeProject); err != nil {
			d.sendEvent(appID, protocol.StatusErrored, 0, nil)
			return err
		}
		d.setDockerStatus(appID, protocol.StatusRunning)
		d.sendEvent(appID, protocol.StatusRunning, 0, nil)
	}
	slog.Info("container app started", "app", appID, "name", def.Name, "kind", def.Kind)
	return nil
}

// stopDockerApp handles VerbStop for container kinds.
func (d *Daemon) stopDockerApp(appID string, def protocol.AppDefinition) error {
	if d.docker == nil {
		return fmt.Errorf("docker is not available on this host")
	}
	var err error
	switch def.Kind {
	case protocol.KindDocker:
		err = d.docker.Stop(def.ContainerID, stopTimeoutS(def))
	case protocol.KindCompose:
		err = dockerapp.ComposeStop(def.ComposeProject)
	}
	if err != nil {
		d.sendEvent(appID, protocol.StatusErrored, 0, nil)
		return err
	}
	d.setDockerStatus(appID, protocol.StatusStopped)
	d.sendEvent(appID, protocol.StatusStopped, 0, nil)
	return nil
}

func (d *Daemon) setDockerStatus(appID string, s protocol.AppStatus) {
	d.mu.Lock()
	d.dockerStatus[appID] = s
	d.mu.Unlock()
}

// dockerLoop polls container-backed apps and reports status transitions the
// engine caused (crash, external docker stop, restart policy...). Crash
// recovery for containers belongs to the engine's own restart policies, so
// this loop observes rather than supervises.
func (d *Daemon) dockerLoop(ctx context.Context) {
	if d.docker == nil {
		return
	}
	tick := time.NewTicker(dockerPollInterval)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			d.pollDockerApps()
		case <-ctx.Done():
			return
		}
	}
}

func (d *Daemon) pollDockerApps() {
	type target struct {
		id  string
		def protocol.AppDefinition
	}
	var targets []target
	d.mu.Lock()
	for id, app := range d.state.Apps {
		if isDockerKind(app.Definition.Kind) {
			targets = append(targets, target{id, app.Definition})
		}
	}
	d.mu.Unlock()

	for _, t := range targets {
		status := protocol.StatusUnknown
		pid := 0
		var exitCode *int
		switch t.def.Kind {
		case protocol.KindDocker:
			cs, err := d.docker.Inspect(t.def.ContainerID)
			if err != nil {
				status = protocol.StatusErrored
			} else if cs.Running {
				status, pid = protocol.StatusRunning, cs.PID
			} else {
				status = protocol.StatusStopped
				ec := cs.ExitCode
				exitCode = &ec
			}
		case protocol.KindCompose:
			st, err := d.docker.ComposeStatus(t.def.ComposeProject)
			if err != nil {
				status = protocol.StatusErrored
			} else if st.Running > 0 {
				status = protocol.StatusRunning
			} else {
				status = protocol.StatusStopped
			}
		}

		d.mu.Lock()
		prev, known := d.dockerStatus[t.id]
		d.dockerStatus[t.id] = status
		d.mu.Unlock()
		if !known || prev != status {
			slog.Info("container app status change", "app", t.id, "from", prev, "to", status)
			d.sendEvent(t.id, status, pid, exitCode)
		}
	}
}

// dockerAppSamples collects one metrics sample per running container app.
// Stats reads block ~1s each on the engine, so they run concurrently.
func (d *Daemon) dockerAppSamples(now int64) []protocol.AppSample {
	if d.docker == nil {
		return nil
	}
	type target struct {
		id  string
		def protocol.AppDefinition
	}
	var targets []target
	d.mu.Lock()
	for id, app := range d.state.Apps {
		if isDockerKind(app.Definition.Kind) && d.dockerStatus[id] == protocol.StatusRunning {
			targets = append(targets, target{id, app.Definition})
		}
	}
	d.mu.Unlock()
	if len(targets) == 0 {
		return nil
	}

	var mu sync.Mutex
	var out []protocol.AppSample
	var wg sync.WaitGroup
	for _, t := range targets {
		wg.Add(1)
		go func(t target) {
			defer wg.Done()
			var ids []string
			switch t.def.Kind {
			case protocol.KindDocker:
				ids = []string{t.def.ContainerID}
			case protocol.KindCompose:
				if st, err := d.docker.ComposeStatus(t.def.ComposeProject); err == nil {
					ids = st.ContainerIDs
				}
			}
			sample := protocol.AppSample{TS: now, AppID: t.id}
			got := false
			for _, cid := range ids {
				if s, err := d.docker.Stats(cid); err == nil {
					sample.CPUPct += s.CPUPct
					sample.MemRSS += s.MemRSS
					got = true
				}
			}
			if got {
				mu.Lock()
				out = append(out, sample)
				mu.Unlock()
			}
		}(t)
	}
	wg.Wait()
	return out
}
