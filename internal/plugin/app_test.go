package plugin

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/wnnz/cpa-toolkit/internal/access"
)

func newTestApp(t *testing.T) (*App, *access.Store) {
	t.Helper()
	dir := t.TempDir()
	app := NewApp(nil)
	t.Cleanup(app.Shutdown)
	cfg := []byte("db_path: " + filepath.ToSlash(filepath.Join(dir, "access.db")) + "\n")
	raw, err := json.Marshal(LifecycleRequest{ConfigYAML: cfg})
	if err != nil {
		t.Fatalf("Marshal lifecycle request error = %v", err)
	}
	resp, err := app.HandleMethod(MethodPluginRegister, raw)
	if err != nil {
		t.Fatalf("HandleMethod(register) error = %v", err)
	}
	var env Envelope
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("Unmarshal register envelope error = %v", err)
	}
	if !env.OK {
		t.Fatalf("register envelope = %#v, want ok", env)
	}
	_, store := app.loaded()
	if store == nil {
		t.Fatal("store is nil after register")
	}
	return app, store
}

func decodeEnvelopeResult[T any](t *testing.T, raw []byte) (Envelope, T) {
	t.Helper()
	var zero T
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("Unmarshal envelope error = %v; raw=%s", err, string(raw))
	}
	if len(env.Result) == 0 {
		return env, zero
	}
	var result T
	if err := json.Unmarshal(env.Result, &result); err != nil {
		t.Fatalf("Unmarshal result error = %v; result=%s", err, string(env.Result))
	}
	return env, result
}

func mustUpsertProviderInstance(t *testing.T, store *access.Store, provider, authID string) string {
	t.Helper()
	instanceID := access.ProviderInstanceID(provider, authID, "", "")
	if err := store.UpsertInventoryItem(context.Background(), access.InventoryItem{
		ID:       instanceID,
		Type:     access.InventoryProviderInstance,
		Provider: provider,
		AuthID:   authID,
	}); err != nil {
		t.Fatalf("UpsertInventoryItem() error = %v", err)
	}
	return instanceID
}

func TestAppFrontendAuthReturnsKeyMetadata(t *testing.T) {
	app, store := newTestApp(t)
	instanceID := mustUpsertProviderInstance(t, store, "codex", "auth-a")
	key, plain, err := store.CreateKey(context.Background(), "team", true, access.Limits{}, []access.Binding{{TargetType: access.BindingProviderInstance, TargetID: instanceID}})
	if err != nil {
		t.Fatalf("CreateKey() error = %v", err)
	}

	req := FrontendAuthRequest{
		Method:  http.MethodPost,
		Path:    "/v1/chat/completions",
		Headers: http.Header{"Authorization": []string{"Bearer " + plain}},
		Body:    []byte(`{"model":"gpt-test"}`),
	}
	rawReq, _ := json.Marshal(req)
	rawResp, err := app.HandleMethod(MethodFrontendAuthAuthenticate, rawReq)
	if err != nil {
		t.Fatalf("HandleMethod(authenticate) error = %v", err)
	}
	env, resp := decodeEnvelopeResult[FrontendAuthResponse](t, rawResp)
	if !env.OK {
		t.Fatalf("authenticate envelope = %#v, want ok", env)
	}
	if !resp.Authenticated {
		t.Fatal("Authenticated = false, want true")
	}
	if resp.Principal != key.ID {
		t.Fatalf("Principal = %q, want %q", resp.Principal, key.ID)
	}
	if resp.Metadata["cpa_toolkit_key_id"] != key.ID {
		t.Fatalf("metadata key id = %q, want %q", resp.Metadata["cpa_toolkit_key_id"], key.ID)
	}
	if resp.Metadata["cpa_access_requested_model"] != "gpt-test" {
		t.Fatalf("requested model metadata = %q, want gpt-test", resp.Metadata["cpa_access_requested_model"])
	}

	req.Headers = http.Header{"Authorization": []string{"Bearer " + key.ID}}
	rawReq, _ = json.Marshal(req)
	rawResp, err = app.HandleMethod(MethodFrontendAuthAuthenticate, rawReq)
	if err != nil {
		t.Fatalf("HandleMethod(authenticate key id) error = %v", err)
	}
	env, resp = decodeEnvelopeResult[FrontendAuthResponse](t, rawResp)
	if !env.OK {
		t.Fatalf("authenticate key id envelope = %#v, want ok", env)
	}
	if resp.Authenticated {
		t.Fatal("Authenticated with key id = true, want false")
	}
}

