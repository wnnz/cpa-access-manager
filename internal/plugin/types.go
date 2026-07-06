package plugin

import (
	"encoding/json"
	"net/http"
	"net/url"
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
	routeKeys      = "/plugins/" + PluginID + "/keys"
	routeRotateKey = "/plugins/" + PluginID + "/keys/rotate"
	routeGroups    = "/plugins/" + PluginID + "/groups"
	routeInventory = "/plugins/" + PluginID + "/inventory"
	routePrices    = "/plugins/" + PluginID + "/prices"
	routeUsage     = "/plugins/" + PluginID + "/usage"
	routeStatus    = "/plugins/" + PluginID + "/status"

	resourceIndex = "/index.html"
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
	Provider     string         `json:"Provider"`
	ExecutorType string         `json:"ExecutorType"`
	Model        string         `json:"Model"`
	Alias        string         `json:"Alias"`
	KeyID        string         `json:"KeyID"`
	KeyIDSnake   string         `json:"key_id"`
	Principal    string         `json:"Principal"`
	APIKey       string         `json:"APIKey"`
	AuthID       string         `json:"AuthID"`
	AuthIndex    string         `json:"AuthIndex"`
	AuthType     string         `json:"AuthType"`
	Source       string         `json:"Source"`
	Failed       bool           `json:"Failed"`
	Metadata     map[string]any `json:"Metadata"`
	Detail       UsageDetailIn  `json:"Detail"`
}

type UsageDetailIn struct {
	InputTokens         int64 `json:"InputTokens"`
	OutputTokens        int64 `json:"OutputTokens"`
	ReasoningTokens     int64 `json:"ReasoningTokens"`
	CachedTokens        int64 `json:"CachedTokens"`
	CacheReadTokens     int64 `json:"CacheReadTokens"`
	CacheCreationTokens int64 `json:"CacheCreationTokens"`
	TotalTokens         int64 `json:"TotalTokens"`
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
