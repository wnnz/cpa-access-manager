package access

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(context.Background(), filepath.Join(t.TempDir(), "access.db"), false)
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestPickCandidate_KeyBoundToSingleAuthID(t *testing.T) {
	store := newTestStore(t)
	key, _, err := store.CreateKey(context.Background(), "team-a", true, Limits{}, []Binding{{TargetType: BindingAuthID, TargetID: "auth-a"}})
	if err != nil {
		t.Fatalf("CreateKey() error = %v", err)
	}

	got, err := store.PickCandidate(context.Background(), key.ID, []Candidate{
		{ID: "auth-b", Provider: "codex", Priority: 100},
		{ID: "auth-a", Provider: "codex", Priority: 1},
	})
	if err != nil {
		t.Fatalf("PickCandidate() error = %v", err)
	}
	if got.ID != "auth-a" {
		t.Fatalf("PickCandidate() = %q, want auth-a", got.ID)
	}
}

func TestPickCandidate_GroupBindingOnlyUsesGroupMembers(t *testing.T) {
	store := newTestStore(t)
	group, err := store.CreateGroup(context.Background(), "g-team", "Team", "", []GroupMember{{MemberType: BindingAuthID, MemberID: "auth-g"}})
	if err != nil {
		t.Fatalf("CreateGroup() error = %v", err)
	}
	key, _, err := store.CreateKey(context.Background(), "team-key", true, Limits{}, []Binding{{TargetType: BindingGroup, TargetID: group.ID}})
	if err != nil {
		t.Fatalf("CreateKey() error = %v", err)
	}

	got, err := store.PickCandidate(context.Background(), key.ID, []Candidate{
		{ID: "auth-x", Provider: "gemini", Priority: 999},
		{ID: "auth-g", Provider: "gemini", Priority: 1},
	})
	if err != nil {
		t.Fatalf("PickCandidate() error = %v", err)
	}
	if got.ID != "auth-g" {
		t.Fatalf("PickCandidate() = %q, want auth-g", got.ID)
	}
}

func TestPickCandidate_GroupAuthFileMemberUsesAuthID(t *testing.T) {
	store := newTestStore(t)
	if err := store.UpsertInventoryItem(context.Background(), InventoryItem{
		ID:       "auth-file-a",
		Type:     InventoryAuthFile,
		Provider: "codex",
		AuthID:   "auth-file-a",
		Name:     "auth-file-a.json",
	}); err != nil {
		t.Fatalf("UpsertInventoryItem() error = %v", err)
	}
	group, err := store.CreateGroup(context.Background(), "g-files", "Files", "", []GroupMember{{MemberType: InventoryAuthFile, MemberID: "auth-file-a"}})
	if err != nil {
		t.Fatalf("CreateGroup() error = %v", err)
	}
	key, _, err := store.CreateKey(context.Background(), "file-key", true, Limits{}, []Binding{{TargetType: BindingGroup, TargetID: group.ID}})
	if err != nil {
		t.Fatalf("CreateKey() error = %v", err)
	}

	got, err := store.PickCandidate(context.Background(), key.ID, []Candidate{
		{ID: "auth-file-b", Provider: "codex", Priority: 1},
		{ID: "auth-file-a", Provider: "codex", Priority: 100},
	})
	if err != nil {
		t.Fatalf("PickCandidate() error = %v", err)
	}
	if got.ID != "auth-file-a" {
		t.Fatalf("PickCandidate() = %q, want auth-file-a", got.ID)
	}
}

func TestUpsertAuthFilesDoesNotExposeAuthFileAsProviderInstance(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	staleID := ProviderInstanceID("codex", "auth-file-a", "", "")
	if err := store.UpsertInventoryItem(ctx, InventoryItem{
		ID:       staleID,
		Type:     InventoryProviderInstance,
		Provider: "codex",
		AuthID:   "auth-file-a",
		Name:     "account@example.com",
		Label:    "account@example.com",
	}); err != nil {
		t.Fatalf("UpsertInventoryItem(stale provider instance) error = %v", err)
	}
	if err := store.UpsertAuthFiles(ctx, []HostAuthFileEntry{{
		ID:       "auth-file-a",
		Provider: "codex",
		Name:     "auth-file-a.json",
		Email:    "account@example.com",
	}}); err != nil {
		t.Fatalf("UpsertAuthFiles() error = %v", err)
	}

	items, err := store.ListInventory(ctx)
	if err != nil {
		t.Fatalf("ListInventory() error = %v", err)
	}
	var authFiles, providerInstances int
	for _, item := range items {
		switch item.Type {
		case InventoryAuthFile:
			authFiles++
		case InventoryProviderInstance:
			providerInstances++
			t.Fatalf("unexpected provider_instance from auth file: %#v", item)
		}
	}
	if authFiles != 1 || providerInstances != 0 {
		t.Fatalf("inventory counts auth_file=%d provider_instance=%d, want 1/0", authFiles, providerInstances)
	}
}

