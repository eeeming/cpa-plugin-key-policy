package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"cpa-key-policy/internal/plugin/web"
	"cpa-key-policy/internal/policy"
)

type App struct {
	store *policy.Store
}

func NewApp() *App {
	return &App{store: policy.NewStore()}
}

func (a *App) HandleMethod(method string, request []byte) ([]byte, error) {
	return safePluginCall(func() ([]byte, error) {
		return a.handleMethod(method, request)
	})
}

func (a *App) handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case MethodPluginRegister, MethodPluginReconfigure:
		if err := a.configure(request); err != nil {
			return nil, err
		}
		return OKEnvelope(a.registration())
	case MethodRequestInterceptBefore:
		return a.interceptBefore(request)
	case MethodRequestInterceptAfter:
		return OKEnvelope(RequestInterceptResponse{})
	case MethodUsageHandle:
		return a.handleUsage(request)
	case MethodManagementRegister:
		return OKEnvelope(a.managementRegistration())
	case MethodManagementHandle:
		return a.handleManagement(request)
	default:
		return ErrorEnvelope("unknown_method", "unknown method: "+method, http.StatusNotFound), nil
	}
}

func safePluginCall(call func() ([]byte, error)) (response []byte, err error) {
	defer func() {
		if recover() != nil {
			response = nil
			err = errors.New("plugin panic recovered")
		}
	}()
	return call()
}

func (a *App) configure(raw []byte) error {
	var req LifecycleRequest
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &req); err != nil {
			return err
		}
	}
	cfg, err := policy.DecodeConfig(req.ConfigYAML)
	if err != nil {
		return err
	}
	if err := a.store.Configure(cfg); err != nil {
		return err
	}
	a.store.StartUsageFlusher()
	return nil
}

func (a *App) Shutdown() {
	a.store.StopUsageFlusher()
}

func (a *App) registration() Registration {
	return Registration{
		SchemaVersion: SchemaVersion,
		Metadata: Metadata{
			Name:             PluginName,
			Version:          Version,
			Author:           "cpa-key-quota",
			GitHubRepository: "https://github.com/eeeming/cpa-plugin-key-policy",
			ConfigFields: []ConfigField{
				{Name: "enabled", Type: "boolean", Description: "Enable or disable this plugin without unloading it."},
				{Name: "state_file", Type: "string", Description: "JSON state file for bound key policies and usage."},
				{Name: "plus_base_url", Type: "string", Description: "CPA-Manager-Plus or Home/Plus base URL. Tries GET /v0/management/model-prices then GET /v0/management/billing/model-prices."},
				{Name: "plus_management_key", Type: "string", Description: "Bearer token for the price API (manager-plus admin key, or Home/Plus management key). Leave empty to skip USD billing."},
				{Name: "keys", Type: "array", Description: "Optional seed policies. State file wins after it exists."},
			},
		},
		Capabilities: Capabilities{
			RequestInterceptor: true,
			UsagePlugin:        true,
			ManagementAPI:      true,
		},
	}
}

func (a *App) interceptBefore(raw []byte) ([]byte, error) {
	var req RequestInterceptRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	decision := a.store.Admit(req.Headers, nil, req.Metadata)
	if !decision.Terminate {
		return OKEnvelope(RequestInterceptResponse{})
	}
	errType, errCode, message := quotaClientError(decision.Reason)
	body, _ := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    errType,
			"param":   nil,
			"code":    errCode,
		},
	})
	headers := http.Header{"Content-Type": []string{"application/json"}}
	if errCode == "rate_limit_exceeded" {
		headers.Set("Retry-After", "60")
	}
	return OKEnvelope(RequestInterceptResponse{
		Terminate:       true,
		StatusCode:      decision.StatusCode,
		ResponseHeaders: headers,
		ResponseBody:    body,
	})
}

// quotaClientError maps the plugin's internal admit reason onto OpenAI's
// public 429 codes: USD caps → insufficient_quota, RPM → rate_limit_exceeded.
func quotaClientError(reason string) (errType, code, message string) {
	switch reason {
	case "daily_exceeded":
		return "insufficient_quota", "insufficient_quota",
			"You exceeded your current quota, please check your plan and billing details. (daily USD limit)"
	case "weekly_exceeded":
		return "insufficient_quota", "insufficient_quota",
			"You exceeded your current quota, please check your plan and billing details. (weekly USD limit)"
	case "rpm_exceeded":
		return "rate_limit_exceeded", "rate_limit_exceeded",
			"Rate limit reached for requests-per-minute."
	default:
		return "rate_limit_exceeded", "rate_limit_exceeded", "quota exceeded"
	}
}

