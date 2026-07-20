// Package tokenwatch harvests LLM token usage from coding-agent transcript
// files on this host: Claude Code JSONL (verified format) and Codex CLI
// session rollouts. Parsing is defensive throughout — these are undocumented
// internal formats that drift between versions, and a parse failure must
// never take the agent down. Unparseable lines are counted, not fatal.
package tokenwatch

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/breakerbox/breakerbox/pkg/protocol"
)

// claudeLine is the subset of a Claude Code transcript line we read.
// Verified against ~/.claude/projects/*/*.jsonl (Claude Code v2.x).
type claudeLine struct {
	Type      string `json:"type"`
	Cwd       string `json:"cwd"`
	SessionID string `json:"sessionId"`
	RequestID string `json:"requestId"`
	Timestamp string `json:"timestamp"`
	Message   struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage *struct {
			InputTokens         int64 `json:"input_tokens"`
			OutputTokens        int64 `json:"output_tokens"`
			CacheCreationInput  int64 `json:"cache_creation_input_tokens"`
			CacheReadInput      int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// parseClaudeLine extracts a usage row from one transcript line. Returns
// (row, true) only for assistant lines carrying usage.
//
// Streaming writes the same message id repeatedly with growing output_tokens;
// all occurrences share the dedup key "<message.id>:<requestId>" and the hub
// keeps the largest output count. input_tokens alone undercounts massively —
// real input is input + cache_read + cache_creation, which the hub prices per
// component.
func parseClaudeLine(line []byte) (protocol.TokenUsageRow, bool) {
	var l claudeLine
	if err := json.Unmarshal(line, &l); err != nil {
		return protocol.TokenUsageRow{}, false
	}
	if l.Type != "assistant" || l.Message.Usage == nil || l.Message.ID == "" {
		return protocol.TokenUsageRow{}, false
	}
	u := l.Message.Usage
	if u.InputTokens == 0 && u.OutputTokens == 0 && u.CacheCreationInput == 0 && u.CacheReadInput == 0 {
		return protocol.TokenUsageRow{}, false
	}
	ts := time.Now().UnixMilli()
	if t, err := time.Parse(time.RFC3339Nano, l.Timestamp); err == nil {
		ts = t.UnixMilli()
	}
	return protocol.TokenUsageRow{
		DedupKey:            l.Message.ID + ":" + l.RequestID,
		Source:              "claude_code",
		Model:               l.Message.Model,
		InputTokens:         u.InputTokens,
		OutputTokens:        u.OutputTokens,
		CacheCreationTokens: u.CacheCreationInput,
		CacheReadTokens:     u.CacheReadInput,
		SessionID:           l.SessionID,
		OccurredAtMS:        ts,
	}, true
}

// claudeCwd pulls the project directory from a transcript line (present on
// every line type we care about).
func claudeCwd(line []byte) string {
	var l struct {
		Cwd string `json:"cwd"`
	}
	_ = json.Unmarshal(line, &l)
	return strings.TrimSpace(l.Cwd)
}
