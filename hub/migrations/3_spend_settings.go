package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// Spend alerting settings: a daily USD threshold checked at token ingest.
func init() {
	m.Register(func(app core.App) error {
		col, err := app.FindCollectionByNameOrId("settings")
		if err != nil {
			return err
		}
		col.Fields.Add(
			&core.BoolField{Name: "notify_spend"},
			// 0 disables the alert.
			&core.NumberField{Name: "spend_threshold_usd"},
		)
		if err := app.Save(col); err != nil {
			return err
		}
		rec, err := app.FindFirstRecordByFilter("settings", "")
		if err != nil || rec == nil {
			return err
		}
		rec.Set("notify_spend", true)
		rec.Set("spend_threshold_usd", 0)
		return app.Save(rec)
	}, func(app core.App) error {
		return nil // additive only
	})
}
