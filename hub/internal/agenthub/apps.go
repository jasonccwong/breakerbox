package agenthub

import (
	"encoding/json"
	"fmt"

	"github.com/pocketbase/pocketbase/core"

	"github.com/breakerbox/breakerbox/pkg/protocol"
)

// onAppRegister creates an apps record for a definition imported on the host.
// Host-side import implies approval. Replies with a fresh AppSync so the
// agent learns the assigned ID.
func (h *Hub) onAppRegister(c *conn, reg protocol.AppRegister) error {
	def := reg.Definition
	if err := def.Validate(); err != nil {
		return fmt.Errorf("rejected app_register: %w", err)
	}
	hash := def.Hash()

	// Idempotency: re-importing the same definition must not duplicate.
	existing, _ := h.app.FindFirstRecordByFilter("apps",
		"system = {:system} && definition_hash = {:hash}",
		map[string]any{"system": c.systemID, "hash": hash})
	if existing == nil {
		col, err := h.app.FindCollectionByNameOrId("apps")
		if err != nil {
			return err
		}
		kind := def.Kind
		if kind == "" {
			kind = protocol.KindProcess
		}
		defJSON, err := json.Marshal(def)
		if err != nil {
			return err
		}
		rec := core.NewRecord(col)
		rec.Set("system", c.systemID)
		rec.Set("name", def.Name)
		rec.Set("kind", string(kind))
		rec.Set("definition", string(defJSON))
		rec.Set("definition_hash", hash)
		rec.Set("approval", string(protocol.ApprovalApproved))
		rec.Set("desired_state", string(protocol.DesiredStopped))
		rec.Set("status", string(protocol.StatusStopped))
		if def.RestartPolicy != nil {
			if rp, err := json.Marshal(def.RestartPolicy); err == nil {
				rec.Set("restart_policy", string(rp))
			}
		}
		if err := h.app.Save(rec); err != nil {
			return err
		}
	}
	return h.sendAppSync(c)
}

// onApprovalEvent applies a host-side approval change to the apps record.
// The agent is authoritative for approval state — that is the security model.
func (h *Hub) onApprovalEvent(ev protocol.ApprovalEvent) error {
	rec, err := h.app.FindRecordById("apps", ev.AppID)
	if err != nil {
		return fmt.Errorf("approval_event for unknown app %s: %w", ev.AppID, err)
	}
	if rec.GetString("definition_hash") != ev.DefinitionHash {
		// Approval refers to a stale definition; ignore. The agent will
		// receive the current definition via app_sync and re-decide.
		return nil
	}
	rec.Set("approval", string(ev.Approval))
	return h.app.Save(rec)
}
