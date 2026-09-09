package policy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// An operator who removes plus_base_url and reconfigures the plugin must stop
// USD billing: the previously configured price source must not survive.
func TestConfigureDropsPriceSourceWhenPlusURLCleared(t *testing.T) {
	store := NewStore()
	dir := t.TempDir()
	if err := store.Configure(Config{Enabled: true, StateFile: filepath.Join(dir, "state.json")}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.StopUsageFlusher)
	store.SetPriceLister(func() ([]ModelPrice, error) {
		return []ModelPrice{{Model: "m", ServiceTier: "*", InputPricePerMillion: 1, Enabled: true}}, nil
	})
	if !store.PricesAvailable() {
		t.Fatal("expected prices available after SetPriceLister")
	}
	if err := store.Configure(Config{Enabled: true, StateFile: filepath.Join(dir, "state.json")}); err != nil {
		t.Fatal(err)
	}
	if store.PricesAvailable() {
		t.Fatal("empty plus_base_url must drop the price source, not keep billing from it")
	}
}

// Once a price source is configured, a reconfigure must re-sync the latest
// table instead of serving the cached one for the rest of the TTL.
func TestConfigureResyncsLatestPriceTable(t *testing.T) {
	var calls int32
	var payload atomic.Value
	payload.Store(`{"prices":{"m":{"prompt":1}}}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != cpampModelPricesPath {
			http.NotFound(w, r)
			return
		}
		atomic.AddInt32(&calls, 1)
		_, _ = w.Write([]byte(payload.Load().(string)))
	}))
	t.Cleanup(srv.Close)

	store := NewStore()
	cfg := Config{
		Enabled:     true,
		StateFile:   filepath.Join(t.TempDir(), "state.json"),
		PlusBaseURL: srv.URL,
	}
	if err := store.Configure(cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.StopUsageFlusher)
	if !store.PricesAvailable() {
		t.Fatal("expected prices from the configured source")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("first fetch calls = %d, want 1", got)
	}

	// Operator edits the Plus price table, then the host reconfigures.
	payload.Store(`{"prices":{"m":{"prompt":9}}}`)
	if err := store.Configure(cfg); err != nil {
		t.Fatal(err)
	}
	if !store.PricesAvailable() {
		t.Fatal("expected prices after reconfigure")
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("reconfigure must refetch, calls = %d, want 2", got)
	}
	prices, ok := store.cachedPrices()
	if !ok || len(prices) != 1 || prices[0].InputPricePerMillion != 9 {
		t.Fatalf("reconfigure served a stale table: %+v", prices)
	}
}

// A fetch that is still in flight when the source is replaced must not commit
// its (now stale) result on top of the new source's cache.
func TestStalePriceFetchDiscardedAfterReconfigure(t *testing.T) {
	store := NewStore()
	if err := store.Configure(Config{Enabled: true, StateFile: filepath.Join(t.TempDir(), "state.json")}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.StopUsageFlusher)

	started := make(chan struct{})
	release := make(chan struct{})
	store.SetPriceLister(func() ([]ModelPrice, error) {
		close(started)
		<-release
		return []ModelPrice{{Model: "stale", ServiceTier: "*", InputPricePerMillion: 1, Enabled: true}}, nil
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = store.PricesAvailable()
	}()
	<-started

	// Replace the source while the first fetch is blocked.
	store.SetPriceLister(func() ([]ModelPrice, error) {
		return []ModelPrice{{Model: "fresh", ServiceTier: "*", InputPricePerMillion: 2, Enabled: true}}, nil
	})
	close(release)
	<-done

	if !store.PricesAvailable() {
		t.Fatal("fresh source must be fetched")
	}
	prices, ok := store.cachedPrices()
	if !ok || len(prices) != 1 || prices[0].Model != "fresh" {
		t.Fatalf("stale in-flight result was committed: %+v", prices)
	}
}

// A request that arrives while the first price fetch is still in flight must
// wait for it instead of admitting with USD enforcement disabled.
func TestColdStartWaitsForInFlightPriceFetch(t *testing.T) {
	store := NewStore()
	if err := store.Configure(Config{Enabled: true, StateFile: filepath.Join(t.TempDir(), "state.json")}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.StopUsageFlusher)

	started := make(chan struct{})
	release := make(chan struct{})
	var calls int32
	store.SetPriceLister(func() ([]ModelPrice, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			close(started)
			<-release
		}
		return []ModelPrice{{Model: "m", ServiceTier: "*", InputPricePerMillion: 1, Enabled: true}}, nil
	})

	first := make(chan bool, 1)
	go func() { first <- store.PricesAvailable() }()
	<-started

	second := make(chan bool, 1)
	go func() { second <- store.PricesAvailable() }()
	select {
	case got := <-second:
		t.Fatalf("second caller returned early with prices=%v", got)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	if !<-first {
		t.Fatal("first caller should see the price table")
	}
	if !<-second {
		t.Fatal("second caller should see the price table after waiting")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("price fetch calls = %d, want 1 (waiters must not refetch)", got)
	}
}

// TestCPAMPPricesUnsetTierFieldsStayZero pins the documented behavior: a price
// field the operator did not set is billed at 0, and is not inherited from the
// base row. (Confirmed intended behavior; not a bug.)
func TestCPAMPPricesUnsetTierFieldsStayZero(t *testing.T) {
	raw := []byte(`{"prices":{"m":{"prompt":5,"completion":30,"contextTiers":[{"thresholdTokens":1000}]}}}`)
	list, err := ParseModelPrices(raw)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := MatchPrice(list, "", "m", "standard", 2000)
	if !ok {
		t.Fatal("no match for the long-context band")
	}
	if got.InputPricePerMillion != 0 || got.OutputPricePerMillion != 0 {
		t.Fatalf("unset tier fields should stay 0, got %+v", got)
	}
	// And the payload must still parse without error (unset = 0, not rejected).
	if _, err := json.Marshal(got); err != nil {
		t.Fatal(err)
	}
}
