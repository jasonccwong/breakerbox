package tokenproxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/breakerbox/breakerbox/pkg/protocol"
)

// collector gathers emitted rows.
type collector struct {
	mu   sync.Mutex
	rows []protocol.TokenUsageRow
}

func (c *collector) emit(rows []protocol.TokenUsageRow) {
	c.mu.Lock()
	c.rows = append(c.rows, rows...)
	c.mu.Unlock()
}

func (c *collector) waitForRow(t *testing.T) protocol.TokenUsageRow {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		if len(c.rows) > 0 {
			row := c.rows[0]
			c.mu.Unlock()
			return row
		}
		c.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no usage row emitted")
	return protocol.TokenUsageRow{}
}

// withFakeUpstream points a provider at a local httptest server.
func withFakeUpstream(t *testing.T, provider string, handler http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	old := upstreams[provider]
	upstreams[provider] = upstream{scheme: "http", host: strings.TrimPrefix(srv.URL, "http://")}
	t.Cleanup(func() { upstreams[provider] = old })
	return srv
}

func startProxy(t *testing.T) (*Proxy, *collector) {
	t.Helper()
	c := &collector{}
	p, err := Start(c.emit)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Stop)
	return p, c
}

func TestAnthropicNonStreaming(t *testing.T) {
	respBody := `{"id":"msg_abc","model":"claude-fable-5","content":[{"type":"text","text":"hi"}],
		"usage":{"input_tokens":10,"output_tokens":25,"cache_creation_input_tokens":100,"cache_read_input_tokens":200}}`
	var gotAuth string
	withFakeUpstream(t, "anthropic", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("X-Api-Key")
		if r.URL.Path != "/v1/messages" {
			t.Errorf("upstream path = %q, want /v1/messages", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, respBody)
	}))
	p, c := startProxy(t)

	req, _ := http.NewRequest("POST",
		fmt.Sprintf("http://127.0.0.1:%d/t/app-1/anthropic/v1/messages", p.Port()),
		strings.NewReader(`{"model":"claude-fable-5","messages":[]}`))
	req.Header.Set("X-Api-Key", "sk-test")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if !strings.Contains(string(body), `"msg_abc"`) {
		t.Error("response body not passed through intact")
	}
	if gotAuth != "sk-test" {
		t.Errorf("auth header not forwarded, got %q", gotAuth)
	}
	row := c.waitForRow(t)
	if row.DedupKey != "proxy:msg_abc" || row.Model != "claude-fable-5" || row.AppID != "app-1" ||
		row.Source != "runtime_proxy" {
		t.Errorf("row identity: %+v", row)
	}
	if row.InputTokens != 10 || row.OutputTokens != 25 || row.CacheCreationTokens != 100 || row.CacheReadTokens != 200 {
		t.Errorf("row tokens: %+v", row)
	}
}

func TestAnthropicStreaming(t *testing.T) {
	sse := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_s1","model":"claude-fable-5","usage":{"input_tokens":7,"output_tokens":1,"cache_read_input_tokens":50}}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","delta":{"text":"hello"}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","usage":{"output_tokens":42}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"
	withFakeUpstream(t, "anthropic", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sse)
	}))
	p, c := startProxy(t)

	resp, err := http.Post(
		fmt.Sprintf("http://127.0.0.1:%d/t/app-2/anthropic/v1/messages", p.Port()),
		"application/json", strings.NewReader(`{"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	echoed, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(echoed), "message_stop") {
		t.Error("SSE stream not passed through")
	}

	row := c.waitForRow(t)
	if row.DedupKey != "proxy:msg_s1" || row.InputTokens != 7 || row.OutputTokens != 42 || row.CacheReadTokens != 50 {
		t.Errorf("streamed row: %+v", row)
	}
}

func TestOpenAIStreamingInjectsIncludeUsage(t *testing.T) {
	var upstreamBody []byte
	sse := `data: {"id":"chatcmpl-1","model":"gpt-5.1","choices":[{"delta":{"content":"hi"}}],"usage":null}` + "\n\n" +
		`data: {"id":"chatcmpl-1","model":"gpt-5.1","choices":[],"usage":{"prompt_tokens":30,"completion_tokens":9,"prompt_tokens_details":{"cached_tokens":12}}}` + "\n\n" +
		"data: [DONE]\n\n"
	withFakeUpstream(t, "openai", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sse)
	}))
	p, c := startProxy(t)

	resp, err := http.Post(
		fmt.Sprintf("http://127.0.0.1:%d/t/app-3/openai/v1/chat/completions", p.Port()),
		"application/json", strings.NewReader(`{"model":"gpt-5.1","stream":true,"messages":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	var sent map[string]any
	if err := json.Unmarshal(upstreamBody, &sent); err != nil {
		t.Fatalf("upstream body not JSON: %v", err)
	}
	so, _ := sent["stream_options"].(map[string]any)
	if so == nil || so["include_usage"] != true {
		t.Errorf("include_usage not injected: %v", sent)
	}

	row := c.waitForRow(t)
	if row.DedupKey != "proxy:chatcmpl-1" || row.Model != "gpt-5.1" {
		t.Errorf("row identity: %+v", row)
	}
	// prompt 30 with 12 cached -> 18 uncached input.
	if row.InputTokens != 18 || row.CacheReadTokens != 12 || row.OutputTokens != 9 {
		t.Errorf("row tokens: %+v", row)
	}
}

func TestErrorResponsesEmitNothing(t *testing.T) {
	withFakeUpstream(t, "openai", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"bad key"}}`, http.StatusUnauthorized)
	}))
	p, c := startProxy(t)
	resp, err := http.Post(
		fmt.Sprintf("http://127.0.0.1:%d/t/app-4/openai/v1/chat/completions", p.Port()),
		"application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status not passed through: %d", resp.StatusCode)
	}
	time.Sleep(150 * time.Millisecond)
	c.mu.Lock()
	n := len(c.rows)
	c.mu.Unlock()
	if n != 0 {
		t.Errorf("error response emitted %d rows", n)
	}
}

func TestEnsureIncludeUsage(t *testing.T) {
	cases := []struct {
		in       string
		injected bool
	}{
		{`{"stream":true}`, true},
		{`{"stream":true,"stream_options":{"include_usage":false}}`, false}, // explicit choice respected
		{`{"stream":false}`, false},
		{`{}`, false},
		{`not json`, false},
	}
	for _, tc := range cases {
		out := ensureIncludeUsage([]byte(tc.in))
		var m map[string]any
		_ = json.Unmarshal(out, &m)
		so, _ := m["stream_options"].(map[string]any)
		got := so != nil && so["include_usage"] == true
		if got != tc.injected {
			t.Errorf("ensureIncludeUsage(%s): injected=%v want %v", tc.in, got, tc.injected)
		}
	}
}

func TestEnvFor(t *testing.T) {
	p, _ := startProxy(t)
	env := p.EnvFor("app-9")
	joined := strings.Join(env, " ")
	if !strings.Contains(joined, fmt.Sprintf("ANTHROPIC_BASE_URL=http://127.0.0.1:%d/t/app-9/anthropic", p.Port())) {
		t.Errorf("anthropic env wrong: %v", env)
	}
	if !strings.Contains(joined, fmt.Sprintf("OPENAI_BASE_URL=http://127.0.0.1:%d/t/app-9/openai/v1", p.Port())) {
		t.Errorf("openai env wrong: %v", env)
	}
}
