package policy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ModelPrice is one Plus / Home billing_model_price row.
type ModelPrice struct {
	Provider                 string  `json:"provider"`
	Model                    string  `json:"model"`
	ServiceTier              string  `json:"service_tier"`
	MinInputTokens           int64   `json:"min_input_tokens"`
	InputPricePerMillion     float64 `json:"input_price_per_million"`
	OutputPricePerMillion    float64 `json:"output_price_per_million"`
	CacheReadPricePerMillion float64 `json:"cache_read_price_per_million"`
	CacheWritePricePerMillion float64 `json:"cache_write_price_per_million"`
	RequestPrice             float64 `json:"request_price"`
	Enabled                  bool    `json:"enabled"`
}

// PriceLister fetches the current Plus model-price table.
type PriceLister func() ([]ModelPrice, error)

const priceCacheTTL = 30 * time.Second

// NormalizeServiceTier maps Plus aliases onto the local Standard tier.
func NormalizeServiceTier(tier string) string {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "", "auto", "default", "standard":
		return "standard"
	case "*":
		return "*"
	default:
		return strings.ToLower(strings.TrimSpace(tier))
	}
}

// MatchPrice applies Plus matching: exact normalized service_tier, then `*`,
// then the greatest min_input_tokens not exceeding inputTokens.
func MatchPrice(prices []ModelPrice, provider, model, serviceTier string, inputTokens int64) (ModelPrice, bool) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return ModelPrice{}, false
	}
	wantTier := NormalizeServiceTier(serviceTier)

	var exact, wildcard []ModelPrice
	for _, p := range prices {
		if !p.Enabled {
			continue
		}
		if strings.ToLower(strings.TrimSpace(p.Model)) != model {
			continue
		}
		ruleProvider := strings.ToLower(strings.TrimSpace(p.Provider))
		if provider != "" && ruleProvider != "" && ruleProvider != provider {
			continue
		}
		if p.MinInputTokens > inputTokens {
			continue
		}
		tier := NormalizeServiceTier(p.ServiceTier)
		if p.ServiceTier == "*" || tier == "*" {
			wildcard = append(wildcard, p)
			continue
		}
		if tier == wantTier {
			exact = append(exact, p)
		}
	}
	pool := exact
	if len(pool) == 0 {
		pool = wildcard
	}
	if len(pool) == 0 {
		return ModelPrice{}, false
	}
	best := pool[0]
	for _, p := range pool[1:] {
		if p.MinInputTokens > best.MinInputTokens {
			best = p
		}
	}
	return best, true
}

// HTTPPriceLister GETs Plus GET /v0/management/billing/model-prices.
func HTTPPriceLister(client *http.Client, baseURL, managementKey string) PriceLister {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	managementKey = strings.TrimSpace(managementKey)
	if client == nil {
		client = http.DefaultClient
	}
	return func() ([]ModelPrice, error) {
		if baseURL == "" {
			return nil, fmt.Errorf("plus_base_url is empty")
		}
		req, err := http.NewRequest(http.MethodGet, baseURL+"/v0/management/billing/model-prices", nil)
		if err != nil {
			return nil, err
		}
		if managementKey != "" {
			req.Header.Set("Authorization", "Bearer "+managementKey)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("plus model-prices: HTTP %d", resp.StatusCode)
		}
		return ParseModelPrices(body)
	}
}

type wirePrice struct {
	Provider                  string   `json:"provider"`
	Model                     string   `json:"model"`
	ServiceTier               string   `json:"service_tier"`
	MinInputTokens            int64    `json:"min_input_tokens"`
	InputPricePerMillion      float64  `json:"input_price_per_million"`
	OutputPricePerMillion     float64  `json:"output_price_per_million"`
	CacheReadPricePerMillion  float64  `json:"cache_read_price_per_million"`
	CacheWritePricePerMillion float64  `json:"cache_write_price_per_million"`
	RequestPrice              float64  `json:"request_price"`
	Enabled                   *bool    `json:"enabled"`
}

func ParseModelPrices(raw []byte) ([]ModelPrice, error) {
	var wrapped struct {
		Items       []wirePrice `json:"items"`
		ModelPrices []wirePrice `json:"model_prices"`
	}
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		var list []wirePrice
		if err2 := json.Unmarshal(raw, &list); err2 != nil {
			return nil, err
		}
		return normalizeListedPrices(list), nil
	}
	list := wrapped.Items
	if len(list) == 0 {
		list = wrapped.ModelPrices
	}
	return normalizeListedPrices(list), nil
}

func normalizeListedPrices(list []wirePrice) []ModelPrice {
	out := make([]ModelPrice, 0, len(list))
	for _, p := range list {
		enabled := true
		if p.Enabled != nil {
			enabled = *p.Enabled
		}
		model := strings.TrimSpace(p.Model)
		if model == "" {
			continue
		}
		tier := strings.TrimSpace(p.ServiceTier)
		if tier == "" {
			tier = "*"
		}
		out = append(out, ModelPrice{
			Provider:                  strings.TrimSpace(p.Provider),
			Model:                     model,
			ServiceTier:               tier,
			MinInputTokens:            p.MinInputTokens,
			InputPricePerMillion:      p.InputPricePerMillion,
			OutputPricePerMillion:     p.OutputPricePerMillion,
			CacheReadPricePerMillion:  p.CacheReadPricePerMillion,
			CacheWritePricePerMillion: p.CacheWritePricePerMillion,
			RequestPrice:              p.RequestPrice,
			Enabled:                   enabled,
		})
	}
	return out
}
