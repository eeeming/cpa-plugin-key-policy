package policy

import (
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

func configureQuotaStore(t *testing.T) *Store {
	t.Helper()
	store := NewStore()
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return now })
	if err := store.Configure(Config{
		Enabled:   true,
		StateFile: filepath.Join(t.TempDir(), "state.json"),
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.StopUsageFlusher)
	return store
}

func bindTestKey(t *testing.T, store *Store, id, plain string, rpm int, daily float64) {
	t.Helper()
	hash, err := HashKey(plain)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertKey(KeyConfig{
		ID:            id,
		Name:          id,
		Enabled:       true,
		KeyHash:       hash,
		KeyPreview:    PreviewKey(plain),
		CallerScope:   CallerScope(plain),
		RPM:           rpm,
		DailyLimitUSD: daily,
	}, true); err != nil {
		t.Fatal(err)
	}
}

func TestAdmitUnknownAndDisabledAreNoops(t *testing.T) {
	store := configureQuotaStore(t)
	bindTestKey(t, store, "off", "sk-off", 1, 1)
	keys := store.Keys()
	keys[0].Enabled = false
	if err := store.UpsertKey(keys[0], true); err != nil {
		t.Fatal(err)
	}

	unknown := store.Admit(http.Header{"Authorization": {"Bearer sk-other"}}, nil, nil)
	if unknown.Known || unknown.Terminate {
		t.Fatalf("unbound = %+v", unknown)
	}
	disabled := store.Admit(http.Header{"Authorization": {"Bearer sk-off"}}, nil, nil)
	if !disabled.Known || disabled.Terminate || disabled.Reason != "policy_disabled" {
		t.Fatalf("disabled = %+v", disabled)
	}
}

func TestAdmitRPMExceeded(t *testing.T) {
	store := configureQuotaStore(t)
	bindTestKey(t, store, "rpm", "sk-rpm", 1, 0)
	hdr := http.Header{"Authorization": {"Bearer sk-rpm"}}
	first := store.Admit(hdr, nil, nil)
	if first.Terminate {
		t.Fatalf("first = %+v", first)
	}
	second := store.Admit(hdr, nil, nil)
	if !second.Terminate || second.Reason != "rpm_exceeded" || second.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second = %+v", second)
	}
}

func TestRecordUsageBillsMatchedPriceAndBlocksDaily(t *testing.T) {
	store := configureQuotaStore(t)
	store.SetPriceLister(func() ([]ModelPrice, error) {
		return []ModelPrice{{
			Provider: "openai", Model: "gpt-4.1-mini", ServiceTier: "*",
			InputPricePerMillion: 1000, Enabled: true,
		}}, nil
	})
	bindTestKey(t, store, "paid", "sk-paid", 100, 1.0)
	hdr := http.Header{"Authorization": {"Bearer sk-paid"}}
	if d := store.Admit(hdr, nil, nil); d.Terminate {
		t.Fatalf("admit before bill = %+v", d)
	}
	cost := store.RecordUsage("sk-paid", "gpt-4.1-mini", "gpt-4.1-mini", "openai", "default", false, UsageDetail{InputTokens: 1000})
	if !nearly(cost, 1.0) {
		t.Fatalf("cost = %v, want 1.0 (1000 tokens @ $1000/M)", cost)
	}
	over := store.Admit(hdr, nil, nil)
	if !over.Terminate || over.Reason != "daily_exceeded" {
		t.Fatalf("over = %+v", over)
	}
}

func TestRecordUsageMatchesPlusInputOutputCacheSplit(t *testing.T) {
	store := configureQuotaStore(t)
	list, err := ParseModelPrices([]byte(`{"prices":{"gpt-5.6-sol":{"prompt":5,"completion":30,"cacheRead":0.5,"cacheCreation":6.25}}}`))
	if err != nil {
		t.Fatal(err)
	}
	store.SetPriceLister(func() ([]ModelPrice, error) { return list, nil })
	bindTestKey(t, store, "paid", "sk-paid", 100, 100)
	// Live gomami 李益茗 gpt-5.6-sol aggregate: uncached 92686, cache 423424, out 4043.
	cost := store.RecordUsage("sk-paid", "gpt-5.6-sol", "gpt-5.6-sol", "openai", "standard", false, UsageDetail{
		InputTokens: 92686 + 423424, OutputTokens: 4043, CacheReadTokens: 423424,
	})
	want := 92686.0*5/1e6 + 423424.0*0.5/1e6 + 4043.0*30/1e6
	if !nearly(cost, want) {
		t.Fatalf("cost = %v want %v", cost, want)
	}
}

