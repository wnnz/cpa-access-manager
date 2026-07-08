package plugin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const (
	ABIVersion    uint32 = 1
	SchemaVersion uint32 = 1

	PluginID   = "cpa-toolkit"
	PluginName = "CPA Toolkit"
	Version    = "0.1.0"

	MethodPluginRegister    = "plugin.register"
	MethodPluginReconfigure = "plugin.reconfigure"

	MethodFrontendAuthIdentifier   = "frontend_auth.identifier"
	MethodFrontendAuthAuthenticate = "frontend_auth.authenticate"
	MethodModelRoute               = "model.route"
	MethodSchedulerPick            = "scheduler.pick"
	MethodUsageHandle              = "usage.handle"
	MethodManagementRegister       = "management.register"
	MethodManagementHandle         = "management.handle"

	MethodHostAuthList = "host.auth.list"
)

const (
	routeKeys       = "/plugins/" + PluginID + "/keys"
	routeRotateKey  = "/plugins/" + PluginID + "/keys/rotate"
	routeGroups     = "/plugins/" + PluginID + "/groups"
	routeInventory  = "/plugins/" + PluginID + "/inventory"
	routePrices     = "/plugins/" + PluginID + "/prices"
	routePricesSync = "/plugins/" + PluginID + "/prices/sync"
	routeUsage      = "/plugins/" + PluginID + "/usage"
	routeStatus     = "/plugins/" + PluginID + "/status"
	routeCPAUpdate  = "/plugins/" + PluginID + "/cpa/update"

	resourceAPIKeys  = "/01-apikey.html"
	resourceUsage    = "/02-usage-statistics.html"
	resourceSettings = "/03-settings.html"
	resourceSharedUI = "/shared-ui.css"
	resourceIndex    = "/index.html"

	legacyResourceAPIKeys  = "/apikey.html"
	legacyResourceUsage    = "/usage-statistics.html"
	legacyResourceSettings = "/settings.html"
)

type Envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *EnvelopeError  `json:"error,omitempty"`
}

