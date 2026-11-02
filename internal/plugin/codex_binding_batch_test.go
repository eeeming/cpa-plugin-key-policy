package plugin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"cpa-key-policy/internal/policy"
)

const batchBindingPath = "/v0/management/plugins/" + PluginID + "/keys/codex-binding/batch"

func newBatchBindingApp(t *testing.T) *App {
	t.Helper()
	app := configureApp(t)
	app.Store().StopUsageFlusher()
	for _, id := range []string{"a", "b"} {
		bindCodexPolicy(t, app, id, map[string]any{
			"codex_auth_ids": []string{"old-account"}, "rpm": 100,
			"daily_limit_usd": 20, "weekly_limit_usd": 80,
		})
	}
	return app
}

func batchAppState(t *testing.T, app *App) []byte {
	t.Helper()
	body, err := os.ReadFile(app.Store().StatePath())
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestBatchBindingManagementPreviewApplyAndSignedSnapshotSurvival(t *testing.T) {
	app := newBatchBindingApp(t)
	inFlight := interceptBearer(t, app, "sk-a")
	if inFlight.Terminate || inFlight.Headers.Get(bindingHeader) == "" {
		t.Fatalf("missing initial binding snapshot: %+v", inFlight)
	}
	beforeKeys, beforeDisk := app.Store().Keys(), batchAppState(t, app)
	request := map[string]any{
		"ids": []string{"b", "a"}, "accounts_mode": "set",
		"codex_auth_ids": []string{" new-account ", "new-account", ""}, "dry_run": true,
	}
	previewResponse := callManagement(t, app, http.MethodPost, batchBindingPath, mustJSON(request))
	if previewResponse.StatusCode != http.StatusOK {
		t.Fatalf("preview: %d %s", previewResponse.StatusCode, previewResponse.Body)
	}
	var preview policy.CodexBindingBatchResult
	if err := json.Unmarshal(previewResponse.Body, &preview); err != nil {
		t.Fatal(err)
	}
	if !preview.DryRun || preview.Updated != 0 || len(preview.Changes) != 2 || preview.Changes[0].ID != "b" || preview.Changes[1].ID != "a" {
		t.Fatalf("preview = %+v", preview)
	}
	if !reflect.DeepEqual(beforeKeys, app.Store().Keys()) || !bytes.Equal(beforeDisk, batchAppState(t, app)) {
		t.Fatal("preview changed memory or disk")
	}
	// Empty group and false fallback must be present, not dropped by omitempty.
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(previewResponse.Body, &wire); err != nil {
		t.Fatal(err)
	}
	var changes []struct {
		Before map[string]json.RawMessage `json:"before"`
		After  map[string]json.RawMessage `json:"after"`
	}
	if err := json.Unmarshal(wire["changes"], &changes); err != nil {
		t.Fatal(err)
	}
	for _, change := range changes {
		for _, binding := range []map[string]json.RawMessage{change.Before, change.After} {
			if len(binding) != 3 || string(binding["codex_group"]) != `""` || string(binding["codex_fallback"]) != "false" {
				t.Fatalf("incomplete binding wire value: %s", previewResponse.Body)
			}
		}
	}
	expected := make([]policy.CodexBindingExpected, 0, len(preview.Changes))
	for _, change := range preview.Changes {
		if !reflect.DeepEqual(change.After.CodexAuthIDs, []string{"new-account"}) {
			t.Fatalf("preview did not normalize accounts: %+v", change)
		}
		expected = append(expected, policy.CodexBindingExpected{ID: change.ID, Before: change.Before})
	}
	request["expected"], request["dry_run"] = expected, false
	applied := callManagement(t, app, http.MethodPost, batchBindingPath, mustJSON(request))
	if applied.StatusCode != http.StatusOK {
		t.Fatalf("apply: %d %s", applied.StatusCode, applied.Body)
	}
	var result policy.CodexBindingBatchResult
	if err := json.Unmarshal(applied.Body, &result); err != nil {
		t.Fatal(err)
	}
	if result.DryRun || result.Updated != 2 || !reflect.DeepEqual(result.Changes, preview.Changes) {
		t.Fatalf("apply differs from preview: %+v", result)
	}
	candidates := []SchedulerAuthCandidate{{ID: "old-account", Provider: "codex"}, {ID: "new-account", Provider: "codex"}}
	for range 2 {
		if got := pickBindingThroughApp(t, app, inFlight.Headers, candidates...); got.AuthID != "old-account" {
			t.Fatalf("in-flight request/retry changed assignment: %+v", got)
		}
	}
	for _, id := range []string{"a", "b"} {
		if got := pickBindingThroughApp(t, app, interceptBearer(t, app, "sk-"+id).Headers, candidates...); got.AuthID != "new-account" {
			t.Fatalf("new request retained old assignment: %+v", got)
		}
	}
	// An intervening edit after this preview must reject the complete batch.
	stale := callManagement(t, app, http.MethodPost, batchBindingPath, mustJSON(request))
	if stale.StatusCode != http.StatusConflict {
		t.Fatalf("stale expected: %d %s", stale.StatusCode, stale.Body)
	}
}

func TestBatchBindingManagementActions(t *testing.T) {
	app := newBatchBindingApp(t)
	cases := []struct {
		body     string
		ids      []string
		group    string
		fallback bool
	}{
		{`{"ids":["a","b"],"accounts_mode":"add","codex_auth_ids":[" z ","old-account","z"]}`, []string{"old-account", "z"}, "", false},
		{`{"ids":["a","b"],"accounts_mode":"remove","codex_auth_ids":["old-account","missing"]}`, []string{"z"}, "", false},
		{`{"ids":["a","b"],"codex_group":" PRO ","codex_fallback":true}`, []string{"z"}, "pro", true},
		{`{"ids":["a","b"],"accounts_mode":"keep","codex_fallback":false}`, []string{"z"}, "pro", false},
		{`{"ids":["a","b"],"accounts_mode":"set","codex_auth_ids":[]}`, []string{}, "pro", false},
		{`{"ids":["a","b"],"clear":true}`, []string{}, "", false},
	}
	for _, tc := range cases {
		response := callManagement(t, app, http.MethodPost, batchBindingPath, []byte(tc.body))
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d %s", tc.body, response.StatusCode, response.Body)
		}
		var result policy.CodexBindingBatchResult
		if err := json.Unmarshal(response.Body, &result); err != nil {
			t.Fatal(err)
		}
		for _, change := range result.Changes {
			if !reflect.DeepEqual(change.After.CodexAuthIDs, tc.ids) || change.After.CodexGroup != tc.group || change.After.CodexFallback != tc.fallback {
				t.Fatalf("%s: unexpected binding %+v", tc.body, change.After)
			}
		}
		for _, key := range app.Store().Keys() {
			if key.RPM != 100 || key.DailyLimitUSD != 20 || key.WeeklyLimitUSD != 80 || key.Name != key.ID || key.CallerScope != policy.CallerScope("sk-"+key.ID) {
				t.Fatalf("batch changed identity or limits: %+v", key)
			}
		}
	}
}

