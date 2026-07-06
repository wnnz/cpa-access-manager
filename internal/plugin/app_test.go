package plugin

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/url"
	"path/filepath"
	"testing"

	"cpa-access-manager/internal/access"
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
	if resp.Metadata["cpa_access_manager_key_id"] != key.ID {
		t.Fatalf("metadata key id = %q, want %q", resp.Metadata["cpa_access_manager_key_id"], key.ID)
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
		Options:  SchedulerPickOptions{Metadata: map[string]any{"cpa_access_manager_key_id": key.ID}},
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
		Metadata: map[string]any{"cpa_access_manager_key_id": key.ID},
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
		Path:   "/v0/management/plugins/cpa-access-manager/keys/rotate",
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
