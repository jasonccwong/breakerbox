// Package tokens ingests agent-harvested LLM usage rows into the token_usage
// collection: dedup via the unique index (streaming rewrites update in
// place), USD cost priced at ingest from a vendored LiteLLM table, and a
// daily spend-threshold alert.
package tokens

import (
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"

	"github.com/breakerbox/breakerbox/pkg/protocol"
)

// Sender is the slice of the notifier the spend alert needs.
type Sender interface {
	Send(title, body, priority, tags string) error
}

// Service prices and stores usage rows.
type Service struct {
	app     core.App
	notify  Sender
	pricing *pricingTable

	mu            sync.Mutex
	lastAlertDay  string // "2006-01-02" of the last spend alert sent
	unpricedSeen  map[string]bool
}

// Register wires the service, the pricing refresh route, and returns it for
// the agent plane to call.
func Register(app core.App, notify Sender) *Service {
	s := &Service{
		app:          app,
		notify:       notify,
		pricing:      loadPricing(app),
		unpricedSeen: map[string]bool{},
	}
	app.OnServe().BindFunc(func(se *core.ServeEvent) error {
		s.registerSummaryRoute(se)
		se.Router.POST("/api/bb/pricing/refresh", func(e *core.RequestEvent) error {
			n, err := s.pricing.refresh(app)
			if err != nil {
				return apis.NewBadRequestError("pricing refresh failed: "+err.Error(), nil)
			}
			return e.JSON(http.StatusOK, map[string]any{"models": n})
		}).Bind(apis.RequireAuth())
		return se.Next()
	})
	return s
}

// Ingest stores one batch from a system's agent. Rows whose dedup_key already
// exists are updated only when the new row reports more output tokens (a
// streaming rewrite completing); everything else is inserted.
func (s *Service) Ingest(systemID string, batch protocol.TokenUsageBatch) error {
	col, err := s.app.FindCollectionByNameOrId("token_usage")
	if err != nil {
		return err
	}
	err = s.app.RunInTransaction(func(tx core.App) error {
		for _, row := range batch.Rows {
			cost, priced := s.pricing.cost(row)
			if !priced {
				s.warnUnpriced(row.Model)
			}
			existing, _ := tx.FindFirstRecordByFilter("token_usage", "dedup_key = {:k}",
				map[string]any{"k": row.DedupKey})
			if existing != nil {
				if row.OutputTokens > int64(existing.GetFloat("output_tokens")) {
					existing.Set("output_tokens", row.OutputTokens)
					existing.Set("cost_usd", cost)
					if err := tx.Save(existing); err != nil {
						return err
					}
				}
				continue
			}
			rec := core.NewRecord(col)
			rec.Set("system", systemID)
			if row.AppID != "" {
				rec.Set("app", row.AppID)
			}
			rec.Set("source", row.Source)
			rec.Set("model", row.Model)
			rec.Set("input_tokens", row.InputTokens)
			rec.Set("output_tokens", row.OutputTokens)
			rec.Set("cache_creation_tokens", row.CacheCreationTokens)
			rec.Set("cache_read_tokens", row.CacheReadTokens)
			rec.Set("cost_usd", cost)
			rec.Set("session_id", row.SessionID)
			rec.Set("dedup_key", row.DedupKey)
			rec.Set("occurred_at", time.UnixMilli(row.OccurredAtMS).UTC())
			if err := tx.Save(rec); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.checkSpendThreshold()
	return nil
}

func (s *Service) warnUnpriced(model string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.unpricedSeen[model] {
		s.unpricedSeen[model] = true
		slog.Warn("no pricing for model; cost recorded as 0", "model", model)
	}
}

// checkSpendThreshold alerts (once per day) when today's total spend crosses
// the configured threshold.
func (s *Service) checkSpendThreshold() {
	settings, err := s.app.FindFirstRecordByFilter("settings", "")
	if err != nil || settings == nil {
		return
	}
	threshold := settings.GetFloat("spend_threshold_usd")
	if threshold <= 0 || !settings.GetBool("notify_spend") {
		return
	}
	today := time.Now().UTC().Format("2006-01-02")
	s.mu.Lock()
	already := s.lastAlertDay == today
	s.mu.Unlock()
	if already {
		return
	}

	var total struct {
		Sum float64 `db:"sum"`
	}
	err = s.app.DB().NewQuery(
		`SELECT COALESCE(SUM(cost_usd), 0) AS sum FROM token_usage WHERE occurred_at >= {:day}`,
	).Bind(map[string]any{"day": today + " 00:00:00.000Z"}).One(&total)
	if err != nil || total.Sum < threshold {
		return
	}

	s.mu.Lock()
	s.lastAlertDay = today
	s.mu.Unlock()
	body := fmt.Sprintf("Today's LLM spend is $%.2f (threshold $%.2f).", total.Sum, threshold)
	if err := s.notify.Send("Token spend threshold crossed", body, "high", "moneybag"); err != nil {
		slog.Warn("spend alert send failed", "err", err)
	}
}
