package policy

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
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

func TestSyncFromPlusAliases(t *testing.T) {
	const newPlain = "sk-alias-new-secret-abcdefgh"
	const unnamedPlain = "sk-unnamed-secret-abcdefgh"
	const existingPlain = "sk-existing-secret-abcdefgh"
	const customPlain = "sk-custom-secret-abcdefgh"
	for _, tc := range []struct {
		name     string
		status   int
		body     string
		aliases  bool
		degraded bool
	}{
		{name: "aliases present", status: http.StatusOK, aliases: true},
		{name: "aliases absent", status: http.StatusOK, body: `{"items":[]}`},
		{name: "aliases unsupported", status: http.StatusNotFound, body: newPlain, degraded: true},
		{name: "malformed aliases", status: http.StatusOK, body: `{"items":"` + newPlain + `"}`, degraded: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hashes := map[string]string{}
			for _, plain := range []string{newPlain, unnamedPlain, existingPlain, customPlain} {
				hash, err := HashKey(plain)
				if err != nil {
					t.Fatal(err)
				}
				hashes[plain] = hash
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer admin-secret" {
					t.Error("missing management credentials")
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
				switch r.URL.Path {
				case plusAPIKeysPath:
					_ = json.NewEncoder(w).Encode(map[string]any{"api-keys": []string{newPlain, unnamedPlain, existingPlain, customPlain}})
				case plusAPIKeyAliasesPath:
					w.WriteHeader(tc.status)
					if tc.aliases {
						_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{
							{"apiKeyHash": strings.TrimPrefix(hashes[newPlain], HashPrefix), "alias": "bob-team", "updatedAtMs": 1793608200107},
							{"apiKeyHash": strings.TrimPrefix(hashes[existingPlain], HashPrefix), "alias": "existing-team", "updatedAtMs": 1793608200107},
							{"apiKeyHash": strings.TrimPrefix(hashes[customPlain], HashPrefix), "alias": "do-not-overwrite", "updatedAtMs": 1793608200107},
						}})
					} else {
						_, _ = w.Write([]byte(tc.body))
					}
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(srv.Close)
			store := NewStore()
			if err := store.Configure(Config{
				Enabled: true, StateFile: filepath.Join(t.TempDir(), "state.json"),
				PlusBaseURL: srv.URL, PlusManagementKey: "admin-secret",
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(store.StopUsageFlusher)
			for _, key := range []KeyConfig{
				{
					ID: "existing", Name: PreviewKey(existingPlain), KeyHash: hashes[existingPlain],
					KeyPreview: PreviewKey(existingPlain), CallerScope: CallerScope(existingPlain),
					Enabled: false, RPM: 7, DailyLimitUSD: 3, WeeklyLimitUSD: 12,
					CodexAuthIDs: []string{"account-b", "account-a"}, CodexGroup: "team", CodexFallback: true,
				},
				{
					ID: "custom", Name: "my-custom-name", KeyHash: hashes[customPlain],
					KeyPreview: PreviewKey(customPlain), CallerScope: CallerScope(customPlain),
					Enabled: true, RPM: 11, DailyLimitUSD: 5, WeeklyLimitUSD: 20,
					CodexAuthIDs: []string{"account-c"},
				},
			} {
				if err := store.UpsertKey(key, false); err != nil {
					t.Fatal(err)
				}
			}
			beforeExisting := *store.findByID("existing")
			beforeCustom := *store.findByID("custom")
			got, err := store.SyncFromPlus()
			if err != nil {
				t.Fatal(err)
			}
			if got.Added != 2 || got.Skipped != 2 || got.Total != 4 || len(got.AddedIDs) != 2 {
				t.Fatalf("sync result = %+v", got)
			}
			if (len(got.Warnings) > 0) != tc.degraded {
				t.Fatalf("degraded diagnostic = %+v", got)
			}
			wantName := PreviewKey(newPlain)
			if tc.aliases {
				wantName = "bob-team"
				if got.Renamed != 1 || !reflect.DeepEqual(got.RenamedIDs, []string{"existing"}) {
					t.Fatalf("renamed result = %+v", got)
				}
				beforeExisting.Name = "existing-team"
				beforeExisting.UpdatedAt = store.findByID("existing").UpdatedAt
			} else if got.Renamed != 0 || len(got.RenamedIDs) != 0 {
				t.Fatalf("unexpected rename = %+v", got)
			}
			if key := store.findByHash(hashes[newPlain]); key == nil || key.Name != wantName {
				t.Fatalf("new named key = %+v", key)
			}
			if key := store.findByHash(hashes[unnamedPlain]); key == nil || key.Name != PreviewKey(unnamedPlain) {
				t.Fatalf("unnamed key = %+v", key)
			}
			if after := store.findByID("existing"); !reflect.DeepEqual(*after, beforeExisting) {
				t.Fatalf("preview rename changed other fields: before=%+v after=%+v", beforeExisting, after)
			}
			if after := store.findByID("custom"); !reflect.DeepEqual(*after, beforeCustom) {
				t.Fatalf("custom policy changed: before=%+v after=%+v", beforeCustom, after)
			}
			state, err := LoadState(store.StatePath())
			if err != nil {
				t.Fatal(err)
			}
			for _, key := range state.Keys {
				if key.ID == "existing" && !reflect.DeepEqual(key, beforeExisting) {
					t.Fatalf("persisted rename = %+v", key)
				}
			}
			raw, err := os.ReadFile(store.StatePath())
			if err != nil {
				t.Fatal(err)
			}
			resultJSON, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{newPlain, unnamedPlain, existingPlain, customPlain, "admin-secret"} {
				if strings.Contains(string(raw), secret) || strings.Contains(string(resultJSON), secret) {
					t.Fatal("state or sync response leaked a secret")
				}
			}
			again, err := store.SyncFromPlus()
			if err != nil || again.Added != 0 || again.Renamed != 0 || again.Skipped != 4 {
				t.Fatalf("repeated sync = %+v, err = %v", again, err)
			}
		})
	}
}

func TestSyncFromPlusRenamesWithoutAdding(t *testing.T) {
	store := configureQuotaStore(t)
	plain := "sk-only-existing-secret-abcdefgh"
	hash, err := HashKey(plain)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertKey(KeyConfig{
		ID: "existing", Name: PreviewKey(plain), KeyHash: hash, KeyPreview: PreviewKey(plain),
		CallerScope: CallerScope(plain), RPM: 17, CodexAuthIDs: []string{"account"},
	}, true); err != nil {
		t.Fatal(err)
	}
	store.SetAPIKeyLister(func() ([]PlusAPIKey, error) {
		return []PlusAPIKey{{Plain: plain, Name: "inline-name-must-not-rename"}}, nil
	})
	store.SetAPIKeyAliasLister(func() (map[string]string, error) {
		return map[string]string{strings.TrimPrefix(hash, HashPrefix): "only-team"}, nil
	})
	got, err := store.SyncFromPlus()
	if err != nil || got.Added != 0 || got.Skipped != 1 || got.Renamed != 1 {
		t.Fatalf("sync = %+v, err = %v", got, err)
	}
	state, err := LoadState(store.StatePath())
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Keys) != 1 || state.Keys[0].Name != "only-team" || state.Keys[0].RPM != 17 || !reflect.DeepEqual(state.Keys[0].CodexAuthIDs, []string{"account"}) {
		t.Fatalf("persisted rename = %+v", state.Keys)
	}
}