func TestAppUnboundKeyDelegatesRoutingToCPA(t *testing.T) {
	app, store := newTestApp(t)
	key, plain, err := store.CreateKey(context.Background(), "cpa-default", true, access.Limits{}, nil)
	if err != nil {
		t.Fatalf("CreateKey() error = %v", err)
	}

	authRaw, _ := json.Marshal(FrontendAuthRequest{
		Method:  http.MethodPost,
		Path:    "/v1/responses",
		Headers: http.Header{"Authorization": []string{"Bearer " + plain}},
		Body:    []byte(`{"model":"gpt-test"}`),
	})
	authResponse, err := app.HandleMethod(MethodFrontendAuthAuthenticate, authRaw)
	if err != nil {
		t.Fatalf("HandleMethod(authenticate) error = %v", err)
	}
	_, authenticated := decodeEnvelopeResult[FrontendAuthResponse](t, authResponse)
	if !authenticated.Authenticated {
		t.Fatal("unbound key was not authenticated")
	}

	routeRaw, _ := json.Marshal(ModelRouteRequest{
		RequestedModel:     "gpt-test",
		Metadata:           map[string]any{"key_id": key.ID},
		AvailableProviders: []string{"codex", "openai"},
	})
	routeResponse, err := app.HandleMethod(MethodModelRoute, routeRaw)
	if err != nil {
		t.Fatalf("HandleMethod(model.route) error = %v", err)
	}
	routeEnv, route := decodeEnvelopeResult[ModelRouteResponse](t, routeResponse)
	if !routeEnv.OK || route.Handled {
		t.Fatalf("model route = env:%#v response:%#v, want unhandled", routeEnv, route)
	}

	schedulerRaw, _ := json.Marshal(SchedulerPickRequest{
		Provider: "codex",
		Model:    "gpt-test",
		Options:  SchedulerPickOptions{Metadata: map[string]any{"key_id": key.ID}},
		Candidates: []SchedulerAuthCandidate{
			{ID: "auth-a", Provider: "codex", Priority: 100},
			{ID: "auth-b", Provider: "codex", Priority: 50},
		},
	})
	schedulerResponse, err := app.HandleMethod(MethodSchedulerPick, schedulerRaw)
	if err != nil {
		t.Fatalf("HandleMethod(scheduler.pick) error = %v", err)
	}
	schedulerEnv, scheduler := decodeEnvelopeResult[SchedulerPickResponse](t, schedulerResponse)
	if !schedulerEnv.OK || scheduler.Handled {
		t.Fatalf("scheduler pick = env:%#v response:%#v, want unhandled", schedulerEnv, scheduler)
	}
}

func TestAppManagementCreatesCustomUnboundKey(t *testing.T) {
	app, _ := newTestApp(t)
	body := []byte(`{"name":"custom","api_key":"custom-management-key","bindings":[]}`)
	raw, _ := json.Marshal(ManagementRequest{Method: http.MethodPost, Path: routeKeys, Body: body})
	response, err := app.HandleMethod(MethodManagementHandle, raw)
	if err != nil {
		t.Fatalf("HandleMethod(management.handle) error = %v", err)
	}
	_, management := decodeEnvelopeResult[ManagementResponse](t, response)
	if management.StatusCode != http.StatusCreated {
		t.Fatalf("management status = %d, body=%s", management.StatusCode, string(management.Body))
	}
	var result struct {
		PlainKey string     `json:"plain_key"`
		Key      access.Key `json:"key"`
	}
	if err := json.Unmarshal(management.Body, &result); err != nil {
		t.Fatalf("Unmarshal management body error = %v", err)
	}
	if result.PlainKey != "custom-management-key" || len(result.Key.Bindings) != 0 {
		t.Fatalf("created custom key = %#v, plain=%q", result.Key, result.PlainKey)
	}
}

