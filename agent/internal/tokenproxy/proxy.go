// Package tokenproxy is the opt-in runtime LLM metering path: a localhost
// reverse proxy the supervisor points apps at via ANTHROPIC_BASE_URL /
// OPENAI_BASE_URL env injection. It forwards requests to the real provider
// over normal client TLS (no MITM, no certificates) and tees usage out of the
// responses. Apps that don't honor the base-URL env vars are simply not
// covered — documented, not worked around.
package tokenproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"strings"
	"time"

	"github.com/breakerbox/breakerbox/pkg/protocol"
)

// Emit receives usage rows harvested from proxied responses. Delivery
// semantics match tokenwatch: the daemon buffers/retries.
type Emit func(rows []protocol.TokenUsageRow)

type upstream struct {
	scheme string
	host   string
}

var upstreams = map[string]upstream{
	"anthropic": {"https", "api.anthropic.com"},
	"openai":    {"https", "api.openai.com"},
}

// upstreamFor resolves a provider's upstream, honoring
// BREAKERBOX_UPSTREAM_<PROVIDER> overrides (corporate gateways, tests).
func upstreamFor(provider string) (upstream, bool) {
	if v := os.Getenv("BREAKERBOX_UPSTREAM_" + strings.ToUpper(provider)); v != "" {
		if u, err := neturl.Parse(v); err == nil && u.Host != "" {
			return upstream{scheme: u.Scheme, host: u.Host}, true
		}
	}
	up, ok := upstreams[provider]
	return up, ok
}

// Proxy is the local metering listener.
type Proxy struct {
	emit     Emit
	listener net.Listener
	server   *http.Server
	client   *http.Client
}

// Start binds 127.0.0.1 on an OS-assigned port and serves until Stop.
func Start(emit Emit) (*Proxy, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p := &Proxy{
		emit:     emit,
		listener: l,
		client: &http.Client{
			// Long ceiling: LLM streams legitimately run for minutes.
			Timeout: 30 * time.Minute,
		},
	}
	p.server = &http.Server{Handler: http.HandlerFunc(p.handle)}
	go func() {
		if err := p.server.Serve(l); err != nil && err != http.ErrServerClosed {
			slog.Error("token proxy serve", "err", err)
		}
	}()
	slog.Info("runtime token proxy listening", "addr", l.Addr())
	return p, nil
}

// Port returns the bound port for env injection.
func (p *Proxy) Port() int { return p.listener.Addr().(*net.TCPAddr).Port }

// Stop shuts the listener down.
func (p *Proxy) Stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = p.server.Shutdown(ctx)
}

// EnvFor returns the injection env vars for one app.
func (p *Proxy) EnvFor(appID string) []string {
	base := fmt.Sprintf("http://127.0.0.1:%d/t/%s", p.Port(), appID)
	return []string{
		"ANTHROPIC_BASE_URL=" + base + "/anthropic",
		// OpenAI SDKs expect the /v1 suffix inside the base URL.
		"OPENAI_BASE_URL=" + base + "/openai/v1",
	}
}

// handle proxies one request: /t/{appID}/{provider}/{rest...}.
func (p *Proxy) handle(w http.ResponseWriter, r *http.Request) {
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/t/"), "/", 3)
	if len(parts) < 3 {
		http.Error(w, "breakerbox token proxy: bad path", http.StatusNotFound)
		return
	}
	appID, providerName, rest := parts[0], parts[1], "/"+parts[2]
	up, ok := upstreamFor(providerName)
	if !ok {
		http.Error(w, "breakerbox token proxy: unknown provider", http.StatusNotFound)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		http.Error(w, "read body", http.StatusBadGateway)
		return
	}
	// OpenAI streaming responses only include usage when the request asks;
	// force it so metering never depends on app cooperation.
	if providerName == "openai" && r.Method == http.MethodPost {
		body = ensureIncludeUsage(body)
	}

	url := up.scheme + "://" + up.host + rest
	if r.URL.RawQuery != "" {
		url += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, url, bytes.NewReader(body))
	if err != nil {
		http.Error(w, "build upstream request", http.StatusBadGateway)
		return
	}
	req.Header = r.Header.Clone()
	req.Header.Del("Accept-Encoding") // parse plain bodies; upstream may gzip otherwise
	req.Host = up.host
	req.ContentLength = int64(len(body))

	resp, err := p.client.Do(req)
	if err != nil {
		http.Error(w, "upstream unreachable: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	// Tee the response through the right usage parser while streaming it back.
	var parser usageParser
	ct := resp.Header.Get("Content-Type")
	switch {
	case resp.StatusCode >= 300:
		parser = nil // errors carry no usage
	case strings.HasPrefix(ct, "text/event-stream"):
		parser = newSSEParser(providerName)
	case strings.HasPrefix(ct, "application/json"):
		parser = newJSONParser(providerName)
	}

	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if parser != nil {
				parser.feed(buf[:n])
			}
			if _, werr := w.Write(buf[:n]); werr != nil {
				break // client went away; keep draining not needed
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr != nil {
			break
		}
	}

	if parser != nil {
		if row, ok := parser.result(); ok {
			row.AppID = appID
			row.Source = "runtime_proxy"
			row.OccurredAtMS = time.Now().UnixMilli()
			p.emit([]protocol.TokenUsageRow{row})
		}
	}
}

// ensureIncludeUsage sets stream_options.include_usage on streaming OpenAI
// requests that didn't ask for it. Non-JSON or non-streaming bodies pass
// through untouched.
func ensureIncludeUsage(body []byte) []byte {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return body
	}
	stream, _ := m["stream"].(bool)
	if !stream {
		return body
	}
	so, _ := m["stream_options"].(map[string]any)
	if so == nil {
		so = map[string]any{}
	}
	if _, set := so["include_usage"]; set {
		return body
	}
	so["include_usage"] = true
	m["stream_options"] = so
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}