func TestPickCandidate_ProviderInstanceDoesNotAllowSameProviderOtherAuth(t *testing.T) {
	store := newTestStore(t)
	instanceID := ProviderInstanceID("codex", "auth-a", "", "")
	if err := store.UpsertInventoryItem(context.Background(), InventoryItem{
		ID:       instanceID,
		Type:     InventoryProviderInstance,
		Provider: "codex",
		AuthID:   "auth-a",
	}); err != nil {
		t.Fatalf("UpsertInventoryItem() error = %v", err)
	}
	key, _, err := store.CreateKey(context.Background(), "instance-key", true, Limits{}, []Binding{{TargetType: BindingProviderInstance, TargetID: instanceID}})
	if err != nil {
		t.Fatalf("CreateKey() error = %v", err)
	}

	got, err := store.PickCandidate(context.Background(), key.ID, []Candidate{
		{ID: "auth-b", Provider: "codex", Priority: 100},
		{ID: "auth-a", Provider: "codex", Priority: 1},
	})
	if err != nil {
		t.Fatalf("PickCandidate() error = %v", err)
	}
	if got.ID != "auth-a" {
		t.Fatalf("PickCandidate() = %q, want auth-a", got.ID)
	}

	_, err = store.PickCandidate(context.Background(), key.ID, []Candidate{{ID: "auth-b", Provider: "codex"}})
	if !errors.Is(err, ErrNoAllowedTarget) {
		t.Fatalf("PickCandidate() error = %v, want ErrNoAllowedTarget", err)
	}
}

func TestAuthenticate_LimitedKeyRejectsUnpricedModel(t *testing.T) {
	store := newTestStore(t)
	instanceID := ProviderInstanceID("codex", "auth-a", "", "")
	if err := store.UpsertInventoryItem(context.Background(), InventoryItem{
		ID:       instanceID,
		Type:     InventoryProviderInstance,
		Provider: "codex",
		AuthID:   "auth-a",
	}); err != nil {
		t.Fatalf("UpsertInventoryItem() error = %v", err)
	}
	limit := 1.0
	_, plain, err := store.CreateKey(context.Background(), "limited", true, Limits{TotalUSD: &limit}, []Binding{{TargetType: BindingProviderInstance, TargetID: instanceID}})
	if err != nil {
		t.Fatalf("CreateKey() error = %v", err)
	}

	_, err = store.Authenticate(context.Background(), plain, "gpt-test", time.Now())
	if !errors.Is(err, ErrMissingPriceRule) {
		t.Fatalf("Authenticate() error = %v, want ErrMissingPriceRule", err)
	}
}

func TestRecordUsageUnpricedModelStillRecordsLedger(t *testing.T) {
	store := newTestStore(t)
	key, _, err := store.CreateKey(context.Background(), "team", true, Limits{}, []Binding{{TargetType: BindingAuthID, TargetID: "auth-a"}})
	if err != nil {
		t.Fatalf("CreateKey() error = %v", err)
	}
	entry, err := store.RecordUsage(context.Background(), UsageEntry{
		KeyID:    key.ID,
		Provider: "codex",
		Model:    "unpriced-model",
		Detail:   UsageDetail{InputTokens: 12, OutputTokens: 34, TotalTokens: 46},
	})
	if err != nil {
		t.Fatalf("RecordUsage() error = %v", err)
	}
	if entry.USD != 0 {
		t.Fatalf("RecordUsage() USD = %v, want 0", entry.USD)
	}
	entries, err := store.ListUsage(context.Background(), key.ID, 10)
	if err != nil {
		t.Fatalf("ListUsage() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("ListUsage() len = %d, want 1", len(entries))
	}
	if entries[0].Detail.TotalTokens != 46 || entries[0].USD != 0 {
		t.Fatalf("usage entry = %#v, want total tokens 46 and USD 0", entries[0])
	}
}

func TestAuthenticate_RejectsKeyIDAsPresentedToken(t *testing.T) {
	store := newTestStore(t)
	key, _, err := store.CreateKey(context.Background(), "team", true, Limits{}, []Binding{{TargetType: BindingAuthID, TargetID: "auth-a"}})
	if err != nil {
		t.Fatalf("CreateKey() error = %v", err)
	}
	_, err = store.Authenticate(context.Background(), key.ID, "", time.Now())
	if !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("Authenticate() error = %v, want ErrKeyNotFound", err)
	}
}

