package plugin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/wnnz/cpa-toolkit/internal/access"
	"github.com/wnnz/cpa-toolkit/internal/web"
)

type keyRequest struct {
	ID               string           `json:"id"`
	Name             string           `json:"name"`
	Enabled          *bool            `json:"enabled"`
	Bindings         []access.Binding `json:"bindings"`
	Limits           *access.Limits   `json:"limits"`
	FiveHourLimitUSD *float64         `json:"five_hour_limit_usd"`
	WeeklyLimitUSD   *float64         `json:"weekly_limit_usd"`
	TotalLimitUSD    *float64         `json:"total_limit_usd"`
}

type groupRequest struct {
	ID          string               `json:"id"`
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Members     []access.GroupMember `json:"members"`
}

type inventoryRequest struct {
	Refresh   bool                       `json:"refresh"`
	Items     []access.InventoryItem     `json:"items"`
	AuthFiles []access.HostAuthFileEntry `json:"auth_files"`
}

type pricesRequest struct {
	Rules []access.PriceRule `json:"rules"`
}

type statusResponse struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Enabled bool   `json:"enabled"`
	DBPath  string `json:"db_path"`
}

type hostAuthListResponse struct {
	Files []access.HostAuthFileEntry `json:"files"`
}

func (a *App) handleManagement(raw []byte) ([]byte, error) {
	var req ManagementRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	if isAPIKeyResource(req.Path) {
		return OKEnvelope(ManagementResponse{
			StatusCode: http.StatusOK,
			Headers: http.Header{
				"content-type":  []string{"text/html; charset=utf-8"},
				"cache-control": []string{"no-store"},
			},
			Body: []byte(web.APIKeyHTML),
		})
	}
	if isSettingsResource(req.Path) {
		return OKEnvelope(ManagementResponse{
			StatusCode: http.StatusOK,
			Headers: http.Header{
				"content-type":  []string{"text/html; charset=utf-8"},
				"cache-control": []string{"no-store"},
			},
			Body: []byte(web.SettingsHTML),
		})
	}
	if isUsageResource(req.Path) {
		return OKEnvelope(ManagementResponse{
			StatusCode: http.StatusOK,
			Headers: http.Header{
				"content-type":  []string{"text/html; charset=utf-8"},
				"cache-control": []string{"no-store"},
			},
			Body: []byte(web.UsageStatisticsHTML),
		})
	}
	cfg, store := a.loaded()
	if store == nil {
		return OKEnvelope(jsonManagement(http.StatusServiceUnavailable, map[string]any{"error": "store is not configured"}))
	}
	path := normalizedPluginPath(req.Path)
	switch path {
	case routeStatus:
		return OKEnvelope(jsonManagement(http.StatusOK, statusResponse{ID: PluginID, Version: Version, Enabled: cfg.Enabled, DBPath: cfg.DBPath}))
	case routeKeys:
		return OKEnvelope(a.handleKeys(store, req))
	case routeRotateKey:
		return OKEnvelope(a.handleRotateKey(store, req))
	case routeGroups:
		return OKEnvelope(a.handleGroups(store, req))
	case routeInventory:
		return OKEnvelope(a.handleInventory(store, req))
	case routePrices:
		return OKEnvelope(a.handlePrices(store, req))
	case routePricesSync:
		return OKEnvelope(a.handlePriceSync(store, req))
		case routeUsage:
			return OKEnvelope(a.handleUsageAPI(store, req))
		case routeCPAUpdate:
			return OKEnvelope(a.handleCPAUpdate(req, cfg))
		default:
			return OKEnvelope(jsonManagement(http.StatusNotFound, map[string]any{"error": "route not found"}))
		}
}

