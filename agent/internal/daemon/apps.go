package daemon

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

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
	if isDockerKind(app.Definition.Kind) {
		return d.startDockerApp(appID, app.Definition)
	}

	logFile, err := d.openLog(appID)
	if err != nil {
		return err
	}
	d.sendEvent(appID, protocol.StatusStarting, 0, nil)
	// Opt-in runtime metering: point provider SDKs at the local proxy.
	var extraEnv []string
	if app.TokenTracking == "runtime" {
		d.mu.Lock()
		proxy := d.proxy
		d.mu.Unlock()
		if proxy != nil {
			extraEnv = proxy.EnvFor(appID)
		} else {
			slog.Warn("app wants runtime token metering but the proxy is not running", "app", appID)
		}
	}
	p, err := supervisor.StartProc(app.Definition, logFile, extraEnv)
	if err != nil {
		d.sendEvent(appID, protocol.StatusErrored, 0, nil)
		return err
	}

	d.mu.Lock()
	d.procs[appID] = p
	d.startTimes[appID] = time.Now()
	// Remember the tree so a restarted agent can reap orphans (see Run).
	if a, ok := d.state.Apps[appID]; ok {
		a.LastPID = p.PID()
		a.LastCmdBase = app.Definition.Cmd
		d.state.Apps[appID] = a
		_ = d.store.Save(d.state)
	}
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
			d.handleUnexpectedExit(appID, code)
		}
	}()
	return nil
}

// stopApp gracefully stops a running app (no-op when already stopped). Any
// pending crash-restart is canceled first — a user stop always wins.
func (d *Daemon) stopApp(appID string) error {
	d.cancelRestart(appID)
	d.mu.Lock()
	app, known := d.state.Apps[appID]
	p, ok := d.procs[appID]
	delete(d.procs, appID)
	delete(d.startTimes, appID)
	d.mu.Unlock()
	if known && isDockerKind(app.Definition.Kind) {
		return d.stopDockerApp(appID, app.Definition)
	}
	if !ok {
		return nil
	}
	if err := p.Stop(); err != nil {
		d.sendEvent(appID, protocol.StatusErrored, 0, nil)
		return err
	}
	d.mu.Lock()
	if a, ok := d.state.Apps[appID]; ok && a.LastPID != 0 {
		a.LastPID, a.LastCmdBase = 0, ""
		d.state.Apps[appID] = a
		_ = d.store.Save(d.state)
	}
	d.mu.Unlock()
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

// logPath returns the log file for a process app.
func (d *Daemon) logPath(appID string) string {
	return filepath.Join(d.store.Dir, "logs", appID+".log")
}

// openLog opens the app's log sink, rotating first when it has grown past the
// cap (one .1 generation kept).
func (d *Daemon) openLog(appID string) (*os.File, error) {
	path := d.logPath(appID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	rotateLogIfNeeded(path)
	return os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
}
