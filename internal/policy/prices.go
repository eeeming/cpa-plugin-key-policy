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
	Provider                  string  `json:"provider"`
	Model                     string  `json:"model"`
	ServiceTier               string  `json:"service_tier"`
	MinInputTokens            int64   `json:"min_input_tokens"`
	InputPricePerMillion      float64 `json:"input_price_per_million"`
	OutputPricePerMillion     float64 `json:"output_price_per_million"`
	CacheReadPricePerMillion  float64 `json:"cache_read_price_per_million"`
	CacheWritePricePerMillion float64 `json:"cache_write_price_per_million"`
	RequestPrice              float64 `json:"request_price"`
	Enabled                   bool    `json:"enabled"`
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

const (
	cpampModelPricesPath = "/v0/management/model-prices"
	homeModelPricesPath  = "/v0/management/billing/model-prices"
)

// HTTPPriceLister GETs CPA-Manager-Plus GET /v0/management/model-prices
// (Bearer admin key). On HTTP 404 it falls back to Home/Plus
// GET /v0/management/billing/model-prices.
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
		code, body, err := getPricePath(client, baseURL+cpampModelPricesPath, managementKey)
		if err != nil {
			return nil, err
		}
		if code == http.StatusNotFound {
			code, body, err = getPricePath(client, baseURL+homeModelPricesPath, managementKey)
			if err != nil {
				return nil, err
			}
		}
		if code < 200 || code >= 300 {
			return nil, fmt.Errorf("plus model-prices: HTTP %d", code)
		}
		return ParseModelPrices(body)
	}
}

func getPricePath(client *http.Client, url, managementKey string) (int, []byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, err
	}
	if managementKey != "" {
		req.Header.Set("Authorization", "Bearer "+managementKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, body, nil
}

type wirePrice struct {
	Provider                  string  `json:"provider"`
	Model                     string  `json:"model"`
	ServiceTier               string  `json:"service_tier"`
	MinInputTokens            int64   `json:"min_input_tokens"`
	InputPricePerMillion      float64 `json:"input_price_per_million"`
	OutputPricePerMillion     float64 `json:"output_price_per_million"`
	CacheReadPricePerMillion  float64 `json:"cache_read_price_per_million"`
	CacheWritePricePerMillion float64 `json:"cache_write_price_per_million"`
	RequestPrice              float64 `json:"request_price"`
	Enabled                   *bool   `json:"enabled"`
}

func ParseModelPrices(raw []byte) ([]ModelPrice, error) {
	var probe struct {
		Prices      json.RawMessage `json:"prices"`
		Items       []wirePrice     `json:"items"`
		ModelPrices []wirePrice     `json:"model_prices"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		var list []wirePrice
		if err2 := json.Unmarshal(raw, &list); err2 != nil {
			return nil, err
		}
		return normalizeListedPrices(list), nil
	}
	if isJSONObject(probe.Prices) {
		return parseCPAMPPrices(probe.Prices)
	}
	list := probe.Items
	if len(list) == 0 {
		list = probe.ModelPrices
	}
	return normalizeListedPrices(list), nil
}

func isJSONObject(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return len(s) > 0 && s[0] == '{'
}

type cpampPrice struct {
	Prompt       float64 `json:"prompt"`
	Completion   float64 `json:"completion"`
	Cache        float64 `json:"cache"`
	CacheRead    float64 `json:"cacheRead"`
	CacheWrite   float64 `json:"cacheWrite"`
	RequestPrice float64 `json:"requestPrice"`
}

func parseCPAMPPrices(raw json.RawMessage) ([]ModelPrice, error) {
	var m map[string]cpampPrice
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	out := make([]ModelPrice, 0, len(m))
	for model, p := range m {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		cacheRead := p.CacheRead
		if cacheRead == 0 {
			cacheRead = p.Cache
		}
		out = append(out, ModelPrice{
			Model:                     model,
			ServiceTier:               "*",
			MinInputTokens:            0,
			InputPricePerMillion:      p.Prompt,
			OutputPricePerMillion:     p.Completion,
			CacheReadPricePerMillion:  cacheRead,
			CacheWritePricePerMillion: p.CacheWrite,
			RequestPrice:              p.RequestPrice,
			Enabled:                   true,
		})
	}
	return out, nil
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
