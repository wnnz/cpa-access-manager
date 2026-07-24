package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/wnnz/cpa-toolkit/internal/access"
)

func TestRoutingSessionHashExtractionOrderAndHashOnly(t *testing.T) {
	headers := map[string][]string{
		"X-Session-ID":        {"header-session"},
		"Session_id":          {"underscore-session"},
		"X-Client-Request-Id": {"request-session"},
	}
	got := routingSessionHash(map[string]any{"execution_session_id": "execution-session"}, headers)
	sum := sha256.Sum256([]byte("header-session"))
	want := hex.EncodeToString(sum[:])
	if got != want || got == "execution-session" {
		t.Fatalf("routingSessionHash() = %q, want SHA-256 %q", got, want)
	}
	if got := routingSessionHash(nil, headers); got != hashRoutingSession("header-session") {
		t.Fatalf("header precedence hash = %q", got)
	}
	if got := routingSessionHash(nil, map[string][]string{"session-id": {"same-value"}}); got != routingSessionHash(map[string]any{"execution_session_id": "same-value"}, nil) {
		t.Fatalf("same identifier from different carriers must hash identically")
	}
	if got := routingSessionHash(map[string]any{"session_id": "metadata-session", "execution_session_id": "execution-session"}, nil); got != hashRoutingSession("metadata-session") {
		t.Fatalf("explicit metadata session must win over execution session, got %q", got)
	}
}

func TestAppSchedulerRoundRobinAndStickySession(t *testing.T) {
	app, store := newTestApp(t)
	_, plain, err := store.CreateKey(context.Background(), "routing", true, access.Limits{}, []access.Binding{
		{TargetType: access.BindingAuthID, TargetID: "auth-a"},
		{TargetType: access.BindingAuthID, TargetID: "auth-b"},
	})
	if err != nil {
		t.Fatalf("CreateKey() error = %v", err)
	}
	pick := func(session string) string {
		t.Helper()
		req := SchedulerPickRequest{
			Provider: "codex",
			Model:    "gpt-test",
			Options: SchedulerPickOptions{
				Headers: map[string][]string{
					"Authorization": {"Bearer " + plain},
					"Session_id":    {session},
				},
				Metadata: map[string]any{"execution_session_id": "shared-execution"},
			},
			Candidates: []SchedulerAuthCandidate{
				{ID: "auth-b", Provider: "codex", Priority: 10},
				{ID: "auth-a", Provider: "codex", Priority: 10},
			},
		}
		raw, _ := json.Marshal(req)
		respRaw, err := app.HandleMethod(MethodSchedulerPick, raw)
		if err != nil {
			t.Fatalf("scheduler.pick error = %v", err)
		}
		env, resp := decodeEnvelopeResult[SchedulerPickResponse](t, respRaw)
		if !env.OK || !resp.Handled {
			t.Fatalf("scheduler response = %#v %#v", env, resp)
		}
		return resp.AuthID
	}
	if got := pick("session-one"); got != "auth-a" {
		t.Fatalf("first session pick = %q, want auth-a", got)
	}
	if got := pick("session-one"); got != "auth-a" {
		t.Fatalf("sticky session pick = %q, want auth-a", got)
	}
	if got := pick("session-two"); got != "auth-b" {
		t.Fatalf("second session pick = %q, want auth-b", got)
	}
}

func TestAppSchedulerKeepsTitleAndConversationModelsOnSameResource(t *testing.T) {
	app, store := newTestApp(t)
	_, plain, err := store.CreateKey(context.Background(), "routing", true, access.Limits{}, []access.Binding{
		{TargetType: access.BindingAuthID, TargetID: "auth-a"},
		{TargetType: access.BindingAuthID, TargetID: "auth-b"},
	})
	if err != nil {
		t.Fatalf("CreateKey() error = %v", err)
	}
	pick := func(model string) string {
		t.Helper()
		req := SchedulerPickRequest{
			Provider: "codex",
			Model:    model,
			Options: SchedulerPickOptions{
				Headers: map[string][]string{
					"Authorization": {"Bearer " + plain},
					"Session_id":    {"same-conversation"},
				},
			},
			Candidates: []SchedulerAuthCandidate{
				{ID: "auth-a", Provider: "codex", Priority: 10},
				{ID: "auth-b", Provider: "codex", Priority: 10},
			},
		}
		raw, _ := json.Marshal(req)
		respRaw, err := app.HandleMethod(MethodSchedulerPick, raw)
		if err != nil {
			t.Fatalf("scheduler.pick error = %v", err)
		}
		_, resp := decodeEnvelopeResult[SchedulerPickResponse](t, respRaw)
		return resp.AuthID
	}

	titleResource := pick("title-model")
	conversationResource := pick("conversation-model")
	if titleResource != conversationResource {
		t.Fatalf("title resource = %q, conversation resource = %q", titleResource, conversationResource)
	}
}

func TestAppModelRouteProviderRoundRobinAndStickySession(t *testing.T) {
	app, store := newTestApp(t)
	codexID := mustUpsertProviderInstance(t, store, "codex", "auth-codex")
	geminiID := mustUpsertProviderInstance(t, store, "gemini", "auth-gemini")
	key, _, err := store.CreateKey(context.Background(), "providers", true, access.Limits{}, []access.Binding{
		{TargetType: access.BindingProviderInstance, TargetID: codexID},
		{TargetType: access.BindingProviderInstance, TargetID: geminiID},
	})
	if err != nil {
		t.Fatalf("CreateKey() error = %v", err)
	}
	route := func(session string) string {
		t.Helper()
		req := ModelRouteRequest{
			RequestedModel: "gpt-test",
			Headers:        http.Header{"Session-Id": {session}},
			Metadata: map[string]any{
				"cpa_toolkit_key_id":   key.ID,
				"execution_session_id": "shared-execution",
			},
			AvailableProviders: []string{"gemini", "codex"},
		}
		raw, _ := json.Marshal(req)
		respRaw, err := app.HandleMethod(MethodModelRoute, raw)
		if err != nil {
			t.Fatalf("model.route error = %v", err)
		}
		env, resp := decodeEnvelopeResult[ModelRouteResponse](t, respRaw)
		if !env.OK || !resp.Handled {
			t.Fatalf("route response = %#v %#v", env, resp)
		}
		return resp.Target
	}
	if got := route("session-one"); got != "codex" {
		t.Fatalf("first provider = %q, want codex", got)
	}
	if got := route("session-one"); got != "codex" {
		t.Fatalf("sticky provider = %q, want codex", got)
	}
	if got := route("session-two"); got != "gemini" {
		t.Fatalf("second provider = %q, want gemini", got)
	}
}
