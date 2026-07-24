package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wnnz/cpa-toolkit/internal/access"
	"gopkg.in/yaml.v3"
)

type HostCaller func(method string, payload []byte) ([]byte, error)

type App struct {
	mu    sync.RWMutex
	cfg   Config
	store *access.Store
	host  HostCaller
}

func NewApp(host HostCaller) *App {
	return &App{host: host}
}

func (a *App) Shutdown() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.store != nil {
		_ = a.store.Close()
		a.store = nil
	}
}

func (a *App) HandleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case MethodPluginRegister, MethodPluginReconfigure:
		if err := a.configure(request); err != nil {
			return nil, err
		}
		return OKEnvelope(a.registration())
	case MethodFrontendAuthIdentifier:
		return OKEnvelope(IdentifierResponse{Identifier: PluginID})
	case MethodFrontendAuthAuthenticate:
		return a.authenticate(request)
	case MethodModelRoute:
		return a.routeModel(request)
	case MethodSchedulerPick:
		return a.pickScheduler(request)
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

func (a *App) configure(raw []byte) error {
	var req LifecycleRequest
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &req); err != nil {
			return err
		}
	}
	cfg := DefaultConfig()
	if len(req.ConfigYAML) > 0 {
		if err := yaml.Unmarshal(req.ConfigYAML, &cfg); err != nil {
			return err
		}
	}
	if strings.TrimSpace(cfg.DBPath) == "" {
		cfg.DBPath = DefaultConfig().DBPath
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, err := access.OpenStore(ctx, cfg.DBPath, cfg.AllowUnpriced)
	if err != nil {
		return err
	}
	a.mu.Lock()
	old := a.store
	a.cfg = cfg
	a.store = store
	a.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	return nil
}

func DefaultConfig() Config {
	return Config{
		Enabled:       true,
		DBPath:        "/opt/cli-proxy-api/plugins/cpa-toolkit.db",
		AllowUnpriced: false,
	}
}

func (a *App) loaded() (Config, *access.Store) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.cfg, a.store
}

func (a *App) registration() Registration {
	return Registration{
		SchemaVersion: SchemaVersion,
		Metadata: Metadata{
			Name:             PluginName,
			Version:          Version,
			Author:           "mossdeck",
			GitHubRepository: "https://github.com/wnnz/cpa-toolkit",
			ConfigFields: []ConfigField{
				{Name: "enabled", Type: "boolean", Description: "Enable or disable plugin request enforcement."},
				{Name: "db_path", Type: "string", Description: "SQLite database path."},
				{Name: "allow_unpriced", Type: "boolean", Description: "Allow USD-limited keys to use models without price rules. Defaults to false."},
			},
		},
		Capabilities: Capabilities{
			FrontendAuthProvider:          true,
			FrontendAuthProviderExclusive: true,
			ModelRouter:                   true,
			Scheduler:                     true,
			UsagePlugin:                   true,
			ManagementAPI:                 true,
		},
	}
}

func (a *App) managementRegistration() ManagementRegistrationResponse {
	routes := []ManagementRoute{
		{Method: http.MethodGet, Path: routeStatus, Description: "Plugin status."},
		{Method: http.MethodGet, Path: routeKeys, Description: "List keys."},
		{Method: http.MethodPost, Path: routeKeys, Description: "Create key."},
		{Method: http.MethodPatch, Path: routeKeys, Description: "Update key."},
		{Method: http.MethodDelete, Path: routeKeys, Description: "Delete key."},
		{Method: http.MethodPost, Path: routeRotateKey, Description: "Rotate key."},
		{Method: http.MethodGet, Path: routeGroups, Description: "List groups."},
		{Method: http.MethodPost, Path: routeGroups, Description: "Create group."},
		{Method: http.MethodPatch, Path: routeGroups, Description: "Update group."},
		{Method: http.MethodDelete, Path: routeGroups, Description: "Delete group."},
		{Method: http.MethodGet, Path: routeInventory, Description: "List inventory."},
		{Method: http.MethodPost, Path: routeInventory, Description: "Refresh or upsert inventory."},
		{Method: http.MethodGet, Path: routePrices, Description: "List price rules."},
		{Method: http.MethodPut, Path: routePrices, Description: "Upsert price rules."},
		{Method: http.MethodPost, Path: routePricesSync, Description: "Sync model price rules from public sources."},
		{Method: http.MethodGet, Path: routeUsage, Description: "List usage ledger."},
		{Method: http.MethodPost, Path: routeCPAUpdate, Description: "Pull and restart the CPA Docker service."},
	}
	return ManagementRegistrationResponse{
		Routes: routes,
		Resources: []ResourceRoute{
			{
				Path:        resourceAPIKeys,
				Menu:        "API Key管理",
				Description: "管理下游 API Key、关联资源和额度。",
			},
			{
				Path:        resourceUsage,
				Menu:        "使用统计",
				Description: "查看请求日志、Token 明细、费用和响应耗时。",
			},
			{
				Path:        resourceSettings,
				Menu:        "设置",
				Description: "配置管理密钥和模型计费规则。",
			},
			{
				Path:        resourceSharedUI,
				Description: "Shared stylesheet for plugin resource pages.",
			},
		},
	}
}