func TestAppSchedulerPickRejectsUnauthorizedWithoutFallback(t *testing.T) {
	app, store := newTestApp(t)
	key, _, err := store.CreateKey(context.Background(), "team", true, access.Limits{}, []access.Binding{{TargetType: access.BindingAuthID, TargetID: "auth-a"}})
	if err != nil {
		t.Fatalf("CreateKey() error = %v", err)
	}
	req := SchedulerPickRequest{
		Provider: "codex",
		Model:    "gpt-test",
		Options:  SchedulerPickOptions{Metadata: map[string]any{"key_id": key.ID}},
		Candidates: []SchedulerAuthCandidate{
			{ID: "auth-b", Provider: "codex", Priority: 100},
		},
	}
	rawReq, _ := json.Marshal(req)
	rawResp, err := app.HandleMethod(MethodSchedulerPick, rawReq)
	if err != nil {
		t.Fatalf("HandleMethod(scheduler.pick) error = %v", err)
	}
	var env Envelope
	if err := json.Unmarshal(rawResp, &env); err != nil {
		t.Fatalf("Unmarshal envelope error = %v", err)
	}
	if env.OK {
		t.Fatalf("scheduler envelope OK = true, want false")
	}
	if env.Error == nil || env.Error.Code != "no_allowed_auth" || env.Error.HTTPStatus != http.StatusForbidden {
		t.Fatalf("scheduler error = %#v, want no_allowed_auth 403", env.Error)
	}
}

func TestAppSchedulerPickSelectsAuthorizedCandidate(t *testing.T) {
	app, store := newTestApp(t)
	key, _, err := store.CreateKey(context.Background(), "team", true, access.Limits{}, []access.Binding{{TargetType: access.BindingAuthID, TargetID: "auth-a"}})
	if err != nil {
		t.Fatalf("CreateKey() error = %v", err)
	}
	req := SchedulerPickRequest{
		Provider: "codex",
		Model:    "gpt-test",
		Options:  SchedulerPickOptions{Metadata: map[string]any{"cpa_toolkit_key_id": key.ID}},
		Candidates: []SchedulerAuthCandidate{
			{ID: "auth-b", Provider: "codex", Priority: 100},
			{ID: "auth-a", Provider: "codex", Priority: 1},
		},
	}
	rawReq, _ := json.Marshal(req)
	rawResp, err := app.HandleMethod(MethodSchedulerPick, rawReq)
	if err != nil {
		t.Fatalf("HandleMethod(scheduler.pick) error = %v", err)
	}
	env, resp := decodeEnvelopeResult[SchedulerPickResponse](t, rawResp)
	if !env.OK {
		t.Fatalf("scheduler envelope = %#v, want ok", env)
	}
	if !resp.Handled || resp.AuthID != "auth-a" {
		t.Fatalf("SchedulerPickResponse = %#v, want handled auth-a", resp)
	}
}

