package access

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestPickCandidateRoutedRoundRobinWithinHighestPriority(t *testing.T) {
	store := newTestStore(t)
	key := createRoutingKey(t, store, map[string]string{"auth-a": "codex", "auth-b": "codex", "auth-low": "codex"})
	candidates := []Candidate{
		{ID: "auth-low", Provider: "codex", Priority: 1},
		{ID: "auth-b", Provider: "codex", Priority: 10},
		{ID: "auth-a", Provider: "codex", Priority: 10},
	}

	for i, want := range []string{"auth-a", "auth-b", "auth-a"} {
		got, err := store.PickCandidateRouted(context.Background(), key.ID, "gpt-test", "codex", "", candidates, time.Time{})
		if err != nil {
			t.Fatalf("pick %d error = %v", i, err)
		}
		if got.ID != want {
			t.Fatalf("pick %d = %q, want %q", i, got.ID, want)
		}
	}

	fallback, err := store.PickCandidateRouted(context.Background(), key.ID, "gpt-test", "codex", "", candidates[:1], time.Time{})
	if err != nil || fallback.ID != "auth-low" {
		t.Fatalf("fallback = %#v, %v; want auth-low", fallback, err)
	}
}

func TestRoutingSessionStickySlidingTTLAndPriorityChange(t *testing.T) {
	store := newTestStore(t)
	key := createRoutingKey(t, store, map[string]string{"auth-a": "codex", "auth-b": "codex"})
	t0 := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	equal := []Candidate{
		{ID: "auth-a", Provider: "codex", Priority: 10},
		{ID: "auth-b", Provider: "codex", Priority: 10},
	}

	first := mustPickRouted(t, store, key.ID, "model-a", "session-one", equal, t0)
	if first.ID != "auth-a" {
		t.Fatalf("first = %q, want auth-a", first.ID)
	}
	if got := mustPickRouted(t, store, key.ID, "model-a", "session-one", equal, t0.Add(59*time.Minute)); got.ID != "auth-a" {
		t.Fatalf("sliding sticky = %q, want auth-a", got.ID)
	}
	if got := mustPickRouted(t, store, key.ID, "model-a", "session-one", equal, t0.Add(90*time.Minute)); got.ID != "auth-a" {
		t.Fatalf("renewed sticky = %q, want auth-a", got.ID)
	}
	if got := mustPickRouted(t, store, key.ID, "model-a", "session-two", equal, t0.Add(90*time.Minute)); got.ID != "auth-b" {
		t.Fatalf("different session = %q, want auth-b", got.ID)
	}

	priorityChanged := []Candidate{
		{ID: "auth-a", Provider: "codex", Priority: 10},
		{ID: "auth-b", Provider: "codex", Priority: 20},
	}
	if got := mustPickRouted(t, store, key.ID, "model-a", "session-one", priorityChanged, t0.Add(91*time.Minute)); got.ID != "auth-b" {
		t.Fatalf("priority reselect = %q, want auth-b", got.ID)
	}

	// Title generation and the main request may use different models, but a
	// conversation must remain on one resource.
	if got := mustPickRouted(t, store, key.ID, "title-model", "session-one", equal, t0.Add(91*time.Minute)); got.ID != "auth-b" {
		t.Fatalf("cross-model sticky = %q, want auth-b", got.ID)
	}
	var rows int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM routing_sessions WHERE key_id = ? AND session_hash = ?`, key.ID, "session-one").Scan(&rows); err != nil {
		t.Fatalf("count routing sessions error = %v", err)
	}
	if rows != 1 {
		t.Fatalf("cross-model session rows = %d, want 1", rows)
	}
}

func TestRoutingSessionExpiresAndPersistsAcrossRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "routing.db")
	store, err := OpenStore(ctx, path, false)
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	key := createRoutingKey(t, store, map[string]string{"auth-a": "codex", "auth-b": "codex"})
	candidates := []Candidate{{ID: "auth-a", Provider: "codex", Priority: 1}, {ID: "auth-b", Provider: "codex", Priority: 1}}
	t0 := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	if got := mustPickRouted(t, store, key.ID, "gpt-test", "persistent", candidates, t0); got.ID != "auth-a" {
		t.Fatalf("initial = %q, want auth-a", got.ID)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	store, err = OpenStore(ctx, path, false)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	defer store.Close()
	if got := mustPickRouted(t, store, key.ID, "gpt-test", "persistent", candidates, t0.Add(30*time.Minute)); got.ID != "auth-a" {
		t.Fatalf("restarted sticky = %q, want auth-a", got.ID)
	}
	// Advance the restarted store's in-memory cursor without changing the
	// persistent session mapping.
	if got := mustPickRouted(t, store, key.ID, "gpt-test", "other", candidates, t0.Add(31*time.Minute)); got.ID != "auth-a" {
		t.Fatalf("other session = %q, want auth-a", got.ID)
	}
	if got := mustPickRouted(t, store, key.ID, "gpt-test", "persistent", candidates, t0.Add(89*time.Minute)); got.ID != "auth-a" {
		t.Fatalf("sliding after restart = %q, want auth-a", got.ID)
	}
	if got := mustPickRouted(t, store, key.ID, "gpt-test", "persistent", candidates, t0.Add(150*time.Minute)); got.ID != "auth-b" {
		t.Fatalf("expired reallocation = %q, want auth-b", got.ID)
	}
}

func TestRoutingSessionDoesNotCrossKeysAndRebindsAfterUnbind(t *testing.T) {
	store := newTestStore(t)
	keyOne := createRoutingKey(t, store, map[string]string{"auth-a": "codex", "auth-b": "codex"})
	keyTwo := createRoutingKey(t, store, map[string]string{"auth-a": "codex", "auth-b": "codex"})
	candidates := []Candidate{{ID: "auth-a", Provider: "codex", Priority: 1}, {ID: "auth-b", Provider: "codex", Priority: 1}}
	now := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	if got := mustPickRouted(t, store, keyOne.ID, "gpt-test", "shared-hash", candidates, now); got.ID != "auth-a" {
		t.Fatalf("key one initial = %q, want auth-a", got.ID)
	}
	if got := mustPickRouted(t, store, keyTwo.ID, "gpt-test", "shared-hash", candidates, now); got.ID != "auth-a" {
		t.Fatalf("key two must have independent cursor/mapping, got %q", got.ID)
	}
	bindings := []Binding{{TargetType: BindingAuthID, TargetID: "auth-b"}}
	if _, err := store.UpdateKey(context.Background(), keyOne.ID, KeyPatch{Bindings: &bindings}); err != nil {
		t.Fatalf("UpdateKey(bindings) error = %v", err)
	}
	if got := mustPickRouted(t, store, keyOne.ID, "gpt-test", "shared-hash", candidates, now.Add(time.Minute)); got.ID != "auth-b" {
		t.Fatalf("rebind after unbind = %q, want auth-b", got.ID)
	}
	if _, err := store.PickCandidateRouted(context.Background(), keyOne.ID, "gpt-test", "codex", "shared-hash", []Candidate{{ID: "auth-a", Provider: "codex", Priority: 1}}, now.Add(2*time.Minute)); err != ErrNoAllowedTarget {
		t.Fatalf("all unauthorized error = %v, want ErrNoAllowedTarget", err)
	}
}

func TestChooseProviderRoutedRoundRobinStickyAndUnavailable(t *testing.T) {
	store := newTestStore(t)
	key := createRoutingKey(t, store, map[string]string{"auth-codex": "codex", "auth-gemini": "gemini"})
	t0 := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)

	for i, want := range []string{"codex", "gemini", "codex"} {
		got, err := store.ChooseProviderRouted(context.Background(), key.ID, "gpt-test", []string{"gemini", "codex"}, "", t0)
		if err != nil || got != want {
			t.Fatalf("provider pick %d = %q, %v; want %q", i, got, err, want)
		}
	}
	sticky, err := store.ChooseProviderRouted(context.Background(), key.ID, "gpt-test", []string{"codex", "gemini"}, "provider-session", t0)
	if err != nil {
		t.Fatalf("sticky provider error = %v", err)
	}
	again, err := store.ChooseProviderRouted(context.Background(), key.ID, "gpt-test", []string{"codex", "gemini"}, "provider-session", t0.Add(time.Minute))
	if err != nil || again != sticky {
		t.Fatalf("sticky provider = %q then %q, %v", sticky, again, err)
	}
	other := "codex"
	if sticky == other {
		other = "gemini"
	}
	reselected, err := store.ChooseProviderRouted(context.Background(), key.ID, "gpt-test", []string{other}, "provider-session", t0.Add(2*time.Minute))
	if err != nil || reselected != other {
		t.Fatalf("unavailable provider reselect = %q, %v; want %q", reselected, err, other)
	}
}

func TestRoutingSessionConcurrentFirstAssignmentAndCascadeDelete(t *testing.T) {
	store := newTestStore(t)
	key := createRoutingKey(t, store, map[string]string{"auth-a": "codex", "auth-b": "codex"})
	candidates := []Candidate{{ID: "auth-a", Provider: "codex", Priority: 1}, {ID: "auth-b", Provider: "codex", Priority: 1}}
	const workers = 24
	results := make(chan string, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := store.PickCandidateRouted(context.Background(), key.ID, "gpt-test", "codex", "concurrent", candidates, time.Now())
			if err != nil {
				errs <- err
				return
			}
			results <- got.ID
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent pick error = %v", err)
	}
	var selected string
	for got := range results {
		if selected == "" {
			selected = got
		}
		if got != selected {
			t.Fatalf("concurrent picks differ: %q and %q", selected, got)
		}
	}
	var rows int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM routing_sessions WHERE key_id = ?`, key.ID).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("routing rows before delete = %d, %v; want 1", rows, err)
	}
	if err := store.DeleteKey(context.Background(), key.ID); err != nil {
		t.Fatalf("DeleteKey() error = %v", err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM routing_sessions WHERE key_id = ?`, key.ID).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("routing rows after delete = %d, %v; want 0", rows, err)
	}
}

func createRoutingKey(t *testing.T, store *Store, authProviders map[string]string) Key {
	t.Helper()
	bindings := make([]Binding, 0, len(authProviders))
	for authID, provider := range authProviders {
		if err := store.UpsertInventoryItem(context.Background(), InventoryItem{ID: authID, Type: InventoryAuthFile, Provider: provider, AuthID: authID}); err != nil {
			t.Fatalf("UpsertInventoryItem(%s) error = %v", authID, err)
		}
		bindings = append(bindings, Binding{TargetType: BindingAuthID, TargetID: authID})
	}
	key, _, err := store.CreateKey(context.Background(), "routing", true, Limits{}, bindings)
	if err != nil {
		t.Fatalf("CreateKey() error = %v", err)
	}
	return key
}

func mustPickRouted(t *testing.T, store *Store, keyID, model, sessionHash string, candidates []Candidate, now time.Time) Candidate {
	t.Helper()
	got, err := store.PickCandidateRouted(context.Background(), keyID, model, "codex", sessionHash, candidates, now)
	if err != nil {
		t.Fatalf("PickCandidateRouted() error = %v", err)
	}
	return got
}