func (a *App) authenticate(raw []byte) ([]byte, error) {
	cfg, store := a.loaded()
	if !cfg.Enabled || store == nil {
		return OKEnvelope(FrontendAuthResponse{Authenticated: false})
	}
	var req FrontendAuthRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	token := bearerToken(req.Headers)
	if token == "" {
		return OKEnvelope(FrontendAuthResponse{Authenticated: false})
	}
	requestedModel := requestedModel(req.Body, req.Query)
	key, err := store.Authenticate(context.Background(), token, requestedModel, time.Now())
	if err != nil {
		if errors.Is(err, access.ErrKeyNotFound) {
			return OKEnvelope(FrontendAuthResponse{Authenticated: false})
		}
		return ErrorEnvelope("access_denied", err.Error(), http.StatusForbidden), nil
	}
	meta := map[string]string{
		"provider":                   PluginID,
		"key_id":                     key.ID,
		"cpa_toolkit_key_id":         key.ID,
		"cpa_access_manager_key_id":  key.ID,
		"cpa_access_requested_model": requestedModel,
	}
	return OKEnvelope(FrontendAuthResponse{
		Authenticated: true,
		Principal:     key.ID,
		Metadata:      meta,
	})
}

func (a *App) routeModel(raw []byte) ([]byte, error) {
	cfg, store := a.loaded()
	if !cfg.Enabled || store == nil {
		return OKEnvelope(ModelRouteResponse{Handled: false})
	}
	var req ModelRouteRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	keyID := keyIDFromMetadata(req.Metadata)
	if keyID == "" {
		keyID = keyIDFromHeaders(context.Background(), store, req.Headers)
	}
	if keyID == "" {
		return OKEnvelope(ModelRouteResponse{Handled: false})
	}
	sessionHash := routingSessionHash(req.Metadata, map[string][]string(req.Headers))
	provider, err := store.ChooseProviderRouted(context.Background(), keyID, req.RequestedModel, req.AvailableProviders, sessionHash, time.Now())
	if err != nil {
		if errors.Is(err, access.ErrNoBindings) {
			return OKEnvelope(ModelRouteResponse{Handled: false})
		}
		return ErrorEnvelope("no_allowed_provider", err.Error(), http.StatusForbidden), nil
	}
	return OKEnvelope(ModelRouteResponse{
		Handled:     true,
		TargetKind:  "provider",
		Target:      provider,
		TargetModel: req.RequestedModel,
		Reason:      PluginID + ":" + keyID,
	})
}

func (a *App) pickScheduler(raw []byte) ([]byte, error) {
	cfg, store := a.loaded()
	if !cfg.Enabled || store == nil {
		return OKEnvelope(SchedulerPickResponse{Handled: false})
	}
	var req SchedulerPickRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	keyID := keyIDFromMetadata(req.Options.Metadata)
	if keyID == "" {
		keyID = keyIDFromHeaders(context.Background(), store, http.Header(req.Options.Headers))
	}
	if keyID == "" {
		return OKEnvelope(SchedulerPickResponse{Handled: false})
	}
	candidates := make([]access.Candidate, 0, len(req.Candidates))
	for _, candidate := range req.Candidates {
		candidates = append(candidates, access.Candidate{
			ID:         candidate.ID,
			Provider:   candidate.Provider,
			Priority:   candidate.Priority,
			Status:     candidate.Status,
			Attributes: candidate.Attributes,
		})
	}
	sessionHash := routingSessionHash(req.Options.Metadata, req.Options.Headers)
	selected, err := store.PickCandidateRouted(context.Background(), keyID, req.Model, req.Provider, sessionHash, candidates, time.Now())
	if err != nil {
		if errors.Is(err, access.ErrNoBindings) {
			return OKEnvelope(SchedulerPickResponse{Handled: false})
		}
		return ErrorEnvelope("no_allowed_auth", err.Error(), http.StatusForbidden), nil
	}
	return OKEnvelope(SchedulerPickResponse{Handled: true, AuthID: selected.ID})
}

