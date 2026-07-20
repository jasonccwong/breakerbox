// Package daemon is the agent's long-running core: it maintains the hub
// connection, reconciles app state, executes app-scoped commands through the
// supervisor, samples metrics, and consumes CLI spool operations.
package daemon

import (
	"context"
	"crypto/ed25519"
	"log/slog"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/breakerbox/breakerbox/agent/internal/appconfig"
	"github.com/breakerbox/breakerbox/agent/internal/collector"
	"github.com/breakerbox/breakerbox/agent/internal/dockerapp"
	"github.com/breakerbox/breakerbox/agent/internal/supervisor"
	"github.com/breakerbox/breakerbox/agent/internal/tokenproxy"
	"github.com/breakerbox/breakerbox/agent/internal/tokenwatch"
	"github.com/breakerbox/breakerbox/agent/internal/transport"
	"github.com/breakerbox/breakerbox/pkg/protocol"
)

const (
	metricsInterval = 30 * time.Second
	spoolInterval   = 2 * time.Second
)

// Daemon runs the agent.
type Daemon struct {
	Version string

	store *appconfig.Store
	state *appconfig.State
	priv  ed25519.PrivateKey
	col   *collector.Collector

	docker *dockerapp.Client // nil when no engine socket on this host

	mu           sync.Mutex
	procs        map[string]*supervisor.Proc  // app ID -> running process
	conn         *transport.Conn              // current connection (nil when offline)
	startTimes   map[string]time.Time         // app ID -> last successful start
	restarts     map[string]*restartState     // app ID -> crash-restart bookkeeping
	metricsBuf   []protocol.MetricsBatch       // buffered while offline, bounded
	dockerStatus map[string]protocol.AppStatus // container app ID -> last seen status
	logStreams   map[string]context.CancelFunc // stream ID -> stop that stream
	proxy        *tokenproxy.Proxy             // nil when the proxy failed to start
	proxyRowsBuf []protocol.TokenUsageRow      // buffered while offline, bounded

	// preApproved holds definition hashes imported locally via
	// `apps import` whose hub-assigned IDs haven't arrived yet. When the
	// corresponding app_sync entry shows up it is approved immediately —
	// the import on this machine WAS the host-side approval.
	preApproved []string
}

// New creates a daemon bound to a state dir.
func New(store *appconfig.Store, priv ed25519.PrivateKey, version string) (*Daemon, error) {
	st, err := store.Load()
	if err != nil {
		return nil, err
	}
	docker, derr := dockerapp.New()
	if derr != nil {
		slog.Info("docker capability absent", "reason", derr)
	}
	return &Daemon{
		Version:      version,
		store:        store,
		state:        st,
		priv:         priv,
		col:          collector.New(),
		docker:       docker,
		procs:        map[string]*supervisor.Proc{},
		startTimes:   map[string]time.Time{},
		restarts:     map[string]*restartState{},
		dockerStatus: map[string]protocol.AppStatus{},
	}, nil
}

// Run blocks until ctx is canceled. It owns the reconnect loop.
func (d *Daemon) Run(ctx context.Context) error {
	if d.state.SystemID == "" || d.state.HubURL == "" {
		slog.Error("agent is not enrolled; run: breakerbox-agent enroll --hub URL --token TOKEN")
		os.Exit(1)
	}

	// Reap orphans from a previous agent run (children survive an agent
	// crash on unix), then resurrect apps whose desired state is running.
	for id, app := range d.state.Apps {
		if app.LastPID > 0 && !isDockerKind(app.Definition.Kind) {
			if supervisor.ReapOrphan(app.LastPID, app.Definition.Cmd) {
				slog.Info("reaped orphaned process tree from previous run", "app", id, "pid", app.LastPID)
			}
			app.LastPID, app.LastCmdBase = 0, ""
			d.state.Apps[id] = app
		}
	}
	_ = d.store.Save(d.state)
	for id, app := range d.state.Apps {
		if app.DesiredState == protocol.DesiredRunning && app.Approval == protocol.ApprovalApproved {
			if err := d.startApp(id); err != nil {
				slog.Error("resurrect app", "app", id, "err", err)
			}
		}
	}

	go d.metricsLoop(ctx)
	go d.spoolLoop(ctx)
	go d.dockerLoop(ctx)
	go tokenwatch.New(d.store.Dir, d.resolveAppByCwd, d.emitTokenRows).Run(ctx)

	// Runtime metering proxy: failure is non-fatal (apps with token_tracking
	// = runtime just run unmetered, with a warning).
	if proxy, err := tokenproxy.Start(d.queueProxyRows); err != nil {
		slog.Warn("runtime token proxy failed to start; runtime metering disabled", "err", err)
	} else {
		d.mu.Lock()
		d.proxy = proxy
		d.mu.Unlock()
		defer proxy.Stop()
	}

	var backoff transport.Backoff
	for ctx.Err() == nil {
		conn, err := transport.Dial(ctx, d.state.HubURL, d.state.SystemID, d.priv)
		if err != nil {
			delay := backoff.Next()
			slog.Warn("hub connection failed; retrying", "err", err, "retry_in", delay.Round(time.Second))
			select {
			case <-time.After(delay):
				continue
			case <-ctx.Done():
				return nil
			}
		}
		slog.Info("connected to hub", "hub", d.state.HubURL)
		backoff.Reset()

		d.mu.Lock()
		d.conn = conn
		d.mu.Unlock()

		d.sendHello(conn)
		d.consume(ctx, conn)

		d.mu.Lock()
		d.conn = nil
		d.mu.Unlock()
		d.cancelAllLogStreams()
		slog.Warn("hub connection lost")
	}
	return nil
}