func (a *App) handleKeys(store *access.Store, req ManagementRequest) ManagementResponse {
	ctx := context.Background()
	switch strings.ToUpper(req.Method) {
	case http.MethodGet:
		keys, err := store.ListKeys(ctx)
		return jsonOrError(keys, err)
	case http.MethodPost:
		var body keyRequest
		if err := decodeJSON(req.Body, &body); err != nil {
			return errorJSON(http.StatusBadRequest, err)
		}
		enabled := true
		if body.Enabled != nil {
			enabled = *body.Enabled
		}
		key, plain, err := store.CreateKey(ctx, body.Name, enabled, bodyLimits(body), body.Bindings)
		if err != nil {
			return errorJSON(http.StatusBadRequest, err)
		}
		return jsonManagement(http.StatusCreated, map[string]any{"key": key, "plain_key": plain})
	case http.MethodPatch:
		var body keyRequest
		if err := decodeJSON(req.Body, &body); err != nil {
			return errorJSON(http.StatusBadRequest, err)
		}
		id := firstNonEmpty(req.Query.Get("id"), body.ID)
		if id == "" {
			return errorJSON(http.StatusBadRequest, errors.New("id is required"))
		}
		patch := access.KeyPatch{Enabled: body.Enabled}
		if body.Name != "" {
			patch.Name = &body.Name
		}
		if hasAnyJSONField(req.Body, "limits", "five_hour_limit_usd", "weekly_limit_usd", "total_limit_usd") {
			limits := bodyLimits(body)
			patch.Limits = &limits
		}
		if hasAnyJSONField(req.Body, "bindings") {
			bindings := body.Bindings
			patch.Bindings = &bindings
		}
		key, err := store.UpdateKey(ctx, id, patch)
		return jsonOrError(key, err)
	case http.MethodDelete:
		id := req.Query.Get("id")
		if id == "" && len(req.Body) > 0 {
			var body keyRequest
			_ = json.Unmarshal(req.Body, &body)
			id = body.ID
		}
		if id == "" {
			return errorJSON(http.StatusBadRequest, errors.New("id is required"))
		}
		err := store.DeleteKey(ctx, id)
		return jsonOrError(map[string]any{"deleted": id}, err)
	default:
		return errorJSON(http.StatusMethodNotAllowed, errors.New("method not allowed"))
	}
}

func (a *App) handleRotateKey(store *access.Store, req ManagementRequest) ManagementResponse {
	if strings.ToUpper(req.Method) != http.MethodPost {
		return errorJSON(http.StatusMethodNotAllowed, errors.New("method not allowed"))
	}
	var body keyRequest
	_ = json.Unmarshal(req.Body, &body)
	id := firstNonEmpty(req.Query.Get("id"), body.ID)
	if id == "" {
		return errorJSON(http.StatusBadRequest, errors.New("id is required"))
	}
	key, plain, err := store.RotateKey(context.Background(), id)
	if err != nil {
		return errorJSON(http.StatusBadRequest, err)
	}
	return jsonManagement(http.StatusOK, map[string]any{"key": key, "plain_key": plain})
}

func (a *App) handleGroups(store *access.Store, req ManagementRequest) ManagementResponse {
	ctx := context.Background()
	switch strings.ToUpper(req.Method) {
	case http.MethodGet:
		groups, err := store.ListGroups(ctx)
		return jsonOrError(groups, err)
	case http.MethodPost:
		var body groupRequest
		if err := decodeJSON(req.Body, &body); err != nil {
			return errorJSON(http.StatusBadRequest, err)
		}
		group, err := store.CreateGroup(ctx, body.ID, body.Name, body.Description, body.Members)
		return jsonOrError(group, err)
	case http.MethodPatch:
		var body groupRequest
		if err := decodeJSON(req.Body, &body); err != nil {
			return errorJSON(http.StatusBadRequest, err)
		}
		id := firstNonEmpty(req.Query.Get("id"), body.ID)
		if id == "" {
			return errorJSON(http.StatusBadRequest, errors.New("id is required"))
		}
		var patch access.GroupPatch
		if hasAnyJSONField(req.Body, "name") {
			patch.Name = &body.Name
		}
		if hasAnyJSONField(req.Body, "description") {
			patch.Description = &body.Description
		}
		if hasAnyJSONField(req.Body, "members") {
			patch.Members = &body.Members
		}
		group, err := store.UpdateGroup(ctx, id, patch)
		return jsonOrError(group, err)
	case http.MethodDelete:
		id := req.Query.Get("id")
		if id == "" && len(req.Body) > 0 {
			var body groupRequest
			_ = json.Unmarshal(req.Body, &body)
			id = body.ID
		}
		if id == "" {
			return errorJSON(http.StatusBadRequest, errors.New("id is required"))
		}
		err := store.DeleteGroup(ctx, id)
		return jsonOrError(map[string]any{"deleted": id}, err)
	default:
		return errorJSON(http.StatusMethodNotAllowed, errors.New("method not allowed"))
	}
}

