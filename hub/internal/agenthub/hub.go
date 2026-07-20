package agenthub

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"
	"github.com/pocketbase/pocketbase/core"

	"github.com/breakerbox/breakerbox/pkg/protocol"
)

// maxClockSkew bounds the accepted age of the WS auth signature timestamp.
const maxClockSkew = 5 * time.Minute

// Hub wires the agent WebSocket plane into a PocketBase app.
type Hub struct {
	app core.App
	reg *registry
}

// Register attaches the agent WS route and the command-dispatch hook.
func Register(pb core.App) *Hub {
	h := &Hub{app: pb, reg: newRegistry()}

	pb.OnServe().BindFunc(func(se *core.ServeEvent) error {
		se.Router.GET("/api/bb/agent/ws", func(e *core.RequestEvent) error {
			h.serveWS(e.Response, e.Request)
			return nil
		})
		se.Router.GET("/api/bb/health", func(e *core.RequestEvent) error {
			return e.JSON(http.StatusOK, map[string]string{"status": "ok"})
		})
		return se.Next()
	})

	// Client created a command record -> validate done by hooks in the
	// commands package; here we dispatch to the live agent.
	pb.OnRecordAfterCreateSuccess("commands").BindFunc(func(e *core.RecordEvent) error {
		rec := e.Record
		cmd := protocol.Cmd{
			CmdID: rec.Id,
			AppID: rec.GetString("app"),
			Verb:  protocol.Verb(rec.GetString("verb")),
		}
		if appRec, err := e.App.FindRecordById("apps", cmd.AppID); err == nil {
			cmd.DefinitionHash = appRec.GetString("definition_hash")
		}
		systemID := rec.GetString("system")
		if err := h.Dispatch(systemID, cmd); err != nil {
			rec.Set("status", "failed")
			rec.Set("error", err.Error())
		} else {
			rec.Set("status", "dispatched")
		}
		if err := e.App.Save(rec); err != nil {
			slog.Error("update command after dispatch", "err", err)
		}
		return e.Next()
	})

	return h
}

// serveWS authenticates and services one agent connection.
func (h *Hub) serveWS(w http.ResponseWriter, r *http.Request) {
	systemID := r.Header.Get("X-System-Id")
	tsHeader := r.Header.Get("X-Timestamp")
	sigB64 := r.Header.Get("X-Signature")
	if systemID == "" || tsHeader == "" || sigB64 == "" {
		http.Error(w, "missing auth headers", http.StatusUnauthorized)
		return
	}

	system, err := h.app.FindRecordById("systems", systemID)
	if err != nil {
		http.Error(w, "unknown system", http.StatusUnauthorized)
		return
	}
	if err := verifySignature(system.GetString("public_key"), systemID, tsHeader, sigB64); err != nil {
		slog.Warn("agent ws auth failed", "system", systemID, "err", err)
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Agents are not browsers; no origin check needed on this route.
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		return
	}

	// Detach from the request context: PB cancels it when the handler
	// returns, but this handler blocks for the connection lifetime anyway.
	ctx, cancel := context.WithCancel(r.Context())
	c := &conn{systemID: systemID, ws: ws, ctx: ctx, cancel: cancel}
	h.reg.add(c)
	h.setSystemStatus(systemID, "online")
	slog.Info("agent connected", "system", systemID)

	defer func() {
		if h.reg.remove(c) {
			h.setSystemStatus(systemID, "offline")
		}
		cancel()
		_ = ws.CloseNow()
		slog.Info("agent disconnected", "system", systemID)
	}()

	for {
		typ, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		var env protocol.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			slog.Warn("bad frame from agent", "system", systemID, "err", err)
			continue
		}
		if err := h.handleFrame(c, system, env); err != nil {
			slog.Error("handle agent frame", "system", systemID, "type", env.T, "err", err)
		}
	}
}

// handleFrame routes one agent->hub message. Unknown types are ignored by
// design (forward compatibility).
func (h *Hub) handleFrame(c *conn, system *core.Record, env protocol.Envelope) error {
	switch env.T {
	case protocol.TypeHello:
		var hello protocol.Hello
		if err := json.Unmarshal(env.D, &hello); err != nil {
			return err
		}
		return h.onHello(c, system, hello)
	case protocol.TypeMetrics:
		var batch protocol.MetricsBatch
		if err := json.Unmarshal(env.D, &batch); err != nil {
			return err
		}
		return h.ingestMetrics(c.systemID, batch)
	case protocol.TypeAppEvent:
		var ev protocol.AppEvent
		if err := json.Unmarshal(env.D, &ev); err != nil {
			return err
		}
		return h.onAppEvent(ev)
	case protocol.TypeCmdAck, protocol.TypeCmdResult:
		return h.onCmdUpdate(env)
	default:
		slog.Debug("ignoring unknown frame type", "type", env.T)
		return nil
	}
}

