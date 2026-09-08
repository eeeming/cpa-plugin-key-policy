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

func TestParseCPAMPPricesExpandsContextAndServiceTiers(t *testing.T) {
	raw := []byte(`{"prices":{"gpt-5.6-sol":{
		"prompt":5,"completion":30,"cacheRead":0.5,"cacheCreation":6.25,
		"contextTiers":[{"thresholdTokens":272000,"prompt":10,"completion":45,"cacheRead":1,"cacheCreation":12.5}],
		"serviceTiers":[{"mode":"fast","serviceTier":"priority","prompt":10,"completion":60,"cacheRead":1,"cacheCreation":12.5}]
	}}}`)
	list, err := ParseModelPrices(raw)
	if err != nil {
		t.Fatal(err)
	}
	base, ok := MatchPrice(list, "openai", "gpt-5.6-sol", "standard", 1000)
	if !ok || base.InputPricePerMillion != 5 || base.CacheReadPricePerMillion != 0.5 || base.CacheWritePricePerMillion != 6.25 {
		t.Fatalf("base = %+v ok=%v", base, ok)
	}
	prio, ok := MatchPrice(list, "openai", "gpt-5.6-sol", "priority", 1000)
	if !ok || prio.InputPricePerMillion != 10 || prio.OutputPricePerMillion != 60 {
		t.Fatalf("priority = %+v ok=%v", prio, ok)
	}
	fast, ok := MatchPrice(list, "openai", "gpt-5.6-sol", "fast", 1000)
	if !ok || fast.InputPricePerMillion != 10 {
		t.Fatalf("fast = %+v ok=%v", fast, ok)
	}
	long, ok := MatchPrice(list, "openai", "gpt-5.6-sol", "standard", 272001)
	if !ok || long.MinInputTokens != 272001 || long.InputPricePerMillion != 10 || long.OutputPricePerMillion != 45 {
		t.Fatalf("long context = %+v ok=%v", long, ok)
	}
	justUnder, ok := MatchPrice(list, "openai", "gpt-5.6-sol", "standard", 272000)
	if !ok || justUnder.InputPricePerMillion != 5 {
		t.Fatalf("at threshold stays base = %+v ok=%v", justUnder, ok)
	}
	// Plus: long-context wildcard wins over priority (no stacking).
	longPrio, ok := MatchPlusPrice(list, "openai", "gpt-5.6-sol", "", "priority", 272001)
	if !ok || longPrio.MinInputTokens != 272001 || longPrio.OutputPricePerMillion != 45 {
		t.Fatalf("priority+long should use context 45 not priority 60: %+v ok=%v", longPrio, ok)
	}
	shortPrio, ok := MatchPlusPrice(list, "openai", "gpt-5.6-sol", "", "priority", 1000)
	if !ok || shortPrio.OutputPricePerMillion != 60 {
		t.Fatalf("short priority = %+v ok=%v", shortPrio, ok)
	}
}

func TestMatchPlusPriceUsesListedNameNotFamilyPrefix(t *testing.T) {
	// Prices come from Plus JSON keys. A newly published model is billed as
	// soon as it appears in the table; there is no gpt-5*/gpt-6* (or any
	// other) family hardcoding in the matcher.
	raw := []byte(`{"prices":{
		"gpt-6-codex":{"prompt":1.25,"completion":10,"cacheRead":0.125,"cacheCreation":1.5625},
		"lab-model-v3":{"prompt":0.2,"completion":0.8}
	}}`)
	list, err := ParseModelPrices(raw)
	if err != nil {
		t.Fatal(err)
	}
	codex, ok := MatchPlusPrice(list, "openai", "gpt-6-codex", "", "standard", 1000)
	if !ok || codex.InputPricePerMillion != 1.25 || codex.OutputPricePerMillion != 10 {
		t.Fatalf("listed gpt-6-codex = %+v ok=%v", codex, ok)
	}
	lab, ok := MatchPlusPrice(list, "", "lab-model-v3", "", "priority", 1000)
	if !ok || lab.InputPricePerMillion != 0.2 || lab.OutputPricePerMillion != 0.8 {
		t.Fatalf("listed lab-model-v3 = %+v ok=%v", lab, ok)
	}
	if _, ok := MatchPlusPrice(list, "openai", "gpt-6", "", "standard", 1000); ok {
		t.Fatal("unlisted sibling name must not inherit a family prefix")
	}
	if _, ok := MatchPlusPrice(list, "openai", "gpt-5.6-sol", "", "standard", 1000); ok {
		t.Fatal("unlisted older family name must not match")
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
