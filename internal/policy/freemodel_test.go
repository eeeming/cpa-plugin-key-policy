package policy

import (
	"net/http"
	"testing"
)

func pricedStore(t *testing.T, prices []ModelPrice, dailyLimit float64) *Store {
	t.Helper()
	store := configureQuotaStore(t)
	store.SetPriceLister(func() ([]ModelPrice, error) { return prices, nil })
	if !store.PricesAvailable() {
		t.Fatal("prices should be available")
	}
	bindTestKey(t, store, "k", "sk-k", 100, dailyLimit)
	return store
}

func admitModel(t *testing.T, store *Store, model string) AdmitDecision {
	t.Helper()
	return store.AdmitRequest(GateRequest{
		Headers: http.Header{"Authorization": {"Bearer sk-k"}},
		Model:   model,
	})
}

// spendOver pushes the key past its daily limit by billing a paid model.
func spendOver(t *testing.T, store *Store, model string) {
	t.Helper()
	store.RecordUsage("sk-k", model, model, "openai", "standard", false, UsageDetail{InputTokens: 2_000_000})
}

func TestFreeModelDetection(t *testing.T) {
	prices := []ModelPrice{
		{Model: "free-all", ServiceTier: "*", Enabled: true},
		{Model: "free-output", ServiceTier: "*", InputPricePerMillion: 0, OutputPricePerMillion: 3, Enabled: true},
		{Model: "free-input", ServiceTier: "*", InputPricePerMillion: 2, OutputPricePerMillion: 0, Enabled: true},
		{Model: "free-request-fee", ServiceTier: "*", RequestPrice: 0.5, Enabled: true},
		{Model: "paid", ServiceTier: "*", InputPricePerMillion: 1, OutputPricePerMillion: 2, Enabled: true},
	}
	cases := []struct {
		model string
		want  bool
	}{
		{"free-all", true},
		{"free-output", false}, // free input but charged output
		{"free-input", false},  // charged input
		{"free-request-fee", false},
		{"paid", false},
		{"not-listed", true}, // unlisted models are never billed
	}
	for _, tc := range cases {
		if got := FreeModel(prices, "openai", tc.model, "", "standard", 1000); got != tc.want {
			t.Errorf("FreeModel(%q) = %v, want %v", tc.model, got, tc.want)
		}
	}

	// An empty table expresses no intent: nothing is treated as free.
	if FreeModel(nil, "openai", "anything", "", "standard", 1000) {
		t.Error("empty price table must not make every model free")
	}
}

// The requested feature: a free model keeps working after the key is over its
// daily cap, while a paid model stays blocked.
func TestFreeModelBypassesDailyCap(t *testing.T) {
	store := pricedStore(t, []ModelPrice{
		{Model: "free", ServiceTier: "*", Enabled: true},
		{Model: "paid", ServiceTier: "*", InputPricePerMillion: 1000, Enabled: true},
	}, 1.0)

	if d := admitModel(t, store, "free"); d.Terminate {
		t.Fatalf("free model admitted before cap: %+v", d)
	}
	spendOver(t, store, "paid")

	// The key is now over its daily cap.
	if d := admitModel(t, store, "paid"); !d.Terminate || d.Reason != "daily_exceeded" {
		t.Fatalf("paid model must stay blocked: %+v", d)
	}
	if d := admitModel(t, store, "free"); d.Terminate {
		t.Fatalf("free model must bypass the USD cap: %+v", d)
	}
	// An unlisted model is also free.
	if d := admitModel(t, store, "brand-new-model"); d.Terminate {
		t.Fatalf("unlisted model must bypass the USD cap: %+v", d)
	}
}

// RPM is a rate limit, not a spend limit, so it still applies to free models.
func TestFreeModelStillRateLimited(t *testing.T) {
	store := configureQuotaStore(t)
	store.SetPriceLister(func() ([]ModelPrice, error) {
		return []ModelPrice{{Model: "free", ServiceTier: "*", Enabled: true}}, nil
	})
	if !store.PricesAvailable() {
		t.Fatal("prices should be available")
	}
	bindTestKey(t, store, "k", "sk-k", 1, 100)

	if d := admitModel(t, store, "free"); d.Terminate {
		t.Fatalf("first request should pass: %+v", d)
	}
	d := admitModel(t, store, "free")
	if !d.Terminate || d.Reason != "rpm_exceeded" || d.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("free model must still obey RPM: %+v", d)
	}
}

// An empty price table is not proof that a model is free, so the exemption must
// not become a blanket bypass when Plus returns no rules.
func TestNoPriceTableDoesNotBypass(t *testing.T) {
	store := configureQuotaStore(t)
	store.SetPriceLister(func() ([]ModelPrice, error) { return nil, nil })
	bindTestKey(t, store, "k", "sk-k", 100, 1.0)
	store.usage.RecordCost("k", "m", 5, 0, 0, 10, 1, 1)

	if !store.PricesAvailable() {
		t.Fatal("an empty but successful fetch still counts as ready")
	}
	if d := admitModel(t, store, "anything"); !d.Terminate || d.Reason != "daily_exceeded" {
		t.Fatalf("empty table must not bypass the cap: %+v", d)
	}
}

// A request with no model information cannot be proven free.
func TestUnknownModelDoesNotBypass(t *testing.T) {
	store := pricedStore(t, []ModelPrice{{Model: "paid", ServiceTier: "*", InputPricePerMillion: 1000, Enabled: true}}, 1.0)
	spendOver(t, store, "paid")

	d := store.AdmitRequest(GateRequest{Headers: http.Header{"Authorization": {"Bearer sk-k"}}})
	if !d.Terminate || d.Reason != "daily_exceeded" {
		t.Fatalf("model-less request must not bypass the cap: %+v", d)
	}
}

// A weekly cap is bypassed the same way, and a disabled policy stays a no-op.
func TestFreeModelBypassesWeeklyCapAndDisabledPolicy(t *testing.T) {
	prices := []ModelPrice{{Model: "paid", ServiceTier: "*", InputPricePerMillion: 1000, Enabled: true}}
	store := configureQuotaStore(t)
	store.SetPriceLister(func() ([]ModelPrice, error) { return prices, nil })
	bindTestKey(t, store, "k", "sk-k", 100, 0)
	key := store.Keys()[0]
	key.WeeklyLimitUSD = 1
	if err := store.UpsertKey(key, true); err != nil {
		t.Fatal(err)
	}
	spendOver(t, store, "paid")

	if d := admitModel(t, store, "paid"); !d.Terminate || d.Reason != "weekly_exceeded" {
		t.Fatalf("paid model must hit the weekly cap: %+v", d)
	}
	if d := admitModel(t, store, "unlisted"); d.Terminate {
		t.Fatalf("free model must bypass the weekly cap: %+v", d)
	}

	key = store.Keys()[0]
	key.Enabled = false
	if err := store.UpsertKey(key, true); err != nil {
		t.Fatal(err)
	}
	if d := admitModel(t, store, "paid"); !d.Known || d.Terminate || d.Reason != "policy_disabled" {
		t.Fatalf("disabled policy must remain a no-op: %+v", d)
	}
}