func TestAppUsageHandleRecordsByPluginPrincipal(t *testing.T) {
	app, store := newTestApp(t)
	instanceID := mustUpsertProviderInstance(t, store, "codex", "auth-a")
	key, _, err := store.CreateKey(context.Background(), "team", true, access.Limits{}, []access.Binding{{TargetType: access.BindingProviderInstance, TargetID: instanceID}})
	if err != nil {
		t.Fatalf("CreateKey() error = %v", err)
	}
	if err := store.UpsertPriceRules(context.Background(), []access.PriceRule{{
		Provider:            "codex",
		Model:               "gpt-test",
		InputUSDPerMillion:  2,
		OutputUSDPerMillion: 4,
	}}); err != nil {
		t.Fatalf("UpsertPriceRules() error = %v", err)
	}

	req := UsageHandleRequest{
		Provider:  "codex",
		Model:     "gpt-test",
		Alias:     "gpt-test",
		APIKey:    key.ID,
		AuthID:    "auth-a",
		AuthIndex: "0",
		Detail: UsageDetailIn{
			InputTokens:  1_000,
			OutputTokens: 500,
			TotalTokens:  1_500,
		},
	}
	rawReq, _ := json.Marshal(req)
	rawResp, err := app.HandleMethod(MethodUsageHandle, rawReq)
	if err != nil {
		t.Fatalf("HandleMethod(usage.handle) error = %v", err)
	}
	var env Envelope
	if err := json.Unmarshal(rawResp, &env); err != nil {
		t.Fatalf("Unmarshal envelope error = %v", err)
	}
	if !env.OK {
		t.Fatalf("usage envelope = %#v, want ok", env)
	}
	entries, err := store.ListUsage(context.Background(), key.ID, 10)
	if err != nil {
		t.Fatalf("ListUsage() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("usage entries len = %d, want 1", len(entries))
	}
	if entries[0].KeyID != key.ID || entries[0].AuthID != "auth-a" || entries[0].Provider != "codex" {
		t.Fatalf("usage entry = %#v", entries[0])
	}
	if math.Abs(entries[0].USD-0.004) > 0.0000001 {
		t.Fatalf("usage USD = %v, want 0.004", entries[0].USD)
	}
}

func TestAppUsageHandleRecordsByMetadataKeyID(t *testing.T) {
	app, store := newTestApp(t)
	instanceID := mustUpsertProviderInstance(t, store, "codex", "auth-a")
	key, _, err := store.CreateKey(context.Background(), "team", true, access.Limits{}, []access.Binding{{TargetType: access.BindingProviderInstance, TargetID: instanceID}})
	if err != nil {
		t.Fatalf("CreateKey() error = %v", err)
	}

	req := UsageHandleRequest{
		Provider: "codex",
		Model:    "unpriced-model",
		Metadata: map[string]any{"cpa_toolkit_key_id": key.ID},
		AuthID:   "auth-a",
		Detail: UsageDetailIn{
			InputTokens:  7,
			OutputTokens: 11,
			TotalTokens:  18,
		},
	}
	rawReq, _ := json.Marshal(req)
	rawResp, err := app.HandleMethod(MethodUsageHandle, rawReq)
	if err != nil {
		t.Fatalf("HandleMethod(usage.handle) error = %v", err)
	}
	var env Envelope
	if err := json.Unmarshal(rawResp, &env); err != nil {
		t.Fatalf("Unmarshal envelope error = %v", err)
	}
	if !env.OK {
		t.Fatalf("usage envelope = %#v, want ok", env)
	}
	entries, err := store.ListUsage(context.Background(), key.ID, 10)
	if err != nil {
		t.Fatalf("ListUsage() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("usage entries len = %d, want 1", len(entries))
	}
	if entries[0].Detail.TotalTokens != 18 || entries[0].USD != 0 {
		t.Fatalf("usage entry = %#v, want total tokens 18 and USD 0", entries[0])
	}
}

func TestAppManagementRotateRouteUsesExactNormalizedPath(t *testing.T) {
	app, store := newTestApp(t)
	key, _, err := store.CreateKey(context.Background(), "team", true, access.Limits{}, nil)
	if err != nil {
		t.Fatalf("CreateKey() error = %v", err)
	}
	req := ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/plugins/cpa-toolkit/keys/rotate",
		Query:  url.Values{"id": []string{key.ID}},
	}
	rawReq, _ := json.Marshal(req)
	rawResp, err := app.HandleMethod(MethodManagementHandle, rawReq)
	if err != nil {
		t.Fatalf("HandleMethod(management.handle) error = %v", err)
	}
	env, resp := decodeEnvelopeResult[ManagementResponse](t, rawResp)
	if !env.OK {
		t.Fatalf("management envelope = %#v, want ok", env)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("management status = %d body=%s, want 200", resp.StatusCode, string(resp.Body))
	}
	var body map[string]any
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("Unmarshal management body error = %v; body=%s", err, string(resp.Body))
	}
	if plain, _ := body["plain_key"].(string); plain == "" {
		t.Fatalf("plain_key missing in body %s", string(resp.Body))
	}
}

func TestAppManagementStatusIncludesCurrentCPAVersion(t *testing.T) {
	app, _ := newTestApp(t)
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/status" {
			t.Fatalf("path = %q, want /v0/management/status", r.URL.Path)
		}
		if got := r.Header.Get("authorization"); got != "Bearer management-secret" {
			t.Fatalf("authorization = %q, want bearer token", got)
		}
		w.Header().Set("X-Cpa-Version", "v7.2.53")
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(cpa.Close)
	t.Setenv("CPA_ACCESS_MANAGER_CPA_BASE", cpa.URL)

	rawReq, _ := json.Marshal(ManagementRequest{
		Method:  http.MethodGet,
		Path:    "/v0/management/plugins/cpa-toolkit/status",
		Headers: http.Header{"Authorization": []string{"Bearer management-secret"}},
	})
	rawResp, err := app.HandleMethod(MethodManagementHandle, rawReq)
	if err != nil {
		t.Fatalf("HandleMethod(management.handle) error = %v", err)
	}
	env, resp := decodeEnvelopeResult[ManagementResponse](t, rawResp)
	if !env.OK {
		t.Fatalf("management envelope = %#v, want ok", env)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("management status = %d body=%s, want 200", resp.StatusCode, string(resp.Body))
	}
	var body statusResponse
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("Unmarshal management body error = %v; body=%s", err, string(resp.Body))
	}
	if body.CurrentCPAVersion != "v7.2.53" {
		t.Fatalf("current CPA version = %q, want v7.2.53", body.CurrentCPAVersion)
	}
}

