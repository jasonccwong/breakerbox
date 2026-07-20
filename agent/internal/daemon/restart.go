package daemon

import (
	"log/slog"
	"time"

	"github.com/breakerbox/breakerbox/pkg/protocol"
)

// Restart policy defaults (PM2 semantics).
const (
	defaultMaxRestarts = 15
	defaultMinUptime   = 1 * time.Second
	defaultBackoffMax  = 60 * time.Second
)

// restartState tracks crash-restart bookkeeping for one app.
type restartState struct {
	count int         // consecutive crashes (reset after a healthy run)
	timer *time.Timer // pending restart, if any
}

// policyFor resolves an app's restart policy with defaults applied.
func policyFor(def protocol.AppDefinition) (maxRestarts int, minUptime, backoffMax time.Duration) {
	maxRestarts, minUptime, backoffMax = defaultMaxRestarts, defaultMinUptime, defaultBackoffMax
	p := def.RestartPolicy
	if p == nil {
		return
	}
	// MaxRestarts 0 means "never auto-restart" only when the policy block is
	// explicitly present; JSON zero-value ambiguity is acceptable here because
	// an author writing restart_policy{} without max_restarts gets no-restart,
	// which is the conservative reading.
	maxRestarts = p.MaxRestarts
	if p.MinUptimeS > 0 {
		minUptime = time.Duration(p.MinUptimeS) * time.Second
	}
	if p.BackoffMaxS > 0 {
		backoffMax = time.Duration(p.BackoffMaxS) * time.Second
	}
	return
}

// handleUnexpectedExit decides what happens after an app's process tree died
// without a stop command: report stopped, or crash-restart per policy.
// Called from the proc watcher goroutine after the proc was removed from
// d.procs.
func (d *Daemon) handleUnexpectedExit(appID string, exitCode int) {
	d.mu.Lock()
	app, ok := d.state.Apps[appID]
	startedAt := d.startTimes[appID]
	delete(d.startTimes, appID)
	d.mu.Unlock()

	// Only crash-restart apps the user wants running.
	if !ok || app.DesiredState != protocol.DesiredRunning || app.Approval != protocol.ApprovalApproved {
		d.sendEvent(appID, protocol.StatusStopped, 0, &exitCode)
		return
	}

	maxRestarts, minUptime, backoffMax := policyFor(app.Definition)
	if maxRestarts <= 0 {
		d.sendEvent(appID, protocol.StatusStopped, 0, &exitCode)
		return
	}

	d.mu.Lock()
	rs := d.restarts[appID]
	if rs == nil {
		rs = &restartState{}
		d.restarts[appID] = rs
	}
	if !startedAt.IsZero() && time.Since(startedAt) >= minUptime {
		rs.count = 0 // it ran healthily before dying; fresh crash window
	}
	rs.count++
	count := rs.count
	d.mu.Unlock()

	if count > maxRestarts {
		slog.Error("app crash-looped past max_restarts; giving up",
			"app", appID, "restarts", count-1, "exit_code", exitCode)
		d.sendEventWithRestarts(appID, protocol.StatusErrored, 0, &exitCode, count-1)
		return
	}

	delay := time.Second << uint(min(count-1, 30))
	if delay > backoffMax || delay <= 0 {
		delay = backoffMax
	}
	slog.Warn("app exited unexpectedly; restarting",
		"app", appID, "exit_code", exitCode, "attempt", count, "of", maxRestarts, "in", delay)
	d.sendEventWithRestarts(appID, protocol.StatusBackoff, 0, &exitCode, count)

	d.mu.Lock()
	if rs.timer != nil {
		rs.timer.Stop()
	}
	rs.timer = time.AfterFunc(delay, func() {
		d.mu.Lock()
		app, ok := d.state.Apps[appID]
		stillWanted := ok && app.DesiredState == protocol.DesiredRunning && app.Approval == protocol.ApprovalApproved
		d.mu.Unlock()
		if !stillWanted {
			d.sendEvent(appID, protocol.StatusStopped, 0, nil)
			return
		}
		if err := d.startApp(appID); err != nil {
			// Start itself failed (bad binary, port taken...): loop back
			// through the same policy machinery as a crash at t=0.
			slog.Error("crash-restart failed", "app", appID, "err", err)
			d.handleUnexpectedExit(appID, -1)
		}
	})
	d.mu.Unlock()
}

// cancelRestart aborts any pending crash-restart and clears counters. Called
// on user-initiated stop and app removal.
func (d *Daemon) cancelRestart(appID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if rs, ok := d.restarts[appID]; ok {
		if rs.timer != nil {
			rs.timer.Stop()
		}
		delete(d.restarts, appID)
	}
}

// sendEventWithRestarts is sendEvent plus the restart counter.
func (d *Daemon) sendEventWithRestarts(appID string, status protocol.AppStatus, pid int, exitCode *int, restarts int) {
	d.send(protocol.TypeAppEvent, protocol.AppEvent{
		AppID: appID, Status: status, PID: pid, ExitCode: exitCode, RestartCount: restarts,
	})
}

// reconcile converges actual state to desired state for every approved app.
// Called after each app_sync (fresh connect and hub-side changes) — this is
// what heals missed commands after an offline stretch.
func (d *Daemon) reconcile() {
	type action struct {
		id    string
		start bool
	}
	var actions []action
	d.mu.Lock()
	for id, app := range d.state.Apps {
		if app.Approval != protocol.ApprovalApproved {
			continue
		}
		var alive bool
		if isDockerKind(app.Definition.Kind) {
			// dockerLoop keeps this current; empty on boot means "unknown",
			// and the resulting start is idempotent at the engine.
			alive = d.dockerStatus[id] == protocol.StatusRunning
		} else {
			p, running := d.procs[id]
			alive = running && p.Alive()
		}
		switch {
		case app.DesiredState == protocol.DesiredRunning && !alive:
			// Don't fight an in-flight backoff timer.
			if rs := d.restarts[id]; rs == nil || rs.timer == nil {
				actions = append(actions, action{id, true})
			}
		case app.DesiredState == protocol.DesiredStopped && alive:
			actions = append(actions, action{id, false})
		}
	}
	d.mu.Unlock()

	for _, a := range actions {
		go func(a action) {
			var err error
			if a.start {
				slog.Info("reconcile: starting app", "app", a.id)
				err = d.startApp(a.id)
			} else {
				slog.Info("reconcile: stopping app", "app", a.id)
				err = d.stopApp(a.id)
			}
			if err != nil {
				slog.Error("reconcile action failed", "app", a.id, "start", a.start, "err", err)
			}
		}(a)
	}
}