func (a *App) handleInventory(store *access.Store, req ManagementRequest) ManagementResponse {
	ctx := context.Background()
	switch strings.ToUpper(req.Method) {
	case http.MethodGet:
		items, err := store.ListInventory(ctx)
		return jsonOrError(items, err)
	case http.MethodPost:
		var body inventoryRequest
		if len(req.Body) > 0 {
			if err := decodeJSON(req.Body, &body); err != nil {
				return errorJSON(http.StatusBadRequest, err)
			}
		}
		if len(body.Items) > 0 {
			for _, item := range body.Items {
				if err := store.UpsertInventoryItem(ctx, item); err != nil {
					return errorJSON(http.StatusBadRequest, err)
				}
			}
		}
		if len(body.AuthFiles) > 0 {
			if err := store.UpsertAuthFiles(ctx, body.AuthFiles); err != nil {
				return errorJSON(http.StatusBadRequest, err)
			}
		}
		if body.Refresh || (len(body.Items) == 0 && len(body.AuthFiles) == 0) {
			files, err := a.hostAuthFiles()
			if err != nil {
				return errorJSON(http.StatusBadRequest, err)
			}
			if err := store.UpsertAuthFiles(ctx, files); err != nil {
				return errorJSON(http.StatusBadRequest, err)
			}
			providers, err := a.hostProviderInventory(ctx, req)
			if err != nil {
				return errorJSON(http.StatusBadRequest, err)
			}
			for _, item := range providers {
				if err := store.UpsertInventoryItem(ctx, item); err != nil {
					return errorJSON(http.StatusBadRequest, err)
				}
			}
		}
		items, err := store.ListInventory(ctx)
		return jsonOrError(items, err)
	default:
		return errorJSON(http.StatusMethodNotAllowed, errors.New("method not allowed"))
	}
}

func (a *App) handlePrices(store *access.Store, req ManagementRequest) ManagementResponse {
	ctx := context.Background()
	switch strings.ToUpper(req.Method) {
	case http.MethodGet:
		rules, err := store.ListPriceRules(ctx)
		return jsonOrError(rules, err)
	case http.MethodPut:
		rules, err := decodePriceRules(req.Body)
		if err != nil {
			return errorJSON(http.StatusBadRequest, err)
		}
		if err := store.UpsertPriceRules(ctx, rules); err != nil {
			return errorJSON(http.StatusBadRequest, err)
		}
		updated, err := store.ListPriceRules(ctx)
		return jsonOrError(updated, err)
	default:
		return errorJSON(http.StatusMethodNotAllowed, errors.New("method not allowed"))
	}
}

