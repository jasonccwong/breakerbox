package metrics

import (
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"

	_ "github.com/breakerbox/breakerbox/hub/migrations"
)

func testApp(t *testing.T) *tests.TestApp {
	t.Helper()
	app, err := tests.NewTestAppWithConfig(core.BaseAppConfig{DataDir: t.TempDir(), EncryptionEnv: "pb_test_env"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Cleanup)
	return app
}

// seedSystem creates a systems record and returns its id.
func seedSystem(t *testing.T, app core.App) string {
	t.Helper()
	col, err := app.FindCollectionByNameOrId("systems")
	if err != nil {
		t.Fatal(err)
	}
	rec := core.NewRecord(col)
	rec.Set("name", "test-system")
	rec.Set("public_key", "AAAA")
	rec.Set("status", "online")
	if err := app.Save(rec); err != nil {
		t.Fatal(err)
	}
	return rec.Id
}

func seed1m(t *testing.T, app core.App, systemID string, at time.Time, cpu float64, netSent float64) {
	t.Helper()
	col, err := app.FindCollectionByNameOrId("system_metrics")
	if err != nil {
		t.Fatal(err)
	}
	rec := core.NewRecord(col)
	rec.Set("system", systemID)
	rec.Set("type", "1m")
	rec.Set("cpu", cpu)
	rec.Set("mem_pct", 50.0)
	rec.Set("mem_used", 1024)
	rec.Set("disk_pct", 70.0)
	rec.Set("net_sent", netSent)
	rec.Set("net_recv", netSent/2)
	rec.Set("created", at.UTC())
	if err := app.Save(rec); err != nil {
		t.Fatal(err)
	}
}

func count(t *testing.T, app core.App, collection, tierName string) int {
	t.Helper()
	recs, err := app.FindRecordsByFilter(collection, "type = {:t}", "", 0, 0, map[string]any{"t": tierName})
	if err != nil {
		t.Fatal(err)
	}
	return len(recs)
}

func TestDownsampleAggregatesCompletedBuckets(t *testing.T) {
	app := testApp(t)
	sys := seedSystem(t, app)

	// now is mid-bucket: 12:35. Bucket 12:00–12:10 and 12:10–12:20 are
	// complete; 12:30–12:40 is current and must NOT be aggregated.
	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	now := base.Add(35 * time.Minute)

	// 12:00–12:10: cpu 10,20,30 -> avg 20; net_sent 100,200,300 -> max 300.
	for i, cpu := range []float64{10, 20, 30} {
		seed1m(t, app, sys, base.Add(time.Duration(i)*3*time.Minute), cpu, float64((i+1)*100))
	}
	// 12:10–12:20: cpu 40 -> avg 40.
	seed1m(t, app, sys, base.Add(12*time.Minute), 40, 400)
	// 12:30–12:40 (current bucket): must be ignored.
	seed1m(t, app, sys, base.Add(31*time.Minute), 99, 999)

	Run(app, now)

	recs, err := app.FindRecordsByFilter("system_metrics", "type = '10m'", "created", 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		for _, r := range recs {
			t.Logf("10m rec: created=%s cpu=%v", r.GetString("created"), r.Get("cpu"))
		}
		t.Fatalf("want 2 aggregated 10m buckets, got %d", len(recs))
	}
	b1, b2 := recs[0], recs[1]
	if got := b1.GetFloat("cpu"); got != 20 {
		t.Errorf("bucket1 cpu avg = %v, want 20", got)
	}
	if got := b1.GetFloat("net_sent"); got != 300 {
		t.Errorf("bucket1 net_sent max = %v, want 300", got)
	}
	if got := b2.GetFloat("cpu"); got != 40 {
		t.Errorf("bucket2 cpu avg = %v, want 40", got)
	}
	if got := b1.GetString("created"); got != "2026-07-20 12:00:00.000Z" {
		t.Errorf("bucket1 created = %q, want bucket start 12:00", got)
	}

	// Idempotency: a second pass must not duplicate buckets.
	Run(app, now)
	if n := count(t, app, "system_metrics", "10m"); n != 2 {
		t.Errorf("after second pass: %d 10m buckets, want 2 (not idempotent)", n)
	}

	// Later pass with the 12:30 bucket now complete aggregates exactly it.
	Run(app, now.Add(10*time.Minute))
	if n := count(t, app, "system_metrics", "10m"); n != 3 {
		t.Errorf("after third pass: %d 10m buckets, want 3", n)
	}
}

func TestRetentionDeletesExpiredRows(t *testing.T) {
	app := testApp(t)
	sys := seedSystem(t, app)
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)

	seed1m(t, app, sys, now.Add(-49*time.Hour), 10, 1) // beyond 48h retention
	seed1m(t, app, sys, now.Add(-1*time.Hour), 20, 2)  // fresh

	Run(app, now)

	recs, err := app.FindRecordsByFilter("system_metrics", "type = '1m'", "", 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("want 1 surviving 1m row, got %d", len(recs))
	}
	if got := recs[0].GetFloat("cpu"); got != 20 {
		t.Errorf("survivor cpu = %v, want the fresh row (20)", got)
	}
}

func TestFullTierChain(t *testing.T) {
	app := testApp(t)
	sys := seedSystem(t, app)

	// Two days of hourly-ish data, then run passes as time advances: rows
	// should climb the 1m -> 10m -> 1h -> 1d chain without loss of averages.
	start := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	for h := 0; h < 26; h++ {
		seed1m(t, app, sys, start.Add(time.Duration(h)*time.Hour), 50, float64(h))
	}
	Run(app, start.Add(27*time.Hour))

	for _, tierName := range []string{"10m", "1h", "1d"} {
		if n := count(t, app, "system_metrics", tierName); n == 0 {
			t.Errorf("tier %s empty after chain run", tierName)
		}
	}
	// The 1d bucket for day one must average to 50.
	recs, _ := app.FindRecordsByFilter("system_metrics", "type = '1d'", "created", 0, 0, nil)
	if len(recs) == 0 {
		t.Fatal("no 1d records")
	}
	if got := recs[0].GetFloat("cpu"); got != 50 {
		t.Errorf("1d cpu = %v, want 50", got)
	}
}
