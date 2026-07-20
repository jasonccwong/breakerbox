package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// Settings: a singleton record holding instance-wide preferences. Created
// here so the UI always has a record to load/update (no create path needed).
func init() {
	m.Register(func(app core.App) error {
		authed := "@request.auth.id != \"\""
		col := core.NewBaseCollection("settings")
		col.Fields.Add(
			// Full ntfy endpoint including topic, e.g. https://ntfy.sh/my-alerts.
			// Empty = notifications disabled.
			&core.TextField{Name: "ntfy_endpoint"},
			&core.BoolField{Name: "notify_app_errors"},
			&core.BoolField{Name: "notify_system_offline"},
		)
		col.ListRule = &authed
		col.ViewRule = &authed
		col.UpdateRule = &authed
		// create/delete stay nil: superuser/hub only — the singleton below is
		// the only record this collection ever holds.
		if err := app.Save(col); err != nil {
			return err
		}

		rec := core.NewRecord(col)
		rec.Set("ntfy_endpoint", "")
		rec.Set("notify_app_errors", true)
		rec.Set("notify_system_offline", true)
		return app.Save(rec)
	}, func(app core.App) error {
		col, err := app.FindCollectionByNameOrId("settings")
		if err != nil {
			return nil
		}
		return app.Delete(col)
	})
}