func TestAppManagementRegistersMultipleResourceMenus(t *testing.T) {
	app, _ := newTestApp(t)

	rawResp, err := app.HandleMethod(MethodManagementRegister, nil)
	if err != nil {
		t.Fatalf("HandleMethod(management.register) error = %v", err)
	}
	env, resp := decodeEnvelopeResult[ManagementRegistrationResponse](t, rawResp)
	if !env.OK {
		t.Fatalf("management register envelope = %#v, want ok", env)
	}
	got := map[string]string{}
	for _, resource := range resp.Resources {
		got[resource.Path] = resource.Menu
	}
	if got[resourceAPIKeys] != "API Key管理" {
		t.Fatalf("API key resource menu = %q, want API Key管理", got[resourceAPIKeys])
	}
	if got[resourceSettings] != "设置" {
		t.Fatalf("settings resource menu = %q, want 设置", got[resourceSettings])
	}
	if got[resourceUsage] != "使用统计" {
		t.Fatalf("usage resource menu = %q, want 使用统计", got[resourceUsage])
	}
	if got[resourceSharedUI] != "" {
		t.Fatalf("shared stylesheet menu = %q, want empty", got[resourceSharedUI])
	}
	wantOrder := []string{resourceAPIKeys, resourceUsage, resourceSettings, resourceSharedUI}
	wantSortedOrder := append([]string(nil), wantOrder...)
	if len(resp.Resources) != len(wantOrder) {
		t.Fatalf("resource count = %d, want %d", len(resp.Resources), len(wantOrder))
	}
	for i, want := range wantOrder {
		if resp.Resources[i].Path != want {
			t.Fatalf("resource[%d] = %q, want %q", i, resp.Resources[i].Path, want)
		}
	}
	sort.SliceStable(wantSortedOrder, func(i, j int) bool {
		return wantSortedOrder[i] < wantSortedOrder[j]
	})
	for i, want := range wantSortedOrder {
		if resp.Resources[i].Path != want {
			t.Fatalf("sorted resource[%d] = %q, want %q", i, resp.Resources[i].Path, want)
		}
	}
}

func TestAppManagementResourcePages(t *testing.T) {
	app, _ := newTestApp(t)
	for _, tc := range []struct {
		path string
		want string
	}{
		{"/v0/resource/plugins/cpa-toolkit/01-apikey.html", "API Key 管理"},
		{"/v0/resource/plugins/cpa-toolkit/02-usage-statistics.html", "requestResourceCombobox"},
		{"/v0/resource/plugins/cpa-toolkit/03-settings.html", "模型计费设置"},
		{"/v0/resource/plugins/cpa-toolkit/apikey.html", "API Key 管理"},
		{"/v0/resource/plugins/cpa-toolkit/settings.html", "模型计费设置"},
		{"/v0/resource/plugins/cpa-toolkit/usage-statistics.html", "使用统计"},
		{"/v0/resource/plugins/cpa-toolkit/shared-ui.css", ".table-shell"},
		{"/v0/resource/plugins/cpa-toolkit/index.html", "API Key 管理"},
	} {
		req := ManagementRequest{Method: http.MethodGet, Path: tc.path}
		rawReq, _ := json.Marshal(req)
		rawResp, err := app.HandleMethod(MethodManagementHandle, rawReq)
		if err != nil {
			t.Fatalf("HandleMethod(%s) error = %v", tc.path, err)
		}
		env, resp := decodeEnvelopeResult[ManagementResponse](t, rawResp)
		if !env.OK {
			t.Fatalf("resource envelope = %#v, want ok", env)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("resource status = %d, want 200", resp.StatusCode)
		}
		if !strings.Contains(string(resp.Body), tc.want) {
			t.Fatalf("resource body for %s does not contain %q", tc.path, tc.want)
		}
	}
}

