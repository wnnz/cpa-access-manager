package access

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"path/filepath"
	"testing"
	"time"
)

func TestKeyPlainTextPersistsAndRotates(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	created, plain, err := store.CreateKey(ctx, "copyable", true, Limits{}, nil)
	if err != nil {
		t.Fatalf("CreateKey() error = %v", err)
	}
	if created.PlainKey != plain || plain == "" {
		t.Fatalf("created PlainKey = %q, want generated plain key", created.PlainKey)
	}

	loaded, err := store.GetKey(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetKey() error = %v", err)
	}
	if loaded.PlainKey != plain {
		t.Fatalf("loaded PlainKey = %q, want %q", loaded.PlainKey, plain)
	}

	rotated, rotatedPlain, err := store.RotateKey(ctx, created.ID)
	if err != nil {
		t.Fatalf("RotateKey() error = %v", err)
	}
	if rotatedPlain == "" || rotatedPlain == plain || rotated.PlainKey != rotatedPlain {
		t.Fatalf("rotated PlainKey = %q, generated = %q, previous = %q", rotated.PlainKey, rotatedPlain, plain)
	}
}

func TestCreateKeyWithPlainAuthenticatesWithoutBindingsAndRejectsDuplicate(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	const custom = "customer-defined-api-key"

	key, plain, err := store.CreateKeyWithPlain(ctx, "custom", true, Limits{}, nil, custom)
	if err != nil {
		t.Fatalf("CreateKeyWithPlain() error = %v", err)
	}
	if plain != custom || key.PlainKey != custom {
		t.Fatalf("created key plain values = %q, %q, want %q", plain, key.PlainKey, custom)
	}
	if len(key.Bindings) != 0 {
		t.Fatalf("created key bindings = %#v, want none", key.Bindings)
	}
	if authenticated, err := store.Authenticate(ctx, custom, "gpt-test", time.Now()); err != nil {
		t.Fatalf("Authenticate() unbound custom key error = %v", err)
	} else if authenticated.ID != key.ID {
		t.Fatalf("Authenticate() key ID = %q, want %q", authenticated.ID, key.ID)
	}

	if _, _, err := store.CreateKeyWithPlain(ctx, "duplicate", true, Limits{}, nil, custom); !errors.Is(err, ErrDuplicateKey) {
		t.Fatalf("CreateKeyWithPlain() duplicate error = %v, want ErrDuplicateKey", err)
	}
}

func TestOpenStoreConfiguresEverySQLiteConnection(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	connections := make([]*sql.Conn, 0, 2)
	for i := 0; i < 2; i++ {
		conn, err := store.db.Conn(ctx)
		if err != nil {
			t.Fatalf("Conn(%d) error = %v", i, err)
		}
		connections = append(connections, conn)
		defer conn.Close()

		var foreignKeys, busyTimeout int
		var journalMode string
		if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
			t.Fatalf("foreign_keys connection %d: %v", i, err)
		}
		if err := conn.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
			t.Fatalf("busy_timeout connection %d: %v", i, err)
		}
		if err := conn.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journalMode); err != nil {
			t.Fatalf("journal_mode connection %d: %v", i, err)
		}
		if foreignKeys != 1 || busyTimeout != 5000 || journalMode != "wal" {
			t.Fatalf("connection %d pragmas = foreign_keys:%d busy_timeout:%d journal_mode:%q", i, foreignKeys, busyTimeout, journalMode)
		}
	}
}

