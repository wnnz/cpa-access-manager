package access

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
)

const RoutingSessionTTL = time.Hour

type routingSession struct {
	Provider  string
	AuthID    string
	ExpiresAt time.Time
}

// ChooseProviderRouted limits routing to the key's authorized providers. A
// session is sticky across models for a sliding hour; requests without a
// session round-robin independently per model.
func (s *Store) ChooseProviderRouted(ctx context.Context, keyID, requestedModel string, available []string, sessionHash string, now time.Time) (string, error) {
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

	s.routingMu.Lock()
	defer s.routingMu.Unlock()
	now = routingNow(now)
	model := strings.TrimSpace(requestedModel)
	sessionHash = strings.TrimSpace(sessionHash)
	if sessionHash != "" {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return "", err
		}
		defer rollback(tx)
		mapped, found, err := loadRoutingSession(ctx, tx, keyID, sessionHash)
		if err != nil {
			return "", err
		}
		if found && mapped.ExpiresAt.After(now) && containsString(allowed, mapped.Provider) {
			if err := saveRoutingSession(ctx, tx, keyID, sessionHash, mapped.Provider, mapped.AuthID, now); err != nil {
				return "", err
			}
			if err := tx.Commit(); err != nil {
				return "", err
			}
			return mapped.Provider, nil
		}
		selected := s.nextRoutingValue("provider\x00"+keyID+"\x00"+model, allowed)
		if err := saveRoutingSession(ctx, tx, keyID, sessionHash, selected, "", now); err != nil {
			return "", err
		}
		if err := deleteExpiredRoutingSessions(ctx, tx, now); err != nil {
			return "", err
		}
		if err := tx.Commit(); err != nil {
			return "", err
		}
		return selected, nil
	}
	return s.nextRoutingValue("provider\x00"+keyID+"\x00"+model, allowed), nil
}

// PickCandidateRouted preserves CPA's Priority semantics and round-robins only
// among authorized candidates tied at the highest currently available priority.
func (s *Store) PickCandidateRouted(ctx context.Context, keyID, model, provider, sessionHash string, candidates []Candidate, now time.Time) (Candidate, error) {
	s.routingMu.Lock()
	defer s.routingMu.Unlock()

	scope, err := s.ResolveScope(ctx, keyID)
	if err != nil {
		return Candidate{}, err
	}
	if !scope.HasBindings {
		return Candidate{}, ErrNoBindings
	}
	_ = s.UpsertProviderCandidates(ctx, candidates)
	provider = strings.TrimSpace(provider)
	matched := make([]Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		if provider != "" && candidate.Provider != provider {
			continue
		}
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
	maxPriority := matched[0].Priority
	top := matched[:0]
	for _, candidate := range matched {
		if candidate.Priority != maxPriority {
			break
		}
		top = append(top, candidate)
	}

	now = routingNow(now)
	model = strings.TrimSpace(model)
	sessionHash = strings.TrimSpace(sessionHash)
	if sessionHash != "" {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return Candidate{}, err
		}
		defer rollback(tx)
		mapped, found, err := loadRoutingSession(ctx, tx, keyID, sessionHash)
		if err != nil {
			return Candidate{}, err
		}
		if found && mapped.ExpiresAt.After(now) {
			for _, candidate := range top {
				if candidate.ID == mapped.AuthID && (mapped.Provider == "" || candidate.Provider == mapped.Provider) {
					if err := saveRoutingSession(ctx, tx, keyID, sessionHash, candidate.Provider, candidate.ID, now); err != nil {
						return Candidate{}, err
					}
					if err := tx.Commit(); err != nil {
						return Candidate{}, err
					}
					return candidate, nil
				}
			}
		}
		selected := s.nextCandidate(keyID, model, provider, maxPriority, top)
		if err := saveRoutingSession(ctx, tx, keyID, sessionHash, selected.Provider, selected.ID, now); err != nil {
			return Candidate{}, err
		}
		if err := deleteExpiredRoutingSessions(ctx, tx, now); err != nil {
			return Candidate{}, err
		}
		if err := tx.Commit(); err != nil {
			return Candidate{}, err
		}
		return selected, nil
	}
	return s.nextCandidate(keyID, model, provider, maxPriority, top), nil
}

func (s *Store) nextCandidate(keyID, model, provider string, priority int, candidates []Candidate) Candidate {
	ids := make([]string, len(candidates))
	byID := make(map[string]Candidate, len(candidates))
	for i, candidate := range candidates {
		ids[i] = candidate.ID
		byID[candidate.ID] = candidate
	}
	id := s.nextRoutingValue("auth\x00"+keyID+"\x00"+model+"\x00"+provider+"\x00"+strconv.Itoa(priority), ids)
	return byID[id]
}

func (s *Store) nextRoutingValue(cursorKey string, values []string) string {
	index := s.routingCursor[cursorKey] % uint64(len(values))
	s.routingCursor[cursorKey]++
	return values[index]
}

func routingNow(now time.Time) time.Time {
	if now.IsZero() {
		return time.Now().UTC()
	}
	return now.UTC()
}

func loadRoutingSession(ctx context.Context, tx *sql.Tx, keyID, sessionHash string) (routingSession, bool, error) {
	var out routingSession
	var expires string
	err := tx.QueryRowContext(ctx, `SELECT provider, auth_id, expires_at FROM routing_sessions WHERE key_id = ? AND model = '' AND session_hash = ?`, keyID, sessionHash).Scan(&out.Provider, &out.AuthID, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return out, false, nil
	}
	if err != nil {
		return out, false, err
	}
	parsed := parseTime(expires)
	if parsed.IsZero() {
		return out, false, nil
	}
	out.ExpiresAt = parsed
	return out, true, nil
}

func saveRoutingSession(ctx context.Context, tx *sql.Tx, keyID, sessionHash, provider, authID string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO routing_sessions(key_id, model, session_hash, provider, auth_id, expires_at, updated_at)
		VALUES(?, '', ?, ?, ?, ?, ?)
		ON CONFLICT(key_id, model, session_hash) DO UPDATE SET provider = excluded.provider, auth_id = excluded.auth_id, expires_at = excluded.expires_at, updated_at = excluded.updated_at`,
		keyID, sessionHash, provider, authID, formatTime(now.Add(RoutingSessionTTL)), formatTime(now))
	return err
}

func deleteExpiredRoutingSessions(ctx context.Context, tx *sql.Tx, now time.Time) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM routing_sessions WHERE expires_at <= ?`, formatTime(now))
	return err
}

func containsString(values []string, value string) bool {
	i := sort.SearchStrings(values, value)
	return i < len(values) && values[i] == value
}
