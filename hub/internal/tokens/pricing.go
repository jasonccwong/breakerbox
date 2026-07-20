package tokens

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pocketbase/pocketbase/core"

	"github.com/breakerbox/breakerbox/pkg/protocol"
)

// pricingSourceURL is LiteLLM's community-maintained price table — the same
// source ccusage uses. Fetched only on explicit user refresh, never at boot.
const pricingSourceURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"

//go:embed pricing.json
var embeddedPricing []byte

// modelPrice is per-token USD pricing for one model.
type modelPrice struct {
	Input         float64 `json:"input_cost_per_token"`
	Output        float64 `json:"output_cost_per_token"`
	CacheCreation float64 `json:"cache_creation_input_token_cost"`
	CacheRead     float64 `json:"cache_read_input_token_cost"`
}

type pricingTable struct {
	mu     sync.RWMutex
	models map[string]modelPrice
}

// overridePath is where a refreshed table persists (survives restarts,
// preferred over the embedded snapshot).
func overridePath(app core.App) string {
	return filepath.Join(app.DataDir(), "pricing.json")
}

func loadPricing(app core.App) *pricingTable {
	t := &pricingTable{models: map[string]modelPrice{}}
	if b, err := os.ReadFile(overridePath(app)); err == nil && t.parse(b) == nil {
		return t
	}
	_ = t.parse(embeddedPricing)
	return t
}

func (t *pricingTable) parse(b []byte) error {
	raw := map[string]modelPrice{}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	cleaned := map[string]modelPrice{}
	for name, p := range raw {
		if p.Input > 0 || p.Output > 0 {
			cleaned[name] = p
		}
	}
	if len(cleaned) == 0 {
		return fmt.Errorf("pricing table empty")
	}
	t.mu.Lock()
	t.models = cleaned
	t.mu.Unlock()
	return nil
}

// lookup finds a model's pricing: exact name, then progressively shorter
// dash-prefixes (handles dated variants like claude-fable-5-20260301).
func (t *pricingTable) lookup(model string) (modelPrice, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if p, ok := t.models[model]; ok {
		return p, true
	}
	parts := strings.Split(model, "-")
	for i := len(parts) - 1; i >= 2; i-- {
		if p, ok := t.models[strings.Join(parts[:i], "-")]; ok {
			return p, true
		}
	}
	return modelPrice{}, false
}

// cost prices one usage row. Unknown models cost 0 and report priced=false.
func (t *pricingTable) cost(row protocol.TokenUsageRow) (float64, bool) {
	p, ok := t.lookup(row.Model)
	if !ok {
		return 0, false
	}
	c := float64(row.InputTokens)*p.Input +
		float64(row.OutputTokens)*p.Output +
		float64(row.CacheCreationTokens)*p.CacheCreation +
		float64(row.CacheReadTokens)*p.CacheRead
	return c, true
}

// refresh downloads the latest table, persists it, and swaps it in. Returns
// the model count.
func (t *pricingTable) refresh(app core.App) (int, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(pricingSourceURL)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("pricing source returned %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return 0, err
	}
	if err := t.parse(b); err != nil {
		return 0, err
	}
	if err := os.WriteFile(overridePath(app), b, 0o600); err != nil {
		return 0, err
	}
	t.mu.RLock()
	n := len(t.models)
	t.mu.RUnlock()
	return n, nil
}
