package tokens

import (
	"net/http"
	"strconv"
	"time"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
)

// registerSummaryRoute mounts GET /api/bb/tokens/summary?days=N — the
// aggregated view the token screens consume. Aggregation happens in SQL; raw
// rows can number tens of thousands and never leave the hub.
func (s *Service) registerSummaryRoute(se *core.ServeEvent) {
	se.Router.GET("/api/bb/tokens/summary", func(e *core.RequestEvent) error {
		days, _ := strconv.Atoi(e.Request.URL.Query().Get("days"))
		if days <= 0 || days > 365 {
			days = 30
		}
		since := time.Now().UTC().AddDate(0, 0, -days+1).Truncate(24 * time.Hour)
		bind := map[string]any{"since": since.Format(types.DefaultDateLayout)}

		type dayModelRow struct {
			Day   string  `db:"day" json:"day"`
			Model string  `db:"model" json:"model"`
			Cost  float64 `db:"cost" json:"cost"`
		}
		var dayModel []dayModelRow
		if err := s.app.DB().NewQuery(`
			SELECT strftime('%Y-%m-%d', occurred_at) AS day, model, SUM(cost_usd) AS cost
			FROM token_usage WHERE occurred_at >= {:since}
			GROUP BY day, model ORDER BY day`).Bind(bind).All(&dayModel); err != nil {
			return apis.NewApiError(http.StatusInternalServerError, "summary query failed", err)
		}

		type appRow struct {
			AppID  string  `db:"app_id" json:"app_id"`
			Name   string  `db:"name" json:"name"`
			Cost   float64 `db:"cost" json:"cost"`
			Input  int64   `db:"input" json:"input"`
			Output int64   `db:"output" json:"output"`
			Rows   int64   `db:"rows" json:"rows"`
		}
		var byApp []appRow
		if err := s.app.DB().NewQuery(`
			SELECT COALESCE(t.app, '') AS app_id, COALESCE(a.name, '') AS name,
			       SUM(t.cost_usd) AS cost,
			       SUM(t.input_tokens + t.cache_creation_tokens + t.cache_read_tokens) AS input,
			       SUM(t.output_tokens) AS output,
			       COUNT(*) AS rows
			FROM token_usage t LEFT JOIN apps a ON a.id = t.app
			WHERE t.occurred_at >= {:since}
			GROUP BY app_id ORDER BY cost DESC`).Bind(bind).All(&byApp); err != nil {
			return apis.NewApiError(http.StatusInternalServerError, "summary query failed", err)
		}

		type kv struct {
			Key  string  `db:"key" json:"key"`
			Cost float64 `db:"cost" json:"cost"`
		}
		var bySource, byModel []kv
		if err := s.app.DB().NewQuery(`
			SELECT source AS key, SUM(cost_usd) AS cost FROM token_usage
			WHERE occurred_at >= {:since} GROUP BY source ORDER BY cost DESC`).
			Bind(bind).All(&bySource); err != nil {
			return apis.NewApiError(http.StatusInternalServerError, "summary query failed", err)
		}
		if err := s.app.DB().NewQuery(`
			SELECT model AS key, SUM(cost_usd) AS cost FROM token_usage
			WHERE occurred_at >= {:since} GROUP BY model ORDER BY cost DESC`).
			Bind(bind).All(&byModel); err != nil {
			return apis.NewApiError(http.StatusInternalServerError, "summary query failed", err)
		}

		var totals struct {
			Cost   float64 `db:"cost" json:"cost"`
			Input  int64   `db:"input" json:"input"`
			Output int64   `db:"output" json:"output"`
			Rows   int64   `db:"rows" json:"rows"`
		}
		if err := s.app.DB().NewQuery(`
			SELECT COALESCE(SUM(cost_usd),0) AS cost,
			       COALESCE(SUM(input_tokens + cache_creation_tokens + cache_read_tokens),0) AS input,
			       COALESCE(SUM(output_tokens),0) AS output,
			       COUNT(*) AS rows
			FROM token_usage WHERE occurred_at >= {:since}`).Bind(bind).One(&totals); err != nil {
			return apis.NewApiError(http.StatusInternalServerError, "summary query failed", err)
		}

		return e.JSON(http.StatusOK, map[string]any{
			"days":      days,
			"day_model": dayModel,
			"by_app":    byApp,
			"by_source": bySource,
			"by_model":  byModel,
			"totals":    totals,
		})
	}).Bind(apis.RequireAuth())
}