// send delivers a frame on the current connection, if any.
func (d *Daemon) send(t string, payload any) {
	d.mu.Lock()
	conn := d.conn
	d.mu.Unlock()
	if conn != nil {
		if err := conn.Send(t, payload); err != nil {
			slog.Debug("send failed", "type", t, "err", err)
		}
	}
}

func (d *Daemon) sendHello(conn *transport.Conn) {
	hostname, _ := os.Hostname()
	digest := map[string]protocol.AppDigest{}
	d.mu.Lock()
	for id, app := range d.state.Apps {
		dg := protocol.AppDigest{
			Status:         protocol.StatusStopped,
			DefinitionHash: app.Hash,
			Approval:       app.Approval,
		}
		if p, ok := d.procs[id]; ok && p.Alive() {
			dg.Status = protocol.StatusRunning
			dg.PID = p.PID()
		}
		digest[id] = dg
	}
	d.mu.Unlock()

	caps := []string{"tokenwatch"}
	if d.docker != nil {
		caps = append(caps, "docker")
		if dockerapp.ComposeAvailable() {
			caps = append(caps, "compose")
		}
	}
	_ = conn.Send(protocol.TypeHello, protocol.Hello{
		AgentVersion:    d.Version,
		ProtocolVersion: protocol.Version,
		OS:              runtime.GOOS,
		Arch:            runtime.GOARCH,
		Hostname:        hostname,
		Capabilities:    caps,
		Apps:            digest,
	})
}

// consume processes hub frames until the connection drops.
func (d *Daemon) consume(ctx context.Context, conn *transport.Conn) {
	for {
		select {
		case env, ok := <-conn.Frames:
			if !ok {
				return
			}
			d.handleFrame(env)
		case <-ctx.Done():
			conn.Close()
			return
		}
	}
}

func (d *Daemon) handleFrame(env protocol.Envelope) {
	switch env.T {
	case protocol.TypeHelloAck:
		// Nothing to do; app_sync follows.
	case protocol.TypeAppSync:
		var sync protocol.AppSync
		if unmarshal(env.D, &sync) {
			d.applyAppSync(sync)
			d.reconcile()
		}
	case protocol.TypeCmd:
		var cmd protocol.Cmd
		if unmarshal(env.D, &cmd) {
			go d.execute(cmd)
		}
	case protocol.TypeLogFollow:
		var req protocol.LogFollow
		if unmarshal(env.D, &req) {
			d.startLogStream(req)
		}
	case protocol.TypeLogCancel:
		var req protocol.LogCancel
		if unmarshal(env.D, &req) {
			d.cancelLogStream(req.StreamID)
		}
	case protocol.TypePing:
		// WebSocket-level pong is handled by the library.
	default:
		slog.Debug("ignoring unknown frame", "type", env.T)
	}
}

