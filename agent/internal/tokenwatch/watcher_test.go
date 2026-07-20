package tokenwatch

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/breakerbox/breakerbox/pkg/protocol"
)

// claudeUsageLine builds a realistic transcript line (shape verified against
// real ~/.claude files, Claude Code v2.x).
func claudeUsageLine(msgID, reqID, cwd string, in, out, cacheCreate, cacheRead int64) string {
	return fmt.Sprintf(`{"type":"assistant","cwd":%q,"sessionId":"sess-1","requestId":%q,"timestamp":"2026-07-20T05:21:00.981Z","message":{"id":%q,"model":"claude-fable-5","usage":{"input_tokens":%d,"output_tokens":%d,"cache_creation_input_tokens":%d,"cache_read_input_tokens":%d}}}`,
		cwd, reqID, msgID, in, out, cacheCreate, cacheRead)
}

type harness struct {
	w      *Watcher
	rows   []protocol.TokenUsageRow
	accept bool
	dir    string // fake ~/.claude/projects/proj
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	proj := filepath.Join(home, ".claude", "projects", "-test-proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	h := &harness{accept: true, dir: proj}
	resolve := func(cwd string) string {
		if cwd == "/srv/myapp" {
			return "app-1"
		}
		return ""
	}
	h.w = New(t.TempDir(), resolve, func(rows []protocol.TokenUsageRow) bool {
		if h.accept {
			h.rows = append(h.rows, rows...)
		}
		return h.accept
	})
	return h
}

func (h *harness) write(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(h.dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func (h *harness) append(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) cycle() {
	h.w.scan()
	h.w.flush()
}

func TestClaudeParseAndAttribution(t *testing.T) {
	h := newHarness(t)
	h.write(t, "s.jsonl",
		claudeUsageLine("msg_1", "req_1", "/srv/myapp", 2, 245, 10465, 28445)+"\n"+
			`{"type":"user","cwd":"/srv/myapp"}`+"\n"+
			claudeUsageLine("msg_2", "req_2", "/somewhere/else", 10, 20, 0, 0)+"\n")
	h.cycle()

	if len(h.rows) != 2 {
		t.Fatalf("want 2 rows, got %d: %+v", len(h.rows), h.rows)
	}
	r := h.rows[0]
	if r.DedupKey != "msg_1:req_1" || r.Source != "claude_code" || r.Model != "claude-fable-5" {
		t.Errorf("row identity wrong: %+v", r)
	}
	if r.InputTokens != 2 || r.OutputTokens != 245 || r.CacheCreationTokens != 10465 || r.CacheReadTokens != 28445 {
		t.Errorf("token fields wrong: %+v", r)
	}
	if r.AppID != "app-1" {
		t.Errorf("cwd should have resolved to app-1, got %q", r.AppID)
	}
	if h.rows[1].AppID != "" {
		t.Errorf("unmatched cwd must yield empty AppID (system bucket), got %q", h.rows[1].AppID)
	}
}

func TestStreamingRewriteEmitsOnlyGrowth(t *testing.T) {
	h := newHarness(t)
	p := h.write(t, "s.jsonl", claudeUsageLine("msg_1", "req_1", "/srv/myapp", 2, 100, 0, 0)+"\n")
	h.cycle()
	// Same message rewritten with identical then larger output counts.
	h.append(t, p, claudeUsageLine("msg_1", "req_1", "/srv/myapp", 2, 100, 0, 0)+"\n")
	h.append(t, p, claudeUsageLine("msg_1", "req_1", "/srv/myapp", 2, 245, 0, 0)+"\n")
	h.cycle()

	if len(h.rows) != 2 {
		t.Fatalf("want 2 emissions (initial + growth), got %d", len(h.rows))
	}
	if h.rows[1].OutputTokens != 245 {
		t.Errorf("growth emission output = %d, want 245", h.rows[1].OutputTokens)
	}
}

func TestPartialLineWaitsForCompletion(t *testing.T) {
	h := newHarness(t)
	full := claudeUsageLine("msg_1", "req_1", "/srv/myapp", 1, 2, 0, 0)
	p := h.write(t, "s.jsonl", full[:40]) // mid-write, no newline
	h.cycle()
	if len(h.rows) != 0 {
		t.Fatalf("partial line must not produce rows, got %d", len(h.rows))
	}
	if h.w.unparsed != 0 {
		t.Fatalf("partial line must not count as unparsed yet, got %d", h.w.unparsed)
	}
	h.append(t, p, full[40:]+"\n")
	h.cycle()
	if len(h.rows) != 1 {
		t.Fatalf("completed line must produce exactly 1 row, got %d", len(h.rows))
	}
}

func TestGarbageLinesCountedNotFatal(t *testing.T) {
	h := newHarness(t)
	h.write(t, "s.jsonl", "{not json at all\n"+claudeUsageLine("m", "r", "/srv/myapp", 1, 2, 0, 0)+"\n")
	h.cycle()
	if len(h.rows) != 1 {
		t.Fatalf("valid row after garbage must survive, got %d rows", len(h.rows))
	}
	if h.w.unparsed != 1 {
		t.Errorf("unparsed counter = %d, want 1", h.w.unparsed)
	}
}

func TestTruncationResetsOffset(t *testing.T) {
	h := newHarness(t)
	// Two lines, then the file is replaced by a SHORTER fresh file — the
	// realistic rotation shape the size-based heuristic (à la tail -F) is
	// designed for. A same-size rewrite is undetectable by design.
	h.write(t, "s.jsonl",
		claudeUsageLine("m1", "r1", "/srv/myapp", 1, 2, 0, 0)+"\n"+
			claudeUsageLine("m1b", "r1b", "/srv/myapp", 5, 6, 0, 0)+"\n")
	h.cycle()
	h.write(t, "s.jsonl", claudeUsageLine("m2", "r2", "/srv/myapp", 3, 4, 0, 0)+"\n")
	h.cycle()
	if len(h.rows) != 3 {
		t.Fatalf("want 2 pre- + 1 post-truncation rows, got %d", len(h.rows))
	}
	if h.rows[2].DedupKey != "m2:r2" {
		t.Errorf("post-truncation row = %+v", h.rows[2])
	}
}

func TestOfflineEmitRetriesWithoutLoss(t *testing.T) {
	h := newHarness(t)
	h.accept = false // hub offline
	h.write(t, "s.jsonl", claudeUsageLine("m1", "r1", "/srv/myapp", 1, 2, 0, 0)+"\n")
	h.cycle()
	if len(h.rows) != 0 {
		t.Fatal("emit rejected but rows recorded")
	}
	h.accept = true
	h.w.flush() // no new scan needed; pending retries
	if len(h.rows) != 1 {
		t.Fatalf("pending rows must deliver after hub returns, got %d", len(h.rows))
	}
}

func TestCheckpointSurvivesRestart(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	proj := filepath.Join(home, ".claude", "projects", "-p")
	_ = os.MkdirAll(proj, 0o755)
	stateDir := t.TempDir()
	path := filepath.Join(proj, "s.jsonl")
	_ = os.WriteFile(path, []byte(claudeUsageLine("m1", "r1", "/x", 1, 2, 0, 0)+"\n"), 0o644)

	var rows1 []protocol.TokenUsageRow
	w1 := New(stateDir, func(string) string { return "" }, func(r []protocol.TokenUsageRow) bool {
		rows1 = append(rows1, r...)
		return true
	})
	w1.scan()
	w1.flush()
	if len(rows1) != 1 {
		t.Fatalf("first watcher: %d rows", len(rows1))
	}

	// New watcher, same state dir: must not re-emit old lines, must see new.
	var rows2 []protocol.TokenUsageRow
	w2 := New(stateDir, func(string) string { return "" }, func(r []protocol.TokenUsageRow) bool {
		rows2 = append(rows2, r...)
		return true
	})
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString(claudeUsageLine("m2", "r2", "/x", 3, 4, 0, 0) + "\n")
	f.Close()
	w2.scan()
	w2.flush()
	if len(rows2) != 1 || rows2[0].DedupKey != "m2:r2" {
		t.Fatalf("restarted watcher rows = %+v, want only m2:r2", rows2)
	}
}

func codexLine50(payload string) string { return payload + "\n" }

func TestCodexDeltas(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	day := filepath.Join(home, ".codex", "sessions", "2026", "07", "20")
	_ = os.MkdirAll(day, 0o755)
	content := codexLine50(`{"timestamp":"2026-07-20T10:00:00.000Z","type":"session_meta","payload":{"type":"session_meta","id":"cs-1","cwd":"/srv/myapp"}}`) +
		codexLine50(`{"timestamp":"2026-07-20T10:00:05.000Z","type":"turn_context","payload":{"type":"turn_context","model":"gpt-5.5-codex"}}`) +
		codexLine50(`{"timestamp":"2026-07-20T10:01:00.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":1000,"cached_input_tokens":400,"output_tokens":50,"total_tokens":1050}}}}`) +
		codexLine50(`{"timestamp":"2026-07-20T10:02:00.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":1600,"cached_input_tokens":900,"output_tokens":80,"total_tokens":1680}}}}`)
	_ = os.WriteFile(filepath.Join(day, "rollout-1.jsonl"), []byte(content), 0o644)

	var rows []protocol.TokenUsageRow
	w := New(t.TempDir(), func(cwd string) string {
		if cwd == "/srv/myapp" {
			return "app-1"
		}
		return ""
	}, func(r []protocol.TokenUsageRow) bool {
		rows = append(rows, r...)
		return true
	})
	w.scan()
	w.flush()

	if len(rows) != 2 {
		t.Fatalf("want 2 delta rows, got %d: %+v", len(rows), rows)
	}
	// First event from zero: input 1000 total, 400 cached -> 600 uncached.
	if rows[0].InputTokens != 600 || rows[0].CacheReadTokens != 400 || rows[0].OutputTokens != 50 {
		t.Errorf("first delta wrong: %+v", rows[0])
	}
	// Second event: +600 input (+500 cached -> 100 uncached), +30 output.
	if rows[1].InputTokens != 100 || rows[1].CacheReadTokens != 500 || rows[1].OutputTokens != 30 {
		t.Errorf("second delta wrong: %+v", rows[1])
	}
	if rows[0].Model != "gpt-5.5-codex" || rows[0].AppID != "app-1" || rows[0].Source != "codex" {
		t.Errorf("row identity wrong: %+v", rows[0])
	}
	if rows[0].DedupKey == rows[1].DedupKey {
		t.Error("codex dedup keys must differ per event")
	}
}
