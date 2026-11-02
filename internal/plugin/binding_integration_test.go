package plugin

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"cpa-key-policy/internal/policy"
)

func bindCodexPolicy(t *testing.T, app *App, id string, fields map[string]any) {
	t.Helper()
	payload := map[string]any{"id": id, "name": id, "key": "sk-" + id}
	for key, value := range fields {
		payload[key] = value
	}
	response := callManagement(t, app, http.MethodPost, "/v0/management/plugins/"+PluginID+"/keys", mustJSON(payload))
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("bind %s: %d %s", id, response.StatusCode, response.Body)
	}
}

func pickBindingThroughApp(t *testing.T, app *App, headers http.Header, candidates ...SchedulerAuthCandidate) SchedulerPickResponse {
	t.Helper()
	raw, err := app.HandleMethod(MethodSchedulerPick, mustJSON(SchedulerPickRequest{
		Provider: "codex", Model: "model", Options: SchedulerOptions{Headers: headers}, Candidates: candidates,
	}))
	if err != nil {
		t.Fatal(err)
	}
	var response SchedulerPickResponse
	decodeEnvelope(t, raw, &response)
	return response
}

func TestBindingSnapshotsSurvivePatchAndReconfigure(t *testing.T) {
	app := configureApp(t)
	bindCodexPolicy(t, app, "a", map[string]any{"codex_auth_ids": []string{"a1"}})
	bindCodexPolicy(t, app, "b", map[string]any{"codex_auth_ids": []string{"b1"}})
	candidates := []SchedulerAuthCandidate{{ID: "a1", Provider: "codex"}, {ID: "b1", Provider: "codex"}}
	h1 := interceptBearer(t, app, "sk-a").Headers
	if got := pickBindingThroughApp(t, app, interceptBearer(t, app, "sk-b").Headers, candidates...); !got.Handled || got.AuthID != "b1" {
		t.Fatalf("key B escaped its binding: %+v", got)
	}
	patched := callManagement(t, app, http.MethodPatch, "/v0/management/plugins/"+PluginID+"/keys", []byte(`{"id":"a","codex_auth_ids":["b1"]}`))
	if patched.StatusCode != http.StatusOK {
		t.Fatalf("patch: %d %s", patched.StatusCode, patched.Body)
	}
	h2 := interceptBearer(t, app, "sk-a").Headers
	if got := pickBindingThroughApp(t, app, h2, candidates...); !got.Handled || got.AuthID != "b1" {
		t.Fatalf("new request did not use new allocation: %+v", got)
	}
	if got := pickBindingThroughApp(t, app, h1, candidates...); !got.Handled || got.AuthID != "a1" {
		t.Fatalf("in-flight retry changed allocation: %+v", got)
	}
	path := app.Store().StatePath()
	raw, err := app.HandleMethod(MethodPluginReconfigure, mustJSON(LifecycleRequest{ConfigYAML: []byte("enabled: true\nstate_file: " + path + "\n")}))
	if err != nil {
		t.Fatal(err)
	}
	decodeEnvelope(t, raw, nil)
	originalStore := app.store
	app.store = nil
	defer func() { app.store = originalStore }()
	if got := pickBindingThroughApp(t, app, h1, candidates...); !got.Handled || got.AuthID != "a1" {
		t.Fatalf("pick depended on live store or reconfigure rotated the signing key: %+v", got)
	}
}

