// Package migrations defines the BreakerBox schema as code so a fresh hub
// binary self-initializes with no manual setup.
package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// Rule fragments. In PocketBase, nil rule = superuser only; "" = public.
var authed = "@request.auth.id != \"\""

func init() {
	m.Register(func(app core.App) error {
		// systems: one record per enrolled host. Created/updated by hub code
		// only (superuser rules); clients read + subscribe.
		systems := core.NewBaseCollection("systems")
		systems.Fields.Add(
			&core.TextField{Name: "name", Required: true},
			&core.TextField{Name: "os"},
			&core.TextField{Name: "arch"},
			&core.TextField{Name: "hostname"},
			&core.TextField{Name: "agent_version"},
			&core.TextField{Name: "public_key", Required: true}, // base64 Ed25519
			&core.SelectField{Name: "status", Values: []string{"online", "offline", "paused"}, Required: true},
			&core.DateField{Name: "last_seen"},
			&core.JSONField{Name: "capabilities"},
			&core.AutodateField{Name: "created", OnCreate: true},
			&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
		)
		systems.ListRule = &authed
		systems.ViewRule = &authed
		if err := app.Save(systems); err != nil {
			return err
		}

		// enroll_tokens: one-time tokens minted in the UI.
		tokens := core.NewBaseCollection("enroll_tokens")
		tokens.Fields.Add(
			&core.TextField{Name: "token_hash", Required: true}, // sha256 of the token
			&core.DateField{Name: "expires_at", Required: true},
			&core.DateField{Name: "used_at"},
			&core.TextField{Name: "created_by"},
			&core.AutodateField{Name: "created", OnCreate: true},
		)
		tokens.AddIndex("idx_enroll_token_hash", true, "token_hash", "")
		if err := app.Save(tokens); err != nil {
			return err
		}

		// apps: one record per registered app on a system.
		apps := core.NewBaseCollection("apps")
		apps.Fields.Add(
			&core.RelationField{Name: "system", Required: true, CollectionId: systems.Id, CascadeDelete: true, MaxSelect: 1},
			&core.TextField{Name: "name", Required: true},
			&core.SelectField{Name: "kind", Values: []string{"process", "docker", "compose"}, Required: true},
			&core.JSONField{Name: "definition", Required: true},
			&core.TextField{Name: "definition_hash", Required: true},
			&core.SelectField{Name: "approval", Values: []string{"pending", "approved", "rejected"}, Required: true},
			&core.SelectField{Name: "desired_state", Values: []string{"running", "stopped"}, Required: true},
			&core.SelectField{Name: "status", Values: []string{"running", "stopped", "starting", "backoff", "errored", "unknown"}, Required: true},
			&core.NumberField{Name: "pid"},
			&core.DateField{Name: "started_at"},
			&core.JSONField{Name: "restart_policy"},
			&core.JSONField{Name: "ports"},
			&core.SelectField{Name: "token_tracking", Values: []string{"off", "dev", "runtime"}},
			&core.AutodateField{Name: "created", OnCreate: true},
			&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
		)
		apps.ListRule = &authed
		apps.ViewRule = &authed
		// create/update via custom hub endpoints only (definition_hash and
		// approval must be hub-computed, never client-supplied).
		if err := app.Save(apps); err != nil {
			return err
		}

		// commands: the client->agent control path and audit log in one.
		// Clients CREATE records here; a hub hook validates + dispatches.
		commands := core.NewBaseCollection("commands")
		commands.Fields.Add(
			&core.RelationField{Name: "app", Required: true, CollectionId: apps.Id, CascadeDelete: true, MaxSelect: 1},
			&core.RelationField{Name: "system", Required: true, CollectionId: systems.Id, CascadeDelete: true, MaxSelect: 1},
			&core.SelectField{Name: "verb", Values: []string{"start", "stop", "restart"}, Required: true},
			&core.SelectField{Name: "status", Values: []string{"pending", "dispatched", "acked", "done", "failed", "timeout"}, Required: true},
			&core.TextField{Name: "requested_by"},
			&core.JSONField{Name: "result"},
			&core.TextField{Name: "error"},
			&core.AutodateField{Name: "created", OnCreate: true},
			&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
		)
		commands.ListRule = &authed
		commands.ViewRule = &authed
		commands.CreateRule = &authed // hook enforces verb whitelist + approval
		if err := app.Save(commands); err != nil {
			return err
		}

		// system_metrics / app_metrics: tiered time series (type discriminator).
		sysMetrics := core.NewBaseCollection("system_metrics")
		sysMetrics.Fields.Add(
			&core.RelationField{Name: "system", Required: true, CollectionId: systems.Id, CascadeDelete: true, MaxSelect: 1},
			&core.SelectField{Name: "type", Values: []string{"1m", "10m", "1h", "1d"}, Required: true},
			&core.NumberField{Name: "cpu"},
			&core.NumberField{Name: "mem_pct"},
			&core.NumberField{Name: "mem_used"},
			&core.NumberField{Name: "disk_pct"},
			&core.NumberField{Name: "net_sent"},
			&core.NumberField{Name: "net_recv"},
			&core.DateField{Name: "created", Required: true},
		)
		sysMetrics.ListRule = &authed
		sysMetrics.ViewRule = &authed
		sysMetrics.AddIndex("idx_sysmetrics_system_type_created", false, "`system`, `type`, `created`", "")
		if err := app.Save(sysMetrics); err != nil {
			return err
		}

		appMetrics := core.NewBaseCollection("app_metrics")
		appMetrics.Fields.Add(
			&core.RelationField{Name: "app", Required: true, CollectionId: apps.Id, CascadeDelete: true, MaxSelect: 1},
			&core.SelectField{Name: "type", Values: []string{"1m", "10m", "1h", "1d"}, Required: true},
			&core.NumberField{Name: "cpu"},
			&core.NumberField{Name: "mem_rss"},
			&core.DateField{Name: "created", Required: true},
		)
		appMetrics.ListRule = &authed
		appMetrics.ViewRule = &authed
		appMetrics.AddIndex("idx_appmetrics_app_type_created", false, "`app`, `type`, `created`", "")
		if err := app.Save(appMetrics); err != nil {
			return err
		}

		// token_usage: deduped LLM usage rows.
		tokenUsage := core.NewBaseCollection("token_usage")
		tokenUsage.Fields.Add(
			&core.RelationField{Name: "app", CollectionId: apps.Id, MaxSelect: 1}, // nullable = unmatched bucket
			&core.RelationField{Name: "system", Required: true, CollectionId: systems.Id, CascadeDelete: true, MaxSelect: 1},
			&core.SelectField{Name: "source", Values: []string{"claude_code", "codex", "runtime_proxy"}, Required: true},
			&core.TextField{Name: "model", Required: true},
			&core.NumberField{Name: "input_tokens"},
			&core.NumberField{Name: "output_tokens"},
			&core.NumberField{Name: "cache_creation_tokens"},
			&core.NumberField{Name: "cache_read_tokens"},
			&core.NumberField{Name: "cost_usd"},
			&core.TextField{Name: "session_id"},
			&core.TextField{Name: "dedup_key", Required: true},
			&core.DateField{Name: "occurred_at", Required: true},
			&core.AutodateField{Name: "created", OnCreate: true},
		)
		tokenUsage.ListRule = &authed
		tokenUsage.ViewRule = &authed
		tokenUsage.AddIndex("idx_token_usage_dedup", true, "dedup_key", "")
		return app.Save(tokenUsage)
	}, func(app core.App) error {
		for _, name := range []string{"token_usage", "app_metrics", "system_metrics", "commands", "apps", "enroll_tokens", "systems"} {
			c, err := app.FindCollectionByNameOrId(name)
			if err != nil {
				continue
			}
			if err := app.Delete(c); err != nil {
				return err
			}
		}
		return nil
	})
}