func (a *App) handleUsage(raw []byte) ([]byte, error) {
	var req UsageHandleRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return OKEnvelope(UsageHandleResponse{})
	}
	_ = a.store.RecordUsageMeta(req.APIKey, req.Alias, req.Model, req.Provider, req.ServiceTier, req.Failed, policy.UsageDetail{
		InputTokens:         req.Detail.InputTokens,
		OutputTokens:        req.Detail.OutputTokens,
		ReasoningTokens:     req.Detail.ReasoningTokens,
		CachedTokens:        req.Detail.CachedTokens,
		CacheReadTokens:     req.Detail.CacheReadTokens,
		CacheCreationTokens: req.Detail.CacheCreationTokens,
		TotalTokens:         req.Detail.TotalTokens,
	}, req.Metadata)
	return OKEnvelope(UsageHandleResponse{})
}

func (a *App) managementRegistration() ManagementRegistrationResponse {
	base := "/plugins/" + PluginID
	return ManagementRegistrationResponse{
		Routes: []ManagementRoute{
			{Method: http.MethodGet, Path: base + "/keys", Description: "List bound key quota policies."},
			{Method: http.MethodPost, Path: base + "/keys", Description: "Bind an existing Plus api-key and set limits."},
			{Method: http.MethodPost, Path: base + "/keys/sync", Description: "Import Plus api-keys that are not yet bound. Existing policies are left unchanged."},
			{Method: http.MethodPost, Path: base + "/keys/reset-windows", Description: "Reset rolling usage windows for bound keys. Limit numbers are unchanged."},
			{Method: http.MethodPatch, Path: base + "/keys", Description: "Update a bound key policy by id."},
			{Method: http.MethodDelete, Path: base + "/keys", Description: "Unbind a key policy by id."},
			{Method: http.MethodGet, Path: base + "/keys/usage", Description: "Usage for one bound key by id."},
			{Method: http.MethodGet, Path: base + "/status", Description: "Show cpa-key-quota runtime status."},
		},
		Resources: []ResourceRoute{
			{Path: web.IndexPath, Menu: "Key Quota", Description: "Bind existing Plus api-keys and view per-key quota usage."},
		},
	}
}

func (a *App) handleManagement(raw []byte) ([]byte, error) {
	var req ManagementRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	path := strings.TrimRight(req.Path, "/")

	resourcePrefix := "/v0/resource/plugins/" + PluginID
	if req.Method == http.MethodGet && strings.HasPrefix(path, resourcePrefix) {
		status, headers, body := web.Serve(strings.TrimPrefix(path, resourcePrefix))
		return OKEnvelope(ManagementResponse{StatusCode: status, Headers: headers, Body: body})
	}

	base := "/v0/management/plugins/" + PluginID
	switch {
	case req.Method == http.MethodGet && path == base+"/keys":
		return OKEnvelope(jsonResponse(http.StatusOK, map[string]any{"keys": a.publicKeys(a.store.Keys())}))
	case req.Method == http.MethodPost && path == base+"/keys/sync":
		return OKEnvelope(a.syncPlusKeys())
	case req.Method == http.MethodPost && path == base+"/keys/reset-windows":
		return OKEnvelope(a.resetWindows(req.Body))
	case req.Method == http.MethodPost && path == base+"/keys":
		return OKEnvelope(a.bindKey(req.Body))
	case req.Method == http.MethodPatch && path == base+"/keys":
		return OKEnvelope(a.patchKey(req.Body))
	case req.Method == http.MethodDelete && path == base+"/keys":
		return OKEnvelope(a.deleteKey(idFromRequest(req.Query, req.Body)))
	case req.Method == http.MethodGet && path == base+"/keys/usage":
		return OKEnvelope(a.keyUsage(idFromRequest(req.Query, req.Body)))
	case req.Method == http.MethodGet && path == base+"/status":
		return OKEnvelope(jsonResponse(http.StatusOK, a.store.Status()))
	default:
		return OKEnvelope(jsonError(http.StatusNotFound, "not_found", "unknown management route"))
	}
}

type keyWriteRequest struct {
	ID             string   `json:"id"`
	Name           *string  `json:"name,omitempty"`
	Enabled        *bool    `json:"enabled,omitempty"`
	Key            string   `json:"key,omitempty"`
	RPM            *int     `json:"rpm,omitempty"`
	DailyLimitUSD  *float64 `json:"daily_limit_usd,omitempty"`
	WeeklyLimitUSD *float64 `json:"weekly_limit_usd,omitempty"`
}

type publicKey struct {
	ID             string              `json:"id"`
	Name           string              `json:"name"`
	Enabled        bool                `json:"enabled"`
	KeyPreview     string              `json:"key_preview"`
	RPM            int                 `json:"rpm"`
	DailyLimitUSD  float64             `json:"daily_limit_usd"`
	WeeklyLimitUSD float64             `json:"weekly_limit_usd"`
	Usage          policy.UsageSummary `json:"usage"`
	CreatedAt      string              `json:"created_at,omitempty"`
	UpdatedAt      string              `json:"updated_at,omitempty"`
	LastAccessAt   string              `json:"last_access_at,omitempty"`
}

