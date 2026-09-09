package plugin

import (
	"encoding/json"
	"net/http"
	"testing"

	"cpa-key-policy/internal/policy"
)

// TestQuotaPipeline is the in-process end-to-end path:
// register → bind → intercept under limit → usage.handle → intercept over limit → GET keys/usage.
// It fails if any hop is skipped.
func TestQuotaPipelineFromCPAMPPrices(t *testing.T) {
	app := configureApp(t)
	list, err := policy.ParseModelPrices([]byte(`{"prices":{"gpt-4.1-mini":{"prompt":1000,"completion":0,"cacheRead":0}}}`))
	if err != nil {
		t.Fatal(err)
	}
	app.Store().SetPriceLister(func() ([]policy.ModelPrice, error) { return list, nil })

	plain := "sk-cpamp-client"
	bind := callManagement(t, app, http.MethodPost, "/v0/management/plugins/"+PluginID+"/keys", mustJSON(map[string]any{
		"id": "cpamp", "name": "cpamp", "key": plain, "rpm": 50, "daily_limit_usd": 1.5,
	}))
	if bind.StatusCode != http.StatusCreated {
		t.Fatalf("bind: %d %s", bind.StatusCode, bind.Body)
	}
	if under := interceptBearer(t, app, plain); under.Terminate {
		t.Fatalf("under-limit: %+v", under)
	}
	_, err = app.HandleMethod(MethodUsageHandle, mustJSON(UsageHandleRequest{
		APIKey: plain, Model: "gpt-4.1-mini", Provider: "openai-compatible",
		Detail: UsageDetail{InputTokens: 1000},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.HandleMethod(MethodUsageHandle, mustJSON(UsageHandleRequest{
		APIKey: plain, Model: "gpt-4.1-mini", Provider: "openai-compatible",
		Detail: UsageDetail{InputTokens: 1000},
	})); err != nil {
		t.Fatal(err)
	}
	over := interceptBearer(t, app, plain)
	if !over.Terminate || over.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("over-limit: %+v", over)
	}
	assertOpenAIQuotaError(t, over, "insufficient_quota")
	usageRaw, err := app.HandleMethod(MethodManagementHandle, mustJSON(ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/" + PluginID + "/keys/usage",
		Query:  map[string][]string{"id": {"cpamp"}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	var usage ManagementResponse
	decodeEnvelope(t, usageRaw, &usage)
	var got map[string]any
	if err := json.Unmarshal(usage.Body, &got); err != nil {
		t.Fatal(err)
	}
	if daily, _ := got["daily_usd"].(float64); daily < 1.9 {
		t.Fatalf("daily_usd=%v want ~2", got["daily_usd"])
	}
}

func TestQuotaPipeline(t *testing.T) {
	app := configureApp(t)
	hops := map[string]bool{}
	mark := func(name string) { hops[name] = true }
	if app.registration().Metadata.Name != PluginID || !app.registration().Capabilities.RequestInterceptor {
		t.Fatalf("register hop failed: %+v", app.registration())
	}
	mark("register")

	app.Store().SetPriceLister(func() ([]policy.ModelPrice, error) {
		return []policy.ModelPrice{{
			Provider: "openai", Model: "gpt-4.1-mini", ServiceTier: "priority",
			MinInputTokens: 0, InputPricePerMillion: 1000, Enabled: true,
		}, {
			Provider: "openai", Model: "gpt-4.1-mini", ServiceTier: "priority",
			MinInputTokens: 2000, InputPricePerMillion: 2500, Enabled: true,
		}}, nil
	})

	plain := "sk-plus-client"
	bind := callManagement(t, app, http.MethodPost, "/v0/management/plugins/"+PluginID+"/keys", mustJSON(map[string]any{
		"id": "client-a", "name": "Client A", "key": plain,
		"rpm": 50, "daily_limit_usd": 1.5,
	}))
	if bind.StatusCode != http.StatusCreated {
		t.Fatalf("bind hop failed: %d %s", bind.StatusCode, bind.Body)
	}
	var bound struct {
		Key publicKey `json:"key"`
	}
	if err := json.Unmarshal(bind.Body, &bound); err != nil {
		t.Fatal(err)
	}
	if bound.Key.KeyPreview == "" || bound.Key.ID != "client-a" {
		t.Fatalf("bind did not persist preview: %+v", bound.Key)
	}
	mark("bind")

	under := interceptBearer(t, app, plain)
	if under.Terminate {
		t.Fatalf("under-limit intercept must admit: %+v", under)
	}
	mark("intercept_under")

	usageRaw, err := app.HandleMethod(MethodUsageHandle, mustJSON(UsageHandleRequest{
		APIKey:      plain,
		Model:       "gpt-4.1-mini",
		Alias:       "gpt-4.1-mini",
		Provider:    "openai",
		ServiceTier: "priority",
		Detail:      UsageDetail{InputTokens: 1000},
	}))
	if err != nil {
		t.Fatal(err)
	}
	decodeEnvelope(t, usageRaw, &UsageHandleResponse{})
	mark("usage")

	// 1000 tokens @ $1000/M = $1.00, still under $1.50 — admit and bill again.
	if second := interceptBearer(t, app, plain); second.Terminate {
		t.Fatalf("second under-limit intercept terminated: %+v", second)
	}
	_, _ = app.HandleMethod(MethodUsageHandle, mustJSON(UsageHandleRequest{
		APIKey:      plain,
		Model:       "gpt-4.1-mini",
		Provider:    "openai",
		ServiceTier: "priority",
		Detail:      UsageDetail{InputTokens: 1000},
	}))

	over := interceptBearer(t, app, plain)
	if !over.Terminate || over.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("over-limit intercept hop failed: %+v", over)
	}
	assertOpenAIQuotaError(t, over, "insufficient_quota")
	mark("intercept_over")

	usageRawMgmt, err := app.HandleMethod(MethodManagementHandle, mustJSON(ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/" + PluginID + "/keys/usage",
		Query:  map[string][]string{"id": {"client-a"}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	var usage ManagementResponse
	decodeEnvelope(t, usageRawMgmt, &usage)
	if usage.StatusCode != http.StatusOK {
		t.Fatalf("usage hop failed: %d %s", usage.StatusCode, usage.Body)
	}
	var got map[string]any
	if err := json.Unmarshal(usage.Body, &got); err != nil {
		t.Fatal(err)
	}
	if got["key_id"] != "client-a" {
		t.Fatalf("usage key_id = %v", got["key_id"])
	}
	if daily, _ := got["daily_usd"].(float64); daily < 1.9 {
		t.Fatalf("daily_usd = %v, want ~2 from two $1 bills", got["daily_usd"])
	}
	mark("keys_usage")

	required := []string{"register", "bind", "intercept_under", "usage", "intercept_over", "keys_usage"}
	for _, hop := range required {
		if !hops[hop] {
			t.Fatalf("skipped hop %q; completed=%v", hop, hops)
		}
	}
}

func interceptBearer(t *testing.T, app *App, plain string) RequestInterceptResponse {
	t.Helper()
	raw, err := app.HandleMethod(MethodRequestInterceptBefore, mustJSON(RequestInterceptRequest{
		Headers:  http.Header{"Authorization": {"Bearer " + plain}},
		Metadata: map[string]any{"caller_scope": policy.CallerScope(plain)},
	}))
	if err != nil {
		t.Fatal(err)
	}
	var resp RequestInterceptResponse
	decodeEnvelope(t, raw, &resp)
	return resp
}

func assertOpenAIQuotaError(t *testing.T, resp RequestInterceptResponse, wantCode string) {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(resp.ResponseBody, &payload); err != nil {
		t.Fatalf("429 body: %v %s", err, resp.ResponseBody)
	}
	errObj, _ := payload["error"].(map[string]any)
	if errObj == nil {
		t.Fatalf("missing error object: %s", resp.ResponseBody)
	}
	if errObj["code"] != wantCode || errObj["type"] != wantCode {
		t.Fatalf("want type=code=%q, got type=%v code=%v body=%s", wantCode, errObj["type"], errObj["code"], resp.ResponseBody)
	}
	if _, ok := errObj["param"]; !ok {
		t.Fatalf("official error object should include param: %s", resp.ResponseBody)
	}
}

func TestQuotaDisabledAndUnboundNoopAndRPM(t *testing.T) {
	app := configureApp(t)
	app.Store().SetPriceLister(func() ([]policy.ModelPrice, error) {
		return []policy.ModelPrice{{Model: "m", ServiceTier: "*", InputPricePerMillion: 1000, Enabled: true}}, nil
	})

	unbound := interceptBearer(t, app, "sk-nobody")
	if unbound.Terminate {
		t.Fatalf("unbound terminate: %+v", unbound)
	}

	disabled := callManagement(t, app, http.MethodPost, "/v0/management/plugins/"+PluginID+"/keys", mustJSON(map[string]any{
		"id": "paused", "name": "paused", "key": "sk-paused", "enabled": false, "daily_limit_usd": 0.01, "rpm": 1,
	}))
	if disabled.StatusCode != http.StatusCreated {
		t.Fatalf("bind disabled: %s", disabled.Body)
	}
	if d := interceptBearer(t, app, "sk-paused"); d.Terminate {
		t.Fatalf("disabled policy must be no-op: %+v", d)
	}
	_, _ = app.HandleMethod(MethodUsageHandle, mustJSON(UsageHandleRequest{
		APIKey: "sk-paused", Model: "m", Detail: UsageDetail{InputTokens: 1_000_000},
	}))
	listed := callManagement(t, app, http.MethodGet, "/v0/management/plugins/"+PluginID+"/keys", nil)
	var keys struct {
		Keys []publicKey `json:"keys"`
	}
	if err := json.Unmarshal(listed.Body, &keys); err != nil {
		t.Fatal(err)
	}
	if len(keys.Keys) != 1 || keys.Keys[0].Usage.DailyUSD != 0 {
		t.Fatalf("disabled usage written: %+v", keys.Keys)
	}

	rpm := callManagement(t, app, http.MethodPost, "/v0/management/plugins/"+PluginID+"/keys", mustJSON(map[string]any{
		"id": "rpm", "key": "sk-rpm", "rpm": 1,
	}))
	if rpm.StatusCode != http.StatusCreated {
		t.Fatalf("bind rpm: %s", rpm.Body)
	}
	if d := interceptBearer(t, app, "sk-rpm"); d.Terminate {
		t.Fatalf("first rpm: %+v", d)
	}
	over := interceptBearer(t, app, "sk-rpm")
	if !over.Terminate {
		t.Fatal("second rpm should 429")
	}
	assertOpenAIQuotaError(t, over, "rate_limit_exceeded")
	if over.ResponseHeaders.Get("Retry-After") != "60" {
		t.Fatalf("Retry-After = %q", over.ResponseHeaders.Get("Retry-After"))
	}
}

func TestMissingPriceSourceUSDSilentRPMWorks(t *testing.T) {
	app := configureApp(t)
	app.Store().SetPriceLister(func() ([]policy.ModelPrice, error) {
		return nil, errUnavailable
	})
	bind := callManagement(t, app, http.MethodPost, "/v0/management/plugins/"+PluginID+"/keys", mustJSON(map[string]any{
		"id": "bare", "key": "sk-bare", "rpm": 1, "daily_limit_usd": 0.0001,
	}))
	if bind.StatusCode != http.StatusCreated {
		t.Fatalf("bind: %s", bind.Body)
	}
	_, _ = app.HandleMethod(MethodUsageHandle, mustJSON(UsageHandleRequest{
		APIKey: "sk-bare", Model: "gpt-4.1-mini", Detail: UsageDetail{InputTokens: 5_000_000},
	}))
	if d := interceptBearer(t, app, "sk-bare"); d.Terminate {
		t.Fatalf("USD must not fire without prices: %+v body=%s", d, d.ResponseBody)
	}
	over := interceptBearer(t, app, "sk-bare")
	if !over.Terminate {
		t.Fatal("RPM should still fire")
	}
	assertOpenAIQuotaError(t, over, "rate_limit_exceeded")
}

var errUnavailable = errString("no plus")

type errString string

func (e errString) Error() string { return string(e) }

func TestWeeklyExceededCode(t *testing.T) {
	app := configureApp(t)
	app.Store().SetPriceLister(func() ([]policy.ModelPrice, error) {
		return []policy.ModelPrice{{Model: "m", ServiceTier: "*", InputPricePerMillion: 1000, Enabled: true}}, nil
	})
	callManagement(t, app, http.MethodPost, "/v0/management/plugins/"+PluginID+"/keys", mustJSON(map[string]any{
		"id": "w", "key": "sk-w", "rpm": 100, "weekly_limit_usd": 0.5,
	}))
	_, _ = app.HandleMethod(MethodUsageHandle, mustJSON(UsageHandleRequest{
		APIKey: "sk-w", Model: "m", Detail: UsageDetail{InputTokens: 1000},
	}))
	over := interceptBearer(t, app, "sk-w")
	if !over.Terminate {
		t.Fatal("expected weekly 429")
	}
	assertOpenAIQuotaError(t, over, "insufficient_quota")
}

func TestGETUsageQueryString(t *testing.T) {
	app := configureApp(t)
	callManagement(t, app, http.MethodPost, "/v0/management/plugins/"+PluginID+"/keys", mustJSON(map[string]any{
		"id": "q", "key": "sk-q",
	}))
	raw, err := app.HandleMethod(MethodManagementHandle, mustJSON(ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/" + PluginID + "/keys/usage",
		Query:  map[string][]string{"id": {"q"}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	var resp ManagementResponse
	decodeEnvelope(t, raw, &resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
}