func TestRecordUsageRepricesCacheWritesAndPriority(t *testing.T) {
	store := configureQuotaStore(t)
	list, err := ParseModelPrices([]byte(`{"prices":{"gpt-5.6-sol":{
		"prompt":5,"completion":30,"cacheRead":0.5,"cacheCreation":6.25,
		"serviceTiers":[{"serviceTier":"priority","prompt":10,"completion":60,"cacheRead":1,"cacheCreation":12.5}]
	}}}`))
	if err != nil {
		t.Fatal(err)
	}
	store.SetPriceLister(func() ([]ModelPrice, error) { return list, nil })
	bindTestKey(t, store, "paid", "sk-paid", 100, 100)
	cost := store.RecordUsage("sk-paid", "gpt-5.6-sol", "gpt-5.6-sol", "openai", "priority", false, UsageDetail{
		InputTokens: 1000, OutputTokens: 100, CachedTokens: 200, CacheCreationTokens: 50,
	})
	// Plus: uncached 1000-200-50=750 @10, cache read 200 @1, cache write 50 @12.5, out 100 @60.
	want := 750.0*10/1e6 + 200.0*1/1e6 + 50.0*12.5/1e6 + 100.0*60/1e6
	if !nearly(cost, want) {
		t.Fatalf("priority+write cost = %v want %v", cost, want)
	}
}

func TestMissingPricesSkipUSDButKeepRPM(t *testing.T) {
	store := configureQuotaStore(t)
	store.SetPriceLister(func() ([]ModelPrice, error) {
		return nil, errUnavailable
	})
	bindTestKey(t, store, "bare", "sk-bare", 1, 0.01)
	hdr := http.Header{"Authorization": {"Bearer sk-bare"}}
	_ = store.RecordUsage("sk-bare", "m", "m", "openai", "*", false, UsageDetail{InputTokens: 1_000_000})
	summary := store.UsageSummaryFor(store.Keys()[0])
	if summary.DailyUSD != 0 {
		t.Fatalf("usd billed without prices: %+v", summary)
	}
	if d := store.Admit(hdr, nil, nil); d.Terminate {
		t.Fatalf("first rpm slot should pass: %+v", d)
	}
	if d := store.Admit(hdr, nil, nil); !d.Terminate || d.Reason != "rpm_exceeded" {
		t.Fatalf("rpm should still fire: %+v", d)
	}
}

var errUnavailable = errString("plus unavailable")

type errString string

func (e errString) Error() string { return string(e) }

func TestCallerScopeLookup(t *testing.T) {
	store := configureQuotaStore(t)
	bindTestKey(t, store, "scoped", "sk-scoped", 10, 0)
	scope := CallerScope("sk-scoped")
	d := store.Admit(http.Header{}, nil, map[string]any{"caller_scope": scope})
	if !d.Known || d.KeyID != "scoped" || d.Terminate {
		t.Fatalf("scope admit = %+v", d)
	}
}

func TestDisabledKeyUsageNotRecorded(t *testing.T) {
	store := configureQuotaStore(t)
	store.SetPriceLister(func() ([]ModelPrice, error) {
		return []ModelPrice{{Model: "m", ServiceTier: "*", InputPricePerMillion: 1000, Enabled: true}}, nil
	})
	bindTestKey(t, store, "off", "sk-off", 10, 10)
	k := store.Keys()[0]
	k.Enabled = false
	if err := store.UpsertKey(k, true); err != nil {
		t.Fatal(err)
	}
	if cost := store.RecordUsage("sk-off", "m", "m", "", "*", false, UsageDetail{InputTokens: 1000}); cost != 0 {
		t.Fatalf("disabled billed %v", cost)
	}
}
