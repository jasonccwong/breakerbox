package tokenwatch

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/breakerbox/breakerbox/pkg/protocol"
)

// Codex CLI rollout files (~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl)
// carry cumulative token totals in token_count events; a per-turn row is the
// delta between consecutive events. The session meta line (first line)
// carries the working directory.

// codexLine covers the two Codex line shapes we read. Codex has shipped both
// {type, payload} and flatter variants; fields are matched permissively.
type codexLine struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Payload   struct {
		Type string `json:"type"`
		// session_meta
		ID  string `json:"id"`
		Cwd string `json:"cwd"`
		// token_count (cumulative)
		Info *struct {
			TotalTokenUsage *codexUsage `json:"total_token_usage"`
			LastTokenUsage  *codexUsage `json:"last_token_usage"`
			ModelContext    string      `json:"model_context_window"`
		} `json:"info"`
		Model string `json:"model"`
	} `json:"payload"`
}

type codexUsage struct {
	InputTokens       int64 `json:"input_tokens"`
	CachedInputTokens int64 `json:"cached_input_tokens"`
	OutputTokens      int64 `json:"output_tokens"`
	ReasoningTokens   int64 `json:"reasoning_output_tokens"`
	TotalTokens       int64 `json:"total_tokens"`
}

// codexState tracks per-file cumulative counters between reads.
type codexState struct {
	SessionID string     `json:"session_id"`
	Cwd       string     `json:"cwd"`
	Model     string     `json:"model"`
	Last      codexUsage `json:"last"`
	Events    int        `json:"events"`
}

// parseCodexLine consumes one line, updating st and possibly yielding a
// delta row. Cumulative counters mean a row only appears when totals grow.
func parseCodexLine(line []byte, st *codexState) (protocol.TokenUsageRow, bool) {
	var l codexLine
	if err := json.Unmarshal(line, &l); err != nil {
		return protocol.TokenUsageRow{}, false
	}
	switch l.Payload.Type {
	case "session_meta":
		st.SessionID = l.Payload.ID
		st.Cwd = l.Payload.Cwd
		return protocol.TokenUsageRow{}, false
	case "turn_context":
		if l.Payload.Model != "" {
			st.Model = l.Payload.Model
		}
		return protocol.TokenUsageRow{}, false
	case "token_count":
		if l.Payload.Info == nil || l.Payload.Info.TotalTokenUsage == nil {
			return protocol.TokenUsageRow{}, false
		}
		cur := *l.Payload.Info.TotalTokenUsage
		prev := st.Last
		st.Last = cur
		st.Events++
		din := cur.InputTokens - prev.InputTokens
		dout := cur.OutputTokens - prev.OutputTokens
		dcache := cur.CachedInputTokens - prev.CachedInputTokens
		if din < 0 || dout < 0 { // counter reset: treat current as fresh
			din, dout, dcache = cur.InputTokens, cur.OutputTokens, cur.CachedInputTokens
		}
		if din == 0 && dout == 0 {
			return protocol.TokenUsageRow{}, false
		}
		ts := time.Now().UnixMilli()
		if t, err := time.Parse(time.RFC3339Nano, l.Timestamp); err == nil {
			ts = t.UnixMilli()
		}
		model := st.Model
		if model == "" {
			model = "gpt-codex" // priced as unknown when absent
		}
		return protocol.TokenUsageRow{
			DedupKey: fmt.Sprintf("codex:%s:%d", st.SessionID, st.Events),
			Source:   "codex",
			Model:    model,
			// Codex "input_tokens" is total including cached; keep the
			// uncached share in InputTokens so pricing mirrors Claude rows.
			InputTokens:     max64(din-dcache, 0),
			CacheReadTokens: dcache,
			OutputTokens:    dout,
			SessionID:       st.SessionID,
			OccurredAtMS:    ts,
		}, true
	}
	return protocol.TokenUsageRow{}, false
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
