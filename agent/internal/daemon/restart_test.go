package daemon

import (
	"crypto/ed25519"
	"runtime"
	"testing"
	"time"

	"github.com/breakerbox/breakerbox/agent/internal/appconfig"
	"github.com/breakerbox/breakerbox/pkg/protocol"
)

func newTestDaemon(t *testing.T) *Daemon {
	t.Helper()
	store := &appconfig.Store{Dir: t.TempDir()}
	_, priv, _ := ed25519.GenerateKey(nil)
	d, err := New(store, priv, "test")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func addApp(d *Daemon, id string, def protocol.AppDefinition, desired protocol.DesiredState) {
	d.mu.Lock()
	d.state.Apps[id] = appconfig.AppState{
		Definition: def, Hash: def.Hash(),
		Approval: protocol.ApprovalApproved, DesiredState: desired,
	}
	d.mu.Unlock()
}

// waitFor polls cond until true or the deadline passes.
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestPolicyFor(t *testing.T) {
	// nil policy -> defaults
	m, mu, bo := policyFor(protocol.AppDefinition{})
	if m != defaultMaxRestarts || mu != defaultMinUptime || bo != defaultBackoffMax {
		t.Errorf("nil policy: got (%d,%v,%v)", m, mu, bo)
	}
	// explicit values
	m, mu, bo = policyFor(protocol.AppDefinition{RestartPolicy: &protocol.RestartPolicy{
		MaxRestarts: 3, MinUptimeS: 5, BackoffMaxS: 7,
	}})
	if m != 3 || mu != 5*time.Second || bo != 7*time.Second {
		t.Errorf("explicit policy: got (%d,%v,%v)", m, mu, bo)
	}
	// explicit block with zero max_restarts -> never restart
	m, _, _ = policyFor(protocol.AppDefinition{RestartPolicy: &protocol.RestartPolicy{MinUptimeS: 1}})
	if m != 0 {
		t.Errorf("explicit block without max_restarts should mean 0, got %d", m)
	}
}

// TestCrashLoopGivesUp runs a command that exits 1 instantly and asserts the
// daemon retries per policy, then stops trying.
func TestCrashLoopGivesUp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	d := newTestDaemon(t)
	def := protocol.AppDefinition{
		SchemaVersion: 1, Name: "crasher", Kind: protocol.KindProcess,
		Cmd: "sh", Args: []string{"-c", "exit 1"},
		RestartPolicy: &protocol.RestartPolicy{MaxRestarts: 2, MinUptimeS: 30, BackoffMaxS: 1},
	}
	addApp(d, "a1", def, protocol.DesiredRunning)

	if err := d.startApp("a1"); err != nil {
		t.Fatal(err)
	}

	// With max_restarts=2 and ~1s backoff each, the daemon should give up
	// within a few seconds: counter passes the max and no proc remains.
	waitFor(t, 10*time.Second, "crash loop to give up", func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		rs := d.restarts["a1"]
		_, running := d.procs["a1"]
		return rs != nil && rs.count > 2 && !running
	})

	// And it must stay given-up: no timer pending.
	d.mu.Lock()
	rs := d.restarts["a1"]
	pending := rs.timer != nil && rs.timer.Stop() // Stop returns true if it was still armed
	d.mu.Unlock()
	if pending {
		t.Error("a restart timer was still pending after giving up")
	}
}

// TestStopCancelsRestart asserts a user stop during backoff wins: no
// resurrection afterwards.
func TestStopCancelsRestart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	d := newTestDaemon(t)
	def := protocol.AppDefinition{
		SchemaVersion: 1, Name: "crasher", Kind: protocol.KindProcess,
		Cmd: "sh", Args: []string{"-c", "exit 1"},
		// Long backoff so the app is parked in backoff when we stop it.
		RestartPolicy: &protocol.RestartPolicy{MaxRestarts: 10, MinUptimeS: 30, BackoffMaxS: 3600},
	}
	addApp(d, "a1", def, protocol.DesiredRunning)

	if err := d.startApp("a1"); err != nil {
		t.Fatal(err)
	}
	// Wait for the crash to be observed (restart state exists).
	waitFor(t, 5*time.Second, "first crash to register", func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.restarts["a1"] != nil && d.restarts["a1"].count >= 1
	})

	// User stops the app (and desired flips, as execute() would do).
	d.mu.Lock()
	a := d.state.Apps["a1"]
	a.DesiredState = protocol.DesiredStopped
	d.state.Apps["a1"] = a
	d.mu.Unlock()
	if err := d.stopApp("a1"); err != nil {
		t.Fatal(err)
	}

	d.mu.Lock()
	_, hasRS := d.restarts["a1"]
	_, running := d.procs["a1"]
	d.mu.Unlock()
	if hasRS || running {
		t.Errorf("after stop: restart state present=%v, proc present=%v; want neither", hasRS, running)
	}
}

// TestHealthyRunResetsCounter: an app that stays up past min_uptime gets its
// crash counter reset, so a later crash restarts from attempt 1.
func TestHealthyRunResetsCounter(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	d := newTestDaemon(t)
	def := protocol.AppDefinition{
		SchemaVersion: 1, Name: "flaky", Kind: protocol.KindProcess,
		// Lives 1.2s, exceeding the 1s default min_uptime -> "healthy" run.
		Cmd: "sh", Args: []string{"-c", "sleep 1.2; exit 1"},
		RestartPolicy: &protocol.RestartPolicy{MaxRestarts: 2, MinUptimeS: 0, BackoffMaxS: 1},
	}
	addApp(d, "a1", def, protocol.DesiredRunning)

	if err := d.startApp("a1"); err != nil {
		t.Fatal(err)
	}
	// Each cycle is ~2.2s (lifetime + backoff); over 8s the counter would
	// exceed max_restarts=2 if it never reset; because each run exceeds
	// min_uptime it must keep restarting with the counter pinned at 1.
	deadline := time.Now().Add(8 * time.Second)
	maxSeen := 0
	for time.Now().Before(deadline) {
		d.mu.Lock()
		if rs := d.restarts["a1"]; rs != nil && rs.count > maxSeen {
			maxSeen = rs.count
		}
		d.mu.Unlock()
		time.Sleep(50 * time.Millisecond)
	}
	if maxSeen > 2 {
		t.Errorf("counter reached %d despite healthy runs; reset is broken", maxSeen)
	}
	// It should still be alive-or-restarting, not given up.
	d.mu.Lock()
	rs := d.restarts["a1"]
	_, running := d.procs["a1"]
	pending := rs != nil && rs.timer != nil
	d.mu.Unlock()
	if !running && !pending {
		t.Error("app neither running nor scheduled to restart; gave up incorrectly")
	}
	_ = d.stopApp("a1") // cleanup
}