func TestAuthenticate_QuotaReachedRejectsNextRequest(t *testing.T) {
	store := newTestStore(t)
	instanceID := ProviderInstanceID("codex", "auth-a", "", "")
	if err := store.UpsertInventoryItem(context.Background(), InventoryItem{
		ID:       instanceID,
		Type:     InventoryProviderInstance,
		Provider: "codex",
		AuthID:   "auth-a",
	}); err != nil {
		t.Fatalf("UpsertInventoryItem() error = %v", err)
	}
	if err := store.UpsertPriceRules(context.Background(), []PriceRule{{
		Provider:            "codex",
		Model:               "gpt-test",
		InputUSDPerMillion:  1,
		OutputUSDPerMillion: 1,
	}}); err != nil {
		t.Fatalf("UpsertPriceRules() error = %v", err)
	}
	limit := 0.01
	key, plain, err := store.CreateKey(context.Background(), "limited", true, Limits{TotalUSD: &limit}, []Binding{{TargetType: BindingProviderInstance, TargetID: instanceID}})
	if err != nil {
		t.Fatalf("CreateKey() error = %v", err)
	}
	if _, err := store.RecordUsage(context.Background(), UsageEntry{
		KeyID:    key.ID,
		Provider: "codex",
		Model:    "gpt-test",
		Detail:   UsageDetail{InputTokens: 10_000},
	}); err != nil {
		t.Fatalf("RecordUsage() error = %v", err)
	}

	_, err = store.Authenticate(context.Background(), plain, "gpt-test", time.Now())
	if !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("Authenticate() error = %v, want ErrQuotaExceeded", err)
	}
}

func TestAuthenticate_FiveHourLimitUsesRollingWindow(t *testing.T) {
	store := newTestStore(t)
	instanceID := ProviderInstanceID("codex", "auth-a", "", "")
	if err := store.UpsertInventoryItem(context.Background(), InventoryItem{
		ID:       instanceID,
		Type:     InventoryProviderInstance,
		Provider: "codex",
		AuthID:   "auth-a",
	}); err != nil {
		t.Fatalf("UpsertInventoryItem() error = %v", err)
	}
	if err := store.UpsertPriceRules(context.Background(), []PriceRule{{
		Provider:           "codex",
		Model:              "gpt-test",
		InputUSDPerMillion: 1,
	}}); err != nil {
		t.Fatalf("UpsertPriceRules() error = %v", err)
	}
	limit := 0.01
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	key, plain, err := store.CreateKey(context.Background(), "limited", true, Limits{FiveHourUSD: &limit}, []Binding{{TargetType: BindingProviderInstance, TargetID: instanceID}})
	if err != nil {
		t.Fatalf("CreateKey() error = %v", err)
	}
	if _, err := store.RecordUsage(context.Background(), UsageEntry{
		KeyID:     key.ID,
		Provider:  "codex",
		Model:     "gpt-test",
		Detail:    UsageDetail{InputTokens: 10_000},
		CreatedAt: now.Add(-5*time.Hour - time.Minute),
	}); err != nil {
		t.Fatalf("RecordUsage(old) error = %v", err)
	}
	if _, err := store.Authenticate(context.Background(), plain, "gpt-test", now); err != nil {
		t.Fatalf("Authenticate() with old 5h usage error = %v", err)
	}
	if _, err := store.RecordUsage(context.Background(), UsageEntry{
		KeyID:     key.ID,
		Provider:  "codex",
		Model:     "gpt-test",
		Detail:    UsageDetail{InputTokens: 10_000},
		CreatedAt: now.Add(-time.Hour),
	}); err != nil {
		t.Fatalf("RecordUsage(recent) error = %v", err)
	}
	_, err = store.Authenticate(context.Background(), plain, "gpt-test", now)
	if !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("Authenticate() error = %v, want ErrQuotaExceeded", err)
	}
}

