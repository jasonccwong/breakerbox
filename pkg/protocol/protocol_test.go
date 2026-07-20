package protocol

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

// TestGoldenWireFormat pins the JSON wire encoding of every message type.
// If this test fails after a change, you have changed the compatibility
// surface between hub and agent — either fix the change or consciously
// regenerate goldens with `go test -run Golden -update ./...` and bump the
// protocol notes in messages.go.
func TestGoldenWireFormat(t *testing.T) {
	exitCode := 137
	cases := map[string]any{
		"envelope": Envelope{V: Version, T: TypeCmd, ID: "01JTEST", TS: 1737300000123, D: json.RawMessage(`{"x":1}`)},
		"hello": Hello{
			AgentVersion: "0.1.0", ProtocolVersion: Version, OS: "darwin", Arch: "arm64",
			Hostname: "test-mac", Capabilities: []string{"docker"},
			Apps: map[string]AppDigest{"app1": {Status: StatusRunning, DefinitionHash: "sha256:abc", Approval: ApprovalApproved, PID: 42}},
		},
		"hello_ack": HelloAck{ServerTimeMS: 1737300000123, MinSupportedProtocol: 1},
		"app_sync": AppSync{Apps: []AppSpec{{
			ID: "app1",
			Definition: AppDefinition{
				SchemaVersion: 1, Name: "demo", Kind: KindProcess, Cmd: "npm",
				Args: []string{"run", "dev"}, Cwd: "/srv/demo",
				Env:   map[string]string{"PORT": "3000"},
				Ports: []int{3000},
				HealthCheck:   &HealthCheck{URL: "http://localhost:3000/health", TimeoutS: 5},
				Stop:          &StopSpec{Signal: "SIGTERM", TimeoutS: 10},
				RestartPolicy: &RestartPolicy{MaxRestarts: 15, MinUptimeS: 1, BackoffMaxS: 60},
			},
			DefinitionHash: "sha256:abc", DesiredState: DesiredRunning,
		}}},
		"cmd":        Cmd{CmdID: "c1", AppID: "app1", Verb: VerbRestart, DefinitionHash: "sha256:abc"},
		"cmd_ack":    CmdAck{CmdID: "c1"},
		"cmd_result": CmdResult{CmdID: "c1", OK: false, Error: "start failed", Detail: "exit status 1"},
		"app_event":  AppEvent{AppID: "app1", Status: StatusStopped, ExitCode: &exitCode, RestartCount: 3, Ports: []Port{{Proto: "tcp", Port: 3000}}},
		"metrics": MetricsBatch{
			Host: []HostSample{{TS: 1737300000123, CPUPct: 12.5, MemPct: 61.2, MemUsed: 10515804160, DiskPct: 71.0, NetSent: 123456, NetRecv: 654321}},
			Apps: []AppSample{{TS: 1737300000123, AppID: "app1", CPUPct: 3.2, MemRSS: 104857600, Ports: []Port{{Proto: "tcp", Port: 3000}}}},
		},
		"log_follow": LogFollow{StreamID: "s1", AppID: "app1", TailN: 200},
		"log_cancel": LogCancel{StreamID: "s1"},
		"log_chunk":  LogChunk{StreamID: "s1", Lines: []string{"listening on :3000"}, EOF: true},
		"token_usage_batch": TokenUsageBatch{Rows: []TokenUsageRow{{
			DedupKey: "msg_01:req_01", AppID: "app1", Source: "claude_code", Model: "claude-fable-5",
			InputTokens: 2, OutputTokens: 245, CacheCreationTokens: 10465, CacheReadTokens: 28445,
			SessionID: "43426d4d", OccurredAtMS: 1737300000123,
		}}},
		"approval_event": ApprovalEvent{AppID: "app1", Approval: ApprovalApproved, DefinitionHash: "sha256:abc"},
	}

	for name, msg := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := json.MarshalIndent(msg, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			golden := filepath.Join("testdata", name+".golden.json")
			if *update {
				if err := os.MkdirAll("testdata", 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(golden, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("missing golden file (run with -update to create): %v", err)
			}
			if string(got) != string(want) {
				t.Errorf("wire format drifted from golden file %s\ngot:\n%s\nwant:\n%s", golden, got, want)
			}
		})
	}
}

func TestValidVerb(t *testing.T) {
	for _, v := range []Verb{VerbStart, VerbStop, VerbRestart} {
		if !ValidVerb(v) {
			t.Errorf("ValidVerb(%q) = false, want true", v)
		}
	}
	for _, v := range []Verb{"exec", "shell", "", "delete", "Start"} {
		if ValidVerb(v) {
			t.Errorf("ValidVerb(%q) = true, want false", v)
		}
	}
}

func TestAppDefinitionValidate(t *testing.T) {
	valid := AppDefinition{SchemaVersion: 1, Name: "demo", Kind: KindProcess, Cmd: "node", Cwd: "/srv"}
	if err := valid.Validate(); err != nil {
		t.Errorf("valid definition rejected: %v", err)
	}

	cases := []struct {
		name string
		def  AppDefinition
	}{
		{"missing name", AppDefinition{SchemaVersion: 1, Kind: KindProcess, Cmd: "node"}},
		{"missing cmd", AppDefinition{SchemaVersion: 1, Name: "x", Kind: KindProcess}},
		{"bad schema version", AppDefinition{SchemaVersion: 99, Name: "x", Cmd: "node"}},
		{"bad kind", AppDefinition{SchemaVersion: 1, Name: "x", Kind: "vm"}},
		{"bad port", AppDefinition{SchemaVersion: 1, Name: "x", Cmd: "node", Ports: []int{99999}}},
		{"docker without container", AppDefinition{SchemaVersion: 1, Name: "x", Kind: KindDocker}},
	}
	for _, tc := range cases {
		if err := tc.def.Validate(); err == nil {
			t.Errorf("%s: expected validation error, got nil", tc.name)
		}
	}

	// Kind defaults to process when empty.
	noKind := AppDefinition{SchemaVersion: 1, Name: "x", Cmd: "node"}
	if err := noKind.Validate(); err != nil {
		t.Errorf("empty kind should default to process: %v", err)
	}
}

func TestAppDefinitionHashStability(t *testing.T) {
	a := AppDefinition{SchemaVersion: 1, Name: "demo", Cmd: "node", Env: map[string]string{"B": "2", "A": "1"}}
	b := AppDefinition{SchemaVersion: 1, Name: "demo", Cmd: "node", Env: map[string]string{"A": "1", "B": "2"}}
	if a.Hash() != b.Hash() {
		t.Error("hash must be stable regardless of env map insertion order")
	}
	c := a
	c.Cmd = "python"
	if a.Hash() == c.Hash() {
		t.Error("different definitions must hash differently")
	}
}