func TestBatchBindingManagementRejectsMalformedAtomically(t *testing.T) {
	app := newBatchBindingApp(t)
	beforeKeys, beforeDisk := app.Store().Keys(), batchAppState(t, app)
	cases := []struct {
		body   string
		status int
	}{
		{`{"ids":["a","missing"],"clear":true}`, http.StatusNotFound},
		{`{"ids":["A"],"clear":true}`, http.StatusNotFound},
		{`{"ids":["a","a"],"clear":true}`, http.StatusBadRequest},
		{`{"ids":[" a"],"clear":true}`, http.StatusBadRequest},
		{`{"ids":[],"clear":true}`, http.StatusBadRequest},
		{`{"ids":["a"]}`, http.StatusBadRequest},
		{`{"ids":["a"],"accounts_mode":"keep"}`, http.StatusBadRequest},
		{`{"ids":["a"],"clear":false}`, http.StatusBadRequest},
		{`{"ids":["a"],"accounts_mode":"bad","codex_auth_ids":[]}`, http.StatusBadRequest},
		{`{"ids":["a"],"accounts_mode":"set"}`, http.StatusBadRequest},
		{`{"ids":["a"],"codex_auth_ids":[]}`, http.StatusBadRequest},
		{`{"ids":["a"],"accounts_mode":"remove","codex_auth_ids":[]}`, http.StatusBadRequest},
		{`{"ids":["a"],"clear":true,"codex_fallback":false}`, http.StatusBadRequest},
		{`{"ids":["a"],"clear":true,"accounts_mode":"keep"}`, http.StatusBadRequest},
		{`{"ids":["a"],"clear":true,"rpm":10}`, http.StatusBadRequest},
		{`{"ids":["a"],"clear":true,"daily_limit_usd":4}`, http.StatusBadRequest},
		{`{"ids":["a"],"clear":true,"unexpected":true}`, http.StatusBadRequest},
		{`{"ids":["a"],"codex_fallback":"false"}`, http.StatusBadRequest},
		{`{"ids":["a"],"codex_group":3}`, http.StatusBadRequest},
		{`{"ids":["a"],"clear":null}`, http.StatusBadRequest},
		{`{"ids":["a"],"clear":true,"dry_run":null}`, http.StatusBadRequest},
		{`{"ids":["a"],"codex_fallback":null}`, http.StatusBadRequest},
		{`{"ids":["a"],"codex_group":null}`, http.StatusBadRequest},
		{`{"ids":["a"],"accounts_mode":null,"clear":true}`, http.StatusBadRequest},
		{`{"ids":["a"],"accounts_mode":"set","codex_auth_ids":null}`, http.StatusBadRequest},
		{`{"ids":["a"],"accounts_mode":"set","codex_auth_ids":[null]}`, http.StatusBadRequest},
		{`{"ids":["a"],"accounts_mode":"set","codex_auth_ids":[1]}`, http.StatusBadRequest},
		{`{"ids":null,"clear":true}`, http.StatusBadRequest},
		{`{"ids":[null],"clear":true}`, http.StatusBadRequest},
		{`{"ids":["a"],"clear":true,"expected":null}`, http.StatusBadRequest},
		{`{"ids":["a"],"clear":true,"expected":[]}`, http.StatusBadRequest},
		{`{"ids":["a"],"clear":true,"expected":[{"id":"a","before":{}}]}`, http.StatusBadRequest},
		{`{"ids":["a"],"clear":true,"expected":[{"id":"a","before":null}]}`, http.StatusBadRequest},
		{`{"ids":["a"],"clear":true,"expected":[{"id":"a","before":{"codex_auth_ids":[],"codex_group":"","codex_fallback":false,"rpm":5}}]}`, http.StatusBadRequest},
		{`{"ids":["a"],"clear":true,"expected":[{"id":"a","before":{"codex_auth_ids":[null],"codex_group":"","codex_fallback":false}}]}`, http.StatusBadRequest},
		{`{"ids":["a","b"],"clear":true,"expected":[{"id":"a","before":{"codex_auth_ids":["old-account"],"codex_group":"","codex_fallback":false}},{"id":"a","before":{"codex_auth_ids":["old-account"],"codex_group":"","codex_fallback":false}}]}`, http.StatusBadRequest},
		{`{"ids":["a"],"clear":true,"expected":[{"id":"b","before":{"codex_auth_ids":["old-account"],"codex_group":"","codex_fallback":false}}]}`, http.StatusBadRequest},
		{`{"ids":["a","b"],"clear":true,"expected":[{"id":"a","before":{"codex_auth_ids":["old-account"],"codex_group":"","codex_fallback":false}},{"id":"b","before":{"codex_auth_ids":["stale"],"codex_group":"","codex_fallback":false}}]}`, http.StatusConflict},
		{`null`, http.StatusBadRequest},
		{`[]`, http.StatusBadRequest},
		{`{"ids":["a"],"clear":true} {}`, http.StatusBadRequest},
		{`{`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.body, func(t *testing.T) {
			response := callManagement(t, app, http.MethodPost, batchBindingPath, []byte(tc.body))
			if response.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d: %s", response.StatusCode, tc.status, response.Body)
			}
			if !reflect.DeepEqual(beforeKeys, app.Store().Keys()) || !bytes.Equal(beforeDisk, batchAppState(t, app)) {
				t.Fatal("rejected batch changed memory or disk")
			}
		})
	}
}

func TestBatchBindingManagementSaveFailureIs500AndAtomic(t *testing.T) {
	app := newBatchBindingApp(t)
	beforeKeys, beforeDisk := app.Store().Keys(), batchAppState(t, app)
	path := app.Store().StatePath()
	backup := filepath.Join(filepath.Dir(path), "saved-state.json")
	if err := os.Rename(path, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	response := callManagement(t, app, http.MethodPost, batchBindingPath, []byte(`{"ids":["a","b"],"clear":true}`))
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(backup, path); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("save failure = %d %s", response.StatusCode, response.Body)
	}
	if !reflect.DeepEqual(beforeKeys, app.Store().Keys()) || !bytes.Equal(beforeDisk, batchAppState(t, app)) {
		t.Fatal("save failure changed memory or saved state")
	}
}
