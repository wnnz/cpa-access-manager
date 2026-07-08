package access

import (
	"errors"
	"time"
)

const (
	BindingGroup            = "group"
	BindingAuthID           = "auth_id"
	BindingProviderInstance = "provider_instance"

	InventoryAuthFile         = "auth_file"
	InventoryProviderInstance = "provider_instance"
)

var (
	ErrKeyNotFound      = errors.New("key not found")
	ErrKeyDisabled      = errors.New("key disabled")
	ErrQuotaExceeded    = errors.New("quota exceeded")
	ErrNoBindings       = errors.New("key has no bindings")
	ErrNoAllowedTarget  = errors.New("no allowed target")
	ErrMissingPriceRule = errors.New("missing price rule")
)

type Limits struct {
	FiveHourUSD *float64 `json:"five_hour_limit_usd,omitempty"`
	WeeklyUSD   *float64 `json:"weekly_limit_usd,omitempty"`
	TotalUSD    *float64 `json:"total_limit_usd,omitempty"`
}

func (l Limits) Enabled() bool {
	return positiveLimit(l.FiveHourUSD) || positiveLimit(l.WeeklyUSD) || positiveLimit(l.TotalUSD)
}

type Key struct {
	ID         string     `json:"id"`
	Name       string     `json:"name,omitempty"`
	KeyPrefix  string     `json:"key_prefix,omitempty"`
	Enabled    bool       `json:"enabled"`
	Limits     Limits     `json:"limits"`
	Bindings   []Binding  `json:"bindings,omitempty"`
	Usage      UsageSums  `json:"usage,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

type KeyPatch struct {
	Name       *string
	Enabled    *bool
	Limits     *Limits
	Bindings   *[]Binding
	Rotate     bool
	PlainKey   string
	KeyHash    string
	KeyPrefix  string
	UpdateTime time.Time
}

type Binding struct {
	KeyID      string `json:"key_id,omitempty"`
	TargetType string `json:"target_type"`
	TargetID   string `json:"target_id"`
}

type Group struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Description string        `json:"description,omitempty"`
	Members     []GroupMember `json:"members,omitempty"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
}

type GroupPatch struct {
	Name        *string
	Description *string
	Members     *[]GroupMember
}

type GroupMember struct {
	GroupID    string `json:"group_id,omitempty"`
	MemberType string `json:"member_type"`
	MemberID   string `json:"member_id"`
}

type InventoryItem struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	Provider  string         `json:"provider,omitempty"`
	AuthID    string         `json:"auth_id,omitempty"`
	AuthIndex string         `json:"auth_index,omitempty"`
	Name      string         `json:"name,omitempty"`
	Label     string         `json:"label,omitempty"`
	Email     string         `json:"email,omitempty"`
	BaseURL   string         `json:"base_url,omitempty"`
	Snapshot  map[string]any `json:"snapshot,omitempty"`
	UpdatedAt time.Time      `json:"updated_at"`
}

type PriceRule struct {
	Provider               string    `json:"provider"`
	Model                  string    `json:"model"`
	InputUSDPerMillion     float64   `json:"input_usd_per_million"`
	OutputUSDPerMillion    float64   `json:"output_usd_per_million"`
	CacheReadUSDPerMillion float64   `json:"cache_read_usd_per_million"`
	UpdatedAt              time.Time `json:"updated_at,omitempty"`
}

type UsageDetail struct {
	InputTokens         int64  `json:"input_tokens"`
	OutputTokens        int64  `json:"output_tokens"`
	ReasoningTokens     int64  `json:"reasoning_tokens,omitempty"`
	ReasoningEffort     string `json:"reasoning_effort,omitempty"`
	CachedTokens        int64  `json:"cached_tokens,omitempty"`
	CacheReadTokens     int64  `json:"cache_read_tokens,omitempty"`
	CacheCreationTokens int64  `json:"cache_creation_tokens,omitempty"`
	TotalTokens         int64  `json:"total_tokens,omitempty"`
}

type UsageEntry struct {
	ID                  int64       `json:"id"`
	KeyID               string      `json:"key_id"`
	RequestID           string      `json:"request_id,omitempty"`
	RequestResource     string      `json:"request_resource,omitempty"`
	AuthID              string      `json:"auth_id,omitempty"`
	AuthIndex           string      `json:"auth_index,omitempty"`
	Provider            string      `json:"provider,omitempty"`
	ProviderInstanceID  string      `json:"provider_instance_id,omitempty"`
	Model               string      `json:"model,omitempty"`
	Alias               string      `json:"alias,omitempty"`
	Detail              UsageDetail `json:"detail"`
	FirstTokenLatencyMS int64       `json:"first_token_latency_ms,omitempty"`
	TotalLatencyMS      int64       `json:"total_latency_ms,omitempty"`
	USD                 float64     `json:"usd"`
	Failed              bool        `json:"failed,omitempty"`
	CreatedAt           time.Time   `json:"created_at"`
}

type UsageSums struct {
	FiveHourUSD float64 `json:"five_hour_usd"`
	WeeklyUSD   float64 `json:"weekly_usd"`
	TotalUSD    float64 `json:"total_usd"`
}

type Candidate struct {
	ID         string            `json:"id"`
	Provider   string            `json:"provider"`
	Priority   int               `json:"priority,omitempty"`
	Status     string            `json:"status,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

type HostAuthFileEntry struct {
	ID             string    `json:"id,omitempty"`
	AuthIndex      string    `json:"auth_index,omitempty"`
	Name           string    `json:"name"`
	Type           string    `json:"type,omitempty"`
	Provider       string    `json:"provider,omitempty"`
	Label          string    `json:"label,omitempty"`
	Status         string    `json:"status,omitempty"`
	StatusMessage  string    `json:"status_message,omitempty"`
	Disabled       bool      `json:"disabled,omitempty"`
	Unavailable    bool      `json:"unavailable,omitempty"`
	RuntimeOnly    bool      `json:"runtime_only,omitempty"`
	Source         string    `json:"source,omitempty"`
	Path           string    `json:"path,omitempty"`
	Size           int64     `json:"size,omitempty"`
	ModTime        time.Time `json:"modtime,omitempty"`
	UpdatedAt      time.Time `json:"updated_at,omitempty"`
	CreatedAt      time.Time `json:"created_at,omitempty"`
	LastRefresh    time.Time `json:"last_refresh,omitempty"`
	NextRetryAfter time.Time `json:"next_retry_after,omitempty"`
	Email          string    `json:"email,omitempty"`
	ProjectID      string    `json:"project_id,omitempty"`
	AccountType    string    `json:"account_type,omitempty"`
	Account        string    `json:"account,omitempty"`
	Priority       int       `json:"priority,omitempty"`
	Note           string    `json:"note,omitempty"`
	Websockets     bool      `json:"websockets,omitempty"`
}

type Scope struct {
	HasBindings         bool
	AuthIDs             map[string]struct{}
	ProviderInstanceIDs map[string]struct{}
	Providers           map[string]struct{}
}

func positiveLimit(v *float64) bool {
	return v != nil && *v > 0
}
