package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"cpa-access-manager/internal/access"
)

const defaultCPAAPIBase = "http://127.0.0.1:8317"

type apiKeyConfigEntry struct {
	APIKey     string `json:"api-key"`
	Priority   int    `json:"priority,omitempty"`
	Prefix     string `json:"prefix,omitempty"`
	BaseURL    string `json:"base-url,omitempty"`
	ProxyURL   string `json:"proxy-url,omitempty"`
	Websockets bool   `json:"websockets,omitempty"`
	AuthIndex  string `json:"auth-index,omitempty"`
	Models     []any  `json:"models,omitempty"`
}

type apiKeyListResponse struct {
	Gemini []apiKeyConfigEntry `json:"gemini-api-key"`
	Claude []apiKeyConfigEntry `json:"claude-api-key"`
	Codex  []apiKeyConfigEntry `json:"codex-api-key"`
	Vertex []apiKeyConfigEntry `json:"vertex-api-key"`
}

type openAICompatKeyEntry struct {
	APIKey    string `json:"api-key"`
	ProxyURL  string `json:"proxy-url,omitempty"`
	AuthIndex string `json:"auth-index,omitempty"`
}

type openAICompatEntry struct {
	Name          string                 `json:"name"`
	Priority      int                    `json:"priority,omitempty"`
	Disabled      bool                   `json:"disabled,omitempty"`
	Prefix        string                 `json:"prefix,omitempty"`
	BaseURL       string                 `json:"base-url"`
	APIKeyEntries []openAICompatKeyEntry `json:"api-key-entries,omitempty"`
	Models        []any                  `json:"models,omitempty"`
	Headers       map[string]string      `json:"headers,omitempty"`
	AuthIndex     string                 `json:"auth-index,omitempty"`
}

type openAICompatListResponse struct {
	OpenAICompatibility []openAICompatEntry `json:"openai-compatibility"`
}

func (a *App) hostProviderInventory(ctx context.Context, req ManagementRequest) ([]access.InventoryItem, error) {
	token := bearerToken(req.Headers)
	if token == "" {
		return nil, fmt.Errorf("management authorization is required to refresh provider inventory")
	}

	items := make([]access.InventoryItem, 0)
	for _, source := range []struct {
		path     string
		key      string
		provider string
		kind     string
	}{
		{path: "/v0/management/gemini-api-key", key: "gemini-api-key", provider: "gemini", kind: "gemini:apikey"},
		{path: "/v0/management/claude-api-key", key: "claude-api-key", provider: "claude", kind: "claude:apikey"},
		{path: "/v0/management/codex-api-key", key: "codex-api-key", provider: "codex", kind: "codex:apikey"},
		{path: "/v0/management/vertex-api-key", key: "vertex-api-key", provider: "vertex", kind: "vertex:apikey"},
	} {
		var payload map[string][]apiKeyConfigEntry
		if err := a.getManagementJSON(ctx, req, token, source.path, &payload); err != nil {
			continue
		}
		for _, entry := range payload[source.key] {
			if item, ok := apiKeyEntryInventoryItem(source.provider, source.kind, entry); ok {
				items = append(items, item)
			}
		}
	}

	var compatPayload openAICompatListResponse
	if err := a.getManagementJSON(ctx, req, token, "/v0/management/openai-compatibility", &compatPayload); err == nil {
		for _, entry := range compatPayload.OpenAICompatibility {
			items = append(items, openAICompatInventoryItems(entry)...)
		}
	}
	return items, nil
}

