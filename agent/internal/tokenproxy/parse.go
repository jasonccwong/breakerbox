package tokenproxy

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/breakerbox/breakerbox/pkg/protocol"
)

// usageParser accumulates response bytes and yields at most one usage row.
type usageParser interface {
	feed(b []byte)
	result() (protocol.TokenUsageRow, bool)
}

// maxJSONBody caps buffered non-streaming bodies (usage lives near the top of
// even huge responses, but cap regardless).
const maxJSONBody = 16 << 20

// ---- non-streaming JSON ----

type jsonParser struct {
	provider string
	buf      bytes.Buffer
}

func newJSONParser(provider string) *jsonParser { return &jsonParser{provider: provider} }

func (p *jsonParser) feed(b []byte) {
	if p.buf.Len() < maxJSONBody {
		p.buf.Write(b)
	}
}

// anthropicUsage mirrors the usage object of the Messages API.
type anthropicUsage struct {
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadTokens     int64 `json:"cache_read_input_tokens"`
}

// openaiUsage mirrors chat/completions + responses API usage. Both the
// legacy (prompt/completion) and the responses-API (input/output) names are
// accepted.
type openaiUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	PromptDetails    struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	InputDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"input_tokens_details"`
}

func (p *jsonParser) result() (protocol.TokenUsageRow, bool) {
	switch p.provider {
	case "anthropic":
		var body struct {
			ID    string          `json:"id"`
			Model string          `json:"model"`
			Usage *anthropicUsage `json:"usage"`
		}
		if json.Unmarshal(p.buf.Bytes(), &body) != nil || body.Usage == nil {
			return protocol.TokenUsageRow{}, false
		}
		return anthropicRow(body.ID, body.Model, *body.Usage), true
	case "openai":
		var body struct {
			ID    string       `json:"id"`
			Model string       `json:"model"`
			Usage *openaiUsage `json:"usage"`
		}
		if json.Unmarshal(p.buf.Bytes(), &body) != nil || body.Usage == nil {
			return protocol.TokenUsageRow{}, false
		}
		return openaiRow(body.ID, body.Model, *body.Usage), true
	}
	return protocol.TokenUsageRow{}, false
}

func anthropicRow(id, model string, u anthropicUsage) protocol.TokenUsageRow {
	return protocol.TokenUsageRow{
		DedupKey:            "proxy:" + id,
		Model:               model,
		InputTokens:         u.InputTokens,
		OutputTokens:        u.OutputTokens,
		CacheCreationTokens: u.CacheCreationTokens,
		CacheReadTokens:     u.CacheReadTokens,
	}
}

func openaiRow(id, model string, u openaiUsage) protocol.TokenUsageRow {
	in := u.PromptTokens
	if in == 0 {
		in = u.InputTokens
	}
	out := u.CompletionTokens
	if out == 0 {
		out = u.OutputTokens
	}
	cached := u.PromptDetails.CachedTokens
	if cached == 0 {
		cached = u.InputDetails.CachedTokens
	}
	return protocol.TokenUsageRow{
		DedupKey:        "proxy:" + id,
		Model:           model,
		InputTokens:     in - cached,
		CacheReadTokens: cached,
		OutputTokens:    out,
	}
}

// ---- streaming SSE ----

// sseParser consumes text/event-stream data lines incrementally.
//
// Anthropic: message_start carries {message: {id, model, usage: input+cache}};
// the final message_delta carries usage.output_tokens (cumulative).
// OpenAI: data chunks; the last chunk before [DONE] carries a non-null usage
// when include_usage was requested (which the proxy forces).
type sseParser struct {
	provider string
	partial  string
	row      protocol.TokenUsageRow
	sawUsage bool
}

func newSSEParser(provider string) *sseParser { return &sseParser{provider: provider} }

func (p *sseParser) feed(b []byte) {
	text := p.partial + string(b)
	lines := strings.Split(text, "\n")
	p.partial = lines[len(lines)-1]
	for _, line := range lines[:len(lines)-1] {
		line = strings.TrimSuffix(line, "\r")
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		p.consumeEvent(data)
	}
}

func (p *sseParser) consumeEvent(data string) {
	switch p.provider {
	case "anthropic":
		var ev struct {
			Type    string `json:"type"`
			Message *struct {
				ID    string          `json:"id"`
				Model string          `json:"model"`
				Usage *anthropicUsage `json:"usage"`
			} `json:"message"`
			Usage *anthropicUsage `json:"usage"` // message_delta
		}
		if json.Unmarshal([]byte(data), &ev) != nil {
			return
		}
		switch ev.Type {
		case "message_start":
			if ev.Message != nil {
				p.row.DedupKey = "proxy:" + ev.Message.ID
				p.row.Model = ev.Message.Model
				if u := ev.Message.Usage; u != nil {
					p.row.InputTokens = u.InputTokens
					p.row.CacheCreationTokens = u.CacheCreationTokens
					p.row.CacheReadTokens = u.CacheReadTokens
					// message_start already carries a small output count.
					p.row.OutputTokens = u.OutputTokens
					p.sawUsage = true
				}
			}
		case "message_delta":
			if ev.Usage != nil && ev.Usage.OutputTokens > 0 {
				p.row.OutputTokens = ev.Usage.OutputTokens
				p.sawUsage = true
			}
		}
	case "openai":
		var chunk struct {
			ID    string       `json:"id"`
			Model string       `json:"model"`
			Usage *openaiUsage `json:"usage"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil {
			return
		}
		if chunk.ID != "" && p.row.DedupKey == "" {
			p.row.DedupKey = "proxy:" + chunk.ID
		}
		if chunk.Model != "" {
			p.row.Model = chunk.Model
		}
		if chunk.Usage != nil {
			u := openaiRow(chunk.ID, chunk.Model, *chunk.Usage)
			p.row.InputTokens = u.InputTokens
			p.row.CacheReadTokens = u.CacheReadTokens
			p.row.OutputTokens = u.OutputTokens
			p.sawUsage = true
		}
	}
}

func (p *sseParser) result() (protocol.TokenUsageRow, bool) {
	if !p.sawUsage || p.row.DedupKey == "" {
		return protocol.TokenUsageRow{}, false
	}
	return p.row, true
}
