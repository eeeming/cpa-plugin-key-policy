package policy

import (
	"fmt"
	"net/http"
	"path/filepath"
	"sync"
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
	// Synthetic subset-provider split: uncached 100000, cache-read 400000, out 4000.
	cost := store.RecordUsage("sk-paid", "gpt-5.6-sol", "gpt-5.6-sol", "openai", "standard", false, UsageDetail{
		InputTokens: 100000 + 400000, OutputTokens: 4000, CacheReadTokens: 400000,
	})
	want := 100000.0*5/1e6 + 400000.0*0.5/1e6 + 4000.0*30/1e6
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

func TestPriceFetchErrorKeepsLastGoodTable(t *testing.T) {
	store := configureQuotaStore(t)
	ok := true
	calls := 0
	store.SetPriceLister(func() ([]ModelPrice, error) {
		calls++
		if !ok {
			return nil, errUnavailable
		}
		return []ModelPrice{{Model: "m", ServiceTier: "*", InputPricePerMillion: 1000, Enabled: true}}, nil
	})
	bindTestKey(t, store, "paid", "sk-paid", 100, 1)
	if !store.PricesAvailable() {
		t.Fatal("expected first fetch to succeed")
	}
	ok = false
	if !store.PricesAvailable() {
		t.Fatal("transient Plus error must keep last good prices")
	}
	if calls != 1 {
		t.Fatalf("still inside TTL: calls=%d", calls)
	}
	store.priceFetchedAt = time.Now().Add(-time.Minute)
	if !store.PricesAvailable() {
		t.Fatal("expired TTL with last-good table should still report prices")
	}
	if calls != 2 {
		t.Fatalf("one retry after TTL: calls=%d", calls)
	}
	if !store.PricesAvailable() {
		t.Fatal("backoff window should still serve last-good prices")
	}
	if calls != 2 {
		t.Fatalf("error must not refetch inside TTL: calls=%d", calls)
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

func TestKeysOrderedByLastAccess(t *testing.T) {
	store := NewStore()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return now })
	if err := store.Configure(Config{Enabled: true, StateFile: filepath.Join(t.TempDir(), "state.json")}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.StopUsageFlusher)
	bindTestKey(t, store, "older", "sk-older", 10, 0)
	bindTestKey(t, store, "newer", "sk-newer", 10, 0)
	listed := store.Keys()
	if len(listed) != 2 || listed[0].ID != "newer" {
		t.Fatalf("unused keys should put newest created first: %+v", listed)
	}
	now = now.Add(time.Minute)
	_ = store.Admit(http.Header{"Authorization": {"Bearer sk-older"}}, nil, nil)
	listed = store.Keys()
	if listed[0].ID != "older" {
		t.Fatalf("last access should be first: %+v", listed)
	}
	now = now.Add(time.Minute)
	_ = store.Admit(http.Header{"Authorization": {"Bearer sk-newer"}}, nil, nil)
	listed = store.Keys()
	if listed[0].ID != "newer" {
		t.Fatalf("newer access should move to front: %+v", listed)
	}
}

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

func TestDailyAndWeeklyWindowsRollFromFirstUse(t *testing.T) {
	now := time.Date(2026, 9, 8, 15, 4, 0, 0, time.UTC)
	ledger := newUsageLedger(func() time.Time { return now })
	key := KeyConfig{ID: "k", DailyLimitUSD: 10, WeeklyLimitUSD: 50}

	before := ledger.Summary(key)
	if before.DailyResetAt != nil || before.WeeklyResetAt != nil {
		t.Fatalf("unused key should have no reset times: %+v", before)
	}

	ledger.RecordCost("k", "m", 1, 0, 0, 1, 0, 1)
	s := ledger.Summary(key)
	if !nearly(s.DailyUSD, 1) || !nearly(s.WeeklyUSD, 1) {
		t.Fatalf("after first bill: %+v", s)
	}
	if s.DailyResetAt == nil || !s.DailyResetAt.Equal(now.Add(24*time.Hour)) {
		t.Fatalf("daily reset = %v, want %v", s.DailyResetAt, now.Add(24*time.Hour))
	}
	if s.WeeklyResetAt == nil || !s.WeeklyResetAt.Equal(now.Add(7*24*time.Hour)) {
		t.Fatalf("weekly reset = %v, want %v", s.WeeklyResetAt, now.Add(7*24*time.Hour))
	}

	// Crossing UTC midnight must not reset a rolling 24h window.
	now = now.Add(12 * time.Hour) // 2026-09-09 03:04 UTC
	s = ledger.Summary(key)
	if !nearly(s.DailyUSD, 1) {
		t.Fatalf("still inside 24h after midnight: %+v", s)
	}

	now = now.Add(13 * time.Hour) // 25h from start
	s = ledger.Summary(key)
	if !nearly(s.DailyUSD, 0) {
		t.Fatalf("daily should roll after 24h: %+v", s)
	}
	if s.DailyResetAt != nil {
		t.Fatalf("idle expired daily must omit reset: %+v", s)
	}
	if !nearly(s.WeeklyUSD, 1) {
		t.Fatalf("weekly still open: %+v", s)
	}

	now = now.Add(7 * 24 * time.Hour)
	s = ledger.Summary(key)
	if !nearly(s.WeeklyUSD, 0) {
		t.Fatalf("weekly should roll after 7d: %+v", s)
	}
	if s.WeeklyResetAt != nil {
		t.Fatalf("idle expired weekly must omit reset: %+v", s)
	}
}

func TestUsageSnapshotCopiesByAlias(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	ledger := newUsageLedger(func() time.Time { return now })
	ledger.RecordCost("k", "m", 1, 0, 0, 10, 1, 1)
	snap := ledger.snapshot()
	ledger.RecordCost("k", "m", 2, 0, 0, 10, 1, 1)
	if got := snap["k"].Daily.TotalUSD; got != 1 {
		t.Fatalf("snapshot daily = %v, want 1", got)
	}
	if got := snap["k"].ByAlias["m"].Daily.TotalUSD; got != 1 {
		t.Fatalf("snapshot by_alias mutated after RecordCost: %v", got)
	}
	if got := ledger.entries["k"].ByAlias["m"].Daily.TotalUSD; got != 3 {
		t.Fatalf("live by_alias = %v, want 3", got)
	}
}

func TestPersistKeepsMemoryAndDiskAlignedUnderConcurrency(t *testing.T) {
	store := configureQuotaStore(t)
	store.SetPriceLister(func() ([]ModelPrice, error) {
		return []ModelPrice{{
			Model: "m", ServiceTier: "*", InputPricePerMillion: 1000, Enabled: true,
		}}, nil
	})
	bindTestKey(t, store, "paid", "sk-paid", 0, 0)

	const n = 40
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			store.RecordUsage("sk-paid", "m", "m", "openai", "standard", false, UsageDetail{InputTokens: 1000})
			_ = store.FlushUsage()
		}()
	}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			k := store.Keys()[0]
			k.Name = fmt.Sprintf("paid-%d", i)
			if err := store.UpsertKey(k, true); err != nil {
				t.Errorf("upsert: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if err := store.FlushUsage(); err != nil {
		t.Fatal(err)
	}

	mem := store.UsageSummaryFor(store.Keys()[0])
	want := float64(n) // 1000 tokens @ $1000/M
	if !nearly(mem.DailyUSD, want) {
		t.Fatalf("memory daily = %v, want %v", mem.DailyUSD, want)
	}
	st, err := LoadState(store.StatePath())
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Keys) != 1 || st.Keys[0].ID != "paid" {
		t.Fatalf("keys = %+v", st.Keys)
	}
	disk := st.Usage["paid"]
	if disk == nil {
		t.Fatal("missing disk usage")
	}
	if !nearly(disk.Daily.TotalUSD, mem.DailyUSD) {
		t.Fatalf("disk daily = %v, memory = %v", disk.Daily.TotalUSD, mem.DailyUSD)
	}
	if !nearly(disk.ByAlias["m"].Daily.TotalUSD, mem.DailyUSD) {
		t.Fatalf("disk by_alias = %+v", disk.ByAlias["m"])
	}
}
