package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCodexCatalogHostFilteringAndSafeOutput(t *testing.T) {
	// Decode the actual host wire shape into the safe projection. Sensitive
	// fields must not survive merging with observed scheduler candidates.
	raw := `[
		{"id":"z","provider":"codex","name":"z.json","status":"active","unavailable":true,"path":"/private/credential-path","token":"host-token-secret","json":{"refresh_token":"raw-json-secret"}},
		{"id":"a","provider":"codex","label":"primary","email":"a@example.com","status":"disabled","disabled":true,"priority":7},
		{"id":"a","provider":"codex","label":"duplicate","disabled":false},
		{"id":"other","provider":"claude","name":"excluded"},
		{"id":"","provider":"codex","name":"missing id"},
		{"id":"  ","provider":"codex","name":"blank id"}
	]`
	var files []HostAuthFile
	if err := json.Unmarshal([]byte(raw), &files); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	app.SetAuthFileLister(func() ([]HostAuthFile, error) { return files, nil })
	app.catalog.observe([]SchedulerAuthCandidate{{
		ID: "a", Provider: "codex", Status: "active", Priority: 100,
		Attributes: map[string]string{"name": "recent.json", "label": "old label", "path": "/private/recent-path", "token": "attrs-token-secret"},
		Metadata:   map[string]any{"access_token": "metadata-token-secret", "raw": map[string]any{"password": "password-secret"}},
	}})

	management := callManagement(t, app, http.MethodGet, "/v0/management/plugins/"+PluginID+"/codex-auths", nil)
	if management.StatusCode != http.StatusOK {
		t.Fatalf("directory HTTP status = %d, body=%s", management.StatusCode, management.Body)
	}
	var response CodexAuthListResponse
	if err := json.Unmarshal(management.Body, &response); err != nil {
		t.Fatalf("decode directory API body: %v", err)
	}
	if !response.Available || response.Source != "host" || response.Error != "" {
		t.Fatalf("host directory not available: %+v", response)
	}
	want := []CodexAuthAccount{
		{ID: "a", Label: "primary", Name: "recent.json", Email: "a@example.com", Status: "disabled", Disabled: true, Priority: 7, Source: "host+recent"},
		{ID: "z", Name: "z.json", Status: "active", Unavailable: true, Source: "host"},
	}
	if !reflect.DeepEqual(response.Accounts, want) {
		t.Fatalf("filtered and merged directory = %+v, want %+v", response.Accounts, want)
	}
	encoded := management.Body
	for _, forbidden := range []string{"credential-path", "host-token-secret", "raw-json-secret", "recent-path", "attrs-token-secret", "metadata-token-secret", "password-secret", `"path"`, `"token"`, `"metadata"`, `"json"`} {
		if strings.Contains(string(encoded), forbidden) {
			t.Errorf("directory leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestCodexCatalogUnavailableAndRecentFallback(t *testing.T) {
	for _, test := range []struct {
		name   string
		lister AuthFileLister
	}{
		{name: "unsupported"},
		{name: "host failure", lister: func() ([]HostAuthFile, error) {
			return []HostAuthFile{{ID: "partial", Provider: "codex"}}, errors.New("/private/error-path token=error-token-secret")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			app := NewApp()
			app.SetAuthFileLister(test.lister)
			empty := app.catalog.list()
			if empty.Available || empty.Source != "unavailable" || empty.Error == "" || empty.Accounts == nil || len(empty.Accounts) != 0 {
				t.Fatalf("unsupported/failed host looked like a complete empty directory: %+v", empty)
			}
			app.catalog.observe([]SchedulerAuthCandidate{
				{ID: "b", Provider: "codex", Status: "active", Priority: 2, Attributes: map[string]string{"name": "b.json", "email": "b@example.com"}},
				{ID: "a", Provider: " CODEX ", Status: "disabled", Attributes: map[string]string{"label": "disabled account"}},
				{ID: "ignored", Provider: "openai"},
			})
			response := app.catalog.list()
			want := []CodexAuthAccount{
				{ID: "a", Label: "disabled account", Status: "disabled", Disabled: true, Source: "recent"},
				{ID: "b", Name: "b.json", Email: "b@example.com", Status: "active", Priority: 2, Source: "recent"},
			}
			if response.Available || response.Source != "recent" || response.Error == "" || !reflect.DeepEqual(response.Accounts, want) {
				t.Fatalf("recent fallback = %+v, want accounts %+v", response, want)
			}
			encoded, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), "error-path") || strings.Contains(string(encoded), "error-token-secret") {
				t.Fatalf("host failure leaked raw error: %s", encoded)
			}
		})
	}
}

func TestCodexCatalogHostMergeAndHistoricalAvailability(t *testing.T) {
	app := NewApp()
	app.catalog.observe([]SchedulerAuthCandidate{
		{ID: "match", Provider: "codex", Status: "unavailable", Priority: 1, Attributes: map[string]string{"name": "old.json", "label": "recent label", "email": "old@example.com"}},
		{ID: "removed", Provider: "codex", Status: "active"},
	})
	app.SetAuthFileLister(func() ([]HostAuthFile, error) {
		return []HostAuthFile{
			{ID: "match", Provider: "codex", Name: "new.json", Email: "new@example.com", Status: "active", Priority: 9},
			{ID: "host-only", Provider: "codex", Status: "active", Disabled: true},
		}, nil
	})
	response := app.catalog.list()
	want := []CodexAuthAccount{
		{ID: "host-only", Status: "active", Disabled: true, Source: "host"},
		{ID: "match", Name: "new.json", Label: "recent label", Email: "new@example.com", Status: "active", Priority: 9, Source: "host+recent"},
		{ID: "removed", Status: "active", Unavailable: true, Source: "recent"},
	}
	if !response.Available || !reflect.DeepEqual(response.Accounts, want) {
		t.Fatalf("host merge = %+v, want %+v", response, want)
	}
	// Successful-list annotations must not rewrite the observed cache, and
	// callers must not be able to mutate the catalog through its response.
	response.Accounts[2].Name = "caller mutation"
	app.SetAuthFileLister(nil)
	fallback := app.catalog.list()
	if len(fallback.Accounts) != 2 || fallback.Accounts[1].ID != "removed" || fallback.Accounts[1].Unavailable || fallback.Accounts[1].Name != "" {
		t.Fatalf("list mutated recent observations: %+v", fallback)
	}
}

func TestCodexCatalogSuccessfulEmptyDirectory(t *testing.T) {
	app := NewApp()
	app.SetAuthFileLister(func() ([]HostAuthFile, error) { return nil, nil })
	response := app.catalog.list()
	if !response.Available || response.Source != "host" || response.Error != "" || response.Accounts == nil || len(response.Accounts) != 0 {
		t.Fatalf("successful empty directory = %+v", response)
	}
	app.catalog.observe([]SchedulerAuthCandidate{{ID: "historical", Provider: "codex", Status: "active"}})
	response = app.catalog.list()
	if !response.Available || response.Source != "host" || len(response.Accounts) != 1 || !response.Accounts[0].Unavailable || response.Accounts[0].Source != "recent" {
		t.Fatalf("absent recent account remained available: %+v", response)
	}
}

func TestCodexCatalogRecentCacheBoundAndRefresh(t *testing.T) {
	catalog := newCodexCatalog()
	for i := range codexRecentLimit {
		catalog.observe([]SchedulerAuthCandidate{{ID: fmt.Sprintf("auth-%04d", i), Provider: "codex"}})
	}
	attrs := map[string]string{"name": "saved name", "token": "not retained"}
	catalog.observe([]SchedulerAuthCandidate{{ID: "auth-0000", Provider: "codex", Attributes: attrs}})
	attrs["name"] = "mutated name"
	catalog.observe([]SchedulerAuthCandidate{{ID: "new", Provider: "codex"}})
	response := catalog.list()
	if len(response.Accounts) != codexRecentLimit || len(catalog.recent) != codexRecentLimit || catalog.order.Len() != codexRecentLimit {
		t.Fatalf("cache exceeded bound: accounts=%d map=%d order=%d", len(response.Accounts), len(catalog.recent), catalog.order.Len())
	}
	if response.Accounts[0].ID != "auth-0000" || response.Accounts[0].Name != "saved name" {
		t.Fatalf("refreshed safe observation was not retained: %+v", response.Accounts[0])
	}
	for _, account := range response.Accounts {
		if account.ID == "auth-0001" {
			t.Fatal("least recently observed account was not evicted")
		}
	}
	if response.Accounts[len(response.Accounts)-1].ID != "new" {
		t.Fatal("new observation missing from bounded cache")
	}
}

func TestCodexCatalogConcurrentInjectionAndObservation(t *testing.T) {
	app := NewApp()
	lister := func() ([]HostAuthFile, error) {
		// The callback may re-enter catalog observation; no catalog lock may
		// be held while invoking external host code.
		app.catalog.observe([]SchedulerAuthCandidate{{ID: "callback", Provider: "codex"}})
		return []HostAuthFile{{ID: "host", Provider: "codex"}}, nil
	}
	app.SetAuthFileLister(lister)
	app.catalog.list()
	var workers sync.WaitGroup
	for worker := range 4 {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			for range 32 {
				app.SetAuthFileLister(lister)
				app.catalog.observe([]SchedulerAuthCandidate{{ID: fmt.Sprintf("worker-%d", worker), Provider: "codex"}})
				app.catalog.list()
				app.SetAuthFileLister(nil)
			}
		}(worker)
	}
	workers.Wait()
	app.SetAuthFileLister(nil)
	response := app.catalog.list()
	if response.Available || response.Source != "recent" || len(response.Accounts) != 5 {
		t.Fatalf("concurrent catalog lost observations: %+v", response)
	}
}

