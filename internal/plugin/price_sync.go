package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/wnnz/cpa-toolkit/internal/access"
)

const defaultPriceSyncURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"

type priceSyncRequest struct {
	SourceURL string `json:"source_url"`
}

type priceSyncResponse struct {
	SourceURL string `json:"source_url"`
	Synced    int    `json:"synced"`
}

type liteLLMPriceSpec struct {
	Provider                string  `json:"litellm_provider"`
	InputCostPerToken       float64 `json:"input_cost_per_token"`
	OutputCostPerToken      float64 `json:"output_cost_per_token"`
	CacheReadInputCostToken float64 `json:"cache_read_input_token_cost"`
}

func syncPriceRules(ctx context.Context, sourceURL string) ([]access.PriceRule, string, error) {
	sourceURL = strings.TrimSpace(sourceURL)
	if sourceURL == "" {
		sourceURL = defaultPriceSyncURL
	}
	if err := validatePriceSyncURL(sourceURL); err != nil {
		return nil, "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("accept", "application/json")
	req.Header.Set("user-agent", PluginID+"/"+Version)

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("price source returned %d", resp.StatusCode)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, "", err
	}
	var specs map[string]liteLLMPriceSpec
	if err := json.Unmarshal(raw, &specs); err != nil {
		return nil, "", err
	}
	return liteLLMPriceRules(specs), sourceURL, nil
}

func validatePriceSyncURL(sourceURL string) error {
	parsed, err := url.Parse(sourceURL)
	if err != nil {
		return err
	}
	if strings.TrimSpace(parsed.Host) == "" {
		return errors.New("price source URL host is required")
	}
	if parsed.Scheme == "https" {
		return nil
	}
	host := strings.ToLower(parsed.Hostname())
	if parsed.Scheme == "http" && (host == "127.0.0.1" || host == "localhost" || host == "::1") {
		return nil
	}
	return errors.New("price source URL must use https")
}

func liteLLMPriceRules(specs map[string]liteLLMPriceSpec) []access.PriceRule {
	seen := map[string]struct{}{}
	rules := make([]access.PriceRule, 0, len(specs))
	for rawModel, spec := range specs {
		rawModel = strings.TrimSpace(rawModel)
		if rawModel == "" || rawModel == "sample_spec" {
			continue
		}
		if spec.InputCostPerToken <= 0 && spec.OutputCostPerToken <= 0 && spec.CacheReadInputCostToken <= 0 {
			continue
		}
		providers := priceProviderAliases(firstNonEmpty(spec.Provider, providerFromModelName(rawModel)))
		models := priceModelAliases(rawModel)
		for _, provider := range providers {
			for _, model := range models {
				key := provider + "\x00" + model
				if _, ok := seen[key]; ok {
					continue
				}
				seen[key] = struct{}{}
				rules = append(rules, access.PriceRule{
					Provider:               provider,
					Model:                  model,
					InputUSDPerMillion:     spec.InputCostPerToken * 1_000_000,
					OutputUSDPerMillion:    spec.OutputCostPerToken * 1_000_000,
					CacheReadUSDPerMillion: spec.CacheReadInputCostToken * 1_000_000,
				})
			}
		}
	}
	return rules
}

func providerFromModelName(model string) string {
	if before, _, ok := strings.Cut(strings.TrimSpace(model), "/"); ok {
		return before
	}
	return ""
}

func priceProviderAliases(provider string) []string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	switch provider {
	case "":
		return nil
	case "openai":
		return []string{"openai", "codex"}
	case "anthropic":
		return []string{"anthropic", "claude"}
	case "vertex_ai", "vertex-ai":
		return []string{"vertex", "vertex_ai"}
	case "google", "google_ai_studio":
		return []string{"gemini"}
	default:
		return []string{provider}
	}
}

func priceModelAliases(model string) []string {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil
	}
	out := []string{model}
	if _, after, ok := strings.Cut(model, "/"); ok {
		after = strings.TrimSpace(after)
		if after != "" && after != model {
			out = append(out, after)
		}
	}
	return uniqueStrings(out)
}

func uniqueStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
