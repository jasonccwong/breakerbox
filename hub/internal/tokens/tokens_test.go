package tokens

import (
	"math"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"

	_ "github.com/breakerbox/breakerbox/hub/migrations"
	"github.com/breakerbox/breakerbox/pkg/protocol"
)

func closeTo(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func timeNowMS() int64 { return time.Now().UnixMilli() }

type fakeSender struct{ sent []string }

func (f *fakeSender) Send(title, body, priority, tags string) error {
	f.sent = append(f.sent, title+": "+body)
	return nil
}

func testService(t *testing.T) (*Service, *tests.TestApp, *fakeSender, string) {
	t.Helper()
	app, err := tests.NewTestAppWithConfig(core.BaseAppConfig{DataDir: t.TempDir(), EncryptionEnv: "pb_test_env"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Cleanup)
	fs := &fakeSender{}
	s := &Service{app: app, notify: fs, pricing: loadPricing(app), unpricedSeen: map[string]bool{}}

	col, err := app.FindCollectionByNameOrId("systems")
	if err != nil {
		t.Fatal(err)
	}
	sys := core.NewRecord(col)
	sys.Set("name", "test-system")
	sys.Set("public_key", "AAAA")
	sys.Set("status", "online")
	if err := app.Save(sys); err != nil {
		t.Fatal(err)
	}
	return s, app, fs, sys.Id
}

func row(key string, out int64) protocol.TokenUsageRow {
	return protocol.TokenUsageRow{
		DedupKey: key, Source: "claude_code", Model: "claude-fable-5",
		InputTokens: 100, OutputTokens: out, CacheCreationTokens: 1000, CacheReadTokens: 2000,
		SessionID: "s1", OccurredAtMS: 1752988800000, // 2025-07-20T05:20Z-ish; value irrelevant
	}
}

func TestIngestPricesAndStores(t *testing.T) {
	s, app, _, sysID := testService(t)
	if err := s.Ingest(sysID, protocol.TokenUsageBatch{Rows: []protocol.TokenUsageRow{row("k1", 50)}}); err != nil {
		t.Fatal(err)
	}
	rec, err := app.FindFirstRecordByFilter("token_usage", "dedup_key = 'k1'")
	if err != nil {
		t.Fatal(err)
	}
	// claude-fable-5: in 1e-5, out 5e-5, cacheCreate 1.25e-5, cacheRead 1e-6.
	want := 100*1e-5 + 50*5e-5 + 1000*1.25e-5 + 2000*1e-6
	if got := rec.GetFloat("cost_usd"); !closeTo(got, want) {
		t.Errorf("cost = %v, want %v", got, want)
	}
	if rec.GetString("app") != "" {
		t.Errorf("unmatched row must land in the system bucket (empty app), got %q", rec.GetString("app"))
	}
	if rec.GetString("system") != sysID {
		t.Errorf("system = %q", rec.GetString("system"))
	}
}

func TestIngestDedupUpsertOnGrowth(t *testing.T) {
	s, app, _, sysID := testService(t)
	// Same dedup key three times: initial, no-growth duplicate, growth.
	_ = s.Ingest(sysID, protocol.TokenUsageBatch{Rows: []protocol.TokenUsageRow{row("k1", 100)}})
	_ = s.Ingest(sysID, protocol.TokenUsageBatch{Rows: []protocol.TokenUsageRow{row("k1", 100)}})
	_ = s.Ingest(sysID, protocol.TokenUsageBatch{Rows: []protocol.TokenUsageRow{row("k1", 245)}})

	recs, err := app.FindRecordsByFilter("token_usage", "dedup_key = 'k1'", "", 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("dedup failed: %d records for one key", len(recs))
	}
	if got := recs[0].GetFloat("output_tokens"); got != 245 {
		t.Errorf("output_tokens = %v, want the grown value 245", got)
	}
}

func TestPricingPrefixFallback(t *testing.T) {
	s, _, _, _ := testService(t)
	r := row("k", 10)
	r.Model = "claude-fable-5-20260301" // dated variant not in the table
	cost, priced := s.pricing.cost(r)
	if !priced || cost <= 0 {
		t.Errorf("dated model variant should price via prefix fallback, got priced=%v cost=%v", priced, cost)
	}
	r.Model = "totally-unknown-model"
	cost, priced = s.pricing.cost(r)
	if priced || cost != 0 {
		t.Errorf("unknown model must be unpriced/zero, got priced=%v cost=%v", priced, cost)
	}
}

func TestSpendAlertFiresOnceAcrossThreshold(t *testing.T) {
	s, app, fs, sysID := testService(t)
	settings, err := app.FindFirstRecordByFilter("settings", "")
	if err != nil {
		t.Fatal(err)
	}
	settings.Set("ntfy_endpoint", "http://example.invalid/topic") // Send is faked
	settings.Set("notify_spend", true)
	settings.Set("spend_threshold_usd", 0.01)
	if err := app.Save(settings); err != nil {
		t.Fatal(err)
	}

	// One big row today: cost ~0.036 > threshold.
	big := row("k1", 500)
	big.OccurredAtMS = timeNowMS()
	_ = s.Ingest(sysID, protocol.TokenUsageBatch{Rows: []protocol.TokenUsageRow{big}})
	if len(fs.sent) != 1 {
		t.Fatalf("want exactly 1 spend alert, got %d (%v)", len(fs.sent), fs.sent)
	}
	// More spend the same day: no second alert.
	big2 := row("k2", 500)
	big2.OccurredAtMS = timeNowMS()
	_ = s.Ingest(sysID, protocol.TokenUsageBatch{Rows: []protocol.TokenUsageRow{big2}})
	if len(fs.sent) != 1 {
		t.Fatalf("spend alert must fire once per day, got %d", len(fs.sent))
	}
}
