package access

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db            *sql.DB
	allowUnpriced bool
}

func OpenStore(ctx context.Context, dbPath string, allowUnpriced bool) (*Store, error) {
	dbPath = strings.TrimSpace(dbPath)
	if dbPath == "" {
		dbPath = "/opt/cli-proxy-api/plugins/cpa-toolkit.db"
	}
	if err := ensureDBDir(dbPath); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	store := &Store{db: db, allowUnpriced: allowUnpriced}
	if err := store.init(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func ensureDBDir(path string) error {
	if path == ":memory:" || strings.HasPrefix(path, "file:") {
		return nil
	}
	dir := filepath.Dir(path)
	if dir == "." || dir == "" {
		return nil
	}
	return os.MkdirAll(dir, 0o755)
}

func (s *Store) init(ctx context.Context) error {
	stmts := []string{
		`PRAGMA foreign_keys = ON`,
		`PRAGMA busy_timeout = 5000`,
		`CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS keys (
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
		)`,
		`CREATE TABLE IF NOT EXISTS groups (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS group_members (
			group_id TEXT NOT NULL,
			member_type TEXT NOT NULL,
			member_id TEXT NOT NULL,
			PRIMARY KEY (group_id, member_type, member_id),
			FOREIGN KEY (group_id) REFERENCES groups(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS key_bindings (
			key_id TEXT NOT NULL,
			target_type TEXT NOT NULL,
			target_id TEXT NOT NULL,
			PRIMARY KEY (key_id, target_type, target_id),
			FOREIGN KEY (key_id) REFERENCES keys(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS inventory_items (
			id TEXT NOT NULL,
			type TEXT NOT NULL,
			provider TEXT NOT NULL DEFAULT '',
			auth_id TEXT NOT NULL DEFAULT '',
			auth_index TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL DEFAULT '',
			label TEXT NOT NULL DEFAULT '',
			email TEXT NOT NULL DEFAULT '',
			base_url TEXT NOT NULL DEFAULT '',
			snapshot_json TEXT NOT NULL DEFAULT '{}',
			updated_at TEXT NOT NULL,
			PRIMARY KEY (id, type)
		)`,
		`CREATE TABLE IF NOT EXISTS price_rules (
			provider TEXT NOT NULL,
			model TEXT NOT NULL,
			input_usd_per_million REAL NOT NULL DEFAULT 0,
			output_usd_per_million REAL NOT NULL DEFAULT 0,
			cache_read_usd_per_million REAL NOT NULL DEFAULT 0,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (provider, model)
		)`,
		`CREATE TABLE IF NOT EXISTS usage_ledger (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			key_id TEXT NOT NULL,
			request_id TEXT NOT NULL DEFAULT '',
			request_resource TEXT NOT NULL DEFAULT '',
			auth_id TEXT NOT NULL DEFAULT '',
			auth_index TEXT NOT NULL DEFAULT '',
			provider TEXT NOT NULL DEFAULT '',
			provider_instance_id TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			alias TEXT NOT NULL DEFAULT '',
			input_tokens INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			reasoning_tokens INTEGER NOT NULL DEFAULT 0,
			cached_tokens INTEGER NOT NULL DEFAULT 0,
			cache_read_tokens INTEGER NOT NULL DEFAULT 0,
			cache_creation_tokens INTEGER NOT NULL DEFAULT 0,
			total_tokens INTEGER NOT NULL DEFAULT 0,
			first_token_latency_ms INTEGER NOT NULL DEFAULT 0,
			total_latency_ms INTEGER NOT NULL DEFAULT 0,
			usd REAL NOT NULL DEFAULT 0,
			failed INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL,
			FOREIGN KEY (key_id) REFERENCES keys(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_key_created ON usage_ledger(key_id, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_inventory_auth_id ON inventory_items(auth_id)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	if err := s.ensureUsageLedgerColumns(ctx); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES(1, ?)`, timeNowString())
	return err
}

func (s *Store) ensureUsageLedgerColumns(ctx context.Context) error {
	columns, err := s.tableColumns(ctx, "usage_ledger")
	if err != nil {
		return err
	}
	additions := []struct {
		name string
		sql  string
	}{
		{"request_id", "request_id TEXT NOT NULL DEFAULT ''"},
		{"request_resource", "request_resource TEXT NOT NULL DEFAULT ''"},
		{"first_token_latency_ms", "first_token_latency_ms INTEGER NOT NULL DEFAULT 0"},
		{"total_latency_ms", "total_latency_ms INTEGER NOT NULL DEFAULT 0"},
	}
	for _, addition := range additions {
		if columns[addition.name] {
			continue
		}
		if _, err := s.db.ExecContext(ctx, "ALTER TABLE usage_ledger ADD COLUMN "+addition.sql); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) tableColumns(ctx context.Context, table string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

func (s *Store) CreateKey(ctx context.Context, name string, enabled bool, limits Limits, bindings []Binding) (Key, string, error) {
	plain, hash, prefix, err := newPlainKey()
	if err != nil {
		return Key{}, "", err
	}
	id, err := newID("key")
	if err != nil {
		return Key{}, "", err
	}
	now := timeNowString()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Key{}, "", err
	}
	defer rollback(tx)
	if _, err := tx.ExecContext(ctx, `INSERT INTO keys(id, name, key_hash, key_prefix, enabled, five_hour_limit_usd, weekly_limit_usd, total_limit_usd, created_at, updated_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, strings.TrimSpace(name), hash, prefix, boolInt(enabled), nullFloat(limits.FiveHourUSD), nullFloat(limits.WeeklyUSD), nullFloat(limits.TotalUSD), now, now); err != nil {
		return Key{}, "", err
	}
	if err := replaceBindings(ctx, tx, id, bindings); err != nil {
		return Key{}, "", err
	}
	if err := tx.Commit(); err != nil {
		return Key{}, "", err
	}
	key, err := s.GetKey(ctx, id)
	return key, plain, err
}

func (s *Store) RotateKey(ctx context.Context, id string) (Key, string, error) {
	plain, hash, prefix, err := newPlainKey()
	if err != nil {
		return Key{}, "", err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE keys SET key_hash = ?, key_prefix = ?, updated_at = ? WHERE id = ?`, hash, prefix, timeNowString(), strings.TrimSpace(id))
	if err != nil {
		return Key{}, "", err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return Key{}, "", ErrKeyNotFound
	}
	key, err := s.GetKey(ctx, id)
	return key, plain, err
}

func (s *Store) UpdateKey(ctx context.Context, id string, patch KeyPatch) (Key, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Key{}, ErrKeyNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Key{}, err
	}
	defer rollback(tx)
	var existingID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM keys WHERE id = ?`, id).Scan(&existingID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Key{}, ErrKeyNotFound
		}
		return Key{}, err
	}
	now := timeNowString()
	if patch.Name != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE keys SET name = ?, updated_at = ? WHERE id = ?`, strings.TrimSpace(*patch.Name), now, id); err != nil {
			return Key{}, err
		}
	}
	if patch.Enabled != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE keys SET enabled = ?, updated_at = ? WHERE id = ?`, boolInt(*patch.Enabled), now, id); err != nil {
			return Key{}, err
		}
	}
	if patch.Limits != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE keys SET five_hour_limit_usd = ?, weekly_limit_usd = ?, total_limit_usd = ?, updated_at = ? WHERE id = ?`,
			nullFloat(patch.Limits.FiveHourUSD), nullFloat(patch.Limits.WeeklyUSD), nullFloat(patch.Limits.TotalUSD), now, id); err != nil {
			return Key{}, err
		}
	}
	if patch.Bindings != nil {
		if err := replaceBindings(ctx, tx, id, *patch.Bindings); err != nil {
			return Key{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Key{}, err
	}
	return s.GetKey(ctx, id)
}

func (s *Store) DeleteKey(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM keys WHERE id = ?`, strings.TrimSpace(id))
	if err != nil {
		return err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return ErrKeyNotFound
	}
	return nil
}

func (s *Store) GetKey(ctx context.Context, id string) (Key, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, name, key_prefix, enabled, five_hour_limit_usd, weekly_limit_usd, total_limit_usd, created_at, updated_at, last_used_at FROM keys WHERE id = ?`, strings.TrimSpace(id))
	key, err := scanKey(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Key{}, ErrKeyNotFound
		}
		return Key{}, err
	}
	key.Bindings, _ = s.KeyBindings(ctx, key.ID)
	key.Usage, _ = s.UsageSums(ctx, key.ID, time.Now())
	return key, nil
}

