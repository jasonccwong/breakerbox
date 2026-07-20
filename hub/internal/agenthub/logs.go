package agenthub

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"

	"github.com/breakerbox/breakerbox/pkg/protocol"
)

// logChunkMsg is what the broker delivers per agent log_chunk frame.
type logChunkMsg struct {
	lines []string
	eof   bool
}

// logBroker routes agent log_chunk frames to waiting SSE handlers.
type logBroker struct {
	mu      sync.Mutex
	streams map[string]chan logChunkMsg
}

func newLogBroker() *logBroker { return &logBroker{streams: map[string]chan logChunkMsg{}} }

func (b *logBroker) open(streamID string) chan logChunkMsg {
	ch := make(chan logChunkMsg, 32)
	b.mu.Lock()
	b.streams[streamID] = ch
	b.mu.Unlock()
	return ch
}

func (b *logBroker) close(streamID string) {
	b.mu.Lock()
	delete(b.streams, streamID)
	b.mu.Unlock()
}

// deliver hands a chunk to its stream; drops when the consumer is slow (log
// viewers are best-effort, backpressure must not block the agent WS reader).
func (b *logBroker) deliver(chunk protocol.LogChunk) {
	b.mu.Lock()
	ch, ok := b.streams[chunk.StreamID]
	b.mu.Unlock()
	if !ok {
		return
	}
	select {
	case ch <- logChunkMsg{lines: chunk.Lines, eof: chunk.EOF}:
	default:
	}
}

// registerLogRoute mounts the SSE endpoint on the PB router.
func (h *Hub) registerLogRoute(se *core.ServeEvent) {
	se.Router.GET("/api/bb/apps/{id}/logs", func(e *core.RequestEvent) error {
		if err := h.authSSE(e); err != nil {
			return err
		}
		appID := e.Request.PathValue("id")
		appRec, err := h.app.FindRecordById("apps", appID)
		if err != nil {
			return apis.NewNotFoundError("unknown app", err)
		}
		systemID := appRec.GetString("system")
		c := h.reg.get(systemID)
		if c == nil {
			return apis.NewApiError(http.StatusServiceUnavailable, "agent for this system is offline", nil)
		}

		tail, _ := strconv.Atoi(e.Request.URL.Query().Get("tail"))
		if tail <= 0 || tail > 2000 {
			tail = 200
		}

		streamID := randomID()
		ch := h.logs.open(streamID)
		defer func() {
			h.logs.close(streamID)
			// Best effort: tell the agent to stop tailing.
			_ = c.send(protocol.TypeLogCancel, protocol.LogCancel{StreamID: streamID})
		}()
		if err := c.send(protocol.TypeLogFollow, protocol.LogFollow{
			StreamID: streamID, AppID: appID, TailN: tail,
		}); err != nil {
			return apis.NewApiError(http.StatusServiceUnavailable, "agent send failed", err)
		}

		w := e.Response
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		flush := func() {
			if flusher != nil {
				flusher.Flush()
			}
		}
		flush()

		heartbeat := time.NewTicker(25 * time.Second)
		defer heartbeat.Stop()
		for {
			select {
			case <-e.Request.Context().Done():
				return nil
			case <-c.ctx.Done(): // agent disconnected mid-stream
				fmt.Fprint(w, "event: eof\ndata: agent disconnected\n\n")
				flush()
				return nil
			case <-heartbeat.C:
				fmt.Fprint(w, ": keepalive\n\n")
				flush()
			case msg := <-ch:
				if len(msg.lines) > 0 {
					data, _ := json.Marshal(msg.lines)
					fmt.Fprintf(w, "data: %s\n\n", data)
				}
				if msg.eof {
					fmt.Fprint(w, "event: eof\ndata: end\n\n")
					flush()
					return nil
				}
				flush()
			}
		}
	})
}

// authSSE authenticates the request either via the normal Authorization
// header or a ?token= query parameter (EventSource cannot set headers).
func (h *Hub) authSSE(e *core.RequestEvent) error {
	if e.Auth != nil {
		return nil
	}
	token := e.Request.URL.Query().Get("token")
	if token == "" {
		token = e.Request.Header.Get("Authorization")
	}
	if token == "" {
		return apis.NewUnauthorizedError("authentication required", nil)
	}
	rec, err := h.app.FindAuthRecordByToken(token, core.TokenTypeAuth)
	if err != nil || rec == nil {
		return apis.NewUnauthorizedError("invalid token", err)
	}
	e.Auth = rec
	return nil
}

func randomID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