func TestAppUsageHandleRecordsRequestMetrics(t *testing.T) {
	app, store := newTestApp(t)
	instanceID := mustUpsertProviderInstance(t, store, "codex", "auth-a")
	key, _, err := store.CreateKey(context.Background(), "team", true, access.Limits{}, []access.Binding{{TargetType: access.BindingProviderInstance, TargetID: instanceID}})
	if err != nil {
		t.Fatalf("CreateKey() error = %v", err)
	}

	req := UsageHandleRequest{
		Provider:            "codex",
		Model:               "gpt-test",
		APIKey:              key.ID,
		AuthID:              "auth-a",
		RequestIDAlt:        "req_123",
		RequestResourceAlt:  "OpenAI Primary",
		FirstTokenLatencyMS: 850,
		DurationMSAlt:       4200,
		Detail: UsageDetailIn{
			InputTokens:     100,
			CacheReadTokens: 40,
			OutputTokens:    20,
			ReasoningEffort: "high",
			TotalTokens:     120,
		},
	}
	rawReq, _ := json.Marshal(req)
	rawResp, err := app.HandleMethod(MethodUsageHandle, rawReq)
	if err != nil {
		t.Fatalf("HandleMethod(usage.handle) error = %v", err)
	}
	var env Envelope
	if err := json.Unmarshal(rawResp, &env); err != nil {
		t.Fatalf("Unmarshal envelope error = %v", err)
	}
	if !env.OK {
		t.Fatalf("usage envelope = %#v, want ok", env)
	}

	entries, err := store.ListUsage(context.Background(), key.ID, 10)
	if err != nil {
		t.Fatalf("ListUsage() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("usage entries len = %d, want 1", len(entries))
	}
	got := entries[0]
	if got.RequestID != "req_123" || got.RequestResource != "OpenAI Primary" || got.FirstTokenLatencyMS != 850 || got.TotalLatencyMS != 4200 {
		t.Fatalf("usage entry metrics = %#v", got)
	}
	if got.Detail.ReasoningEffort != "high" {
		t.Fatalf("usage reasoning effort = %q, want high", got.Detail.ReasoningEffort)
	}
}

func TestAppManagementUsageFiltersRequestResource(t *testing.T) {
	app, store := newTestApp(t)
	key, _, err := store.CreateKey(context.Background(), "team", true, access.Limits{}, []access.Binding{{TargetType: access.BindingAuthID, TargetID: "auth-a"}})
	if err != nil {
		t.Fatalf("CreateKey() error = %v", err)
	}
	for _, resource := range []string{"OpenAI Primary", "Claude Backup"} {
		if _, err := store.RecordUsage(context.Background(), access.UsageEntry{
			KeyID:           key.ID,
			RequestResource: resource,
			Provider:        "codex",
			Model:           "unpriced-model",
		}); err != nil {
			t.Fatalf("RecordUsage(%q) error = %v", resource, err)
		}
	}

	rawReq, _ := json.Marshal(ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/cpa-toolkit/usage",
		Query:  url.Values{"request_resource": []string{"primary"}},
	})
	rawResp, err := app.HandleMethod(MethodManagementHandle, rawReq)
	if err != nil {
		t.Fatalf("HandleMethod(management.handle) error = %v", err)
	}
	env, resp := decodeEnvelopeResult[ManagementResponse](t, rawResp)
	if !env.OK || resp.StatusCode != http.StatusOK {
		t.Fatalf("management response = %#v %#v, want 200", env, resp)
	}
	var entries []access.UsageEntry
	if err := json.Unmarshal(resp.Body, &entries); err != nil {
		t.Fatalf("Unmarshal usage response error = %v", err)
	}
	if len(entries) != 1 || entries[0].RequestResource != "OpenAI Primary" {
		t.Fatalf("filtered usage = %#v, want OpenAI Primary", entries)
	}
}

func TestAppManagementUsageAggregatesAllRowsByKey(t *testing.T) {
	app, store := newTestApp(t)
	key, _, err := store.CreateKey(context.Background(), "team", true, access.Limits{}, []access.Binding{{TargetType: access.BindingAuthID, TargetID: "auth-a"}})
	if err != nil {
		t.Fatalf("CreateKey() error = %v", err)
	}
	start := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 205; i++ {
		if _, err := store.RecordUsage(context.Background(), access.UsageEntry{
			KeyID: key.ID, Provider: "codex", Model: "unpriced-model", CreatedAt: start.Add(time.Hour),
			Detail: access.UsageDetail{TotalTokens: 10},
		}); err != nil {
			t.Fatalf("RecordUsage(%d) error = %v", i, err)
		}
	}
	rawReq, _ := json.Marshal(ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/cpa-toolkit/usage",
		Query: url.Values{
			"aggregate": {"key"},
			"since":     {start.Format(time.RFC3339Nano)},
			"before":    {start.Add(24 * time.Hour).Format(time.RFC3339Nano)},
		},
	})
	rawResp, err := app.HandleMethod(MethodManagementHandle, rawReq)
	if err != nil {
		t.Fatalf("HandleMethod(management.handle) error = %v", err)
	}
	_, resp := decodeEnvelopeResult[ManagementResponse](t, rawResp)
	var totals []access.UsageAggregate
	if err := json.Unmarshal(resp.Body, &totals); err != nil {
		t.Fatalf("Unmarshal usage totals error = %v", err)
	}
	if resp.StatusCode != http.StatusOK || len(totals) != 1 || totals[0].Requests != 205 || totals[0].Tokens != 2050 {
		t.Fatalf("usage totals response = status:%d totals:%#v", resp.StatusCode, totals)
	}
}