type EnvelopeError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Retryable  bool   `json:"retryable,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

type LifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

type Config struct {
	Enabled       bool   `yaml:"enabled" json:"enabled"`
	DBPath        string `yaml:"db_path" json:"db_path"`
	AllowUnpriced bool   `yaml:"allow_unpriced" json:"allow_unpriced"`
}

type Registration struct {
	SchemaVersion uint32       `json:"schema_version"`
	Metadata      Metadata     `json:"metadata"`
	Capabilities  Capabilities `json:"capabilities"`
}

type Metadata struct {
	Name             string        `json:"Name"`
	Version          string        `json:"Version"`
	Author           string        `json:"Author"`
	GitHubRepository string        `json:"GitHubRepository"`
	Logo             string        `json:"Logo,omitempty"`
	ConfigFields     []ConfigField `json:"ConfigFields"`
}

type ConfigField struct {
	Name        string   `json:"Name"`
	Type        string   `json:"Type"`
	EnumValues  []string `json:"EnumValues,omitempty"`
	Description string   `json:"Description"`
}

type Capabilities struct {
	FrontendAuthProvider          bool `json:"frontend_auth_provider"`
	FrontendAuthProviderExclusive bool `json:"frontend_auth_provider_exclusive,omitempty"`
	ModelRouter                   bool `json:"model_router"`
	Scheduler                     bool `json:"scheduler"`
	UsagePlugin                   bool `json:"usage_plugin"`
	ManagementAPI                 bool `json:"management_api"`
}

type IdentifierResponse struct {
	Identifier string `json:"identifier"`
}

type FrontendAuthRequest struct {
	Method  string      `json:"Method"`
	Path    string      `json:"Path"`
	Headers http.Header `json:"Headers"`
	Query   url.Values  `json:"Query"`
	Body    []byte      `json:"Body"`
}

type FrontendAuthResponse struct {
	Authenticated bool              `json:"Authenticated"`
	Principal     string            `json:"Principal,omitempty"`
	Metadata      map[string]string `json:"Metadata,omitempty"`
}

type ModelRouteRequest struct {
	SourceFormat       string         `json:"SourceFormat"`
	RequestedModel     string         `json:"RequestedModel"`
	Stream             bool           `json:"Stream"`
	Headers            http.Header    `json:"Headers"`
	Query              url.Values     `json:"Query"`
	Body               []byte         `json:"Body"`
	Metadata           map[string]any `json:"Metadata"`
	AvailableProviders []string       `json:"AvailableProviders"`
}

type ModelRouteResponse struct {
	Handled     bool   `json:"Handled"`
	TargetKind  string `json:"TargetKind,omitempty"`
	Target      string `json:"Target,omitempty"`
	TargetModel string `json:"TargetModel,omitempty"`
	Reason      string `json:"Reason,omitempty"`
}

type SchedulerPickRequest struct {
	Provider   string                   `json:"Provider,omitempty"`
	Providers  []string                 `json:"Providers,omitempty"`
	Model      string                   `json:"Model"`
	Stream     bool                     `json:"Stream,omitempty"`
	Options    SchedulerPickOptions     `json:"Options"`
	Candidates []SchedulerAuthCandidate `json:"Candidates"`
}

type SchedulerPickOptions struct {
	Headers  map[string][]string `json:"Headers,omitempty"`
	Metadata map[string]any      `json:"Metadata,omitempty"`
}

type SchedulerAuthCandidate struct {
	ID         string            `json:"ID"`
	Provider   string            `json:"Provider"`
	Priority   int               `json:"Priority,omitempty"`
	Status     string            `json:"Status,omitempty"`
	Attributes map[string]string `json:"Attributes,omitempty"`
	Metadata   map[string]any    `json:"Metadata,omitempty"`
}

type SchedulerPickResponse struct {
	AuthID          string `json:"AuthID,omitempty"`
	DelegateBuiltin string `json:"DelegateBuiltin,omitempty"`
	Handled         bool   `json:"Handled"`
}

type UsageHandleRequest struct {
	Provider               string         `json:"Provider"`
	ExecutorType           string         `json:"ExecutorType"`
	Model                  string         `json:"Model"`
	Alias                  string         `json:"Alias"`
	KeyID                  string         `json:"KeyID"`
	KeyIDSnake             string         `json:"key_id"`
	Principal              string         `json:"Principal"`
	APIKey                 string         `json:"APIKey"`
	APIKeyAlt              string         `json:"api_key"`
	AuthID                 string         `json:"AuthID"`
	AuthIDAlt              string         `json:"auth_id"`
	AuthIndex              string         `json:"AuthIndex"`
	AuthIndexAlt           string         `json:"auth_index"`
	AuthType               string         `json:"AuthType"`
	Source                 string         `json:"Source"`
	RequestID              string         `json:"RequestID"`
	RequestIDAlt           string         `json:"request_id"`
	RequestResource        string         `json:"RequestResource"`
	RequestResourceAlt     string         `json:"request_resource"`
	TTFT                   any            `json:"TTFT"`
	TTFTAlt                any            `json:"ttft"`
	Latency                any            `json:"Latency"`
	LatencyAlt             any            `json:"latency"`
	Duration               any            `json:"Duration"`
	DurationAlt            any            `json:"duration"`
	FirstTokenLatencyMS    int64          `json:"FirstTokenLatencyMS"`
	FirstTokenLatencyMSAlt int64          `json:"first_token_latency_ms"`
	TTFTMS                 int64          `json:"TTFTMS"`
	TTFTMSAlt              int64          `json:"ttft_ms"`
	TotalLatencyMS         int64          `json:"TotalLatencyMS"`
	TotalLatencyMSAlt      int64          `json:"total_latency_ms"`
	LatencyMS              int64          `json:"LatencyMS"`
	LatencyMSAlt           int64          `json:"latency_ms"`
	DurationMS             int64          `json:"DurationMS"`
	DurationMSAlt          int64          `json:"duration_ms"`
	ReasoningEffort        string         `json:"ReasoningEffort"`
	ReasoningEffortAlt     string         `json:"reasoning_effort"`
	Failed                 bool           `json:"Failed"`
	Metadata               map[string]any `json:"Metadata"`
	Detail                 UsageDetailIn  `json:"Detail"`
}

type UsageDetailIn struct {
	InputTokens         int64  `json:"InputTokens"`
	OutputTokens        int64  `json:"OutputTokens"`
	ReasoningTokens     int64  `json:"ReasoningTokens"`
	ReasoningEffort     string `json:"ReasoningEffort"`
	CachedTokens        int64  `json:"CachedTokens"`
	CacheReadTokens     int64  `json:"CacheReadTokens"`
	CacheCreationTokens int64  `json:"CacheCreationTokens"`
	TotalTokens         int64  `json:"TotalTokens"`
}

func (d *UsageDetailIn) UnmarshalJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	d.InputTokens = firstJSONInt(fields, "InputTokens", "input_tokens", "inputTokens", "PromptTokens", "prompt_tokens", "promptTokens")
	d.OutputTokens = firstJSONInt(fields, "OutputTokens", "output_tokens", "outputTokens", "CompletionTokens", "completion_tokens", "completionTokens")
	d.ReasoningTokens = firstJSONInt(fields, "ReasoningTokens", "reasoning_tokens", "reasoningTokens")
	d.ReasoningEffort = firstJSONString(fields, "ReasoningEffort", "reasoning_effort", "reasoningEffort")
	if v := nestedJSONString(fields, "reasoning", "effort", "reasoning_effort"); v != "" {
		d.ReasoningEffort = v
	}
	if v := nestedJSONInt(fields, "output_tokens_details", "reasoning_tokens"); v != 0 {
		d.ReasoningTokens = v
	}
	if v := nestedJSONInt(fields, "completion_tokens_details", "reasoning_tokens"); v != 0 {
		d.ReasoningTokens = v
	}
	d.CachedTokens = firstJSONInt(fields, "CachedTokens", "cached_tokens", "cachedTokens")
	if v := nestedJSONInt(fields, "input_tokens_details", "cached_tokens"); v != 0 {
		d.CachedTokens = v
	}
	if v := nestedJSONInt(fields, "prompt_tokens_details", "cached_tokens"); v != 0 {
		d.CachedTokens = v
	}
	d.CacheReadTokens = firstJSONInt(fields, "CacheReadTokens", "cache_read_tokens", "cacheReadTokens", "CacheReadInputTokens", "cache_read_input_tokens", "cacheReadInputTokens")
	d.CacheCreationTokens = firstJSONInt(fields, "CacheCreationTokens", "cache_creation_tokens", "cacheCreationTokens", "CacheCreationInputTokens", "cache_creation_input_tokens", "cacheCreationInputTokens")
	d.TotalTokens = firstJSONInt(fields, "TotalTokens", "total_tokens", "totalTokens")
	return nil
}

func firstJSONString(fields map[string]json.RawMessage, keys ...string) string {
	for _, key := range keys {
		raw, ok := fields[key]
		if !ok {
			continue
		}
		if value, ok := decodeJSONString(raw); ok {
			return value
		}
	}
	return ""
}

func nestedJSONString(fields map[string]json.RawMessage, objectKey string, keys ...string) string {
	raw, ok := fields[objectKey]
	if !ok {
		return ""
	}
	var nested map[string]json.RawMessage
	if err := json.Unmarshal(raw, &nested); err != nil {
		return ""
	}
	return firstJSONString(nested, keys...)
}

func firstJSONInt(fields map[string]json.RawMessage, keys ...string) int64 {
	for _, key := range keys {
		raw, ok := fields[key]
		if !ok {
			continue
		}
		if value, ok := decodeJSONInt(raw); ok {
			return value
		}
	}
	return 0
}

func nestedJSONInt(fields map[string]json.RawMessage, objectKey string, keys ...string) int64 {
	raw, ok := fields[objectKey]
	if !ok {
		return 0
	}
	var nested map[string]json.RawMessage
	if err := json.Unmarshal(raw, &nested); err != nil {
		return 0
	}
	return firstJSONInt(nested, keys...)
}

func decodeJSONInt(raw []byte) (int64, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return 0, false
	}
	switch v := value.(type) {
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return i, true
		}
		if f, err := v.Float64(); err == nil {
			return int64(f), true
		}
	case string:
		if i, err := strconv.ParseInt(v, 10, 64); err == nil {
			return i, true
		}
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return int64(f), true
		}
	}
	return 0, false
}

func decodeJSONString(raw []byte) (string, bool) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v), strings.TrimSpace(v) != ""
	case map[string]any:
		if effort, ok := v["effort"]; ok {
			s := strings.TrimSpace(fmt.Sprint(effort))
			return s, s != "" && s != "<nil>"
		}
	}
	return "", false
}

type ManagementRegistrationResponse struct {
	Routes    []ManagementRoute `json:"Routes,omitempty"`
	Resources []ResourceRoute   `json:"Resources,omitempty"`
}

type ManagementRoute struct {
	Method      string `json:"Method"`
	Path        string `json:"Path"`
	Menu        string `json:"Menu,omitempty"`
	Description string `json:"Description,omitempty"`
}

type ResourceRoute struct {
	Path        string `json:"Path"`
	Menu        string `json:"Menu,omitempty"`
	Description string `json:"Description,omitempty"`
}

type ManagementRequest struct {
	Method         string      `json:"Method"`
	Path           string      `json:"Path"`
	Headers        http.Header `json:"Headers"`
	Query          url.Values  `json:"Query"`
	Body           []byte      `json:"Body"`
	HostCallbackID string      `json:"host_callback_id,omitempty"`
}

type ManagementResponse struct {
	StatusCode int         `json:"StatusCode"`
	Headers    http.Header `json:"Headers"`
	Body       []byte      `json:"Body"`
}