func (s *Store) ListKeys(ctx context.Context) ([]Key, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, key_prefix, enabled, five_hour_limit_usd, weekly_limit_usd, total_limit_usd, created_at, updated_at, last_used_at FROM keys ORDER BY created_at DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := make([]Key, 0)
	for rows.Next() {
		key, err := scanKey(rows)
		if err != nil {
			return nil, err
		}
		key.Bindings, _ = s.KeyBindings(ctx, key.ID)
		key.Usage, _ = s.UsageSums(ctx, key.ID, time.Now())
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func (s *Store) Authenticate(ctx context.Context, token string, requestedModel string, now time.Time) (Key, error) {
	key, err := s.KeyByPresentedToken(ctx, token)
	if err != nil {
		return Key{}, err
	}
	if !key.Enabled {
		return Key{}, ErrKeyDisabled
	}
	if err := s.CheckQuota(ctx, key, now); err != nil {
		return Key{}, err
	}
	if err := s.ValidateBindingsAndPricing(ctx, key, requestedModel); err != nil {
		return Key{}, err
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE keys SET last_used_at = ?, updated_at = ? WHERE id = ?`, formatTime(now), formatTime(now), key.ID)
	return key, nil
}

func (s *Store) KeyByPresentedToken(ctx context.Context, token string) (Key, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return Key{}, ErrKeyNotFound
	}
	hash := hashToken(token)
	row := s.db.QueryRowContext(ctx, `SELECT id, name, key_prefix, enabled, five_hour_limit_usd, weekly_limit_usd, total_limit_usd, created_at, updated_at, last_used_at FROM keys WHERE key_hash = ?`, hash)
	key, err := scanKey(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Key{}, ErrKeyNotFound
		}
		return Key{}, err
	}
	key.Bindings, _ = s.KeyBindings(ctx, key.ID)
	key.Usage, _ = s.UsageSums(ctx, key.ID, time.Now())
	return key, nil
}

func (s *Store) KeyByIDOrPresentedToken(ctx context.Context, value string) (Key, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return Key{}, ErrKeyNotFound
	}
	key, err := s.KeyByPresentedToken(ctx, value)
	if err == nil {
		return key, nil
	}
	if !errors.Is(err, ErrKeyNotFound) {
		return Key{}, err
	}
	return s.GetKey(ctx, value)
}

func (s *Store) ValidateBindingsAndPricing(ctx context.Context, key Key, requestedModel string) error {
	scope, err := s.ResolveScope(ctx, key.ID)
	if err != nil {
		return err
	}
	if !scope.HasBindings {
		return ErrNoBindings
	}
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" || !key.Limits.Enabled() || s.allowUnpriced {
		return nil
	}
	for provider := range scope.Providers {
		if _, err := s.PriceRule(ctx, provider, requestedModel); err == nil {
			return nil
		}
	}
	return ErrMissingPriceRule
}

func (s *Store) CheckQuota(ctx context.Context, key Key, now time.Time) error {
	sums, err := s.UsageSums(ctx, key.ID, now)
	if err != nil {
		return err
	}
	if limitReached(key.Limits.FiveHourUSD, sums.FiveHourUSD) {
		return fmt.Errorf("%w: 5h limit reached", ErrQuotaExceeded)
	}
	if limitReached(key.Limits.WeeklyUSD, sums.WeeklyUSD) {
		return fmt.Errorf("%w: weekly limit reached", ErrQuotaExceeded)
	}
	if limitReached(key.Limits.TotalUSD, sums.TotalUSD) {
		return fmt.Errorf("%w: total limit reached", ErrQuotaExceeded)
	}
	return nil
}

func (s *Store) UsageSums(ctx context.Context, keyID string, now time.Time) (UsageSums, error) {
	var out UsageSums
	if now.IsZero() {
		now = time.Now()
	}
	fiveStart := formatTime(now.Add(-5 * time.Hour))
	weekStart := formatTime(now.Add(-7 * 24 * time.Hour))
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(usd), 0) FROM usage_ledger WHERE key_id = ? AND created_at >= ?`, keyID, fiveStart).Scan(&out.FiveHourUSD); err != nil {
		return out, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(usd), 0) FROM usage_ledger WHERE key_id = ? AND created_at >= ?`, keyID, weekStart).Scan(&out.WeeklyUSD); err != nil {
		return out, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(usd), 0) FROM usage_ledger WHERE key_id = ?`, keyID).Scan(&out.TotalUSD); err != nil {
		return out, err
	}
	return out, nil
}

func (s *Store) KeyBindings(ctx context.Context, keyID string) ([]Binding, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key_id, target_type, target_id FROM key_bindings WHERE key_id = ? ORDER BY target_type, target_id`, keyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Binding, 0)
	for rows.Next() {
		var b Binding
		if err := rows.Scan(&b.KeyID, &b.TargetType, &b.TargetID); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) ResolveScope(ctx context.Context, keyID string) (Scope, error) {
	scope := Scope{
		AuthIDs:             map[string]struct{}{},
		ProviderInstanceIDs: map[string]struct{}{},
		Providers:           map[string]struct{}{},
	}
	bindings, err := s.KeyBindings(ctx, keyID)
	if err != nil {
		return scope, err
	}
	for _, binding := range bindings {
		scope.HasBindings = true
		if err := s.applyTarget(ctx, &scope, binding.TargetType, binding.TargetID); err != nil {
			return scope, err
		}
	}
	return scope, nil
}

func (s *Store) applyTarget(ctx context.Context, scope *Scope, targetType, targetID string) error {
	targetType = normalizeTargetType(targetType)
	targetID = strings.TrimSpace(targetID)
	if targetID == "" {
		return nil
	}
	switch targetType {
	case BindingGroup:
		members, err := s.GroupMembers(ctx, targetID)
		if err != nil {
			return err
		}
		for _, member := range members {
			if err := s.applyTarget(ctx, scope, member.MemberType, member.MemberID); err != nil {
				return err
			}
		}
	case BindingAuthID, InventoryAuthFile:
		scope.AuthIDs[targetID] = struct{}{}
		if item, ok := s.inventoryByType(ctx, InventoryAuthFile, targetID); ok {
			addProvider(scope, item.Provider)
		}
	case BindingProviderInstance:
		scope.ProviderInstanceIDs[targetID] = struct{}{}
		if item, ok := s.inventoryByType(ctx, InventoryProviderInstance, targetID); ok {
			if item.AuthID != "" {
				scope.AuthIDs[item.AuthID] = struct{}{}
			}
			addProvider(scope, item.Provider)
		}
	}
	return nil
}

func (s *Store) ChooseProvider(ctx context.Context, keyID, requestedModel string, available []string) (string, error) {
	key, err := s.GetKey(ctx, keyID)
	if err != nil {
		return "", err
	}
	scope, err := s.ResolveScope(ctx, keyID)
	if err != nil {
		return "", err
	}
	if !scope.HasBindings {
		return "", ErrNoBindings
	}
	allowed := make([]string, 0, len(scope.Providers))
	availableSet := stringSet(available)
	for provider := range scope.Providers {
		if provider == "" {
			continue
		}
		if len(availableSet) > 0 {
			if _, ok := availableSet[provider]; !ok {
				continue
			}
		}
		if key.Limits.Enabled() && !s.allowUnpriced && strings.TrimSpace(requestedModel) != "" {
			if _, err := s.PriceRule(ctx, provider, requestedModel); err != nil {
				continue
			}
		}
		allowed = append(allowed, provider)
	}
	sort.Strings(allowed)
	if len(allowed) == 0 {
		return "", ErrNoAllowedTarget
	}
	return allowed[0], nil
}

func (s *Store) PickCandidate(ctx context.Context, keyID string, candidates []Candidate) (Candidate, error) {
	scope, err := s.ResolveScope(ctx, keyID)
	if err != nil {
		return Candidate{}, err
	}
	if !scope.HasBindings {
		return Candidate{}, ErrNoBindings
	}
	_ = s.UpsertProviderCandidates(ctx, candidates)
	matched := make([]Candidate, 0)
	for _, candidate := range candidates {
		if scope.AllowsCandidate(candidate) {
			matched = append(matched, candidate)
		}
	}
	if len(matched) == 0 {
		return Candidate{}, ErrNoAllowedTarget
	}
	sort.SliceStable(matched, func(i, j int) bool {
		if matched[i].Priority != matched[j].Priority {
			return matched[i].Priority > matched[j].Priority
		}
		return matched[i].ID < matched[j].ID
	})
	return matched[0], nil
}

func (sc Scope) AllowsCandidate(candidate Candidate) bool {
	authID := strings.TrimSpace(candidate.ID)
	if authID != "" {
		if _, ok := sc.AuthIDs[authID]; ok {
			return true
		}
	}
	authIndex := firstAttr(candidate.Attributes, "auth_index", "index")
	provider := normalizeProvider(candidate.Provider)
	instanceID := ProviderInstanceID(provider, authID, authIndex, firstAttr(candidate.Attributes, "base_url", "url", "endpoint"))
	if _, ok := sc.ProviderInstanceIDs[instanceID]; ok {
		return true
	}
	return false
}

func (s *Store) UpsertAuthFiles(ctx context.Context, entries []HostAuthFileEntry) error {
	authIDs := make([]string, 0, len(entries))
	for _, entry := range entries {
		id := strings.TrimSpace(entry.ID)
		if id == "" {
			id = strings.TrimSpace(entry.AuthIndex)
		}
		if id == "" {
			id = strings.TrimSpace(entry.Name)
		}
		if id == "" {
			continue
		}
		provider := normalizeProvider(firstNonEmpty(entry.Provider, entry.Type))
		snapshot := map[string]any{}
		raw, _ := json.Marshal(entry)
		_ = json.Unmarshal(raw, &snapshot)
		item := InventoryItem{
			ID:        id,
			Type:      InventoryAuthFile,
			Provider:  provider,
			AuthID:    id,
			AuthIndex: strings.TrimSpace(entry.AuthIndex),
			Name:      strings.TrimSpace(entry.Name),
			Label:     strings.TrimSpace(entry.Label),
			Email:     strings.TrimSpace(entry.Email),
			Snapshot:  snapshot,
			UpdatedAt: time.Now().UTC(),
		}
		if err := s.UpsertInventoryItem(ctx, item); err != nil {
			return err
		}
		authIDs = append(authIDs, item.AuthID)
	}
	return s.deleteAuthFileProviderInstances(ctx, authIDs)
}

func (s *Store) deleteAuthFileProviderInstances(ctx context.Context, authIDs []string) error {
	authIDs = uniqueNonEmpty(authIDs)
	if len(authIDs) == 0 {
		return nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(authIDs)), ",")
	args := make([]any, 0, len(authIDs)+1)
	args = append(args, InventoryProviderInstance)
	for _, authID := range authIDs {
		args = append(args, authID)
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM inventory_items
		WHERE type = ?
		  AND COALESCE(base_url, '') = ''
		  AND COALESCE(auth_id, '') IN (`+placeholders+`)
		  AND id = provider || ':' || auth_id`, args...)
	return err
}

