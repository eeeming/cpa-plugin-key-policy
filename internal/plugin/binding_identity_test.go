package plugin

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cpa-key-policy/internal/policy"
)

func TestBindingKeyIdentityDoesNotDependOnAliasOrExposePlaintext(t *testing.T) {
	const nameless = "sk-no-alias-private-0123456789"
	const named = "sk-named-private-9876543210"
	var logs bytes.Buffer
	app := NewApp()
	app.logger = log.New(&logs, "", 0)
	t.Cleanup(app.Shutdown)
	path := filepath.Join(t.TempDir(), "state.json")
	config := "enabled: true\nstate_file: " + path + "\nplus_management_key: " + nameless + "\n"
	raw, err := app.HandleMethod(MethodPluginRegister, mustJSON(LifecycleRequest{ConfigYAML: []byte(config)}))
	if err != nil {
		t.Fatal(err)
	}
	decodeEnvelope(t, raw, nil)
	app.Store().SetAPIKeyLister(func() ([]policy.PlusAPIKey, error) {
		return []policy.PlusAPIKey{{Plain: nameless}, {Plain: named, Name: "Customer Alias"}}, nil
	})
	apiBodies := [][]byte{raw}
	synced := callManagement(t, app, http.MethodPost, "/v0/management/plugins/"+PluginID+"/keys/sync", nil)
	if synced.StatusCode != http.StatusOK {
		t.Fatalf("sync: %d %s", synced.StatusCode, synced.Body)
	}
	apiBodies = append(apiBodies, synced.Body)
	var namelessID, namedID string
	for id, key := range listPublicKeys(t, app) {
		switch key.Name {
		case policy.PreviewKey(nameless):
			namelessID = id
		case "Customer Alias":
			namedID = id
		}
	}
	if namelessID == "" || namedID == "" || namelessID == namedID {
		t.Fatal("sync must expose a masked key without an alias and the supplied alias when present")
	}
	for id, authID := range map[string]string{namelessID: "a1", namedID: "b1"} {
		patched := callManagement(t, app, http.MethodPatch, "/v0/management/plugins/"+PluginID+"/keys", mustJSON(map[string]any{
			"id": id, "codex_auth_ids": []string{authID},
		}))
		if patched.StatusCode != http.StatusOK {
			t.Fatalf("configure alias-independent binding: %d %s", patched.StatusCode, patched.Body)
		}
		apiBodies = append(apiBodies, patched.Body)
	}
	candidates := []SchedulerAuthCandidate{{ID: "a1", Provider: "codex"}, {ID: "b1", Provider: "codex"}}
	for plain, authID := range map[string]string{nameless: "a1", named: "b1"} {
		before := interceptBearer(t, app, plain)
		if got := pickBindingThroughApp(t, app, before.Headers, candidates...); got.AuthID != authID {
			t.Fatalf("binding required an alias: %+v", got)
		}
		encoded, _, found := strings.Cut(before.Headers.Get(bindingHeader), ".")
		if !found {
			t.Fatal("missing signed snapshot")
		}
		payload, err := base64.RawURLEncoding.DecodeString(encoded)
		if err != nil {
			t.Fatal(err)
		}
		var snapshot bindingSnapshot
		if err := json.Unmarshal(payload, &snapshot); err != nil {
			t.Fatal(err)
		}
		hash, err := policy.HashKey(plain)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.KeyID != hash || strings.Contains(string(payload), plain) || strings.Contains(string(payload), "Customer Alias") {
			t.Fatalf("internal header must carry only irreversible key identity and authorization data: %s", payload)
		}
	}
	renamed := callManagement(t, app, http.MethodPatch, "/v0/management/plugins/"+PluginID+"/keys", mustJSON(map[string]any{
		"id": namedID, "name": "Renamed Customer",
	}))
	if renamed.StatusCode != http.StatusOK {
		t.Fatalf("rename: %d %s", renamed.StatusCode, renamed.Body)
	}
	apiBodies = append(apiBodies, renamed.Body)
	if key := listPublicKeys(t, app)[namedID]; key.Name != "Renamed Customer" || len(key.CodexAuthIDs) != 1 || key.CodexAuthIDs[0] != "b1" {
		t.Fatalf("rename lost stable key binding: %+v", key)
	}
	if got := pickBindingThroughApp(t, app, interceptBearer(t, app, named).Headers, candidates...); got.AuthID != "b1" {
		t.Fatalf("renamed key changed routing: %+v", got)
	}
	// An accidentally pasted key in the alias field is masked even without
	// resubmitting plaintext through the key-rotation field.
	masked := callManagement(t, app, http.MethodPatch, "/v0/management/plugins/"+PluginID+"/keys", mustJSON(map[string]any{
		"id": namedID, "name": named,
	}))
	if masked.StatusCode != http.StatusOK {
		t.Fatalf("mask accidental alias: %d %s", masked.StatusCode, masked.Body)
	}
	apiBodies = append(apiBodies, masked.Body)
	for _, suffix := range []string{"/keys", "/status", "/codex-auths"} {
		response := callManagement(t, app, http.MethodGet, "/v0/management/plugins/"+PluginID+suffix, nil)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("get %s: %d %s", suffix, response.StatusCode, response.Body)
		}
		apiBodies = append(apiBodies, response.Body)
	}
	state, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	apiBodies = append(apiBodies, state, logs.Bytes())
	for _, body := range apiBodies {
		for _, plain := range []string{nameless, named} {
			if bytes.Contains(body, []byte(plain)) {
				t.Fatal("plaintext key leaked through management, state, or startup diagnostics")
			}
		}
	}
	if !strings.Contains(logs.String(), "unknown") || !strings.Contains(logs.String(), "Home") {
		t.Fatal("startup must make unprobeable scheduler state and the Home limitation visible")
	}
}

func TestBindWithoutAliasUsesMaskedKeyDisplay(t *testing.T) {
	app := configureApp(t)
	plain := "sk-bind-without-alias-0123456789"
	response := callManagement(t, app, http.MethodPost, "/v0/management/plugins/"+PluginID+"/keys", mustJSON(map[string]any{
		"key": plain, "codex_auth_ids": []string{"a1"},
	}))
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("bind without alias: %d %s", response.StatusCode, response.Body)
	}
	var payload struct {
		Key publicKey `json:"key"`
	}
	if err := json.Unmarshal(response.Body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Key.Name != policy.PreviewKey(plain) || strings.Contains(string(response.Body), plain) {
		t.Fatalf("missing alias was not replaced with a masked display: %+v", payload.Key)
	}
	if got := pickBindingThroughApp(t, app, interceptBearer(t, app, plain).Headers, SchedulerAuthCandidate{ID: "a1", Provider: "codex"}); got.AuthID != "a1" {
		t.Fatalf("nameless bound key did not route: %+v", got)
	}
}
