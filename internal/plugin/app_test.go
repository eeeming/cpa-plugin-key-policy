package plugin

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"cpa-key-policy/internal/policy"
)

func decodeEnvelope(t *testing.T, raw []byte, dest any) Envelope {
	t.Helper()
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("envelope: %v raw=%s", err, raw)
	}
	if !env.OK {
		t.Fatalf("envelope not ok: %+v", env.Error)
	}
	if dest != nil && len(env.Result) > 0 {
		if err := json.Unmarshal(env.Result, dest); err != nil {
			t.Fatalf("result: %v (%s)", err, env.Result)
		}
	}
	return env
}

func configureApp(t *testing.T) *App {
	t.Helper()
	app, _ := configureAppReg(t)
	return app
}

func configureAppReg(t *testing.T) (*App, Registration) {
	t.Helper()
	app := NewApp()
	yaml := []byte("enabled: true\nstate_file: \"" + filepath.ToSlash(filepath.Join(t.TempDir(), "state.json")) + "\"\n")
	req, _ := json.Marshal(LifecycleRequest{ConfigYAML: yaml})
	raw, err := app.HandleMethod(MethodPluginRegister, req)
	if err != nil {
		t.Fatal(err)
	}
	var reg Registration
	decodeEnvelope(t, raw, &reg)
	t.Cleanup(app.Shutdown)
	return app, reg
}

func TestRegistrationIdentityAndCapabilities(t *testing.T) {
	_, reg := configureAppReg(t)
	if reg.SchemaVersion < 2 {
		t.Fatalf("schema = %d, want >= 2", reg.SchemaVersion)
	}
	if reg.Metadata.Name != "cpa-key-quota" {
		t.Fatalf("name = %q", reg.Metadata.Name)
	}
	c := reg.Capabilities
	if c.FrontendAuthProvider || c.FrontendAuthProviderExclusive || c.ModelRouter || c.Scheduler || c.ResponseInterceptor {
		t.Fatalf("must not declare dropped capabilities: %+v", c)
	}
	if !c.RequestInterceptor || !c.UsagePlugin || !c.ManagementAPI {
		t.Fatalf("missing required capabilities: %+v", c)
	}
}

func TestUnknownMethodDoesNotRegisterAuth(t *testing.T) {
	app := configureApp(t)
	raw, err := app.HandleMethod("frontend_auth.authenticate", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if env.OK || env.Error == nil || env.Error.Code != "unknown_method" {
		t.Fatalf("frontend_auth should be unknown: %+v", env)
	}
}

func TestInterceptUnknownKeyNoop(t *testing.T) {
	app := configureApp(t)
	raw, err := app.HandleMethod(MethodRequestInterceptBefore, mustJSON(RequestInterceptRequest{
		Headers: http.Header{"Authorization": {"Bearer sk-unknown"}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	var resp RequestInterceptResponse
	decodeEnvelope(t, raw, &resp)
	if resp.Terminate {
		t.Fatalf("unbound must not terminate: %+v", resp)
	}
}

func TestPatchRotatesPlaintextWithoutStoringIt(t *testing.T) {
	app := configureApp(t)
	oldKey, newKey := "sk-old-secret", "sk-new-secret"
	bind := callManagement(t, app, http.MethodPost, "/v0/management/plugins/cpa-key-quota/keys", mustJSON(map[string]any{
		"id": "rot", "name": "rot", "key": oldKey, "rpm": 10, "daily_limit_usd": 5,
	}))
	if bind.StatusCode != http.StatusCreated {
		t.Fatalf("bind = %d %s", bind.StatusCode, bind.Body)
	}
	if strings.Contains(string(bind.Body), oldKey) {
		t.Fatalf("bind response leaked plaintext: %s", bind.Body)
	}

	patched := callManagement(t, app, http.MethodPatch, "/v0/management/plugins/cpa-key-quota/keys", mustJSON(map[string]any{
		"id": "rot", "key": newKey,
	}))
	if patched.StatusCode != http.StatusOK {
		t.Fatalf("patch = %d %s", patched.StatusCode, patched.Body)
	}
	if strings.Contains(string(patched.Body), newKey) || strings.Contains(string(patched.Body), oldKey) {
		t.Fatalf("patch response leaked plaintext: %s", patched.Body)
	}
	var got struct {
		Key publicKey `json:"key"`
	}
	if err := json.Unmarshal(patched.Body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Key.KeyPreview == "" || strings.Contains(got.Key.KeyPreview, newKey) {
		t.Fatalf("preview = %q", got.Key.KeyPreview)
	}

	listed := callManagement(t, app, http.MethodGet, "/v0/management/plugins/cpa-key-quota/keys", nil)
	if strings.Contains(string(listed.Body), `"key_hash"`) || strings.Contains(string(listed.Body), newKey) {
		t.Fatalf("list leaked hash or plaintext: %s", listed.Body)
	}

	if d := interceptBearer(t, app, oldKey); d.Terminate {
		t.Fatalf("old key should be unbound no-op: %+v", d)
	}
	if d := interceptBearer(t, app, newKey); d.Terminate {
		t.Fatalf("new key should be bound and admitted: %+v", d)
	}
}

func TestBindRequiresPlaintext(t *testing.T) {
	app := configureApp(t)
	resp := callManagement(t, app, http.MethodPost, "/v0/management/plugins/cpa-key-quota/keys", []byte(`{"id":"x","name":"x"}`))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", resp.StatusCode, resp.Body)
	}
}

func mustJSON(v any) []byte {
	raw, _ := json.Marshal(v)
	return raw
}

func callManagement(t *testing.T, app *App, method, path string, body []byte) ManagementResponse {
	t.Helper()
	raw, err := app.HandleMethod(MethodManagementHandle, mustJSON(ManagementRequest{
		Method: method,
		Path:   path,
		Body:   body,
	}))
	if err != nil {
		t.Fatal(err)
	}
	var resp ManagementResponse
	decodeEnvelope(t, raw, &resp)
	return resp
}

func TestCallerScopeFromMetadata(t *testing.T) {
	app := configureApp(t)
	plain := "sk-meta"
	bind := callManagement(t, app, http.MethodPost, "/v0/management/plugins/cpa-key-quota/keys", mustJSON(map[string]any{
		"id": "meta", "name": "meta", "key": plain, "rpm": 10,
	}))
	if bind.StatusCode != http.StatusCreated {
		t.Fatalf("bind = %d %s", bind.StatusCode, bind.Body)
	}
	raw, err := app.HandleMethod(MethodRequestInterceptBefore, mustJSON(RequestInterceptRequest{
		Metadata: map[string]any{"caller_scope": policy.CallerScope(plain)},
	}))
	if err != nil {
		t.Fatal(err)
	}
	var resp RequestInterceptResponse
	decodeEnvelope(t, raw, &resp)
	if resp.Terminate {
		t.Fatalf("bound via caller_scope should admit: %+v", resp)
	}
}