func (s *Store) UpsertProviderCandidates(ctx context.Context, candidates []Candidate) error {
	for _, candidate := range candidates {
		provider := normalizeProvider(candidate.Provider)
		authID := strings.TrimSpace(candidate.ID)
		if provider == "" || authID == "" {
			continue
		}
		authIndex := firstAttr(candidate.Attributes, "auth_index", "index")
		baseURL := firstAttr(candidate.Attributes, "base_url", "url", "endpoint")
		item := InventoryItem{
			ID:        ProviderInstanceID(provider, authID, authIndex, baseURL),
			Type:      InventoryProviderInstance,
			Provider:  provider,
			AuthID:    authID,
			AuthIndex: authIndex,
			BaseURL:   baseURL,
			Snapshot: map[string]any{
				"id":         candidate.ID,
				"provider":   candidate.Provider,
				"priority":   candidate.Priority,
				"status":     candidate.Status,
				"attributes": candidate.Attributes,
			},
			UpdatedAt: time.Now().UTC(),
		}
		if err := s.UpsertInventoryItem(ctx, item); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) UpsertInventoryItem(ctx context.Context, item InventoryItem) error {
	item.ID = strings.TrimSpace(item.ID)
	item.Type = normalizeTargetType(item.Type)
	item.Provider = normalizeProvider(item.Provider)
	if item.Type == "" || item.ID == "" {
		return nil
	}
	if item.UpdatedAt.IsZero() {
		item.UpdatedAt = time.Now().UTC()
	}
	snapshot := "{}"
	if item.Snapshot != nil {
		raw, _ := json.Marshal(item.Snapshot)
		if len(raw) > 0 {
			snapshot = string(raw)
		}
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO inventory_items(id, type, provider, auth_id, auth_index, name, label, email, base_url, snapshot_json, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id, type) DO UPDATE SET provider = excluded.provider, auth_id = excluded.auth_id, auth_index = excluded.auth_index, name = excluded.name, label = excluded.label, email = excluded.email, base_url = excluded.base_url, snapshot_json = excluded.snapshot_json, updated_at = excluded.updated_at`,
		item.ID, item.Type, item.Provider, strings.TrimSpace(item.AuthID), strings.TrimSpace(item.AuthIndex), strings.TrimSpace(item.Name), strings.TrimSpace(item.Label), strings.TrimSpace(item.Email), strings.TrimSpace(item.BaseURL), snapshot, formatTime(item.UpdatedAt))
	return err
}

func (s *Store) ListInventory(ctx context.Context) ([]InventoryItem, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, type, provider, auth_id, auth_index, name, label, email, base_url, snapshot_json, updated_at FROM inventory_items ORDER BY type, provider, name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]InventoryItem, 0)
	for rows.Next() {
		item, err := scanInventory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) inventoryByType(ctx context.Context, typ, id string) (InventoryItem, bool) {
	row := s.db.QueryRowContext(ctx, `SELECT id, type, provider, auth_id, auth_index, name, label, email, base_url, snapshot_json, updated_at FROM inventory_items WHERE type = ? AND id = ?`, normalizeTargetType(typ), strings.TrimSpace(id))
	item, err := scanInventory(row)
	return item, err == nil
}

func (s *Store) UpsertPriceRules(ctx context.Context, rules []PriceRule) error {
	now := timeNowString()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	for _, rule := range rules {
		provider := normalizeProvider(rule.Provider)
		model := strings.TrimSpace(rule.Model)
		if provider == "" || model == "" {
			continue
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO price_rules(provider, model, input_usd_per_million, output_usd_per_million, cache_read_usd_per_million, updated_at)
			VALUES(?, ?, ?, ?, ?, ?)
			ON CONFLICT(provider, model) DO UPDATE SET input_usd_per_million = excluded.input_usd_per_million, output_usd_per_million = excluded.output_usd_per_million, cache_read_usd_per_million = excluded.cache_read_usd_per_million, updated_at = excluded.updated_at`,
			provider, model, rule.InputUSDPerMillion, rule.OutputUSDPerMillion, rule.CacheReadUSDPerMillion, now)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ListPriceRules(ctx context.Context) ([]PriceRule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT provider, model, input_usd_per_million, output_usd_per_million, cache_read_usd_per_million, updated_at FROM price_rules ORDER BY provider, model`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]PriceRule, 0)
	for rows.Next() {
		var rule PriceRule
		var updated string
		if err := rows.Scan(&rule.Provider, &rule.Model, &rule.InputUSDPerMillion, &rule.OutputUSDPerMillion, &rule.CacheReadUSDPerMillion, &updated); err != nil {
			return nil, err
		}
		rule.UpdatedAt = parseTime(updated)
		out = append(out, rule)
	}
	return out, rows.Err()
}

func (s *Store) PriceRule(ctx context.Context, provider, model string) (PriceRule, error) {
	row := s.db.QueryRowContext(ctx, `SELECT provider, model, input_usd_per_million, output_usd_per_million, cache_read_usd_per_million, updated_at FROM price_rules WHERE provider = ? AND model = ?`, normalizeProvider(provider), strings.TrimSpace(model))
	var rule PriceRule
	var updated string
	if err := row.Scan(&rule.Provider, &rule.Model, &rule.InputUSDPerMillion, &rule.OutputUSDPerMillion, &rule.CacheReadUSDPerMillion, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PriceRule{}, ErrMissingPriceRule
		}
		return PriceRule{}, err
	}
	rule.UpdatedAt = parseTime(updated)
	return rule, nil
}

func (s *Store) RecordUsage(ctx context.Context, entry UsageEntry) (UsageEntry, error) {
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now().UTC()
	}
	entry.Provider = normalizeProvider(entry.Provider)
	if entry.Provider == "" && entry.AuthID != "" {
		if item, ok := s.inventoryByType(ctx, InventoryAuthFile, entry.AuthID); ok {
			entry.Provider = item.Provider
		}
	}
	if entry.ProviderInstanceID == "" && entry.Provider != "" && entry.AuthID != "" {
		entry.ProviderInstanceID = ProviderInstanceID(entry.Provider, entry.AuthID, entry.AuthIndex, "")
	}
	rule, err := s.PriceRule(ctx, entry.Provider, entry.Model)
	if err != nil {
		if !errors.Is(err, ErrMissingPriceRule) {
			return UsageEntry{}, err
		}
	} else {
		entry.USD = CalculateUSD(rule, entry.Detail)
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO usage_ledger(key_id, request_id, request_resource, auth_id, auth_index, provider, provider_instance_id, model, alias, input_tokens, output_tokens, reasoning_tokens, cached_tokens, cache_read_tokens, cache_creation_tokens, total_tokens, first_token_latency_ms, total_latency_ms, usd, failed, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.KeyID, strings.TrimSpace(entry.RequestID), strings.TrimSpace(entry.RequestResource), entry.AuthID, entry.AuthIndex, entry.Provider, entry.ProviderInstanceID, entry.Model, entry.Alias, entry.Detail.InputTokens, entry.Detail.OutputTokens, entry.Detail.ReasoningTokens, entry.Detail.CachedTokens, entry.Detail.CacheReadTokens, entry.Detail.CacheCreationTokens, entry.Detail.TotalTokens, entry.FirstTokenLatencyMS, entry.TotalLatencyMS, entry.USD, boolInt(entry.Failed), formatTime(entry.CreatedAt))
	if err != nil {
		return UsageEntry{}, err
	}
	entry.ID, _ = res.LastInsertId()
	return entry, nil
}

func (s *Store) ListUsage(ctx context.Context, keyID string, limit int) ([]UsageEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	args := []any{}
	query := `SELECT id, key_id, request_id, request_resource, auth_id, auth_index, provider, provider_instance_id, model, alias, input_tokens, output_tokens, reasoning_tokens, cached_tokens, cache_read_tokens, cache_creation_tokens, total_tokens, first_token_latency_ms, total_latency_ms, usd, failed, created_at FROM usage_ledger`
	if strings.TrimSpace(keyID) != "" {
		query += ` WHERE key_id = ?`
		args = append(args, strings.TrimSpace(keyID))
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]UsageEntry, 0)
	for rows.Next() {
		var e UsageEntry
		var created string
		var failed int
		if err := rows.Scan(&e.ID, &e.KeyID, &e.RequestID, &e.RequestResource, &e.AuthID, &e.AuthIndex, &e.Provider, &e.ProviderInstanceID, &e.Model, &e.Alias, &e.Detail.InputTokens, &e.Detail.OutputTokens, &e.Detail.ReasoningTokens, &e.Detail.CachedTokens, &e.Detail.CacheReadTokens, &e.Detail.CacheCreationTokens, &e.Detail.TotalTokens, &e.FirstTokenLatencyMS, &e.TotalLatencyMS, &e.USD, &failed, &created); err != nil {
			return nil, err
		}
		e.Failed = failed != 0
		e.CreatedAt = parseTime(created)
		out = append(out, e)
	}
	return out, rows.Err()
}

func CalculateUSD(rule PriceRule, detail UsageDetail) float64 {
	cacheRead := max64(detail.CacheReadTokens, 0)
	input := max64(detail.InputTokens-cacheRead, 0)
	output := max64(detail.OutputTokens, 0)
	return float64(input)*rule.InputUSDPerMillion/1_000_000 +
		float64(output)*rule.OutputUSDPerMillion/1_000_000 +
		float64(cacheRead)*rule.CacheReadUSDPerMillion/1_000_000
}

func (s *Store) CreateGroup(ctx context.Context, id, name, description string, members []GroupMember) (Group, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		var err error
		id, err = newID("grp")
		if err != nil {
			return Group{}, err
		}
	}
	now := timeNowString()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Group{}, err
	}
	defer rollback(tx)
	if _, err := tx.ExecContext(ctx, `INSERT INTO groups(id, name, description, created_at, updated_at) VALUES(?, ?, ?, ?, ?)`, id, strings.TrimSpace(name), strings.TrimSpace(description), now, now); err != nil {
		return Group{}, err
	}
	if err := replaceGroupMembers(ctx, tx, id, members); err != nil {
		return Group{}, err
	}
	if err := tx.Commit(); err != nil {
		return Group{}, err
	}
	return s.GetGroup(ctx, id)
}

func (s *Store) UpdateGroup(ctx context.Context, id string, patch GroupPatch) (Group, error) {
	id = strings.TrimSpace(id)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Group{}, err
	}
	defer rollback(tx)

	var existingID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM groups WHERE id = ?`, id).Scan(&existingID); err != nil {
		return Group{}, err
	}
	now := timeNowString()
	if patch.Name != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE groups SET name = ?, updated_at = ? WHERE id = ?`, strings.TrimSpace(*patch.Name), now, id); err != nil {
			return Group{}, err
		}
	}
	if patch.Description != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE groups SET description = ?, updated_at = ? WHERE id = ?`, strings.TrimSpace(*patch.Description), now, id); err != nil {
			return Group{}, err
		}
	}
	if patch.Members != nil {
		if err := replaceGroupMembers(ctx, tx, id, *patch.Members); err != nil {
			return Group{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE groups SET updated_at = ? WHERE id = ?`, now, id); err != nil {
			return Group{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Group{}, err
	}
	return s.GetGroup(ctx, id)
}

func (s *Store) DeleteGroup(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM groups WHERE id = ?`, strings.TrimSpace(id))
	if err != nil {
		return err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) GetGroup(ctx context.Context, id string) (Group, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, name, description, created_at, updated_at FROM groups WHERE id = ?`, strings.TrimSpace(id))
	var g Group
	var created, updated string
	if err := row.Scan(&g.ID, &g.Name, &g.Description, &created, &updated); err != nil {
		return Group{}, err
	}
	g.CreatedAt = parseTime(created)
	g.UpdatedAt = parseTime(updated)
	g.Members, _ = s.GroupMembers(ctx, g.ID)
	return g, nil
}

func (s *Store) ListGroups(ctx context.Context) ([]Group, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, description, created_at, updated_at FROM groups ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Group, 0)
	for rows.Next() {
		var g Group
		var created, updated string
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &created, &updated); err != nil {
			return nil, err
		}
		g.CreatedAt = parseTime(created)
		g.UpdatedAt = parseTime(updated)
		g.Members, _ = s.GroupMembers(ctx, g.ID)
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *Store) GroupMembers(ctx context.Context, groupID string) ([]GroupMember, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT group_id, member_type, member_id FROM group_members WHERE group_id = ? ORDER BY member_type, member_id`, strings.TrimSpace(groupID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]GroupMember, 0)
	for rows.Next() {
		var member GroupMember
		if err := rows.Scan(&member.GroupID, &member.MemberType, &member.MemberID); err != nil {
			return nil, err
		}
		out = append(out, member)
	}
	return out, rows.Err()
}

func replaceBindings(ctx context.Context, tx *sql.Tx, keyID string, bindings []Binding) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM key_bindings WHERE key_id = ?`, keyID); err != nil {
		return err
	}
	for _, binding := range bindings {
		targetType := normalizeTargetType(binding.TargetType)
		targetID := strings.TrimSpace(binding.TargetID)
		if targetType == "" || targetID == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO key_bindings(key_id, target_type, target_id) VALUES(?, ?, ?)`, keyID, targetType, targetID); err != nil {
			return err
		}
	}
	return nil
}