func (a *App) handleUsage(raw []byte) ([]byte, error) {
	_, store := a.loaded()
	if store == nil {
		return OKEnvelope(map[string]any{})
	}
	var req UsageHandleRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return OKEnvelope(map[string]any{})
	}
	keyIdentifier := firstNonEmpty(req.KeyID, req.KeyIDSnake, req.Principal, keyIDFromMetadata(req.Metadata), req.APIKey, req.APIKeyAlt, req.Source)
	key, err := store.KeyByIDOrPresentedToken(context.Background(), keyIdentifier)
	if err != nil {
		return OKEnvelope(map[string]any{})
	}
	_, err = store.RecordUsage(context.Background(), access.UsageEntry{
		KeyID:           key.ID,
		RequestID:       firstNonEmpty(req.RequestID, req.RequestIDAlt, metadataString(req.Metadata, "request_id", "id", "response_id")),
		RequestResource: firstNonEmpty(req.RequestResource, req.RequestResourceAlt, metadataString(req.Metadata, "request_resource", "resource", "target", "path", "url")),
		AuthID:          strings.TrimSpace(firstNonEmpty(req.AuthID, req.AuthIDAlt)),
		AuthIndex:       strings.TrimSpace(firstNonEmpty(req.AuthIndex, req.AuthIndexAlt)),
		Provider:        req.Provider,
		Model:           firstNonEmpty(req.Model, req.Alias),
		Alias:           req.Alias,
		FirstTokenLatencyMS: firstPositiveInt64(
			req.FirstTokenLatencyMS,
			req.FirstTokenLatencyMSAlt,
			req.TTFTMS,
			req.TTFTMSAlt,
			durationMillis(req.TTFT),
			durationMillis(req.TTFTAlt),
			metadataMillis(req.Metadata, "first_token_latency_ms", "ttft_ms", "first_token_ms"),
			metadataDurationMillis(req.Metadata, "TTFT", "ttft"),
		),
		TotalLatencyMS: firstPositiveInt64(
			req.TotalLatencyMS,
			req.TotalLatencyMSAlt,
			req.LatencyMS,
			req.LatencyMSAlt,
			req.DurationMS,
			req.DurationMSAlt,
			durationMillis(req.Latency),
			durationMillis(req.LatencyAlt),
			durationMillis(req.Duration),
			durationMillis(req.DurationAlt),
			metadataMillis(req.Metadata, "total_latency_ms", "latency_ms", "duration_ms", "elapsed_ms"),
			metadataDurationMillis(req.Metadata, "Latency", "latency", "Duration", "duration"),
		),
		Failed: req.Failed,
		Detail: access.UsageDetail{
			InputTokens:     req.Detail.InputTokens,
			OutputTokens:    req.Detail.OutputTokens,
			ReasoningTokens: req.Detail.ReasoningTokens,
			ReasoningEffort: firstNonEmpty(
				req.Detail.ReasoningEffort,
				req.ReasoningEffort,
				req.ReasoningEffortAlt,
				metadataString(req.Metadata, "reasoning_effort", "reasoningEffort", "ReasoningEffort"),
			),
			CachedTokens:        req.Detail.CachedTokens,
			CacheReadTokens:     req.Detail.CacheReadTokens,
			CacheCreationTokens: req.Detail.CacheCreationTokens,
			TotalTokens:         req.Detail.TotalTokens,
		},
	})
	if err != nil {
		return OKEnvelope(map[string]any{})
	}
	return OKEnvelope(map[string]any{})
}

func OKEnvelope(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return json.Marshal(Envelope{OK: true, Result: raw})
}

