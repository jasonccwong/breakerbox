// Package commands enforces the control-plane invariants on the commands
// collection (the client->agent path): fixed verb whitelist, approved apps
// only, consistent app/system pairing, hub-owned status field — plus the
// timeout sweeper for commands whose agent never answered.
package commands

import (
	"log/slog"
	"time"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"

	"github.com/breakerbox/breakerbox/pkg/protocol"
)

// dispatchTimeout is how long a command may sit in dispatched/acked before
// the sweeper declares it dead.
const dispatchTimeout = 60 * time.Second

// Register attaches validation hooks and the timeout sweeper.
func Register(app core.App) {
	// Request-level hook: fires only for client API creates, so internal
	// saves by hub code are unaffected.
	app.OnRecordCreateRequest("commands").BindFunc(func(e *core.RecordRequestEvent) error {
		verb := protocol.Verb(e.Record.GetString("verb"))
		if !protocol.ValidVerb(verb) {
			return apis.NewBadRequestError("invalid verb", nil)
		}

		appRec, err := e.App.FindRecordById("apps", e.Record.GetString("app"))
		if err != nil {
			return apis.NewBadRequestError("unknown app", err)
		}
		if appRec.GetString("approval") != string(protocol.ApprovalApproved) {
			return apis.NewBadRequestError("app definition is not approved on its host yet", nil)
		}
		// The system field must match the app's system — clients can't
		// route a command at a different host.
		e.Record.Set("system", appRec.GetString("system"))
		// Status lifecycle is hub-owned.
		e.Record.Set("status", "pending")
		if e.Auth != nil {
			e.Record.Set("requested_by", e.Auth.Id)
		}

		// Toggling desired_state rides along with the command: a start/stop
		// records intent so resurrect-on-boot and reconciliation agree.
		switch verb {
		case protocol.VerbStart:
			appRec.Set("desired_state", string(protocol.DesiredRunning))
			_ = e.App.Save(appRec)
		case protocol.VerbStop:
			appRec.Set("desired_state", string(protocol.DesiredStopped))
			_ = e.App.Save(appRec)
		}
		return e.Next()
	})

	app.Cron().MustAdd("commands_timeout_sweeper", "* * * * *", func() {
		cutoff := time.Now().Add(-dispatchTimeout).UTC().Format(types.DefaultDateLayout)
		stale, err := app.FindRecordsByFilter("commands",
			"(status = 'dispatched' || status = 'acked') && updated < {:cutoff}",
			"", 200, 0, map[string]any{"cutoff": cutoff})
		if err != nil {
			slog.Error("command sweeper query", "err", err)
			return
		}
		for _, rec := range stale {
			rec.Set("status", "timeout")
			rec.Set("error", "agent did not complete the command in time")
			if err := app.Save(rec); err != nil {
				slog.Error("command sweeper save", "cmd", rec.Id, "err", err)
			}
		}
		if len(stale) > 0 {
			slog.Info("swept stale commands", "count", len(stale))
		}
	})
}