func replaceGroupMembers(ctx context.Context, tx *sql.Tx, groupID string, members []GroupMember) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM group_members WHERE group_id = ?`, groupID); err != nil {
		return err
	}
	for _, member := range members {
		memberType := normalizeTargetType(member.MemberType)
		memberID := strings.TrimSpace(member.MemberID)
		if memberType == "" || memberID == "" {
			continue
		}
		if memberType == BindingGroup {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO group_members(group_id, member_type, member_id) VALUES(?, ?, ?)`, groupID, memberType, memberID); err != nil {
			return err
		}
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanKey(row rowScanner) (Key, error) {
	var key Key
	var enabled int
	var five, weekly, total sql.NullFloat64
	var created, updated string
	var last sql.NullString
	if err := row.Scan(&key.ID, &key.Name, &key.KeyPrefix, &enabled, &five, &weekly, &total, &created, &updated, &last); err != nil {
		return Key{}, err
	}
	key.Enabled = enabled != 0
	key.Limits = Limits{FiveHourUSD: floatPtr(five), WeeklyUSD: floatPtr(weekly), TotalUSD: floatPtr(total)}
	key.CreatedAt = parseTime(created)
	key.UpdatedAt = parseTime(updated)
	if last.Valid && strings.TrimSpace(last.String) != "" {
		t := parseTime(last.String)
		key.LastUsedAt = &t
	}
	return key, nil
}