func (a *App) getManagementJSON(ctx context.Context, req ManagementRequest, token, path string, dst any) error {
	base := strings.TrimRight(os.Getenv("CPA_ACCESS_MANAGER_CPA_BASE"), "/")
	if base == "" {
		base = defaultCPAAPIBase
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return err
	}
	httpReq.Header.Set("authorization", "Bearer "+token)
	httpReq.Header.Set("accept", "application/json")
	if userAgent := strings.TrimSpace(req.Headers.Get("user-agent")); userAgent != "" {
		httpReq.Header.Set("user-agent", userAgent)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("management %s returned %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(dst)
}

func apiKeyEntryInventoryItem(provider, kind string, entry apiKeyConfigEntry) (access.InventoryItem, bool) {
	apiKey := strings.TrimSpace(entry.APIKey)
	baseURL := strings.TrimSpace(entry.BaseURL)
	proxyURL := strings.TrimSpace(entry.ProxyURL)
	if apiKey == "" {
		return access.InventoryItem{}, false
	}
	parts := []string{apiKey, baseURL}
	if provider == "vertex" {
		parts = append(parts, proxyURL)
	}
	authID := stableAuthID(kind, parts...)
	label := providerDisplayName(provider) + " API Key"
	if baseURL != "" {
		label += " - " + baseURL
	}
	snapshot := map[string]any{
		"source":     "config:" + provider + "-api-key",
		"api_key":    maskSecret(apiKey),
		"base_url":   baseURL,
		"proxy_url":  proxyURL,
		"prefix":     strings.TrimSpace(entry.Prefix),
		"models":     entry.Models,
		"websockets": entry.Websockets,
		"priority":   entry.Priority,
	}
	return access.InventoryItem{
		ID:        access.ProviderInstanceID(provider, authID, entry.AuthIndex, baseURL),
		Type:      access.InventoryProviderInstance,
		Provider:  provider,
		AuthID:    authID,
		AuthIndex: strings.TrimSpace(entry.AuthIndex),
		Name:      label,
		Label:     label,
		BaseURL:   baseURL,
		Snapshot:  snapshot,
		UpdatedAt: time.Now().UTC(),
	}, true
}

func openAICompatInventoryItems(entry openAICompatEntry) []access.InventoryItem {
	if entry.Disabled {
		return nil
	}
	providerName := strings.ToLower(strings.TrimSpace(entry.Name))
	if providerName == "" {
		providerName = "openai-compatibility"
	}
	provider := openAICompatibleProviderKey(providerName)
	baseURL := strings.TrimSpace(entry.BaseURL)
	idKind := "openai-compatibility:" + providerName
	out := make([]access.InventoryItem, 0, max(1, len(entry.APIKeyEntries)))
	if len(entry.APIKeyEntries) == 0 {
		authID := stableAuthID(idKind, baseURL)
		out = append(out, access.InventoryItem{
			ID:        access.ProviderInstanceID(provider, authID, entry.AuthIndex, baseURL),
			Type:      access.InventoryProviderInstance,
			Provider:  provider,
			AuthID:    authID,
			AuthIndex: strings.TrimSpace(entry.AuthIndex),
			Name:      compatLabel(entry.Name, baseURL),
			Label:     compatLabel(entry.Name, baseURL),
			BaseURL:   baseURL,
			Snapshot:  openAICompatSnapshot(entry, nil),
			UpdatedAt: time.Now().UTC(),
		})
		return out
	}
	for _, keyEntry := range entry.APIKeyEntries {
		apiKey := strings.TrimSpace(keyEntry.APIKey)
		proxyURL := strings.TrimSpace(keyEntry.ProxyURL)
		authID := stableAuthID(idKind, apiKey, baseURL, proxyURL)
		out = append(out, access.InventoryItem{
			ID:        access.ProviderInstanceID(provider, authID, keyEntry.AuthIndex, baseURL),
			Type:      access.InventoryProviderInstance,
			Provider:  provider,
			AuthID:    authID,
			AuthIndex: strings.TrimSpace(keyEntry.AuthIndex),
			Name:      compatLabel(entry.Name, baseURL),
			Label:     compatLabel(entry.Name, baseURL),
			BaseURL:   baseURL,
			Snapshot:  openAICompatSnapshot(entry, &keyEntry),
			UpdatedAt: time.Now().UTC(),
		})
	}
	return out
}

func openAICompatSnapshot(entry openAICompatEntry, keyEntry *openAICompatKeyEntry) map[string]any {
	snapshot := map[string]any{
		"source":   "config:openai-compatibility",
		"name":     strings.TrimSpace(entry.Name),
		"base_url": strings.TrimSpace(entry.BaseURL),
		"prefix":   strings.TrimSpace(entry.Prefix),
		"models":   entry.Models,
		"priority": entry.Priority,
	}
	if keyEntry != nil {
		snapshot["api_key"] = maskSecret(keyEntry.APIKey)
		snapshot["proxy_url"] = strings.TrimSpace(keyEntry.ProxyURL)
	}
	return snapshot
}

func stableAuthID(kind string, parts ...string) string {
	hasher := sha256.New()
	hasher.Write([]byte(strings.TrimSpace(kind)))
	for _, part := range parts {
		hasher.Write([]byte{0})
		hasher.Write([]byte(strings.TrimSpace(part)))
	}
	digest := hex.EncodeToString(hasher.Sum(nil))
	if len(digest) < 12 {
		digest = fmt.Sprintf("%012s", digest)
	}
	return strings.TrimSpace(kind) + ":" + digest[:12]
}

func openAICompatibleProviderKey(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return "openai-compatibility"
	}
	if name == "openai-compatibility" || strings.HasPrefix(name, "openai-compatible-") {
		return name
	}
	return "openai-compatible-" + name
}

func providerDisplayName(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "codex":
		return "Codex"
	case "claude":
		return "Claude"
	case "gemini":
		return "Gemini"
	case "vertex":
		return "Vertex"
	default:
		return strings.TrimSpace(provider)
	}
}

func compatLabel(name, baseURL string) string {
	label := strings.TrimSpace(name)
	if label == "" {
		label = "OpenAI Compatible"
	}
	if trimmed := strings.TrimSpace(baseURL); trimmed != "" {
		label += " - " + trimmed
	}
	return label
}

func maskSecret(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if len(value) <= 10 {
		return strings.Repeat("*", len(value))
	}
	return value[:4] + strings.Repeat("*", 6) + value[len(value)-4:]
}
