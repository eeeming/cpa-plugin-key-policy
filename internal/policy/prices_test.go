package policy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMatchPriceExactTierThenWildcard(t *testing.T) {
	prices := []ModelPrice{
		{Provider: "openai", Model: "gpt-4.1-mini", ServiceTier: "*", MinInputTokens: 0, InputPricePerMillion: 1, Enabled: true},
		{Provider: "openai", Model: "gpt-4.1-mini", ServiceTier: "priority", MinInputTokens: 0, InputPricePerMillion: 2.5, Enabled: true},
	}
	got, ok := MatchPrice(prices, "openai", "gpt-4.1-mini", "priority", 100)
	if !ok || got.InputPricePerMillion != 2.5 {
		t.Fatalf("exact tier = %+v ok=%v, want 2.5", got, ok)
	}
	got, ok = MatchPrice(prices, "openai", "gpt-4.1-mini", "flex", 100)
	if !ok || got.InputPricePerMillion != 1 {
		t.Fatalf("wildcard fallback = %+v ok=%v, want 1", got, ok)
	}
}

func TestMatchPriceGreatestMinInputTokens(t *testing.T) {
	prices := []ModelPrice{
		{Model: "gpt-5.5", ServiceTier: "priority", MinInputTokens: 0, InputPricePerMillion: 1, Enabled: true},
		{Model: "gpt-5.5", ServiceTier: "priority", MinInputTokens: 272001, InputPricePerMillion: 2.5, Enabled: true},
	}
	got, ok := MatchPrice(prices, "", "gpt-5.5", "priority", 272001)
	if !ok || got.MinInputTokens != 272001 || got.InputPricePerMillion != 2.5 {
		t.Fatalf("high band = %+v ok=%v", got, ok)
	}
	got, ok = MatchPrice(prices, "", "gpt-5.5", "priority", 1000)
	if !ok || got.MinInputTokens != 0 || got.InputPricePerMillion != 1 {
		t.Fatalf("low band = %+v ok=%v", got, ok)
	}
}

func TestMatchPriceSkipsDisabledAndNormalizesTier(t *testing.T) {
	prices := []ModelPrice{
		{Model: "m", ServiceTier: "standard", MinInputTokens: 0, InputPricePerMillion: 4, Enabled: true},
		{Model: "m", ServiceTier: "*", MinInputTokens: 0, InputPricePerMillion: 9, Enabled: false},
	}
	got, ok := MatchPrice(prices, "", "m", "auto", 10)
	if !ok || got.InputPricePerMillion != 4 {
		t.Fatalf("auto→standard = %+v ok=%v", got, ok)
	}
}

func TestParseModelPricesEnabledDefaultsTrue(t *testing.T) {
	raw := []byte(`{"items":[{"provider":"openai","model":"gpt-4.1-mini","input_price_per_million":0.4}]}`)
	list, err := ParseModelPrices(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || !list[0].Enabled || list[0].ServiceTier != "*" || list[0].InputPricePerMillion != 0.4 {
		t.Fatalf("parsed %+v", list)
	}
}

func TestParseCPAMPPricesMap(t *testing.T) {
	raw := []byte(`{"prices":{"deepseek-v4-flash":{"prompt":0.14,"completion":0.28,"cacheRead":0.0028}}}`)
	list, err := ParseModelPrices(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("len=%d list=%+v", len(list), list)
	}
	got := list[0]
	if got.Model != "deepseek-v4-flash" {
		t.Fatalf("model=%q", got.Model)
	}
	if got.InputPricePerMillion != 0.14 || got.OutputPricePerMillion != 0.28 {
		t.Fatalf("usd/M input=%v output=%v", got.InputPricePerMillion, got.OutputPricePerMillion)
	}
	if got.CacheReadPricePerMillion != 0.0028 {
		t.Fatalf("cacheRead=%v", got.CacheReadPricePerMillion)
	}
	if !got.Enabled || got.ServiceTier != "*" {
		t.Fatalf("enabled/tier %+v", got)
	}
	matched, ok := MatchPrice(list, "openai-compatible", "deepseek-v4-flash", "standard", 100)
	if !ok || matched.InputPricePerMillion != 0.14 {
		t.Fatalf("match %+v ok=%v", matched, ok)
	}
}

func TestHTTPPriceLister(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/billing/model-prices" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer mgmt" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []ModelPrice{{
				Provider: "openai", Model: "gpt-4.1-mini", ServiceTier: "*",
				InputPricePerMillion: 1.25, Enabled: true,
			}},
		})
	}))
	t.Cleanup(srv.Close)
	list, err := HTTPPriceLister(srv.Client(), srv.URL, "mgmt")()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].InputPricePerMillion != 1.25 {
		t.Fatalf("list = %+v", list)
	}
}

func TestHTTPPriceListerCPAMP(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/v0/management/model-prices" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer admin" {
			http.Error(w, `{"error":"invalid admin key"}`, http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"prices": map[string]any{
				"deepseek-v4-flash": map[string]any{
					"prompt": 0.14, "completion": 0.28, "cacheRead": 0.0028,
				},
			},
		})
	}))
	t.Cleanup(srv.Close)
	list, err := HTTPPriceLister(srv.Client(), srv.URL, "admin")()
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v0/management/model-prices" {
		t.Fatalf("path=%q", gotPath)
	}
	if gotAuth != "Bearer admin" {
		t.Fatalf("auth=%q", gotAuth)
	}
	if len(list) != 1 || list[0].Model != "deepseek-v4-flash" || list[0].InputPricePerMillion != 0.14 || list[0].OutputPricePerMillion != 0.28 {
		t.Fatalf("list = %+v", list)
	}
}