func TestSyncFromPlusAliasErrorIsPrivate(t *testing.T) {
	store := configureQuotaStore(t)
	const plain = "sk-private-sync-secret-abcdefgh"
	store.SetAPIKeyLister(func() ([]PlusAPIKey, error) {
		return []PlusAPIKey{{Plain: plain}}, nil
	})
	store.SetAPIKeyAliasLister(func() (map[string]string, error) {
		return nil, errors.New("GET https://admin:password@example.test?key=" + plain + ": upstream body " + plain)
	})
	got, err := store.SyncFromPlus()
	if err != nil || got.Added != 1 || len(got.Warnings) != 1 {
		t.Fatalf("sync = %+v, err = %v", got, err)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{plain, "admin:password", "example.test", "upstream body"} {
		if strings.Contains(string(raw), value) {
			t.Fatal("alias diagnostic exposed upstream secrets")
		}
	}
}

func TestHTTPAPIKeyAliasListerRejectsUnsafeErrors(t *testing.T) {
	const secret = "sk-upstream-secret-abcdefgh"
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "not found", status: http.StatusNotFound, body: secret},
		{name: "invalid JSON", status: http.StatusOK, body: secret},
		{name: "wrong items type", status: http.StatusOK, body: `{"items":"` + secret + `"}`},
		{name: "missing items", status: http.StatusOK, body: `{}`},
		{name: "null items", status: http.StatusOK, body: `{"items":null}`},
		{name: "prefixed hash", status: http.StatusOK, body: `{"items":[{"apiKeyHash":"sha256:` + strings.Repeat("a", 64) + `","alias":"` + secret + `"}]}`},
		{name: "invalid hash", status: http.StatusOK, body: `{"items":[{"apiKeyHash":"` + strings.Repeat("z", 64) + `","alias":"` + secret + `"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(srv.Close)
			_, err := HTTPAPIKeyAliasLister(srv.Client(), srv.URL, "admin-secret")()
			if err == nil {
				t.Fatal("expected alias error")
			}
			for _, value := range []string{secret, "admin-secret", srv.URL, tc.body} {
				if strings.Contains(err.Error(), value) {
					t.Fatal("alias error exposed upstream content")
				}
			}
		})
	}
	t.Run("network error", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		srv.Close()
		_, err := HTTPAPIKeyAliasLister(srv.Client(), srv.URL+"?key="+secret, "admin-secret")()
		if err == nil {
			t.Fatal("expected alias transport error")
		}
		for _, value := range []string{secret, "admin-secret", srv.URL} {
			if strings.Contains(err.Error(), value) {
				t.Fatal("alias transport error exposed request secrets")
			}
		}
	})
}

func TestSyncFromPlusDoesNotPersistSecretAliases(t *testing.T) {
	store := configureQuotaStore(t)
	const plain = "sk-alias-plaintext-secret-abcdefgh"
	hash, err := HashKey(plain)
	if err != nil {
		t.Fatal(err)
	}
	store.SetAPIKeyLister(func() ([]PlusAPIKey, error) {
		return []PlusAPIKey{{Plain: plain}}, nil
	})
	store.SetAPIKeyAliasLister(func() (map[string]string, error) {
		return map[string]string{strings.TrimPrefix(hash, HashPrefix): "owner " + plain}, nil
	})
	got, err := store.SyncFromPlus()
	if err != nil || got.Added != 1 {
		t.Fatalf("sync = %+v, err = %v", got, err)
	}
	if key := store.findByHash(hash); key == nil || key.Name != PreviewKey(plain) {
		t.Fatalf("unsafe alias persisted: %+v", key)
	}
	got, err = store.SyncFromPlus()
	if err != nil || got.Renamed != 0 || got.Skipped != 1 {
		t.Fatalf("unsafe existing alias rename: %+v, err = %v", got, err)
	}
	raw, err := os.ReadFile(store.StatePath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), plain) {
		t.Fatal("state leaked plaintext alias")
	}
}

func TestSyncFromPlusInlineNameDoesNotRenameExistingKey(t *testing.T) {
	store := configureQuotaStore(t)
	const plain = "sk-inline-existing-secret-abcdefgh"
	hash, err := HashKey(plain)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertKey(KeyConfig{
		ID: "existing", Name: PreviewKey(plain), KeyHash: hash, KeyPreview: PreviewKey(plain),
	}, true); err != nil {
		t.Fatal(err)
	}
	store.SetAPIKeyLister(func() ([]PlusAPIKey, error) {
		return []PlusAPIKey{{Plain: plain, Name: "inline-name"}}, nil
	})
	got, err := store.SyncFromPlus()
	if err != nil || got.Skipped != 1 || got.Renamed != 0 {
		t.Fatalf("sync = %+v, err = %v", got, err)
	}
	if key := store.findByID("existing"); key == nil || key.Name != PreviewKey(plain) {
		t.Fatalf("inline name overwrote existing preview: %+v", key)
	}
}