func TestAppManagementUsageSummaryUsesFilters(t *testing.T) {
	app, store := newTestApp(t)
	key, _, err := store.CreateKey(context.Background(), "team", true, access.Limits{}, []access.Binding{{TargetType: access.BindingAuthID, TargetID: "auth-a"}})
	if err != nil {
		t.Fatalf("CreateKey() error = %v", err)
	}
	start := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	for _, resource := range []string{"OpenAI Primary", "Claude Backup"} {
		if _, err := store.RecordUsage(context.Background(), access.UsageEntry{
			KeyID: key.ID, RequestResource: resource, Provider: "codex", Model: "unpriced-model", CreatedAt: start.Add(time.Hour),
			Detail: access.UsageDetail{InputTokens: 10, OutputTokens: 5},
		}); err != nil {
			t.Fatalf("RecordUsage(%q) error = %v", resource, err)
		}
	}
	rawReq, _ := json.Marshal(ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/cpa-toolkit/usage",
		Query: url.Values{
			"aggregate":        {"summary"},
			"request_resource": {"primary"},
			"since":            {start.Format(time.RFC3339Nano)},
			"before":           {start.Add(24 * time.Hour).Format(time.RFC3339Nano)},
		},
	})
	rawResp, err := app.HandleMethod(MethodManagementHandle, rawReq)
	if err != nil {
		t.Fatalf("HandleMethod(management.handle) error = %v", err)
	}
	_, resp := decodeEnvelopeResult[ManagementResponse](t, rawResp)
	var summary access.UsageSummary
	if err := json.Unmarshal(resp.Body, &summary); err != nil {
		t.Fatalf("Unmarshal usage summary error = %v", err)
	}
	if resp.StatusCode != http.StatusOK || summary.Requests != 1 || summary.InputTokens != 10 || summary.OutputTokens != 5 || summary.TotalTokens != 15 {
		t.Fatalf("usage summary response = status:%d summary:%#v", resp.StatusCode, summary)
	}
}

