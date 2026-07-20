// Package metrics owns the hub's time-series lifecycle: tiered downsampling
// (1m -> 10m -> 1h -> 1d, Beszel-style) and per-tier retention. Finer tiers
// answer recent-range queries; coarser tiers keep months of history while the
// SQLite file stays small.
package metrics

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
)

// tier describes one downsampling step: aggregate `from` rows into `to`
// buckets of `width`, and delete `from` rows older than `retain`.
type tier struct {
	from   string
	to     string        // empty for the last tier (no further aggregation)
	width  time.Duration // bucket width of `to`
	retain time.Duration // how long `from` rows live
}

var tiers = []tier{
	{from: "1m", to: "10m", width: 10 * time.Minute, retain: 48 * time.Hour},
	{from: "10m", to: "1h", width: time.Hour, retain: 10 * 24 * time.Hour},
	{from: "1h", to: "1d", width: 24 * time.Hour, retain: 30 * 24 * time.Hour},
	{from: "1d", retain: 2 * 365 * 24 * time.Hour},
}

// bucketExpr returns the SQLite expression that truncates `created` to the
// start of its bucket for a given target tier width.
func bucketExpr(width time.Duration) string {
	switch width {
	case 10 * time.Minute:
		return `strftime('%Y-%m-%d %H:', created) || printf('%02d:00.000Z', (CAST(strftime('%M', created) AS INTEGER)/10)*10)`
	case time.Hour:
		return `strftime('%Y-%m-%d %H:00:00.000Z', created)`
	case 24 * time.Hour:
		return `strftime('%Y-%m-%d 00:00:00.000Z', created)`
	default:
		panic(fmt.Sprintf("unsupported tier width %v", width))
	}
}

// series describes one metrics collection's shape for the generic aggregator.
type series struct {
	collection string
	keyField   string   // "system" or "app"
	avgFields  []string // gauge fields -> AVG per bucket
	maxFields  []string // cumulative counters -> MAX per bucket
}

var allSeries = []series{
	{
		collection: "system_metrics", keyField: "system",
		avgFields: []string{"cpu", "mem_pct", "mem_used", "disk_pct"},
		maxFields: []string{"net_sent", "net_recv"},
	},
	{
		collection: "app_metrics", keyField: "app",
		avgFields: []string{"cpu", "mem_rss"},
	},
}

// Register mounts the downsampling/retention cron. It runs every 10 minutes;
// higher tiers are cheap no-ops between their bucket boundaries.
func Register(app core.App) {
	app.Cron().MustAdd("metrics_downsample", "*/10 * * * *", func() {
		Run(app, time.Now().UTC())
	})
}

// Run executes one downsample+retention pass against `now` (injectable for
// tests).
func Run(app core.App, now time.Time) {
	for _, s := range allSeries {
		for _, t := range tiers {
			if t.to != "" {
				if err := aggregateTier(app, s, t, now); err != nil {
					slog.Error("metrics downsample", "collection", s.collection, "tier", t.from, "err", err)
				}
			}
			if err := enforceRetention(app, s.collection, t.from, now.Add(-t.retain)); err != nil {
				slog.Error("metrics retention", "collection", s.collection, "tier", t.from, "err", err)
			}
		}
	}
}

// aggregateTier rolls completed `from`-tier buckets into `to`-tier records.
// Progress tracking is implicit: only buckets newer than the target tier's
// last record (per key) and older than the current, incomplete bucket are
// aggregated, so the pass is idempotent without a bookkeeping table.
func aggregateTier(app core.App, s series, t tier, now time.Time) error {
	cutoff := now.Truncate(t.width).Format(types.DefaultDateLayout)

	// Last aggregated bucket per key in the target tier.
	lastRows := []struct {
		Key  string `db:"key"`
		Last string `db:"last"`
	}{}
	err := app.DB().NewQuery(fmt.Sprintf(
		`SELECT %s AS key, MAX(created) AS last FROM %s WHERE type = '%s' GROUP BY %s`,
		s.keyField, s.collection, t.to, s.keyField,
	)).All(&lastRows)
	if err != nil {
		return err
	}
	last := map[string]string{}
	for _, r := range lastRows {
		last[r.Key] = r.Last
	}

	// Aggregate all completed source buckets.
	selects := fmt.Sprintf("%s AS key, %s AS bucket, COUNT(*) AS cnt", s.keyField, bucketExpr(t.width))
	for _, f := range s.avgFields {
		selects += fmt.Sprintf(", AVG(%s) AS %s", f, f)
	}
	for _, f := range s.maxFields {
		selects += fmt.Sprintf(", MAX(%s) AS %s", f, f)
	}
	rows := []map[string]any{}
	q := app.DB().NewQuery(fmt.Sprintf(
		`SELECT %s FROM %s WHERE type = '%s' AND created < {:cutoff} GROUP BY key, bucket ORDER BY bucket`,
		selects, s.collection, t.from,
	)).Bind(map[string]any{"cutoff": cutoff})
	if err := rowsInto(q, &rows); err != nil {
		return err
	}

	col, err := app.FindCollectionByNameOrId(s.collection)
	if err != nil {
		return err
	}
	inserted := 0
	err = app.RunInTransaction(func(tx core.App) error {
		for _, r := range rows {
			key, _ := r["key"].(string)
			bucket, _ := r["bucket"].(string)
			// Skip buckets already aggregated for this key. String compare is
			// safe: both sides share the stored date layout.
			if lastBucket, ok := last[key]; ok && bucket <= lastBucket {
				continue
			}
			rec := core.NewRecord(col)
			rec.Set(s.keyField, key)
			rec.Set("type", t.to)
			for _, f := range append(append([]string{}, s.avgFields...), s.maxFields...) {
				rec.Set(f, r[f])
			}
			rec.Set("created", bucket)
			if err := tx.Save(rec); err != nil {
				return err
			}
			inserted++
		}
		return nil
	})
	if err == nil && inserted > 0 {
		slog.Debug("downsampled", "collection", s.collection, "from", t.from, "to", t.to, "buckets", inserted)
	}
	return err
}

// enforceRetention hard-deletes expired rows of one tier. Raw SQL: metric
// rows need no cascade or hook semantics, and record-by-record deletion is
// pathological on SQLite at this volume.
func enforceRetention(app core.App, collection, tierName string, before time.Time) error {
	_, err := app.DB().NewQuery(fmt.Sprintf(
		`DELETE FROM %s WHERE type = '%s' AND created < {:before}`,
		collection, tierName,
	)).Bind(map[string]any{"before": before.Format(types.DefaultDateLayout)}).Execute()
	return err
}

// rowsInto runs a dbx query into a []map[string]any (dbx's All needs typed
// structs; metric field sets vary per series).
func rowsInto(q *dbx.Query, out *[]map[string]any) error {
	rows, err := q.Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		m := map[string]any{}
		for i, c := range cols {
			if b, ok := vals[i].([]byte); ok {
				m[c] = string(b)
			} else {
				m[c] = vals[i]
			}
		}
		*out = append(*out, m)
	}
	return rows.Err()
}