func ErrorEnvelope(code, message string, status int) []byte {
	raw, _ := json.Marshal(Envelope{
		OK: false,
		Error: &EnvelopeError{
			Code:       code,
			Message:    message,
			HTTPStatus: status,
		},
	})
	return raw
}

func keyIDFromHeaders(ctx context.Context, store *access.Store, headers http.Header) string {
	token := bearerToken(headers)
	if token == "" || store == nil {
		return ""
	}
	key, err := store.KeyByPresentedToken(ctx, token)
	if err != nil {
		return ""
	}
	return key.ID
}

func keyIDFromMetadata(meta map[string]any) string {
	for _, key := range []string{"cpa_toolkit_key_id", "cpa_access_manager_key_id", "key_id", "api_key", "principal"} {
		if value, ok := meta[key]; ok {
			if s := strings.TrimSpace(fmt.Sprint(value)); s != "" {
				return s
			}
		}
	}
	return ""
}

func metadataString(meta map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := meta[key]; ok {
			if s := strings.TrimSpace(fmt.Sprint(value)); s != "" && s != "<nil>" {
				return s
			}
		}
	}
	return ""
}

func metadataMillis(meta map[string]any, keys ...string) int64 {
	for _, key := range keys {
		value, ok := meta[key]
		if !ok {
			continue
		}
		if millis := numericMillis(value); millis > 0 {
			return millis
		}
	}
	return 0
}

func metadataDurationMillis(meta map[string]any, keys ...string) int64 {
	for _, key := range keys {
		value, ok := meta[key]
		if !ok {
			continue
		}
		if millis := durationMillis(value); millis > 0 {
			return millis
		}
	}
	return 0
}

func numericMillis(value any) int64 {
	switch v := value.(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		if v > 0 {
			return int64(v + 0.5)
		}
	case json.Number:
		n, _ := v.Float64()
		if n > 0 {
			return int64(n + 0.5)
		}
	case string:
		n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err == nil && n > 0 {
			return int64(n + 0.5)
		}
	}
	return 0
}

func durationMillis(value any) int64 {
	switch v := value.(type) {
	case nil:
		return 0
	case int64:
		return nanosToMillis(v)
	case int:
		return nanosToMillis(int64(v))
	case float64:
		if v > 0 {
			return nanosToMillis(int64(v + 0.5))
		}
	case json.Number:
		n, _ := v.Int64()
		if n > 0 {
			return nanosToMillis(n)
		}
		f, _ := v.Float64()
		if f > 0 {
			return nanosToMillis(int64(f + 0.5))
		}
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return 0
		}
		if d, err := time.ParseDuration(s); err == nil {
			return nanosToMillis(int64(d))
		}
		n, err := strconv.ParseFloat(s, 64)
		if err == nil && n > 0 {
			return nanosToMillis(int64(n + 0.5))
		}
	}
	return 0
}

func nanosToMillis(nanos int64) int64 {
	if nanos <= 0 {
		return 0
	}
	millis := (nanos + int64(time.Millisecond)/2) / int64(time.Millisecond)
	if millis <= 0 {
		return 1
	}
	return millis
}

func firstPositiveInt64(values ...int64) int64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func bearerToken(headers http.Header) string {
	if headers == nil {
		return ""
	}
	for _, name := range []string{"Authorization", "authorization"} {
		for _, value := range headers.Values(name) {
			value = strings.TrimSpace(value)
			if strings.HasPrefix(strings.ToLower(value), "bearer ") {
				return strings.TrimSpace(value[7:])
			}
			if value != "" {
				return value
			}
		}
	}
	for _, name := range []string{"X-API-Key", "x-api-key", "api-key"} {
		if value := strings.TrimSpace(headers.Get(name)); value != "" {
			return value
		}
	}
	return ""
}

func requestedModel(body []byte, query map[string][]string) string {
	if len(body) > 0 {
		var payload map[string]any
		if json.Unmarshal(body, &payload) == nil {
			if model, ok := payload["model"].(string); ok {
				return strings.TrimSpace(model)
			}
		}
	}
	if query != nil {
		for _, key := range []string{"model", "requested_model"} {
			if values := query[key]; len(values) > 0 {
				return strings.TrimSpace(values[0])
			}
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