func TestBindingAdmissionDoesNotRefetchPolicyAfterQuotaCheck(t *testing.T) {
	app := configureApp(t)
	bindCodexPolicy(t, app, "a", map[string]any{"codex_auth_ids": []string{"a1"}})
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	app.Store().SetPriceLister(func() ([]policy.ModelPrice, error) {
		close(started)
		<-release
		return []policy.ModelPrice{{Model: "paid", InputPricePerMillion: 1, Enabled: true}}, nil
	})
	type result struct {
		raw []byte
		err error
	}
	finished := make(chan result, 1)
	go func() {
		raw, err := app.HandleMethod(MethodRequestInterceptBefore, mustJSON(RequestInterceptRequest{
			Headers: http.Header{"Authorization": {"Bearer sk-a"}}, Model: "paid",
		}))
		finished <- result{raw, err}
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("admission never reached the quota price lookup")
	}
	patched := callManagement(t, app, http.MethodPatch, "/v0/management/plugins/"+PluginID+"/keys", []byte(`{"id":"a","codex_auth_ids":["b1"]}`))
	if patched.StatusCode != http.StatusOK {
		t.Fatalf("patch during admission: %d %s", patched.StatusCode, patched.Body)
	}
	unblock()
	var completed result
	select {
	case completed = <-finished:
	case <-time.After(10 * time.Second):
		t.Fatal("admission did not finish")
	}
	if completed.err != nil {
		t.Fatal(completed.err)
	}
	var response RequestInterceptResponse
	decodeEnvelope(t, completed.raw, &response)
	candidates := []SchedulerAuthCandidate{{ID: "a1", Provider: "codex"}, {ID: "b1", Provider: "codex"}}
	if got := pickBindingThroughApp(t, app, response.Headers, candidates...); got.AuthID != "a1" {
		t.Fatalf("before refetched policy after admission's quota check: %+v", got)
	}
	if got := pickBindingThroughApp(t, app, interceptBearer(t, app, "sk-a").Headers, candidates...); got.AuthID != "b1" {
		t.Fatalf("next admission failed to observe PATCH: %+v", got)
	}
}

