package agenthub

// Spike A (Phase 0): prove that PocketBase-as-framework can carry the custom
// agent WebSocket plane and sustained metric write volume simultaneously.
// These tests boot a real PB app (temp dir), serve the real WS route, and
// drive it with a fake in-test agent using real Ed25519 auth.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"

	"github.com/breakerbox/breakerbox/pkg/protocol"
	_ "github.com/breakerbox/breakerbox/hub/migrations"
)

// testHub boots a fresh PB app with migrations applied and the agenthub
// routes served on a random localhost port.
func testHub(t *testing.T) (*tests.TestApp, *Hub, string) {
	t.Helper()

	app, err := tests.NewTestAppWithConfig(core.BaseAppConfig{
		DataDir:       t.TempDir(),
		EncryptionEnv: "pb_test_env",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Cleanup)

	h := Register(app)

	port := freePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	go func() {
		// Serve blocks until the app is cleaned up; error is expected then.
		_ = apis.Serve(app, apis.ServeConfig{HttpAddr: addr, ShowStartBanner: false})
	}()
	waitForHTTP(t, "http://"+addr+"/api/bb/health")
	return app, h, addr
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

func waitForHTTP(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("server at %s did not become ready", url)
}

// makeSystem inserts a systems record with the given Ed25519 public key.
func makeSystem(t *testing.T, app core.App, pub ed25519.PublicKey, name string) *core.Record {
	t.Helper()
	col, err := app.FindCollectionByNameOrId("systems")
	if err != nil {
		t.Fatal(err)
	}
	rec := core.NewRecord(col)
	rec.Set("name", name)
	rec.Set("public_key", base64.StdEncoding.EncodeToString(pub))
	rec.Set("status", "offline")
	if err := app.Save(rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

// fakeAgent is an in-test agent speaking the real protocol over the real route.
type fakeAgent struct {
	ws     *websocket.Conn
	frames chan protocol.Envelope
	ctx    context.Context
	cancel context.CancelFunc
}

func dialAgent(t *testing.T, addr, systemID string, priv ed25519.PrivateKey) *fakeAgent {
	t.Helper()
	ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
	sig := ed25519.Sign(priv, []byte(systemID+"|"+ts))
	hdr := http.Header{}
	hdr.Set("X-System-Id", systemID)
	hdr.Set("X-Timestamp", ts)
	hdr.Set("X-Signature", base64.StdEncoding.EncodeToString(sig))

	ctx, cancel := context.WithCancel(context.Background())
	ws, _, err := websocket.Dial(ctx, "ws://"+addr+"/api/bb/agent/ws", &websocket.DialOptions{HTTPHeader: hdr})
	if err != nil {
		cancel()
		t.Fatalf("agent dial failed: %v", err)
	}
	a := &fakeAgent{ws: ws, frames: make(chan protocol.Envelope, 64), ctx: ctx, cancel: cancel}
	go func() {
		for {
			_, data, err := ws.Read(ctx)
			if err != nil {
				close(a.frames)
				return
			}
			var env protocol.Envelope
			if json.Unmarshal(data, &env) == nil {
				a.frames <- env
			}
		}
	}()
	t.Cleanup(func() { a.cancel(); _ = ws.CloseNow() })
	return a
}

func (a *fakeAgent) send(t *testing.T, typ string, payload any) {
	t.Helper()
	d, _ := json.Marshal(payload)
	b, _ := json.Marshal(protocol.Envelope{V: protocol.Version, T: typ, TS: time.Now().UnixMilli(), D: d})
	if err := a.ws.Write(a.ctx, websocket.MessageText, b); err != nil {
		t.Fatalf("agent send %s: %v", typ, err)
	}
}

// expect waits for the next frame of the given type, skipping others.
func (a *fakeAgent) expect(t *testing.T, typ string, timeout time.Duration) protocol.Envelope {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case env, ok := <-a.frames:
			if !ok {
				t.Fatalf("connection closed while waiting for %s", typ)
			}
			if env.T == typ {
				return env
			}
		case <-deadline:
			t.Fatalf("timed out waiting for frame type %s", typ)
		}
	}
}

func TestAgentWSAuth(t *testing.T) {
	app, _, addr := testHub(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	system := makeSystem(t, app, pub, "auth-test")

	dial := func(sysID, ts, sig string) int {
		hdr := http.Header{}
		hdr.Set("X-System-Id", sysID)
		hdr.Set("X-Timestamp", ts)
		hdr.Set("X-Signature", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		ws, resp, err := websocket.Dial(ctx, "ws://"+addr+"/api/bb/agent/ws", &websocket.DialOptions{HTTPHeader: hdr})
		if err == nil {
			ws.CloseNow()
			return http.StatusSwitchingProtocols
		}
		if resp != nil {
			return resp.StatusCode
		}
		return 0
	}

	now := strconv.FormatInt(time.Now().UnixMilli(), 10)
	goodSig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(system.Id+"|"+now)))

	if code := dial(system.Id, now, goodSig); code != http.StatusSwitchingProtocols {
		t.Errorf("valid signature rejected: status %d", code)
	}
	if code := dial(system.Id, now, base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))); code != http.StatusUnauthorized {
		t.Errorf("forged signature accepted: status %d", code)
	}
	if code := dial("nonexistent-system", now, goodSig); code != http.StatusUnauthorized {
		t.Errorf("unknown system accepted: status %d", code)
	}
	stale := strconv.FormatInt(time.Now().Add(-10*time.Minute).UnixMilli(), 10)
	staleSig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(system.Id+"|"+stale)))
	if code := dial(system.Id, stale, staleSig); code != http.StatusUnauthorized {
		t.Errorf("replayed stale timestamp accepted: status %d", code)
	}
}