func (a *App) syncPlusKeys() ManagementResponse {
	got, err := a.store.SyncFromPlus()
	if err != nil {
		msg := err.Error()
		status := http.StatusBadGateway
		code := "plus_sync_failed"
		if strings.Contains(msg, "plus_base_url is empty") {
			status = http.StatusBadRequest
			code = "plus_not_configured"
		}
		return jsonError(status, code, msg)
	}
	return jsonResponse(http.StatusOK, got)
}

func (a *App) bindKey(body []byte) ManagementResponse {
	var req keyWriteRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return jsonError(http.StatusBadRequest, "invalid_json", err.Error())
	}
	plain := strings.TrimSpace(req.Key)
	if plain == "" {
		return jsonError(http.StatusBadRequest, "missing_key", "existing Plus api-key plaintext is required")
	}
	hash, err := policy.HashKey(plain)
	if err != nil {
		return jsonError(http.StatusBadRequest, "invalid_key", err.Error())
	}
	id := strings.TrimSpace(req.ID)
	if id == "" {
		id = "k-" + strings.TrimPrefix(hash, policy.HashPrefix)[:12]
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	rpm := 0
	if req.RPM != nil {
		rpm = *req.RPM
	}
	name := id
	if req.Name != nil && strings.TrimSpace(*req.Name) != "" {
		name = strings.TrimSpace(*req.Name)
	}
	name = nameWithoutPlaintext(name, plain)
	item := policy.KeyConfig{
		ID:             id,
		Name:           name,
		Enabled:        enabled,
		KeyHash:        hash,
		KeyPreview:     policy.PreviewKey(plain),
		CallerScope:    policy.CallerScope(plain),
		RPM:            rpm,
		DailyLimitUSD:  applyFloat64(req.DailyLimitUSD, 0),
		WeeklyLimitUSD: applyFloat64(req.WeeklyLimitUSD, 0),
	}
	if err := a.store.UpsertKey(item, true); err != nil {
		return jsonError(http.StatusBadRequest, "invalid_policy", err.Error())
	}
	return jsonResponse(http.StatusCreated, map[string]any{"key": a.publicKeyFromConfig(item)})
}

func (a *App) patchKey(body []byte) ManagementResponse {
	var req keyWriteRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return jsonError(http.StatusBadRequest, "invalid_json", err.Error())
	}
	id := strings.TrimSpace(req.ID)
	if id == "" {
		return jsonError(http.StatusBadRequest, "missing_id", "id is required")
	}
	keys := a.store.Keys()
	var current *policy.KeyConfig
	for i := range keys {
		if keys[i].ID == id {
			copy := keys[i]
			current = &copy
			break
		}
	}
	if current == nil {
		return jsonError(http.StatusNotFound, "not_found", "key not found")
	}
	if req.Name != nil {
		current.Name = strings.TrimSpace(*req.Name)
	}
	if req.Enabled != nil {
		current.Enabled = *req.Enabled
	}
	if req.RPM != nil {
		current.RPM = *req.RPM
	}
	if req.DailyLimitUSD != nil {
		current.DailyLimitUSD = *req.DailyLimitUSD
	}
	if req.WeeklyLimitUSD != nil {
		current.WeeklyLimitUSD = *req.WeeklyLimitUSD
	}
	plain := strings.TrimSpace(req.Key)
	if plain != "" {
		hash, err := policy.HashKey(plain)
		if err != nil {
			return jsonError(http.StatusBadRequest, "invalid_key", err.Error())
		}
		current.KeyHash = hash
		current.KeyPreview = policy.PreviewKey(plain)
		current.CallerScope = policy.CallerScope(plain)
		current.Name = nameWithoutPlaintext(current.Name, plain)
	}
	if err := a.store.UpsertKey(*current, true); err != nil {
		return jsonError(http.StatusBadRequest, "invalid_policy", err.Error())
	}
	return jsonResponse(http.StatusOK, map[string]any{"key": a.publicKeyFromConfig(*current)})
}

func (a *App) resetWindows(body []byte) ManagementResponse {
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return jsonError(http.StatusBadRequest, "invalid_json", err.Error())
	}
	n := 0
	for _, id := range req.IDs {
		if strings.TrimSpace(id) != "" {
			n++
		}
	}
	if n == 0 {
		return jsonError(http.StatusBadRequest, "missing_ids", "ids is required")
	}
	got, err := a.store.ResetWindows(req.IDs)
	if err != nil {
		return jsonError(http.StatusBadRequest, "reset_failed", err.Error())
	}
	return jsonResponse(http.StatusOK, got)
}

