// Package agenthub owns the hub side of the agent WebSocket plane: signature
// auth, the live connection registry, frame routing, and command dispatch.
package agenthub

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/breakerbox/breakerbox/pkg/protocol"
)

// conn is one live agent connection.
type conn struct {
	systemID string
	ws       *websocket.Conn
	sendMu   sync.Mutex
	ctx      context.Context
	cancel   context.CancelFunc
}

func (c *conn) send(t string, payload any) error {
	d, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	env := protocol.Envelope{V: protocol.Version, T: t, TS: time.Now().UnixMilli(), D: d}
	b, err := json.Marshal(env)
	if err != nil {
		return err
	}
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	writeCtx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
	defer cancel()
	return c.ws.Write(writeCtx, websocket.MessageText, b)
}

// registry tracks live agent connections by system ID.
type registry struct {
	mu    sync.RWMutex
	conns map[string]*conn
}

func newRegistry() *registry {
	return &registry{conns: make(map[string]*conn)}
}

// add registers a connection, closing any previous connection for the same
// system (an agent restart reconnects before the old TCP session times out).
func (r *registry) add(c *conn) {
	r.mu.Lock()
	old := r.conns[c.systemID]
	r.conns[c.systemID] = c
	r.mu.Unlock()
	if old != nil {
		old.cancel()
		_ = old.ws.Close(websocket.StatusPolicyViolation, "superseded by new connection")
	}
}

// remove drops a connection if it is still the current one for its system.
func (r *registry) remove(c *conn) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.conns[c.systemID] == c {
		delete(r.conns, c.systemID)
		return true
	}
	return false
}

// get returns the live connection for a system, or nil.
func (r *registry) get(systemID string) *conn {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.conns[systemID]
}

// Dispatch sends a command to the named system's agent. It returns an error
// if the agent is not connected.
func (h *Hub) Dispatch(systemID string, cmd protocol.Cmd) error {
	c := h.reg.get(systemID)
	if c == nil {
		return fmt.Errorf("agent for system %s is not connected", systemID)
	}
	return c.send(protocol.TypeCmd, cmd)
}