func TestBindingClientHeaderIsReplacedAndTamperingRejected(t *testing.T) {
	app := configureApp(t)
	bindCodexPolicy(t, app, "a", map[string]any{"codex_auth_ids": []string{"a1"}})
	incoming := http.Header{
		"Authorization":                {"Bearer sk-a"},
		bindingHeader:                  {"client-forged"},
		strings.ToLower(bindingHeader): {"another-forgery"},
		strings.ToUpper(bindingHeader): {"third-forgery"},
	}
	raw, err := app.HandleMethod(MethodRequestInterceptBefore, mustJSON(RequestInterceptRequest{Headers: incoming}))
	if err != nil {
		t.Fatal(err)
	}
	var before RequestInterceptResponse
	decodeEnvelope(t, raw, &before)
	for name := range incoming {
		if !strings.EqualFold(name, bindingHeader) {
			continue
		}
		found := false
		for _, clear := range before.ClearHeaders {
			found = found || clear == name
		}
		if !found {
			t.Fatalf("client header spelling %q was not cleared: %+v", name, before)
		}
	}
	if got := pickBindingThroughApp(t, app, before.Headers, SchedulerAuthCandidate{ID: "a1", Provider: "codex"}); got.AuthID != "a1" {
		t.Fatalf("forged header was not replaced: %+v", got)
	}
	tampered := before.Headers.Clone()
	tampered.Set(bindingHeader, before.Headers.Get(bindingHeader)+"tampered")
	raw, err = app.HandleMethod(MethodSchedulerPick, mustJSON(SchedulerPickRequest{
		Options: SchedulerOptions{Headers: tampered}, Candidates: []SchedulerAuthCandidate{{ID: "a1", Provider: "codex"}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	var rejected Envelope
	if err := json.Unmarshal(raw, &rejected); err != nil {
		t.Fatal(err)
	}
	if rejected.OK || rejected.Error == nil || rejected.Error.HTTPStatus < 400 {
		t.Fatalf("tampering fell through to native scheduling: %s", raw)
	}
	unboundRaw, err := app.HandleMethod(MethodRequestInterceptBefore, mustJSON(RequestInterceptRequest{Headers: http.Header{
		"Authorization": {"Bearer sk-unknown"}, bindingHeader: {before.Headers.Get(bindingHeader)},
	}}))
	if err != nil {
		t.Fatal(err)
	}
	var unbound RequestInterceptResponse
	decodeEnvelope(t, unboundRaw, &unbound)
	if got := pickBindingThroughApp(t, app, unbound.Headers, SchedulerAuthCandidate{ID: "a1", Provider: "codex"}); got.Handled {
		t.Fatalf("unbound request reused a supplied binding: %+v", got)
	}
}

func TestBindingQuota429AndAfterHeaderCleanup(t *testing.T) {
	app := configureApp(t)
	bindCodexPolicy(t, app, "a", map[string]any{"codex_auth_ids": []string{"a1"}, "rpm": 1})
	if got := interceptBearer(t, app, "sk-a"); got.Terminate || got.Headers.Get(bindingHeader) == "" {
		t.Fatalf("first request was not bound: %+v", got)
	}
	over := interceptBearer(t, app, "sk-a")
	if !over.Terminate || over.StatusCode != http.StatusTooManyRequests || over.Headers.Get(bindingHeader) != "" {
		t.Fatalf("over-quota request acquired a snapshot or lost 429: %+v", over)
	}
	assertOpenAIQuotaError(t, over, "rate_limit_exceeded")
	raw, err := app.HandleMethod(MethodRequestInterceptAfter, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var after RequestInterceptResponse
	decodeEnvelope(t, raw, &after)
	if !reflect.DeepEqual(after.ClearHeaders, []string{bindingHeader}) {
		t.Fatalf("after did not strip the internal header: %+v", after)
	}
}

func TestBindingPatchPreservesOmittedFieldsAndClearsExplicitValues(t *testing.T) {
	app := configureApp(t)
	bindCodexPolicy(t, app, "a", map[string]any{
		"codex_auth_ids": []string{" a1 ", "", "a1"}, "codex_group": " Pro ", "codex_fallback": true, "daily_limit_usd": 12,
	})
	response := callManagement(t, app, http.MethodPatch, "/v0/management/plugins/"+PluginID+"/keys", []byte(`{"id":"a","rpm":9}`))
	if response.StatusCode != http.StatusOK {
		t.Fatalf("quota patch: %d %s", response.StatusCode, response.Body)
	}
	key := listPublicKeys(t, app)["a"]
	if !reflect.DeepEqual(key.CodexAuthIDs, []string{"a1"}) || key.CodexGroup != "pro" || !key.CodexFallback || key.DailyLimitUSD != 12 {
		t.Fatalf("unrelated patch lost normalized binding/quota: %+v", key)
	}
	response = callManagement(t, app, http.MethodPatch, "/v0/management/plugins/"+PluginID+"/keys", []byte(`{"id":"a","codex_auth_ids":[],"codex_group":"","codex_fallback":false}`))
	if response.StatusCode != http.StatusOK {
		t.Fatalf("clear binding: %d %s", response.StatusCode, response.Body)
	}
	key = listPublicKeys(t, app)["a"]
	if len(key.CodexAuthIDs) != 0 || key.CodexGroup != "" || key.CodexFallback || key.RPM != 9 || key.DailyLimitUSD != 12 {
		t.Fatalf("explicit clear changed quota or retained binding: %+v", key)
	}
	if got := pickBindingThroughApp(t, app, interceptBearer(t, app, "sk-a").Headers, SchedulerAuthCandidate{ID: "other", Provider: "codex"}); got.Handled {
		t.Fatalf("cleared binding still intercepted scheduling: %+v", got)
	}
}

func TestBindingOldStateLeavesNativeSchedulerInControl(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.json")
	hash, err := policy.HashKey("sk-legacy")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, mustJSON(map[string]any{
		"version": 1, "keys": []map[string]any{{"id": "legacy", "name": "Legacy", "enabled": true, "key_hash": hash}},
	}), 0o600); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	t.Cleanup(app.Shutdown)
	raw, err := app.HandleMethod(MethodPluginRegister, mustJSON(LifecycleRequest{ConfigYAML: []byte("state_file: " + path + "\n")}))
	if err != nil {
		t.Fatal(err)
	}
	decodeEnvelope(t, raw, nil)
	if got := pickBindingThroughApp(t, app, interceptBearer(t, app, "sk-legacy").Headers, SchedulerAuthCandidate{ID: "any", Provider: "codex"}); got.Handled {
		t.Fatalf("legacy policy unexpectedly restricted credentials: %+v", got)
	}
}