// applyAppSync reconciles hub-authoritative definitions with local state.
// Approval is local-authoritative: a changed hash resets local approval to
// pending; an unchanged hash keeps the local decision.
func (d *Daemon) applyAppSync(sync protocol.AppSync) {
	d.mu.Lock()
	defer d.mu.Unlock()

	seen := map[string]bool{}
	changed := false
	for _, spec := range sync.Apps {
		seen[spec.ID] = true
		local, exists := d.state.Apps[spec.ID]
		if exists && local.Hash == spec.DefinitionHash {
			if local.DesiredState != spec.DesiredState || local.TokenTracking != spec.TokenTracking {
				local.DesiredState = spec.DesiredState
				// Note: a token_tracking change applies on next app start —
				// env vars cannot be injected into a running process.
				local.TokenTracking = spec.TokenTracking
				d.state.Apps[spec.ID] = local
				changed = true
			}
			continue
		}
		// New app or changed definition.
		approval := protocol.ApprovalPending
		if i := indexOf(d.preApproved, spec.DefinitionHash); i >= 0 {
			// Locally imported definition: the import was the approval.
			approval = protocol.ApprovalApproved
			d.preApproved = append(d.preApproved[:i], d.preApproved[i+1:]...)
		} else if isDockerKind(spec.Definition.Kind) {
			// Lifecycle verbs against a container the user already created
			// are inherently host-scoped: the hub can name a container but
			// cannot inject a command line, so approval is automatic.
			approval = protocol.ApprovalApproved
		} else if exists {
			slog.Info("definition changed; approval reset to pending", "app", spec.ID, "name", spec.Definition.Name)
		}
		d.state.Apps[spec.ID] = appconfig.AppState{
			Definition:    spec.Definition,
			Hash:          spec.DefinitionHash,
			Approval:      approval,
			DesiredState:  spec.DesiredState,
			TokenTracking: spec.TokenTracking,
		}
		changed = true
		go d.send(protocol.TypeApprovalEvent, protocol.ApprovalEvent{
			AppID: spec.ID, Approval: approval, DefinitionHash: spec.DefinitionHash,
		})
	}
	// Apps removed on the hub: stop and forget.
	for id := range d.state.Apps {
		if !seen[id] {
			if p, ok := d.procs[id]; ok {
				go func(p *supervisor.Proc) { _ = p.Stop() }(p)
				delete(d.procs, id)
			}
			if rs, ok := d.restarts[id]; ok {
				if rs.timer != nil {
					rs.timer.Stop()
				}
				delete(d.restarts, id)
			}
			delete(d.state.Apps, id)
			changed = true
		}
	}
	if changed {
		if err := d.store.Save(d.state); err != nil {
			slog.Error("persist state", "err", err)
		}
	}
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

// execute runs one hub command through the supervisor.
func (d *Daemon) execute(cmd protocol.Cmd) {
	d.send(protocol.TypeCmdAck, protocol.CmdAck{CmdID: cmd.CmdID})

	fail := func(msg string) {
		slog.Warn("command refused", "cmd", cmd.CmdID, "verb", cmd.Verb, "reason", msg)
		d.send(protocol.TypeCmdResult, protocol.CmdResult{CmdID: cmd.CmdID, OK: false, Error: msg})
	}

	d.mu.Lock()
	app, ok := d.state.Apps[cmd.AppID]
	d.mu.Unlock()
	if !ok {
		fail("unknown app on this host")
		return
	}
	// The security gate: hash must match AND the host must have approved.
	if app.Hash != cmd.DefinitionHash {
		fail("definition hash mismatch (stale command)")
		return
	}
	if app.Approval != protocol.ApprovalApproved {
		fail("app definition not approved on this host (run: breakerbox-agent apps approve " + cmd.AppID + ")")
		return
	}

	// Record intent locally so resurrect-on-boot and reconciliation agree
	// with the hub even if this connection drops right after execution.
	persistDesired := func(ds protocol.DesiredState) {
		d.mu.Lock()
		if a, ok := d.state.Apps[cmd.AppID]; ok && a.DesiredState != ds {
			a.DesiredState = ds
			d.state.Apps[cmd.AppID] = a
			_ = d.store.Save(d.state)
		}
		d.mu.Unlock()
	}

	var err error
	switch cmd.Verb {
	case protocol.VerbStart:
		persistDesired(protocol.DesiredRunning)
		err = d.startApp(cmd.AppID)
	case protocol.VerbStop:
		persistDesired(protocol.DesiredStopped)
		err = d.stopApp(cmd.AppID)
	case protocol.VerbRestart:
		persistDesired(protocol.DesiredRunning)
		if err = d.stopApp(cmd.AppID); err == nil {
			err = d.startApp(cmd.AppID)
		}
	default:
		fail("verb not in fixed set")
		return
	}
	if err != nil {
		fail(err.Error())
		return
	}
	d.send(protocol.TypeCmdResult, protocol.CmdResult{CmdID: cmd.CmdID, OK: true})
}

func unmarshal(data []byte, v any) bool {
	if err := jsonUnmarshal(data, v); err != nil {
		slog.Warn("bad frame payload", "err", err)
		return false
	}
	return true
}
