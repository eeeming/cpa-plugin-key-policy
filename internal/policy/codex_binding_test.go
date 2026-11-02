package policy

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCodexBindingLegacyStateAndRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"keys":[{"id":"legacy","name":"Legacy","enabled":true,"rpm":7,"daily_limit_usd":4}],"usage":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	if err := store.Configure(Config{Enabled: true, StateFile: path}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.StopUsageFlusher)
	legacy := store.Keys()[0]
	if len(legacy.CodexAuthIDs) != 0 || legacy.CodexGroup != "" || legacy.CodexFallback {
		t.Fatalf("legacy key acquired a binding: %+v", legacy)
	}
	if err := store.UpsertKey(KeyConfig{
		ID: "bound", Enabled: true, CodexAuthIDs: []string{" b1 ", "", "a1", "a1"},
		CodexGroup: " Pro ", CodexFallback: true,
	}, true); err != nil {
		t.Fatal(err)
	}
	state, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if state.Version != 1 {
		t.Fatalf("state version = %d", state.Version)
	}
	byID := make(map[string]KeyConfig)
	for _, key := range state.Keys {
		byID[key.ID] = key
	}
	bound := byID["bound"]
	if !reflect.DeepEqual(bound.CodexAuthIDs, []string{"a1", "b1"}) || bound.CodexGroup != "pro" || !bound.CodexFallback {
		t.Fatalf("binding did not survive normalized persistence: %+v", bound)
	}
	if key := byID["legacy"]; key.RPM != 7 || key.DailyLimitUSD != 4 || len(key.CodexAuthIDs) != 0 {
		t.Fatalf("legacy quota changed: %+v", key)
	}
	reloaded := NewStore()
	if err := reloaded.Configure(Config{Enabled: true, StateFile: path}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reloaded.StopUsageFlusher)
	got, _, ok := reloaded.ModelUsageFor("bound")
	if !ok || !reflect.DeepEqual(got.CodexAuthIDs, bound.CodexAuthIDs) || got.CodexGroup != bound.CodexGroup || got.CodexFallback != bound.CodexFallback {
		t.Fatalf("restart lost binding: %+v", got)
	}
}

func TestCodexBindingPolicyCopiesStayIsolated(t *testing.T) {
	store := configureQuotaStore(t)
	hash, err := HashKey("sk-bound")
	if err != nil {
		t.Fatal(err)
	}
	input := KeyConfig{ID: "bound", Enabled: true, KeyHash: hash, CodexAuthIDs: []string{"a1"}}
	if err := store.UpsertKey(input, true); err != nil {
		t.Fatal(err)
	}
	input.CodexAuthIDs[0] = "input-mutation"
	admit := func() *KeyConfig {
		t.Helper()
		decision := store.Admit(http.Header{"Authorization": {"Bearer sk-bound"}}, nil, nil)
		if !decision.Allowed || decision.Policy == nil {
			t.Fatalf("admission failed: %+v", decision)
		}
		return decision.Policy
	}
	old := admit()
	if !reflect.DeepEqual(old.CodexAuthIDs, []string{"a1"}) {
		t.Fatalf("input slice escaped into store: %+v", old)
	}
	listed := store.Keys()
	listed[0].CodexAuthIDs[0] = "list-mutation"
	view, _, _ := store.ModelUsageFor("bound")
	view.CodexAuthIDs[0] = "view-mutation"
	if got := admit(); !reflect.DeepEqual(got.CodexAuthIDs, []string{"a1"}) {
		t.Fatalf("management view mutated live binding: %+v", got)
	}
	rollback := errors.New("reject update")
	if _, err := store.UpdateKey("bound", func(key *KeyConfig) error {
		key.CodexAuthIDs[0] = "rejected"
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatalf("update error = %v", err)
	}
	if got := admit(); !reflect.DeepEqual(got.CodexAuthIDs, []string{"a1"}) {
		t.Fatalf("failed update mutated live binding: %+v", got)
	}
	updated, err := store.UpdateKey("bound", func(key *KeyConfig) error {
		key.CodexAuthIDs[0] = "b1"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	updated.CodexAuthIDs[0] = "returned-mutation"
	if got := admit(); !reflect.DeepEqual(got.CodexAuthIDs, []string{"b1"}) {
		t.Fatalf("returned update aliases live binding: %+v", got)
	}
	if !reflect.DeepEqual(old.CodexAuthIDs, []string{"a1"}) {
		t.Fatalf("in-flight snapshot changed after PATCH: %+v", old)
	}
}