func scanInventory(row rowScanner) (InventoryItem, error) {
	var item InventoryItem
	var snapshot, updated string
	if err := row.Scan(&item.ID, &item.Type, &item.Provider, &item.AuthID, &item.AuthIndex, &item.Name, &item.Label, &item.Email, &item.BaseURL, &snapshot, &updated); err != nil {
		return InventoryItem{}, err
	}
	if strings.TrimSpace(snapshot) != "" {
		_ = json.Unmarshal([]byte(snapshot), &item.Snapshot)
	}
	item.UpdatedAt = parseTime(updated)
	return item, nil
}

func newPlainKey() (plain string, hash string, prefix string, err error) {
	buf := make([]byte, 32)
	if _, err = rand.Read(buf); err != nil {
		return "", "", "", err
	}
	plain = "cam_" + base64.RawURLEncoding.EncodeToString(buf)
	hash = hashToken(plain)
	prefix = maskToken(plain)
	return plain, hash, prefix, nil
}

func maskToken(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if len(value) <= 8 {
		if len(value) <= 4 {
			return value
		}
		return value[:3] + "***" + value[len(value)-2:]
	}
	return value[:8] + "******" + value[len(value)-4:]
}

func newID(prefix string) (string, error) {
	buf := make([]byte, 10)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(buf), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

func ProviderInstanceID(provider, authID, authIndex, baseURL string) string {
	provider = normalizeProvider(provider)
	authID = strings.TrimSpace(authID)
	authIndex = strings.TrimSpace(authIndex)
	baseURL = normalizeBaseURL(baseURL)
	switch {
	case provider != "" && authID != "":
		return provider + ":" + authID
	case provider != "" && authIndex != "":
		return provider + ":idx:" + authIndex
	case provider != "" && baseURL != "":
		return provider + ":url:" + baseURL
	default:
		return strings.Trim(strings.Join([]string{provider, authID, authIndex, baseURL}, ":"), ":")
	}
}

func normalizeTargetType(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	v = strings.ReplaceAll(v, "-", "_")
	switch v {
	case BindingGroup, BindingAuthID, BindingProviderInstance, InventoryAuthFile:
		return v
	default:
		return v
	}
}

func normalizeProvider(v string) string {
	return strings.ToLower(strings.TrimSpace(v))
}

func normalizeBaseURL(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	u, err := url.Parse(v)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return strings.TrimRight(v, "/")
	}
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String()
}

