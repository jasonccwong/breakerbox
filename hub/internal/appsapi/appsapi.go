// Package appsapi exposes the authenticated app-management endpoints. Apps
// records are never created directly via the PB CRUD API because the
// definition hash must be hub-computed and the approval state hub/agent-owned.
package appsapi

import (
	"encoding/json"
	"net/http"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"

	"github.com/breakerbox/breakerbox/hub/internal/agenthub"
	"github.com/breakerbox/breakerbox/pkg/protocol"
)

// Register attaches the apps routes.
func Register(app core.App, hub *agenthub.Hub) {
	app.OnServe().BindFunc(func(se *core.ServeEvent) error {
		// Create (or update) an app from a definition. The definition may
		// come from the manual form or from a pasted breakerbox.app.json.
		se.Router.POST("/api/bb/apps", func(e *core.RequestEvent) error {
			if e.Auth == nil {
				return apis.NewUnauthorizedError("authentication required", nil)
			}
			var body struct {
				System     string                 `json:"system"`
				AppID      string                 `json:"app_id"` // set = update existing
				Definition protocol.AppDefinition `json:"definition"`
			}
			if err := e.BindBody(&body); err != nil {
				return apis.NewBadRequestError("invalid body", err)
			}
			if body.Definition.SchemaVersion == 0 {
				body.Definition.SchemaVersion = protocol.AppDefSchemaVersion
			}
			if body.Definition.Kind == "" {
				body.Definition.Kind = protocol.KindProcess
			}
			if err := body.Definition.Validate(); err != nil {
				return apis.NewBadRequestError(err.Error(), nil)
			}
			if _, err := e.App.FindRecordById("systems", body.System); err != nil {
				return apis.NewBadRequestError("unknown system", nil)
			}

			defJSON, err := json.Marshal(body.Definition)
			if err != nil {
				return apis.NewInternalServerError("", err)
			}
			hash := body.Definition.Hash()

			var rec *core.Record
			if body.AppID != "" {
				rec, err = e.App.FindRecordById("apps", body.AppID)
				if err != nil {
					return apis.NewBadRequestError("unknown app", nil)
				}
				if rec.GetString("definition_hash") != hash {
					// Definition changed: approval resets until the host
					// re-approves (the security invariant).
					rec.Set("approval", string(protocol.ApprovalPending))
				}
			} else {
				col, err := e.App.FindCollectionByNameOrId("apps")
				if err != nil {
					return apis.NewInternalServerError("", err)
				}
				rec = core.NewRecord(col)
				rec.Set("system", body.System)
				rec.Set("approval", string(protocol.ApprovalPending))
				rec.Set("desired_state", string(protocol.DesiredStopped))
				rec.Set("status", string(protocol.StatusUnknown))
			}
			rec.Set("name", body.Definition.Name)
			rec.Set("kind", string(body.Definition.Kind))
			rec.Set("definition", string(defJSON))
			rec.Set("definition_hash", hash)
			if err := e.App.Save(rec); err != nil {
				return apis.NewInternalServerError("save app", err)
			}

			// Best-effort push of the new definition to a live agent.
			hub.SyncSystem(body.System)

			return e.JSON(http.StatusOK, map[string]any{
				"app_id":          rec.Id,
				"definition_hash": hash,
				"approval":        rec.GetString("approval"),
			})
		})

		return se.Next()
	})
}