func (a *App) handlePriceSync(store *access.Store, req ManagementRequest) ManagementResponse {
	if strings.ToUpper(req.Method) != http.MethodPost {
		return errorJSON(http.StatusMethodNotAllowed, errors.New("method not allowed"))
	}
	var body priceSyncRequest
	if len(strings.TrimSpace(string(req.Body))) > 0 {
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return errorJSON(http.StatusBadRequest, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	rules, sourceURL, err := syncPriceRules(ctx, body.SourceURL)
	if err != nil {
		return errorJSON(http.StatusBadRequest, err)
	}
	if err := store.UpsertPriceRules(context.Background(), rules); err != nil {
		return errorJSON(http.StatusBadRequest, err)
	}
	return jsonManagement(http.StatusOK, priceSyncResponse{SourceURL: sourceURL, Synced: len(rules)})
}

func (a *App) handleUsageAPI(store *access.Store, req ManagementRequest) ManagementResponse {
	if strings.ToUpper(req.Method) != http.MethodGet {
		return errorJSON(http.StatusMethodNotAllowed, errors.New("method not allowed"))
	}
	limit, _ := strconv.Atoi(req.Query.Get("limit"))
	entries, err := store.ListUsage(context.Background(), req.Query.Get("key_id"), limit)
	return jsonOrError(entries, err)
}

func (a *App) hostAuthFiles() ([]access.HostAuthFileEntry, error) {
	if a.host == nil {
		return nil, errors.New("host callback is unavailable")
	}
	result, err := a.callHost(MethodHostAuthList, map[string]any{})
	if err != nil {
		return nil, err
	}
	var resp hostAuthListResponse
	if err := json.Unmarshal(result, &resp); err != nil {
		return nil, err
	}
	return resp.Files, nil
}

func (a *App) callHost(method string, payload any) (json.RawMessage, error) {
	if a.host == nil {
		return nil, errors.New("host callback is unavailable")
	}
	rawReq, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	rawResp, err := a.host(method, rawReq)
	if err != nil {
		return nil, err
	}
	var env Envelope
	if err := json.Unmarshal(rawResp, &env); err != nil {
		return nil, err
	}
	if !env.OK {
		if env.Error != nil {
			return nil, errors.New(env.Error.Message)
		}
		return nil, errors.New("host callback failed")
	}
	return env.Result, nil
}

func bodyLimits(body keyRequest) access.Limits {
	if body.Limits != nil {
		return *body.Limits
	}
	return access.Limits{
		FiveHourUSD: body.FiveHourLimitUSD,
		WeeklyUSD:   body.WeeklyLimitUSD,
		TotalUSD:    body.TotalLimitUSD,
	}
}

func decodePriceRules(raw []byte) ([]access.PriceRule, error) {
	var wrapper pricesRequest
	if err := json.Unmarshal(raw, &wrapper); err == nil && len(wrapper.Rules) > 0 {
		return wrapper.Rules, nil
	}
	var rules []access.PriceRule
	if err := json.Unmarshal(raw, &rules); err != nil {
		return nil, err
	}
	return rules, nil
}

func decodeJSON(raw []byte, dst any) error {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return errors.New("request body is required")
	}
	return json.Unmarshal(raw, dst)
}

func hasAnyJSONField(raw []byte, names ...string) bool {
	if len(raw) == 0 {
		return false
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return false
	}
	for _, name := range names {
		if _, ok := obj[name]; ok {
			return true
		}
	}
	return false
}

func jsonOrError(v any, err error) ManagementResponse {
	if err != nil {
		return errorJSON(statusFromError(err), err)
	}
	return jsonManagement(http.StatusOK, v)
}

func jsonManagement(status int, v any) ManagementResponse {
	raw, err := json.Marshal(v)
	if err != nil {
		return errorJSON(http.StatusInternalServerError, err)
	}
	return ManagementResponse{
		StatusCode: status,
		Headers:    http.Header{"content-type": []string{"application/json; charset=utf-8"}},
		Body:       raw,
	}
}

func errorJSON(status int, err error) ManagementResponse {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	raw, _ := json.Marshal(map[string]any{"error": msg})
	return ManagementResponse{
		StatusCode: status,
		Headers:    http.Header{"content-type": []string{"application/json; charset=utf-8"}},
		Body:       raw,
	}
}

func statusFromError(err error) int {
	switch {
	case errors.Is(err, access.ErrKeyNotFound):
		return http.StatusNotFound
	case errors.Is(err, sql.ErrNoRows):
		return http.StatusNotFound
	default:
		return http.StatusBadRequest
	}
}

func normalizedPluginPath(path string) string {
	path = strings.TrimSpace(path)
	routes := []string{routeRotateKey, routeKeys, routeGroups, routeInventory, routePricesSync, routePrices, routeUsage, routeCPAUpdate, routeStatus}
	for _, route := range routes {
		if strings.HasSuffix(path, route) || strings.Contains(path, route) {
			return route
		}
	}
	if strings.HasPrefix(path, "/v0/management") {
		return strings.TrimPrefix(path, "/v0/management")
	}
	return path
}

func isAPIKeyResource(path string) bool {
	path = strings.TrimSpace(path)
	return hasResourceSuffix(path, resourceAPIKeys, legacyResourceAPIKeys, resourceIndex)
}

func isSettingsResource(path string) bool {
	path = strings.TrimSpace(path)
	return hasResourceSuffix(path, resourceSettings, legacyResourceSettings)
}

func isUsageResource(path string) bool {
	path = strings.TrimSpace(path)
	return hasResourceSuffix(path, resourceUsage, legacyResourceUsage)
}

func hasResourceSuffix(path string, suffixes ...string) bool {
	for _, suffix := range suffixes {
		if strings.HasSuffix(path, "/v0/resource/plugins/"+PluginID+suffix) ||
			strings.HasSuffix(path, "/resource/plugins/"+PluginID+suffix) ||
			strings.HasSuffix(path, suffix) {
			return true
		}
	}
	return false
}