func TestCommandRoundTrip(t *testing.T) {
	app, _, addr := testHub(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	system := makeSystem(t, app, pub, "roundtrip")

	agent := dialAgent(t, addr, system.Id, priv)
	agent.send(t, protocol.TypeHello, protocol.Hello{
		AgentVersion: "test", ProtocolVersion: protocol.Version,
		OS: "darwin", Arch: "arm64", Hostname: "spike",
	})
	agent.expect(t, protocol.TypeHelloAck, 5*time.Second)
	agent.expect(t, protocol.TypeAppSync, 5*time.Second)

	// System should now be online.
	sysRec, _ := app.FindRecordById("systems", system.Id)
	if got := sysRec.GetString("status"); got != "online" {
		t.Errorf("system status after hello = %q, want online", got)
	}

	// Register an app, then create a command record — the hub hook must
	// dispatch it to the connected agent.
	appsCol, _ := app.FindCollectionByNameOrId("apps")
	appRec := core.NewRecord(appsCol)
	appRec.Set("system", system.Id)
	appRec.Set("name", "demo")
	appRec.Set("kind", "process")
	appRec.Set("definition", `{"schema_version":1,"name":"demo","cmd":"sleep","args":["60"]}`)
	appRec.Set("definition_hash", "sha256:test")
	appRec.Set("approval", "approved")
	appRec.Set("desired_state", "stopped")
	appRec.Set("status", "stopped")
	if err := app.Save(appRec); err != nil {
		t.Fatal(err)
	}

	cmdCol, _ := app.FindCollectionByNameOrId("commands")
	cmdRec := core.NewRecord(cmdCol)
	cmdRec.Set("app", appRec.Id)
	cmdRec.Set("system", system.Id)
	cmdRec.Set("verb", "start")
	cmdRec.Set("status", "pending")
	if err := app.Save(cmdRec); err != nil {
		t.Fatal(err)
	}

	env := agent.expect(t, protocol.TypeCmd, 5*time.Second)
	var cmd protocol.Cmd
	if err := json.Unmarshal(env.D, &cmd); err != nil {
		t.Fatal(err)
	}
	if cmd.AppID != appRec.Id || cmd.Verb != protocol.VerbStart || cmd.DefinitionHash != "sha256:test" {
		t.Errorf("dispatched cmd = %+v", cmd)
	}

	// Ack + result; the commands record must reach status=done.
	agent.send(t, protocol.TypeCmdAck, protocol.CmdAck{CmdID: cmd.CmdID})
	agent.send(t, protocol.TypeCmdResult, protocol.CmdResult{CmdID: cmd.CmdID, OK: true})

	waitFor(t, 5*time.Second, func() bool {
		rec, err := app.FindRecordById("commands", cmd.CmdID)
		return err == nil && rec.GetString("status") == "done"
	}, "command record to reach status=done")

	// Status transition via app_event.
	agent.send(t, protocol.TypeAppEvent, protocol.AppEvent{
		AppID: appRec.Id, Status: protocol.StatusRunning, PID: 4242,
		Ports: []protocol.Port{{Proto: "tcp", Port: 3000}},
	})
	waitFor(t, 5*time.Second, func() bool {
		rec, err := app.FindRecordById("apps", appRec.Id)
		return err == nil && rec.GetString("status") == "running" && rec.GetFloat("pid") == 4242
	}, "apps record to reflect running status")
}

// TestMetricsWriteLoad simulates 50 systems reporting one 30s collection
// cycle (1 host + 3 app samples each) and asserts (a) ingest keeps up well
// under real-time pace and (b) a command round-trip stays fast while a
// sustained ingest storm is running — the load/latency proxy for "PB realtime
// does not degrade under metric writes".
func TestMetricsWriteLoad(t *testing.T) {
	app, h, addr := testHub(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)

	const nSystems = 50
	systems := make([]*core.Record, nSystems)
	appsCol, _ := app.FindCollectionByNameOrId("apps")
	appIDs := make([][]string, nSystems)
	for i := range systems {
		systems[i] = makeSystem(t, app, pub, fmt.Sprintf("load-%d", i))
		for j := 0; j < 3; j++ {
			rec := core.NewRecord(appsCol)
			rec.Set("system", systems[i].Id)
			rec.Set("name", fmt.Sprintf("app-%d-%d", i, j))
			rec.Set("kind", "process")
			rec.Set("definition", `{"schema_version":1,"name":"x","cmd":"sleep"}`)
			rec.Set("definition_hash", "sha256:x")
			rec.Set("approval", "approved")
			rec.Set("desired_state", "running")
			rec.Set("status", "running")
			if err := app.Save(rec); err != nil {
				t.Fatal(err)
			}
			appIDs[i] = append(appIDs[i], rec.Id)
		}
	}

	batchFor := func(i int) protocol.MetricsBatch {
		now := time.Now().UnixMilli()
		b := protocol.MetricsBatch{
			Host: []protocol.HostSample{{TS: now, CPUPct: 10, MemPct: 50, MemUsed: 1 << 30, DiskPct: 60, NetSent: 1000, NetRecv: 2000}},
		}
		for _, id := range appIDs[i] {
			b.Apps = append(b.Apps, protocol.AppSample{TS: now, AppID: id, CPUPct: 1.5, MemRSS: 1 << 20})
		}
		return b
	}

	// (a) One full collection cycle for all 50 systems (200 rows) must land
	// far faster than the 30s collection interval.
	start := time.Now()
	for i := 0; i < nSystems; i++ {
		if err := h.ingestMetrics(systems[i].Id, batchFor(i)); err != nil {
			t.Fatal(err)
		}
	}
	cycleDur := time.Since(start)
	t.Logf("ingested one 50-system cycle (200 rows) in %s", cycleDur)
	if cycleDur > 5*time.Second {
		t.Errorf("ingest cycle took %s; 50-system scale is not viable on PB (spike FAIL, consider raw SQLite fallback)", cycleDur)
	}

	// (b) Command round-trip latency during a sustained ingest storm.
	system := systems[0]
	agent := dialAgent(t, addr, system.Id, priv)
	agent.send(t, protocol.TypeHello, protocol.Hello{AgentVersion: "test", ProtocolVersion: protocol.Version, OS: "darwin", Arch: "arm64", Hostname: "load"})
	agent.expect(t, protocol.TypeHelloAck, 5*time.Second)
	agent.expect(t, protocol.TypeAppSync, 5*time.Second)

	stop := make(chan struct{})
	storeErr := make(chan error, 1)
	go func() {
		for {
			select {
			case <-stop:
				storeErr <- nil
				return
			default:
			}
			for i := 0; i < nSystems; i++ {
				if err := h.ingestMetrics(systems[i].Id, batchFor(i)); err != nil {
					storeErr <- err
					return
				}
			}
		}
	}()

	cmdCol, _ := app.FindCollectionByNameOrId("commands")
	var worst time.Duration
	for round := 0; round < 5; round++ {
		cmdRec := core.NewRecord(cmdCol)
		cmdRec.Set("app", appIDs[0][0])
		cmdRec.Set("system", system.Id)
		cmdRec.Set("verb", "restart")
		cmdRec.Set("status", "pending")
		sent := time.Now()
		if err := app.Save(cmdRec); err != nil {
			t.Fatal(err)
		}
		env := agent.expect(t, protocol.TypeCmd, 10*time.Second)
		lat := time.Since(sent)
		if lat > worst {
			worst = lat
		}
		var cmd protocol.Cmd
		_ = json.Unmarshal(env.D, &cmd)
		agent.send(t, protocol.TypeCmdResult, protocol.CmdResult{CmdID: cmd.CmdID, OK: true})
	}
	close(stop)
	if err := <-storeErr; err != nil {
		t.Fatalf("ingest storm failed: %v", err)
	}
	t.Logf("worst command dispatch latency under ingest storm: %s", worst)
	if worst > 2*time.Second {
		t.Errorf("command latency %s under write load; control plane degraded (spike FAIL)", worst)
	}
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool, what string) {
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