func TestCodexCatalogDiagnosticsObserveEverySchedulerPick(t *testing.T) {
	catalog := newCodexCatalog()
	initial := catalog.list().Diagnostics
	if initial.HomeMode != "unknown" || initial.HighestPriorityScheduler != "unknown" || initial.SchedulerAcrossPriorities != "unknown" || initial.SchedulerPickCount != 0 || initial.LastSchedulerPickAt != "" {
		t.Fatalf("unobserved scheduler diagnostics = %+v", initial)
	}
	if initial.Warning != "Home 模式下插件 scheduler 会被绕过，绑定不生效；需确保本插件为最高 priority scheduler" {
		t.Fatalf("missing routing-effectiveness warning: %q", initial.Warning)
	}
	initialJSON, err := json.Marshal(initial)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(initialJSON), "last_scheduler_pick_at") {
		t.Fatalf("unobserved scheduler fabricated a last-pick timestamp: %s", initialJSON)
	}
	start := time.Now().UTC()
	catalog.observe(nil)
	first := catalog.list().Diagnostics
	end := time.Now().UTC()
	if first.SchedulerPickCount != 1 {
		t.Fatalf("empty scheduler pick was not counted: %+v", first)
	}
	last, err := time.Parse(time.RFC3339Nano, first.LastSchedulerPickAt)
	if err != nil || !strings.HasSuffix(first.LastSchedulerPickAt, "Z") {
		t.Fatalf("last scheduler pick is not explicit UTC RFC3339Nano: %q (%v)", first.LastSchedulerPickAt, err)
	}
	if last.Before(start) || last.After(end) {
		t.Fatalf("last scheduler pick %s is outside observation interval [%s, %s]", last, start, end)
	}
	catalog.observe([]SchedulerAuthCandidate{{ID: "not-codex", Provider: "claude"}})
	catalog.observe([]SchedulerAuthCandidate{{ID: "a", Provider: "codex"}, {ID: "b", Provider: "codex"}})
	final := catalog.list().Diagnostics
	if final.SchedulerPickCount != 3 {
		t.Fatalf("count must reflect calls rather than Codex candidate count: %+v", final)
	}
	finalTime, err := time.Parse(time.RFC3339Nano, final.LastSchedulerPickAt)
	if err != nil || !strings.HasSuffix(final.LastSchedulerPickAt, "Z") || finalTime.Before(last) {
		t.Fatalf("last-pick time did not advance correctly: %+v (%v)", final, err)
	}
	if final.HomeMode != "unknown" || final.HighestPriorityScheduler != "unknown" || final.SchedulerAcrossPriorities != "unknown" || final.Warning != initial.Warning {
		t.Fatalf("observed picks must not claim routing effectiveness: %+v", final)
	}
	if got := catalog.list().Diagnostics; got != final {
		t.Fatalf("reading the directory changed scheduler diagnostics: before=%+v after=%+v", final, got)
	}
}

func TestCodexCatalogDiagnosticsSnapshotBeforeHostCallback(t *testing.T) {
	app := NewApp()
	app.SetAuthFileLister(func() ([]HostAuthFile, error) {
		app.catalog.observe(nil)
		return nil, nil
	})
	first := app.catalog.list()
	if !first.Available || first.Diagnostics.SchedulerPickCount != 0 || first.Diagnostics.LastSchedulerPickAt != "" {
		t.Fatalf("directory did not preserve pre-callback diagnostics snapshot: %+v", first)
	}
	app.SetAuthFileLister(nil)
	second := app.catalog.list()
	if second.Diagnostics.SchedulerPickCount != 1 || second.Diagnostics.LastSchedulerPickAt == "" {
		t.Fatalf("callback observation was not visible to the next snapshot: %+v", second)
	}
}