func (a *App) deleteKey(id string) ManagementResponse {
	if err := a.store.DeleteKey(id); err != nil {
		if errors.Is(err, policy.ErrUnknownKey) {
			return jsonError(http.StatusNotFound, "not_found", "key not found")
		}
		return jsonError(http.StatusBadRequest, "invalid_request", err.Error())
	}
	return jsonResponse(http.StatusOK, map[string]any{"deleted": true, "id": strings.TrimSpace(id)})
}

func (a *App) keyUsage(id string) ManagementResponse {
	id = strings.TrimSpace(id)
	if id == "" {
		return jsonError(http.StatusBadRequest, "missing_id", "id is required")
	}
	key, models, ok := a.store.ModelUsageFor(id)
	if !ok {
		return jsonError(http.StatusNotFound, "not_found", "key not found")
	}
	summary := a.store.UsageSummaryFor(key)
	return jsonResponse(http.StatusOK, map[string]any{
		"key_id":              key.ID,
		"key_name":            key.Name,
		"enabled":             key.Enabled,
		"daily_limit_usd":     key.DailyLimitUSD,
		"weekly_limit_usd":    key.WeeklyLimitUSD,
		"daily_usd":           summary.DailyUSD,
		"weekly_usd":          summary.WeeklyUSD,
		"daily_call_count":    summary.DailyCallCount,
		"weekly_call_count":   summary.WeeklyCallCount,
		"daily_window_start":  summary.DailyWindowStart,
		"weekly_window_start": summary.WeeklyWindowStart,
		"daily_reset_at":      summary.DailyResetAt,
		"weekly_reset_at":     summary.WeeklyResetAt,
		"models":              models,
	})
}

func idFromRequest(query map[string][]string, body []byte) string {
	if query != nil {
		for _, name := range []string{"id", "key_id"} {
			if values := query[name]; len(values) > 0 && strings.TrimSpace(values[0]) != "" {
				return strings.TrimSpace(values[0])
			}
		}
	}
	var payload struct {
		ID    string `json:"id"`
		KeyID string `json:"key_id"`
	}
	if len(body) > 0 && json.Unmarshal(body, &payload) == nil {
		if strings.TrimSpace(payload.ID) != "" {
			return strings.TrimSpace(payload.ID)
		}
		return strings.TrimSpace(payload.KeyID)
	}
	return ""
}

func (a *App) publicKeys(keys []policy.KeyConfig) []publicKey {
	out := make([]publicKey, 0, len(keys))
	for _, key := range keys {
		out = append(out, a.publicKeyFromConfig(key))
	}
	return out
}

func (a *App) publicKeyFromConfig(key policy.KeyConfig) publicKey {
	out := publicKey{
		ID:             key.ID,
		Name:           key.Name,
		Enabled:        key.Enabled,
		KeyPreview:     key.KeyPreview,
		RPM:            key.RPM,
		DailyLimitUSD:  key.DailyLimitUSD,
		WeeklyLimitUSD: key.WeeklyLimitUSD,
		Usage:          a.store.UsageSummaryFor(key),
	}
	if !key.CreatedAt.IsZero() {
		out.CreatedAt = key.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	if !key.UpdatedAt.IsZero() {
		out.UpdatedAt = key.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	if !key.LastAccessAt.IsZero() {
		out.LastAccessAt = key.LastAccessAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	return out
}

func nameWithoutPlaintext(name, plain string) string {
	name = strings.TrimSpace(name)
	plain = strings.TrimSpace(plain)
	if plain != "" && (name == plain || strings.Contains(name, plain)) {
		return policy.PreviewKey(plain)
	}
	return name
}

func applyFloat64(v *float64, def float64) float64 {
	if v == nil {
		return def
	}
	return *v
}

func jsonResponse(status int, payload any) ManagementResponse {
	body, err := json.Marshal(payload)
	if err != nil {
		return jsonError(http.StatusInternalServerError, "json_error", err.Error())
	}
	return ManagementResponse{
		StatusCode: status,
		Headers:    http.Header{"Content-Type": []string{"application/json; charset=utf-8"}},
		Body:       body,
	}
}

func jsonError(status int, code, message string) ManagementResponse {
	if status <= 0 {
		status = http.StatusInternalServerError
	}
	body, _ := json.Marshal(map[string]any{
		"error": map[string]string{
			"code":    strings.TrimSpace(code),
			"message": strings.TrimSpace(message),
		},
	})
	return ManagementResponse{
		StatusCode: status,
		Headers:    http.Header{"Content-Type": []string{"application/json; charset=utf-8"}},
		Body:       body,
	}
}

func (a *App) Store() *policy.Store {
	if a == nil {
		return nil
	}
	return a.store
}

func DebugEnvelope(raw []byte) string {
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Sprintf("invalid envelope: %v", err)
	}
	if env.Error != nil {
		return env.Error.Message
	}
	return string(env.Result)
}
