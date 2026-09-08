package policy

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParsePlusAPIKeysStringList(t *testing.T) {
	list, err := ParsePlusAPIKeys([]byte(`{"api-keys":["sk-aaa"," sk-aaa ","","sk-bbb"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Plain != "sk-aaa" || list[1].Plain != "sk-bbb" {
		t.Fatalf("list = %+v", list)
	}
}

func TestParsePlusAPIKeysNamedObjects(t *testing.T) {
	list, err := ParsePlusAPIKeys([]byte(`{"items":[{"key":"sk-one","name":"Alice"},{"api_key":"sk-two"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Name != "Alice" || list[1].Plain != "sk-two" {
		t.Fatalf("list = %+v", list)
	}
}

func TestSyncFromPlusAddsMissingAndSkipsBound(t *testing.T) {
	store := NewStore()
	if err := store.Configure(Config{Enabled: true, StateFile: filepath.Join(t.TempDir(), "state.json")}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.StopUsageFlusher)
	hash, err := HashKey("sk-already")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertKey(KeyConfig{
		ID: "existing", Name: "keep-me", Enabled: true, KeyHash: hash,
		KeyPreview: PreviewKey("sk-already"), CallerScope: CallerScope("sk-already"),
		DailyLimitUSD: 3,
	}, true); err != nil {
		t.Fatal(err)
	}
	store.SetAPIKeyLister(func() ([]PlusAPIKey, error) {
		return []PlusAPIKey{
			{Plain: "sk-already"},
			{Plain: "sk-new-one", Name: "New One"},
			{Plain: "sk-new-two"},
		}, nil
	})

	got, err := store.SyncFromPlus()
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != 3 || got.Added != 2 || got.Skipped != 1 {
		t.Fatalf("sync = %+v", got)
	}

	keys := store.Keys()
	if len(keys) != 3 {
		t.Fatalf("keys = %+v", keys)
	}
	byName := map[string]KeyConfig{}
	for _, k := range keys {
		byName[k.Name] = k
		if k.KeyHash == "" || k.KeyPreview == "" || k.CallerScope == "" {
			t.Fatalf("missing identity fields: %+v", k)
		}
		if !strings.HasPrefix(k.KeyHash, HashPrefix) || k.KeyHash == k.KeyPreview {
			t.Fatalf("hash must be sha256, not preview: %+v", k)
		}
	}
	if byName["keep-me"].DailyLimitUSD != 3 {
		t.Fatalf("existing limits overwritten: %+v", byName["keep-me"])
	}
	if byName["New One"].DailyLimitUSD != 0 || !byName["New One"].Enabled {
		t.Fatalf("new named key = %+v", byName["New One"])
	}

	raw, err := os.ReadFile(store.StatePath())
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"sk-already", "sk-new-one", "sk-new-two"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("state leaked %s", secret)
		}
	}
	st, err := LoadState(store.StatePath())
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range st.Keys {
		if !strings.HasPrefix(k.KeyHash, HashPrefix) || k.KeyPreview == "" {
			t.Fatalf("persisted identity = %+v", k)
		}
	}
}

func TestHTTPAPIKeyLister(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/api-keys" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer admin" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"api-keys":["sk-from-plus"]}`))
	}))
	t.Cleanup(srv.Close)
	list, err := HTTPAPIKeyLister(srv.Client(), srv.URL, "admin")()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Plain != "sk-from-plus" {
		t.Fatalf("list = %+v", list)
	}
}