func TestAuthenticate_WeeklyLimitUsesRollingSevenDays(t *testing.T) {
	store := newTestStore(t)
	instanceID := ProviderInstanceID("codex", "auth-a", "", "")
	if err := store.UpsertInventoryItem(context.Background(), InventoryItem{
		ID:       instanceID,
		Type:     InventoryProviderInstance,
		Provider: "codex",
		AuthID:   "auth-a",
	}); err != nil {
		t.Fatalf("UpsertInventoryItem() error = %v", err)
	}
	if err := store.UpsertPriceRules(context.Background(), []PriceRule{{
		Provider:           "codex",
		Model:              "gpt-test",
		InputUSDPerMillion: 1,
	}}); err != nil {
		t.Fatalf("UpsertPriceRules() error = %v", err)
	}
	limit := 0.01
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	key, plain, err := store.CreateKey(context.Background(), "limited", true, Limits{WeeklyUSD: &limit}, []Binding{{TargetType: BindingProviderInstance, TargetID: instanceID}})
	if err != nil {
		t.Fatalf("CreateKey() error = %v", err)
	}
	if _, err := store.RecordUsage(context.Background(), UsageEntry{
		KeyID:     key.ID,
		Provider:  "codex",
		Model:     "gpt-test",
		Detail:    UsageDetail{InputTokens: 10_000},
		CreatedAt: now.Add(-7*24*time.Hour - time.Minute),
	}); err != nil {
		t.Fatalf("RecordUsage(old) error = %v", err)
	}
	if _, err := store.Authenticate(context.Background(), plain, "gpt-test", now); err != nil {
		t.Fatalf("Authenticate() with old weekly usage error = %v", err)
	}
	if _, err := store.RecordUsage(context.Background(), UsageEntry{
		KeyID:     key.ID,
		Provider:  "codex",
		Model:     "gpt-test",
		Detail:    UsageDetail{InputTokens: 10_000},
		CreatedAt: now.Add(-24 * time.Hour),
	}); err != nil {
		t.Fatalf("RecordUsage(recent) error = %v", err)
	}
	_, err = store.Authenticate(context.Background(), plain, "gpt-test", now)
	if !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("Authenticate() error = %v, want ErrQuotaExceeded", err)
	}
}

func TestUpdateKeyMissingReturnsErrKeyNotFound(t *testing.T) {
	store := newTestStore(t)
	name := "new-name"
	_, err := store.UpdateKey(context.Background(), "key_missing", KeyPatch{Name: &name})
	if !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("UpdateKey() error = %v, want ErrKeyNotFound", err)
	}
}

func TestUpdateGroupPatchPreservesUnspecifiedDescription(t *testing.T) {
	store := newTestStore(t)
	group, err := store.CreateGroup(context.Background(), "g-team", "Team", "keep me", nil)
	if err != nil {
		t.Fatalf("CreateGroup() error = %v", err)
	}
	name := "Renamed"
	updated, err := store.UpdateGroup(context.Background(), group.ID, GroupPatch{Name: &name})
	if err != nil {
		t.Fatalf("UpdateGroup() error = %v", err)
	}
	if updated.Name != "Renamed" {
		t.Fatalf("UpdateGroup() name = %q, want Renamed", updated.Name)
	}
	if updated.Description != "keep me" {
		t.Fatalf("UpdateGroup() description = %q, want keep me", updated.Description)
	}
	description := ""
	updated, err = store.UpdateGroup(context.Background(), group.ID, GroupPatch{Description: &description})
	if err != nil {
		t.Fatalf("UpdateGroup(clear description) error = %v", err)
	}
	if updated.Description != "" {
		t.Fatalf("UpdateGroup() description = %q, want empty", updated.Description)
	}
}

func TestCalculateUSD_UsesCacheReadPriceForCachedInput(t *testing.T) {
	got := CalculateUSD(PriceRule{
		InputUSDPerMillion:     2,
		OutputUSDPerMillion:    10,
		CacheReadUSDPerMillion: 0.5,
	}, UsageDetail{
		InputTokens:     1_000_000,
		OutputTokens:    100_000,
		CacheReadTokens: 400_000,
	})
	want := 2.4
	if math.Abs(got-want) > 0.0000001 {
		t.Fatalf("CalculateUSD() = %v, want %v", got, want)
	}
}
