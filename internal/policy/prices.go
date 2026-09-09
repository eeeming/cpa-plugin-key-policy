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

const (
	priceCacheTTL = 30 * time.Second
	// priceHTTPTimeout bounds one price HTTP request.
	priceHTTPTimeout = 15 * time.Second
	// priceFetchWait bounds how long a cold-start request waits for the
	// in-flight first fetch. The lister may issue two requests (the CPAMP path,
	// then the Home/Plus fallback after a 404), so the bound must cover both
	// plus margin; otherwise waiters would give up while the fetch is still
	// running and Admit would skip the USD gate (fail open).
	priceFetchWait = 2*priceHTTPTimeout + 5*time.Second
)

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

// MatchPlusPrice follows Plus costForPriceWithServiceTier: a long-context
// wildcard band (min_input_tokens > 0) wins over service-tier / priority
// prices. Otherwise exact service_tier, then `*`. Tries model then alias.
func MatchPlusPrice(prices []ModelPrice, provider, model, alias, serviceTier string, inputTokens int64) (ModelPrice, bool) {
	try := func(name string) (ModelPrice, bool) {
		name = strings.TrimSpace(name)
		if name == "" {
			return ModelPrice{}, false
		}
		context, ok := MatchPrice(prices, provider, name, "*", inputTokens)
		if ok && context.MinInputTokens > 0 {
			return context, true
		}
		return MatchPrice(prices, provider, name, serviceTier, inputTokens)
	}
	if rule, ok := try(model); ok {
		return rule, true
	}
	if strings.TrimSpace(alias) != "" && !strings.EqualFold(alias, model) {
		return try(alias)
	}
	return ModelPrice{}, false
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
		client = &http.Client{Timeout: priceHTTPTimeout}
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
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
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
	Prompt        float64            `json:"prompt"`
	Completion    float64            `json:"completion"`
	Cache         float64            `json:"cache"`
	CacheRead     float64            `json:"cacheRead"`
	CacheWrite    float64            `json:"cacheWrite"`
	CacheCreation float64            `json:"cacheCreation"`
	RequestPrice  float64            `json:"requestPrice"`
	ContextTiers  []cpampContextTier `json:"contextTiers"`
	ServiceTiers  []cpampServiceTier `json:"serviceTiers"`
}

type cpampContextTier struct {
	ThresholdTokens int64   `json:"thresholdTokens"`
	Prompt          float64 `json:"prompt"`
	Completion      float64 `json:"completion"`
	Cache           float64 `json:"cache"`
	CacheRead       float64 `json:"cacheRead"`
	CacheCreation   float64 `json:"cacheCreation"`
}

type cpampServiceTier struct {
	Mode          string  `json:"mode"`
	ServiceTier   string  `json:"serviceTier"`
	Prompt        float64 `json:"prompt"`
	Completion    float64 `json:"completion"`
	Cache         float64 `json:"cache"`
	CacheRead     float64 `json:"cacheRead"`
	CacheCreation float64 `json:"cacheCreation"`
}

func parseCPAMPPrices(raw json.RawMessage) ([]ModelPrice, error) {
	var m map[string]cpampPrice
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	out := make([]ModelPrice, 0, len(m)*3)
	for model, p := range m {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		out = append(out, cpampRow(model, "*", 0, p.Prompt, p.Completion, p.Cache, p.CacheRead, p.CacheWrite, p.CacheCreation, p.RequestPrice))
		for _, tier := range p.ContextTiers {
			minTok := tier.ThresholdTokens
			if minTok > 0 {
				minTok++ // Plus: high band when input is strictly greater than threshold.
			}
			out = append(out, cpampRow(model, "*", minTok, tier.Prompt, tier.Completion, tier.Cache, tier.CacheRead, 0, tier.CacheCreation, p.RequestPrice))
		}
		for _, tier := range p.ServiceTiers {
			name := strings.ToLower(strings.TrimSpace(tier.ServiceTier))
			if name == "" {
				name = strings.ToLower(strings.TrimSpace(tier.Mode))
			}
			if name == "" {
				continue
			}
			row := cpampRow(model, name, 0, tier.Prompt, tier.Completion, tier.Cache, tier.CacheRead, 0, tier.CacheCreation, p.RequestPrice)
			out = append(out, row)
			if mode := strings.ToLower(strings.TrimSpace(tier.Mode)); mode != "" && mode != name {
				dup := row
				dup.ServiceTier = mode
				out = append(out, dup)
			}
		}
	}
	return out, nil
}

func cpampRow(model, serviceTier string, minTok int64, prompt, completion, cache, cacheRead, cacheWrite, cacheCreation, request float64) ModelPrice {
	read := cacheRead
	if read == 0 {
		read = cache
	}
	if read == 0 && prompt != 0 {
		read = prompt * 0.1 // Plus fallback when cache-read is unset.
	}
	write := cacheWrite
	if write == 0 {
		write = cacheCreation
	}
	if serviceTier == "" {
		serviceTier = "*"
	}
	return ModelPrice{
		Model:                     model,
		ServiceTier:               serviceTier,
		MinInputTokens:            minTok,
		InputPricePerMillion:      prompt,
		OutputPricePerMillion:     completion,
		CacheReadPricePerMillion:  read,
		CacheWritePricePerMillion: write,
		RequestPrice:              request,
		Enabled:                   true,
	}
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
