package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wnnz/cpa-toolkit/internal/access"
)

func TestHostProviderInventoryFetchesCodexAPIKey(t *testing.T) {
	const token = "management-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("authorization") != "Bearer "+token {
			http.Error(w, "bad auth", http.StatusUnauthorized)
			return
		}
		w.Header().Set("content-type", "application/json")
		switch r.URL.Path {
		case "/v0/management/codex-api-key":
			_, _ = w.Write([]byte(`{"codex-api-key":[{"api-key":"sk-b666d84fa84eceb102ac23b13ea56ea2782749c10b33a150ff25f094c2752c7e","base-url":"https://sub2api.515290.xyz","websockets":true,"models":[{"name":"gpt-5.5"}],"auth-index":"cbcd9e9774d55169"}]}`))
		case "/v0/management/openai-compatibility":
			_, _ = w.Write([]byte(`{"openai-compatibility":[]}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer server.Close()
	t.Setenv("CPA_ACCESS_MANAGER_CPA_BASE", server.URL)

	app := NewApp(nil)
	items, err := app.hostProviderInventory(context.Background(), ManagementRequest{
		Headers: http.Header{"Authorization": []string{"Bearer " + token}},
	})
	if err != nil {
		t.Fatalf("hostProviderInventory() error = %v", err)
	}
	item := findInventoryItem(items, "codex")
	if item == nil {
		t.Fatalf("codex provider item not found in %#v", items)
	}
	wantAuthID := stableAuthID("codex:apikey", "sk-b666d84fa84eceb102ac23b13ea56ea2782749c10b33a150ff25f094c2752c7e", "https://sub2api.515290.xyz")
	if item.Type != access.InventoryProviderInstance || item.AuthID != wantAuthID || item.AuthIndex != "cbcd9e9774d55169" {
		t.Fatalf("provider item = %#v, want provider_instance auth id/index", *item)
	}
	if item.ID != access.ProviderInstanceID("codex", wantAuthID, "cbcd9e9774d55169", "https://sub2api.515290.xyz") {
		t.Fatalf("item ID = %q, want provider instance ID", item.ID)
	}
	raw, _ := json.Marshal(item.Snapshot)
	if string(raw) == "" || strings.Contains(string(raw), "sk-b666d84fa84eceb102ac23b13ea56ea2782749c10b33a150ff25f094c2752c7e") {
		t.Fatalf("snapshot leaked full api key: %s", string(raw))
	}
}

func TestHostProviderInventoryFetchesOpenAICompatibleEntries(t *testing.T) {
	const token = "management-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("authorization") != "Bearer "+token {
			http.Error(w, "bad auth", http.StatusUnauthorized)
			return
		}
		w.Header().Set("content-type", "application/json")
		switch r.URL.Path {
		case "/v0/management/openai-compatibility":
			_, _ = w.Write([]byte(`{"openai-compatibility":[{"name":"opencode","base-url":"https://api.example.com","api-key-entries":[{"api-key":"sk-openai-compatible","proxy-url":"http://proxy.local","auth-index":"idx-open"}],"models":[{"name":"model-a"}]}]}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer server.Close()
	t.Setenv("CPA_ACCESS_MANAGER_CPA_BASE", server.URL)

	app := NewApp(nil)
	items, err := app.hostProviderInventory(context.Background(), ManagementRequest{
		Headers: http.Header{"Authorization": []string{"Bearer " + token}},
	})
	if err != nil {
		t.Fatalf("hostProviderInventory() error = %v", err)
	}
	item := findInventoryItem(items, "openai-compatible-opencode")
	if item == nil {
		t.Fatalf("openai-compatible provider item not found in %#v", items)
	}
	wantAuthID := stableAuthID("openai-compatibility:opencode", "sk-openai-compatible", "https://api.example.com", "http://proxy.local")
	if item.AuthID != wantAuthID || item.AuthIndex != "idx-open" || item.BaseURL != "https://api.example.com" {
		t.Fatalf("provider item = %#v, want openai-compatible auth id/index/base url", *item)
	}
}

func findInventoryItem(items []access.InventoryItem, provider string) *access.InventoryItem {
	for i := range items {
		if items[i].Provider == provider {
			return &items[i]
		}
	}
	return nil
}
