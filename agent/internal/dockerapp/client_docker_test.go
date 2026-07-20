//go:build docker

// Integration tests against a real local Docker engine. Run with:
//
//	docker run -d --name bb-test-nginx nginx:alpine
//	go test -tags docker ./internal/dockerapp/
//	docker rm -f bb-test-nginx
package dockerapp

import (
	"testing"
	"time"
)

const testContainer = "bb-test-nginx"

func mustClient(t *testing.T) *Client {
	t.Helper()
	c, err := New()
	if err != nil {
		t.Skipf("no docker engine: %v", err)
	}
	return c
}

func TestLifecycle(t *testing.T) {
	c := mustClient(t)

	cs, err := c.Inspect(testContainer)
	if err != nil {
		t.Fatalf("inspect (did you create %s?): %v", testContainer, err)
	}
	t.Logf("initial: running=%v pid=%d name=%s", cs.Running, cs.PID, cs.Name)

	if err := c.Stop(testContainer, 5); err != nil {
		t.Fatalf("stop: %v", err)
	}
	cs, _ = c.Inspect(testContainer)
	if cs.Running {
		t.Fatal("container still running after Stop")
	}

	if err := c.Start(testContainer); err != nil {
		t.Fatalf("start: %v", err)
	}
	cs, _ = c.Inspect(testContainer)
	if !cs.Running || cs.PID == 0 {
		t.Fatalf("container not running after Start: %+v", cs)
	}

	// Idempotency: starting a running container is a no-op (engine 304).
	if err := c.Start(testContainer); err != nil {
		t.Fatalf("second start not idempotent: %v", err)
	}

	if err := c.Restart(testContainer, 5); err != nil {
		t.Fatalf("restart: %v", err)
	}
	cs, _ = c.Inspect(testContainer)
	if !cs.Running {
		t.Fatal("container not running after Restart")
	}
}

func TestStatsAndLogs(t *testing.T) {
	c := mustClient(t)
	if cs, err := c.Inspect(testContainer); err != nil || !cs.Running {
		_ = c.Start(testContainer)
		time.Sleep(time.Second)
	}

	s, err := c.Stats(testContainer)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if s.MemRSS == 0 {
		t.Error("stats returned zero memory for a running nginx")
	}
	t.Logf("stats: cpu=%.2f%% mem=%d bytes", s.CPUPct, s.MemRSS)

	lines, err := c.Logs(testContainer, 50)
	if err != nil {
		t.Fatalf("logs: %v", err)
	}
	if len(lines) == 0 {
		t.Error("expected some nginx startup log lines")
	}
	t.Logf("logs: %d lines, first: %.80s", len(lines), lines[0])
}
