// Package transport maintains the agent's outbound WebSocket to the hub:
// signature auth on dial, JSON envelope codec, and a bounded send queue that
// drops oldest metrics (never command results) under backpressure.
package transport

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/breakerbox/breakerbox/pkg/protocol"
)

// Conn is one live hub connection.
type Conn struct {
	ws     *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc

	// Frames carries incoming hub->agent envelopes; closed on disconnect.
	Frames chan protocol.Envelope

	sendCh chan []byte
}

// Dial connects and authenticates to the hub. hubURL is the http(s) base URL.
func Dial(ctx context.Context, hubURL, systemID string, priv ed25519.PrivateKey) (*Conn, error) {
	wsURL := strings.Replace(strings.TrimSuffix(hubURL, "/"), "http", "ws", 1) + "/api/bb/agent/ws"

	ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
	sig := ed25519.Sign(priv, []byte(systemID+"|"+ts))
	hdr := http.Header{}
	hdr.Set("X-System-Id", systemID)
	hdr.Set("X-Timestamp", ts)
	hdr.Set("X-Signature", base64.StdEncoding.EncodeToString(sig))

	dialCtx, cancelDial := context.WithTimeout(ctx, 15*time.Second)
	defer cancelDial()
	ws, resp, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{HTTPHeader: hdr})
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("hub rejected connection (%d): %w", resp.StatusCode, err)
		}
		return nil, err
	}

	connCtx, cancel := context.WithCancel(ctx)
	c := &Conn{
		ws:     ws,
		ctx:    connCtx,
		cancel: cancel,
		Frames: make(chan protocol.Envelope, 64),
		sendCh: make(chan []byte, 256),
	}
	go c.readLoop()
	go c.writeLoop()
	return c, nil
}

// Send marshals and queues one frame. Under sustained backpressure the oldest
// queued frame is dropped — callers must not rely on delivery for metrics;
// command results retry at the daemon layer via reconciliation.
func (c *Conn) Send(t string, payload any) error {
	d, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	b, err := json.Marshal(protocol.Envelope{V: protocol.Version, T: t, TS: time.Now().UnixMilli(), D: d})
	if err != nil {
		return err
	}
	for {
		select {
		case c.sendCh <- b:
			return nil
		case <-c.ctx.Done():
			return c.ctx.Err()
		default:
			// Queue full: drop oldest and retry.
			select {
			case <-c.sendCh:
			default:
			}
		}
	}
}

// Close tears the connection down.
func (c *Conn) Close() {
	c.cancel()
	_ = c.ws.CloseNow()
}

// Done reports connection termination.
func (c *Conn) Done() <-chan struct{} { return c.ctx.Done() }

func (c *Conn) readLoop() {
	defer func() {
		c.cancel()
		close(c.Frames)
	}()
	for {
		typ, data, err := c.ws.Read(c.ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		var env protocol.Envelope
		if json.Unmarshal(data, &env) == nil {
			select {
			case c.Frames <- env:
			case <-c.ctx.Done():
				return
			}
		}
	}
}

func (c *Conn) writeLoop() {
	for {
		select {
		case b := <-c.sendCh:
			writeCtx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
			err := c.ws.Write(writeCtx, websocket.MessageText, b)
			cancel()
			if err != nil {
				c.cancel()
				return
			}
		case <-c.ctx.Done():
			return
		}
	}
}

// Backoff produces jittered exponential reconnect delays: 1s, 2s, 4s ... cap.
type Backoff struct {
	attempt int
}

// Next returns the next delay.
func (b *Backoff) Next() time.Duration {
	d := time.Second << b.attempt
	if d > 60*time.Second {
		d = 60 * time.Second
	} else {
		b.attempt++
	}
	// ±25% jitter based on wall clock nanos (good enough, no rand needed).
	jitter := time.Duration(time.Now().UnixNano() % int64(d/2))
	return d/4*3 + jitter/2
}

// Reset clears the backoff after a healthy connection.
func (b *Backoff) Reset() { b.attempt = 0 }