func (h *Hub) onHello(c *conn, system *core.Record, hello protocol.Hello) error {
	// Re-set status here: this record was fetched before serveWS marked the
	// system online, and saving the stale snapshot would revert it.
	system.Set("status", "online")
	system.Set("agent_version", hello.AgentVersion)
	system.Set("os", hello.OS)
	system.Set("arch", hello.Arch)
	system.Set("hostname", hello.Hostname)
	system.Set("last_seen", time.Now().UTC())
	if caps, err := json.Marshal(hello.Capabilities); err == nil {
		system.Set("capabilities", string(caps))
	}
	if err := h.app.Save(system); err != nil {
		return err
	}
	if err := c.send(protocol.TypeHelloAck, protocol.HelloAck{
		ServerTimeMS:         time.Now().UnixMilli(),
		MinSupportedProtocol: protocol.Version,
	}); err != nil {
		return err
	}
	return h.sendAppSync(c)
}

// sendAppSync pushes the authoritative full-state app list to one agent.
func (h *Hub) sendAppSync(c *conn) error {
	records, err := h.app.FindRecordsByFilter("apps", "system = {:system}", "-created", 500, 0,
		map[string]any{"system": c.systemID})
	if err != nil {
		return err
	}
	sync := protocol.AppSync{Apps: make([]protocol.AppSpec, 0, len(records))}
	for _, rec := range records {
		var def protocol.AppDefinition
		if err := json.Unmarshal([]byte(rec.GetString("definition")), &def); err != nil {
			slog.Error("bad stored app definition", "app", rec.Id, "err", err)
			continue
		}
		sync.Apps = append(sync.Apps, protocol.AppSpec{
			ID:             rec.Id,
			Definition:     def,
			DefinitionHash: rec.GetString("definition_hash"),
			DesiredState:   protocol.DesiredState(rec.GetString("desired_state")),
		})
	}
	return c.send(protocol.TypeAppSync, sync)
}

// onAppEvent applies an agent-reported status transition to the apps record.
func (h *Hub) onAppEvent(ev protocol.AppEvent) error {
	rec, err := h.app.FindRecordById("apps", ev.AppID)
	if err != nil {
		return fmt.Errorf("app_event for unknown app %s: %w", ev.AppID, err)
	}
	rec.Set("status", string(ev.Status))
	rec.Set("pid", ev.PID)
	if ev.Status == protocol.StatusRunning {
		rec.Set("started_at", time.Now().UTC())
	}
	if ports, err := json.Marshal(ev.Ports); err == nil {
		rec.Set("ports", string(ports))
	}
	return h.app.Save(rec)
}

// onCmdUpdate applies cmd_ack / cmd_result to the commands record.
func (h *Hub) onCmdUpdate(env protocol.Envelope) error {
	if env.T == protocol.TypeCmdAck {
		var ack protocol.CmdAck
		if err := json.Unmarshal(env.D, &ack); err != nil {
			return err
		}
		rec, err := h.app.FindRecordById("commands", ack.CmdID)
		if err != nil {
			return err
		}
		rec.Set("status", "acked")
		return h.app.Save(rec)
	}
	var res protocol.CmdResult
	if err := json.Unmarshal(env.D, &res); err != nil {
		return err
	}
	rec, err := h.app.FindRecordById("commands", res.CmdID)
	if err != nil {
		return err
	}
	if res.OK {
		rec.Set("status", "done")
	} else {
		rec.Set("status", "failed")
		rec.Set("error", res.Error)
	}
	if detail, err := json.Marshal(res); err == nil {
		rec.Set("result", string(detail))
	}
	return h.app.Save(rec)
}

func (h *Hub) setSystemStatus(systemID, status string) {
	rec, err := h.app.FindRecordById("systems", systemID)
	if err != nil {
		return
	}
	rec.Set("status", status)
	rec.Set("last_seen", time.Now().UTC())
	if err := h.app.Save(rec); err != nil {
		slog.Error("set system status", "system", systemID, "err", err)
	}
}

// verifySignature checks the Ed25519 signature over "systemID|timestamp".
func verifySignature(pubKeyB64, systemID, tsHeader, sigB64 string) error {
	pub, err := base64.StdEncoding.DecodeString(pubKeyB64)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("stored public key invalid")
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return fmt.Errorf("signature not base64: %w", err)
	}
	tsMillis, err := strconv.ParseInt(tsHeader, 10, 64)
	if err != nil {
		return fmt.Errorf("timestamp not numeric: %w", err)
	}
	age := time.Since(time.UnixMilli(tsMillis))
	if age > maxClockSkew || age < -maxClockSkew {
		return fmt.Errorf("timestamp outside allowed skew: %s", age)
	}
	msg := []byte(systemID + "|" + tsHeader)
	if !ed25519.Verify(ed25519.PublicKey(pub), msg, sig) {
		return fmt.Errorf("signature mismatch")
	}
	return nil
}