func TestOpenStoreAddsPlainKeyColumnToExistingDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	_, err = db.ExecContext(ctx, `CREATE TABLE keys (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL DEFAULT '',
		key_hash TEXT NOT NULL UNIQUE,
		key_prefix TEXT NOT NULL DEFAULT '',
		enabled INTEGER NOT NULL DEFAULT 1,
		five_hour_limit_usd REAL NULL,
		weekly_limit_usd REAL NULL,
		total_limit_usd REAL NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		last_used_at TEXT NULL
	)`)
	if err != nil {
		_ = db.Close()
		t.Fatalf("create legacy keys table error = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("legacy db Close() error = %v", err)
	}

	store, err := OpenStore(ctx, path, false)
	if err != nil {
		t.Fatalf("OpenStore() migration error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	key, plain, err := store.CreateKey(ctx, "after-migration", true, Limits{}, nil)
	if err != nil {
		t.Fatalf("CreateKey() after migration error = %v", err)
	}
	if key.PlainKey != plain || plain == "" {
		t.Fatalf("PlainKey after migration = %q, want generated key", key.PlainKey)
	}
}

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

func TestUpsertProviderCandidatesUpdatesAuthFileWithoutDuplicatingIt(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if err := store.UpsertAuthFiles(ctx, []HostAuthFileEntry{{
		ID:       "auth-file-a",
		Provider: "codex",
		Name:     "auth-file-a.json",
		Label:    "account@example.com",
		Email:    "account@example.com",
	}}); err != nil {
		t.Fatalf("UpsertAuthFiles() error = %v", err)
	}
	if err := store.UpsertProviderCandidates(ctx, []Candidate{
		{ID: "auth-file-a", Provider: "codex", Priority: 7, Status: "ready", Attributes: map[string]string{"source": "file"}},
		{ID: "configured-provider", Provider: "codex", Priority: 7, Status: "ready", Attributes: map[string]string{"base_url": "https://example.com"}},
	}); err != nil {
		t.Fatalf("UpsertProviderCandidates() error = %v", err)
	}

	items, err := store.ListInventory(ctx)
	if err != nil {
		t.Fatalf("ListInventory() error = %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("inventory count = %d, want 2: %#v", len(items), items)
	}
	var authFile InventoryItem
	var providerInstances int
	for _, item := range items {
		switch item.Type {
		case InventoryAuthFile:
			authFile = item
		case InventoryProviderInstance:
			providerInstances++
			if item.AuthID == "auth-file-a" {
				t.Fatalf("auth file was duplicated as provider_instance: %#v", item)
			}
		}
	}
	if providerInstances != 1 {
		t.Fatalf("provider_instance count = %d, want 1", providerInstances)
	}
	if authFile.Name != "auth-file-a.json" || authFile.Label != "account@example.com" {
		t.Fatalf("auth file display fields were not preserved: %#v", authFile)
	}
	if got := authFile.Snapshot["priority"]; got != float64(7) && got != 7 {
		t.Fatalf("auth file priority snapshot = %#v, want 7", got)
	}
	if got := authFile.Snapshot["status"]; got != "ready" {
		t.Fatalf("auth file status snapshot = %#v, want ready", got)
	}
}

func TestSyncAuthFilesRemovesDeletedFilesAndFilenameProviderDuplicates(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	currentProviderID := ProviderInstanceID("codex", "codex-current.json", "", "")
	key, _, err := store.CreateKey(ctx, "oauth", true, Limits{}, []Binding{{TargetType: BindingProviderInstance, TargetID: currentProviderID}})
	if err != nil {
		t.Fatalf("CreateKey() error = %v", err)
	}
	if err := store.UpsertAuthFiles(ctx, []HostAuthFileEntry{
		{ID: "expired@example.com", Provider: "codex", Name: "codex-expired.json", Email: "expired@example.com"},
	}); err != nil {
		t.Fatalf("UpsertAuthFiles(expired) error = %v", err)
	}
	if err := store.UpsertInventoryItem(ctx, InventoryItem{
		ID: ProviderInstanceID("codex", "codex-expired.json", "", ""), Type: InventoryProviderInstance,
		Provider: "codex", AuthID: "codex-expired.json", Name: "codex-expired.json",
	}); err != nil {
		t.Fatalf("UpsertInventoryItem(expired duplicate) error = %v", err)
	}
	if err := store.UpsertInventoryItem(ctx, InventoryItem{
		ID: currentProviderID, Type: InventoryProviderInstance, Provider: "codex",
		AuthID: "codex-current.json", Name: "codex-current.json",
	}); err != nil {
		t.Fatalf("UpsertInventoryItem(current duplicate) error = %v", err)
	}

	if err := store.SyncAuthFiles(ctx, []HostAuthFileEntry{
		{ID: "current@example.com", Provider: "codex", Name: "codex-current.json", Email: "current@example.com"},
	}); err != nil {
		t.Fatalf("SyncAuthFiles() error = %v", err)
	}
	if err := store.UpsertProviderCandidates(ctx, []Candidate{{ID: "codex-current.json", Provider: "codex", Status: "ready"}}); err != nil {
		t.Fatalf("UpsertProviderCandidates(current filename) error = %v", err)
	}

	items, err := store.ListInventory(ctx)
	if err != nil {
		t.Fatalf("ListInventory() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("inventory = %#v, want only current auth file", items)
	}
	if items[0].Type != InventoryAuthFile || items[0].ID != "current@example.com" || items[0].Name != "codex-current.json" {
		t.Fatalf("current inventory item = %#v", items[0])
	}
	if got := items[0].Snapshot["status"]; got != "ready" {
		t.Fatalf("current auth-file status = %#v, want ready", got)
	}
	bindings, err := store.KeyBindings(ctx, key.ID)
	if err != nil {
		t.Fatalf("KeyBindings() error = %v", err)
	}
	if len(bindings) != 1 || bindings[0].TargetType != BindingAuthID || bindings[0].TargetID != "current@example.com" {
		t.Fatalf("migrated bindings = %#v", bindings)
	}
}

func TestUpsertProviderCandidatesPreservesConfiguredProviderDisplayFields(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	authID := "codex:apikey:f019226f368a"
	itemID := ProviderInstanceID("codex", authID, "", "https://sub2api.515290.xyz")
	if err := store.UpsertInventoryItem(ctx, InventoryItem{
		ID:       itemID,
		Type:     InventoryProviderInstance,
		Provider: "codex",
		AuthID:   authID,
		Name:     "Codex API Key - https://sub2api.515290.xyz",
		Label:    "Codex API Key - https://sub2api.515290.xyz",
		BaseURL:  "https://sub2api.515290.xyz",
		Snapshot: map[string]any{"source": "config:codex-api-key"},
	}); err != nil {
		t.Fatalf("UpsertInventoryItem() error = %v", err)
	}

	if err := store.UpsertProviderCandidates(ctx, []Candidate{{
		ID:       authID,
		Provider: "codex",
		Priority: 3,
		Status:   "active",
		Attributes: map[string]string{
			"base_url": "https://sub2api.515290.xyz",
		},
	}}); err != nil {
		t.Fatalf("UpsertProviderCandidates() error = %v", err)
	}

	item, ok := store.inventoryByType(ctx, InventoryProviderInstance, itemID)
	if !ok {
		t.Fatal("configured provider inventory item is missing")
	}
	if item.Name != "Codex API Key - https://sub2api.515290.xyz" || item.Label != item.Name {
		t.Fatalf("display fields = name:%q label:%q", item.Name, item.Label)
	}
	if item.BaseURL != "https://sub2api.515290.xyz" {
		t.Fatalf("BaseURL = %q", item.BaseURL)
	}
	if item.Snapshot["source"] != "config:codex-api-key" || item.Snapshot["status"] != "active" {
		t.Fatalf("Snapshot = %#v", item.Snapshot)
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

func TestRecordUsageStoresRequestMetadata(t *testing.T) {
	store := newTestStore(t)
	key, _, err := store.CreateKey(context.Background(), "team", true, Limits{}, []Binding{{TargetType: BindingAuthID, TargetID: "auth-a"}})
	if err != nil {
		t.Fatalf("CreateKey() error = %v", err)
	}
	_, err = store.RecordUsage(context.Background(), UsageEntry{
		KeyID:               key.ID,
		RequestID:           "req_store_1",
		RequestResource:     "openai-prod-2025.json",
		AuthID:              "auth-a",
		Provider:            "codex",
		Model:               "unpriced-model",
		FirstTokenLatencyMS: 730,
		TotalLatencyMS:      4100,
		Detail:              UsageDetail{InputTokens: 12, CacheReadTokens: 3, OutputTokens: 34, ReasoningEffort: "higth", TotalTokens: 46},
	})
	if err != nil {
		t.Fatalf("RecordUsage() error = %v", err)
	}
	entries, err := store.ListUsage(context.Background(), key.ID, 10)
	if err != nil {
		t.Fatalf("ListUsage() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("ListUsage() len = %d, want 1", len(entries))
	}
	got := entries[0]
	if got.RequestID != "req_store_1" || got.RequestResource != "openai-prod-2025.json" || got.FirstTokenLatencyMS != 730 || got.TotalLatencyMS != 4100 {
		t.Fatalf("usage metadata = %#v", got)
	}
	if got.Detail.ReasoningEffort != "high" {
		t.Fatalf("reasoning effort = %q, want high", got.Detail.ReasoningEffort)
	}
}

func TestListUsageFilteredByRequestResource(t *testing.T) {
	store := newTestStore(t)
	key, _, err := store.CreateKey(context.Background(), "team", true, Limits{}, []Binding{{TargetType: BindingAuthID, TargetID: "auth-a"}})
	if err != nil {
		t.Fatalf("CreateKey() error = %v", err)
	}
	for _, resource := range []string{"OpenAI Primary", "Claude_100%"} {
		if _, err := store.RecordUsage(context.Background(), UsageEntry{
			KeyID:           key.ID,
			RequestResource: resource,
			Provider:        "codex",
			Model:           "unpriced-model",
		}); err != nil {
			t.Fatalf("RecordUsage(%q) error = %v", resource, err)
		}
	}

	entries, err := store.ListUsageFiltered(context.Background(), UsageFilter{
		KeyID:           key.ID,
		RequestResource: "openai pri",
	}, 10)
	if err != nil {
		t.Fatalf("ListUsageFiltered() error = %v", err)
	}
	if len(entries) != 1 || entries[0].RequestResource != "OpenAI Primary" {
		t.Fatalf("ListUsageFiltered() = %#v, want OpenAI Primary", entries)
	}

	entries, err = store.ListUsageFiltered(context.Background(), UsageFilter{RequestResource: "_100%"}, 10)
	if err != nil {
		t.Fatalf("ListUsageFiltered(literal wildcard) error = %v", err)
	}
	if len(entries) != 1 || entries[0].RequestResource != "Claude_100%" {
		t.Fatalf("ListUsageFiltered(literal wildcard) = %#v, want Claude_100%%", entries)
	}
}

func TestUsageTotalsByKeyAggregatesAllRowsInTimeRange(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	key, _, err := store.CreateKey(ctx, "team", true, Limits{}, []Binding{{TargetType: BindingAuthID, TargetID: "auth-a"}})
	if err != nil {
		t.Fatalf("CreateKey() error = %v", err)
	}
	if err := store.UpsertPriceRules(ctx, []PriceRule{{Provider: "codex", Model: "gpt-test", InputUSDPerMillion: 1}}); err != nil {
		t.Fatalf("UpsertPriceRules() error = %v", err)
	}
	localZone := time.FixedZone("UTC+8", 8*60*60)
	start := time.Date(2026, 7, 22, 0, 0, 0, 0, localZone)
	for i := 0; i < 205; i++ {
		if _, err := store.RecordUsage(ctx, UsageEntry{
			KeyID: key.ID, Provider: "codex", Model: "gpt-test", CreatedAt: start.Add(time.Hour),
			Detail: UsageDetail{InputTokens: 10, TotalTokens: 10},
		}); err != nil {
			t.Fatalf("RecordUsage(%d) error = %v", i, err)
		}
	}
	if _, err := store.RecordUsage(ctx, UsageEntry{
		KeyID: key.ID, Provider: "codex", Model: "gpt-test", CreatedAt: start.Add(-time.Second),
		Detail: UsageDetail{InputTokens: 999, TotalTokens: 999},
	}); err != nil {
		t.Fatalf("RecordUsage(outside range) error = %v", err)
	}

	totals, err := store.UsageTotalsByKey(ctx, start, start.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("UsageTotalsByKey() error = %v", err)
	}
	if len(totals) != 1 || totals[0].KeyID != key.ID || totals[0].Requests != 205 || totals[0].Tokens != 2050 {
		t.Fatalf("UsageTotalsByKey() = %#v", totals)
	}
	if diff := totals[0].CostUSD - 0.00205; diff < -1e-12 || diff > 1e-12 {
		t.Fatalf("CostUSD = %.12f, want 0.00205", totals[0].CostUSD)
	}
}

func TestUsageSummaryAggregatesBeyondDetailLimitWithFilters(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	key, _, err := store.CreateKey(ctx, "team", true, Limits{}, []Binding{{TargetType: BindingAuthID, TargetID: "auth-a"}})
	if err != nil {
		t.Fatalf("CreateKey() error = %v", err)
	}
	if err := store.UpsertPriceRules(ctx, []PriceRule{{
		Provider: "codex", Model: "gpt-test", InputUSDPerMillion: 1, OutputUSDPerMillion: 2, CacheReadUSDPerMillion: 0.5,
	}}); err != nil {
		t.Fatalf("UpsertPriceRules() error = %v", err)
	}
	start := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 505; i++ {
		if _, err := store.RecordUsage(ctx, UsageEntry{
			KeyID: key.ID, RequestResource: "OpenAI Primary", Provider: "codex", Model: "gpt-test", CreatedAt: start.Add(time.Hour),
			FirstTokenLatencyMS: 100, TotalLatencyMS: 400,
			Detail: UsageDetail{InputTokens: 100, CacheReadTokens: 20, OutputTokens: 30},
		}); err != nil {
			t.Fatalf("RecordUsage(%d) error = %v", i, err)
		}
	}
	for _, entry := range []UsageEntry{
		{KeyID: key.ID, RequestResource: "Claude Backup", Provider: "codex", Model: "gpt-test", CreatedAt: start.Add(time.Hour), Detail: UsageDetail{InputTokens: 999}},
		{KeyID: key.ID, RequestResource: "OpenAI Primary", Provider: "codex", Model: "gpt-test", CreatedAt: start.Add(-time.Second), Detail: UsageDetail{InputTokens: 999}},
	} {
		if _, err := store.RecordUsage(ctx, entry); err != nil {
			t.Fatalf("RecordUsage(excluded) error = %v", err)
		}
	}

	filter := UsageFilter{
		RequestResource: "openai pri",
		Since:           start,
		Before:          start.Add(24 * time.Hour),
	}
	summary, err := store.UsageSummary(ctx, filter)
	if err != nil {
		t.Fatalf("UsageSummary() error = %v", err)
	}
	if summary.Requests != 505 || summary.InputTokens != 40_400 || summary.CacheReadTokens != 10_100 || summary.OutputTokens != 15_150 || summary.TotalTokens != 65_650 {
		t.Fatalf("UsageSummary() = %#v", summary)
	}
	if diff := summary.CostUSD - 0.07575; diff < -1e-12 || diff > 1e-12 {
		t.Fatalf("CostUSD = %.12f, want 0.07575", summary.CostUSD)
	}
	if summary.FirstTokenLatencyMS != 100 || summary.TotalLatencyMS != 400 {
		t.Fatalf("latencies = %.2f/%.2f, want 100/400", summary.FirstTokenLatencyMS, summary.TotalLatencyMS)
	}
	details, err := store.ListUsageFiltered(ctx, filter, 500)
	if err != nil {
		t.Fatalf("ListUsageFiltered() error = %v", err)
	}
	if len(details) != 500 {
		t.Fatalf("detail rows = %d, want capped 500", len(details))
	}
}

func TestListUsageFilteredByInventoryResourceID(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	key, _, err := store.CreateKey(ctx, "team", true, Limits{}, []Binding{{TargetType: BindingAuthID, TargetID: "auth-a"}})
	if err != nil {
		t.Fatalf("CreateKey() error = %v", err)
	}
	providerID := ProviderInstanceID("codex", "provider-auth", "", "https://example.com")
	if err := store.UpsertInventoryItem(ctx, InventoryItem{
		ID:       providerID,
		Type:     InventoryProviderInstance,
		Provider: "codex",
		AuthID:   "provider-auth",
		Name:     "Codex API Key - https://example.com",
	}); err != nil {
		t.Fatalf("UpsertInventoryItem() error = %v", err)
	}
	for _, authID := range []string{"provider-auth", "other-auth"} {
		if _, err := store.RecordUsage(ctx, UsageEntry{
			KeyID:    key.ID,
			AuthID:   authID,
			Provider: "codex",
			Model:    "unpriced-model",
		}); err != nil {
			t.Fatalf("RecordUsage(%q) error = %v", authID, err)
		}
	}

	entries, err := store.ListUsageFiltered(ctx, UsageFilter{ResourceID: providerID}, 10)
	if err != nil {
		t.Fatalf("ListUsageFiltered() error = %v", err)
	}
	if len(entries) != 1 || entries[0].AuthID != "provider-auth" {
		t.Fatalf("ListUsageFiltered() = %#v, want provider-auth", entries)
	}
}

func TestRecordUsageInfersCacheReadTokensFromCachedTokens(t *testing.T) {
	store := newTestStore(t)
	key, _, err := store.CreateKey(context.Background(), "team", true, Limits{}, []Binding{{TargetType: BindingAuthID, TargetID: "auth-a"}})
	if err != nil {
		t.Fatalf("CreateKey() error = %v", err)
	}
	if err := store.UpsertPriceRules(context.Background(), []PriceRule{{
		Provider:               "codex",
		Model:                  "gpt-test",
		InputUSDPerMillion:     2,
		OutputUSDPerMillion:    10,
		CacheReadUSDPerMillion: 0.5,
	}}); err != nil {
		t.Fatalf("UpsertPriceRules() error = %v", err)
	}

	entry, err := store.RecordUsage(context.Background(), UsageEntry{
		KeyID:    key.ID,
		Provider: "codex",
		Model:    "gpt-test",
		Detail:   UsageDetail{InputTokens: 1_000_000, OutputTokens: 100_000, CachedTokens: 400_000},
	})
	if err != nil {
		t.Fatalf("RecordUsage() error = %v", err)
	}
	if entry.Detail.CacheReadTokens != 400_000 {
		t.Fatalf("recorded cache read tokens = %d, want 400000", entry.Detail.CacheReadTokens)
	}
	if math.Abs(entry.USD-2.4) > 0.0000001 {
		t.Fatalf("RecordUsage() USD = %v, want 2.4", entry.USD)
	}
	entries, err := store.ListUsage(context.Background(), key.ID, 10)
	if err != nil {
		t.Fatalf("ListUsage() error = %v", err)
	}
	if len(entries) != 1 || entries[0].Detail.CacheReadTokens != 400_000 {
		t.Fatalf("ledger entry = %#v, want inferred cache read tokens", entries)
	}
}

func TestReplacePriceRulesRemovesExistingRules(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if err := store.UpsertPriceRules(ctx, []PriceRule{{
		Provider: "azure",
		Model:    "gpt-5.5",
	}}); err != nil {
		t.Fatalf("UpsertPriceRules() error = %v", err)
	}
	if err := store.ReplacePriceRules(ctx, []PriceRule{{
		Provider:            "openai",
		Model:               "gpt-5.5",
		InputUSDPerMillion:  5,
		OutputUSDPerMillion: 30,
	}}); err != nil {
		t.Fatalf("ReplacePriceRules() error = %v", err)
	}

	if _, err := store.PriceRule(ctx, "azure", "gpt-5.5"); !errors.Is(err, ErrMissingPriceRule) {
		t.Fatalf("azure PriceRule() error = %v, want ErrMissingPriceRule", err)
	}
	if _, err := store.PriceRule(ctx, "openai", "gpt-5.5"); err != nil {
		t.Fatalf("openai PriceRule() error = %v", err)
	}
	if rule, err := store.PriceRule(ctx, "codex", "gpt-5.5"); err != nil {
		t.Fatalf("codex PriceRule() fallback error = %v", err)
	} else if rule.Provider != "openai" {
		t.Fatalf("codex PriceRule() provider = %q, want openai fallback", rule.Provider)
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

func TestCalculateUSD_UsesCachedTokensAsCacheReadFallback(t *testing.T) {
	got := CalculateUSD(PriceRule{
		InputUSDPerMillion:     2,
		OutputUSDPerMillion:    10,
		CacheReadUSDPerMillion: 0.5,
	}, UsageDetail{
		InputTokens:  1_000_000,
		OutputTokens: 100_000,
		CachedTokens: 400_000,
	})
	want := 2.4
	if math.Abs(got-want) > 0.0000001 {
		t.Fatalf("CalculateUSD() = %v, want %v", got, want)
	}
}
