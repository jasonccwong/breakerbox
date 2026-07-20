package daemon

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/breakerbox/breakerbox/agent/internal/supervisor"
	"github.com/breakerbox/breakerbox/pkg/protocol"
)

func jsonUnmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }

// startApp launches an approved app and watches for exit.
func (d *Daemon) startApp(appID string) error {
	d.mu.Lock()
	app, ok := d.state.Apps[appID]
	if !ok {
		d.mu.Unlock()
		return fmt.Errorf("unknown app %s", appID)
	}
	if p, running := d.procs[appID]; running && p.Alive() {
		d.mu.Unlock()
		return nil // already running: start is idempotent
	}
	d.mu.Unlock()

	if app.Approval != protocol.ApprovalApproved {
		return fmt.Errorf("app not approved on this host")
	}
	if app.Definition.Kind != "" && app.Definition.Kind != protocol.KindProcess {
		return fmt.Errorf("kind %q lands in Phase 2 (docker/compose)", app.Definition.Kind)
	}

	logFile, err := d.openLog(appID)
	if err != nil {
		return err
	}
	d.sendEvent(appID, protocol.StatusStarting, 0, nil)
	p, err := supervisor.StartProc(app.Definition, logFile)
	if err != nil {
		d.sendEvent(appID, protocol.StatusErrored, 0, nil)
		return err
	}

	d.mu.Lock()
	d.procs[appID] = p
	d.mu.Unlock()
	d.sendEvent(appID, protocol.StatusRunning, p.PID(), nil)
	slog.Info("app started", "app", appID, "name", app.Definition.Name, "pid", p.PID())

	go func() {
		<-p.Done()
		logFile.Close()
		code := p.ExitCode()
		d.mu.Lock()
		// Only report if this proc is still the current one (not superseded
		// by a restart).
		current := d.procs[appID] == p
		if current {
			delete(d.procs, appID)
		}
		d.mu.Unlock()
		if current {
			slog.Info("app exited", "app", appID, "exit_code", code)
			d.sendEvent(appID, protocol.StatusStopped, 0, &code)
		}
	}()
	return nil
}

// stopApp gracefully stops a running app (no-op when already stopped).
func (d *Daemon) stopApp(appID string) error {
	d.mu.Lock()
	p, ok := d.procs[appID]
	delete(d.procs, appID)
	d.mu.Unlock()
	if !ok {
		return nil
	}
	if err := p.Stop(); err != nil {
		d.sendEvent(appID, protocol.StatusErrored, 0, nil)
		return err
	}
	d.sendEvent(appID, protocol.StatusStopped, 0, nil)
	return nil
}

func (d *Daemon) sendEvent(appID string, status protocol.AppStatus, pid int, exitCode *int) {
	ev := protocol.AppEvent{AppID: appID, Status: status, PID: pid, ExitCode: exitCode}
	if status == protocol.StatusRunning && pid != 0 {
		// Attach currently known listen ports (may lag one cache refresh).
		ev.Ports = d.col.AppSample(appID, pid).Ports
	}
	d.send(protocol.TypeAppEvent, ev)
}

// openLog opens the app's rotating log sink (plain file in Phase 1; ring
// buffer + rotation in Phase 2).
func (d *Daemon) openLog(appID string) (*os.File, error) {
	dir := filepath.Join(d.store.Dir, "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(filepath.Join(dir, appID+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
}