func TestAppUsageHandleInfersCacheReadFromCachedTokens(t *testing.T) {
	app, store := newTestApp(t)
	instanceID := mustUpsertProviderInstance(t, store, "codex", "auth-a")
	key, _, err := store.CreateKey(context.Background(), "team", true, access.Limits{}, []access.Binding{{TargetType: access.BindingProviderInstance, TargetID: instanceID}})
	if err != nil {
		t.Fatalf("CreateKey() error = %v", err)
	}
	if err := store.UpsertPriceRules(context.Background(), []access.PriceRule{{
		Provider:               "codex",
		Model:                  "gpt-test",
		InputUSDPerMillion:     2,
		OutputUSDPerMillion:    10,
		CacheReadUSDPerMillion: 0.5,
	}}); err != nil {
		t.Fatalf("UpsertPriceRules() error = %v", err)
	}

	rawResp, err := app.HandleMethod(MethodUsageHandle, []byte(`{
		"provider":"codex",
		"model":"gpt-test",
		"api_key":`+strconvQuote(key.ID)+`,
		"auth_id":"auth-a",
		"detail":{
			"input_tokens":1000000,
			"output_tokens":100000,
			"cached_tokens":400000,
			"total_tokens":1100000
		}
	}`))
	if err != nil {
		t.Fatalf("HandleMethod(usage.handle) error = %v", err)
	}
	var env Envelope
	if err := json.Unmarshal(rawResp, &env); err != nil {
		t.Fatalf("Unmarshal envelope error = %v", err)
	}
	if !env.OK {
		t.Fatalf("usage envelope = %#v, want ok", env)
	}

	entries, err := store.ListUsage(context.Background(), key.ID, 10)
	if err != nil {
		t.Fatalf("ListUsage() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("usage entries len = %d, want 1", len(entries))
	}
	got := entries[0]
	if got.Detail.CacheReadTokens != 400_000 {
		t.Fatalf("cache read tokens = %d, want 400000", got.Detail.CacheReadTokens)
	}
	if math.Abs(got.USD-2.4) > 0.0000001 {
		t.Fatalf("usage USD = %v, want 2.4", got.USD)
	}
}

func TestAppUsageHandleRecordsCPADurationFields(t *testing.T) {
	app, store := newTestApp(t)
	instanceID := mustUpsertProviderInstance(t, store, "codex", "auth-a")
	key, _, err := store.CreateKey(context.Background(), "team", true, access.Limits{}, []access.Binding{{TargetType: access.BindingProviderInstance, TargetID: instanceID}})
	if err != nil {
		t.Fatalf("CreateKey() error = %v", err)
	}

	req := UsageHandleRequest{
		Provider: "codex",
		Model:    "gpt-test",
		APIKey:   key.ID,
		AuthID:   "auth-a",
		TTFT:     int64(850 * time.Millisecond),
		Latency:  int64(4200 * time.Millisecond),
		Detail: UsageDetailIn{
			InputTokens:  100,
			OutputTokens: 20,
			TotalTokens:  120,
		},
	}
	rawReq, _ := json.Marshal(req)
	rawResp, err := app.HandleMethod(MethodUsageHandle, rawReq)
	if err != nil {
		t.Fatalf("HandleMethod(usage.handle) error = %v", err)
	}
	var env Envelope
	if err := json.Unmarshal(rawResp, &env); err != nil {
		t.Fatalf("Unmarshal envelope error = %v", err)
	}
	if !env.OK {
		t.Fatalf("usage envelope = %#v, want ok", env)
	}

	entries, err := store.ListUsage(context.Background(), key.ID, 10)
	if err != nil {
		t.Fatalf("ListUsage() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("usage entries len = %d, want 1", len(entries))
	}
	got := entries[0]
	if got.FirstTokenLatencyMS != 850 || got.TotalLatencyMS != 4200 {
		t.Fatalf("usage duration metrics = first %d total %d, want 850/4200", got.FirstTokenLatencyMS, got.TotalLatencyMS)
	}
}

func TestAppManagementSyncPricesFromSource(t *testing.T) {
	app, store := newTestApp(t)
	if err := store.UpsertPriceRules(context.Background(), []access.PriceRule{{
		Provider: "ai21",
		Model:    "j2-ultra",
	}}); err != nil {
		t.Fatalf("seed stale price rule error = %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{
			"gpt-test": {
				"litellm_provider": "openai",
				"input_cost_per_token": 0.000001,
				"cache_read_input_token_cost": 0.00000025,
				"output_cost_per_token": 0.000004
			},
			"anthropic/claude-test": {
				"litellm_provider": "anthropic",
				"input_cost_per_token": 0.000003,
				"output_cost_per_token": 0.000015
			},
			"azure/gpt-test": {
				"litellm_provider": "azure",
				"input_cost_per_token": 0.000002,
				"output_cost_per_token": 0.000008
			}
		}`))
	}))
	defer server.Close()

	req := ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/plugins/cpa-toolkit/prices/sync",
		Body:   []byte(`{"source_url":` + strconvQuote(server.URL) + `}`),
	}
	rawReq, _ := json.Marshal(req)
	rawResp, err := app.HandleMethod(MethodManagementHandle, rawReq)
	if err != nil {
		t.Fatalf("HandleMethod(price sync) error = %v", err)
	}
	env, resp := decodeEnvelopeResult[ManagementResponse](t, rawResp)
	if !env.OK {
		t.Fatalf("price sync envelope = %#v, want ok", env)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("price sync status = %d body=%s, want 200", resp.StatusCode, string(resp.Body))
	}
	codexRule, err := store.PriceRule(context.Background(), "codex", "gpt-test")
	if err != nil {
		t.Fatalf("codex PriceRule() error = %v", err)
	}
	if codexRule.InputUSDPerMillion != 1 || codexRule.CacheReadUSDPerMillion != 0.25 || codexRule.OutputUSDPerMillion != 4 {
		t.Fatalf("codex rule = %#v, want 1/0.25/4", codexRule)
	}
	claudeRule, err := store.PriceRule(context.Background(), "claude", "claude-test")
	if err != nil {
		t.Fatalf("claude PriceRule() error = %v", err)
	}
	if claudeRule.InputUSDPerMillion != 3 || claudeRule.OutputUSDPerMillion != 15 {
		t.Fatalf("claude rule = %#v, want 3/15", claudeRule)
	}
	rules, err := store.ListPriceRules(context.Background())
	if err != nil {
		t.Fatalf("ListPriceRules() error = %v", err)
	}
	for _, rule := range rules {
		if rule.Provider == "azure" || rule.Provider == "ai21" {
			t.Fatalf("unexpected stale or non-official rule: %#v", rule)
		}
	}
}

func strconvQuote(value string) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}
