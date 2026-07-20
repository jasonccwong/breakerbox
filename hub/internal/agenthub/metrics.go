package agenthub

import (
	"time"

	"github.com/pocketbase/pocketbase/core"

	"github.com/breakerbox/breakerbox/pkg/protocol"
)

// ingestMetrics writes one agent metrics batch into the 1m tier. Tier
// downsampling and retention run separately (Phase 2 cron).
//
// Writes are batched in a single transaction: metric volume is the main write
// load on the hub (Spike A), and per-record transactions are ~50x slower on
// SQLite.
func (h *Hub) ingestMetrics(systemID string, batch protocol.MetricsBatch) error {
	sysCol, err := h.app.FindCollectionByNameOrId("system_metrics")
	if err != nil {
		return err
	}
	appCol, err := h.app.FindCollectionByNameOrId("app_metrics")
	if err != nil {
		return err
	}
	return h.app.RunInTransaction(func(tx core.App) error {
		for _, s := range batch.Host {
			rec := core.NewRecord(sysCol)
			rec.Set("system", systemID)
			rec.Set("type", "1m")
			rec.Set("cpu", s.CPUPct)
			rec.Set("mem_pct", s.MemPct)
			rec.Set("mem_used", s.MemUsed)
			rec.Set("disk_pct", s.DiskPct)
			rec.Set("net_sent", s.NetSent)
			rec.Set("net_recv", s.NetRecv)
			rec.Set("created", time.UnixMilli(s.TS).UTC())
			if err := tx.Save(rec); err != nil {
				return err
			}
		}
		for _, s := range batch.Apps {
			rec := core.NewRecord(appCol)
			rec.Set("app", s.AppID)
			rec.Set("type", "1m")
			rec.Set("cpu", s.CPUPct)
			rec.Set("mem_rss", s.MemRSS)
			rec.Set("created", time.UnixMilli(s.TS).UTC())
			if err := tx.Save(rec); err != nil {
				return err
			}
		}
		return nil
	})
}