func addProvider(scope *Scope, provider string) {
	provider = normalizeProvider(provider)
	if provider != "" {
		scope.Providers[provider] = struct{}{}
	}
}

func stringSet(values []string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, value := range values {
		value = normalizeProvider(value)
		if value != "" {
			out[value] = struct{}{}
		}
	}
	return out
}

func firstAttr(attrs map[string]string, keys ...string) string {
	for _, key := range keys {
		if attrs == nil {
			return ""
		}
		if value := strings.TrimSpace(attrs[key]); value != "" {
			return value
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func uniqueNonEmpty(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
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

func nullFloat(v *float64) any {
	if v == nil || *v <= 0 {
		return nil
	}
	return *v
}

func floatPtr(v sql.NullFloat64) *float64 {
	if !v.Valid || v.Float64 <= 0 {
		return nil
	}
	out := v.Float64
	return &out
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func limitReached(limit *float64, used float64) bool {
	return limit != nil && *limit > 0 && used >= *limit
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		t = time.Now()
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func timeNowString() string {
	return formatTime(time.Now())
}

func parseTime(v string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(v))
	if err != nil {
		return time.Time{}
	}
	return t
}

func rollback(tx *sql.Tx) {
	if tx != nil {
		_ = tx.Rollback()
	}
}

func max64(v int64, floor int64) int64 {
	if v < floor {
		return floor
	}
	return v
}
