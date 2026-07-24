package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

func routingSessionHash(metadata map[string]any, headers map[string][]string) string {
	// Client conversation IDs must win over execution_session_id. Some clients
	// reuse one execution ID across multiple conversations, which would collapse
	// otherwise independent sticky sessions onto the same resource.
	for _, name := range []string{"X-Session-ID", "Session-Id", "Session_id"} {
		if value := headerValue(headers, name); value != "" {
			return hashRoutingSession(value)
		}
	}
	for _, name := range []string{"session_id", "sessionId", "conversation_id", "conversationId", "prompt_cache_key"} {
		if value := routingMetadataString(metadata, name); value != "" {
			return hashRoutingSession(value)
		}
	}
	if value := routingMetadataString(metadata, "execution_session_id"); value != "" {
		return hashRoutingSession(value)
	}
	if value := headerValue(headers, "X-Client-Request-Id"); value != "" {
		return hashRoutingSession(value)
	}
	return ""
}

func routingMetadataString(metadata map[string]any, name string) string {
	for key, raw := range metadata {
		if !strings.EqualFold(strings.TrimSpace(key), name) || raw == nil {
			continue
		}
		switch value := raw.(type) {
		case string:
			return strings.TrimSpace(value)
		case fmt.Stringer:
			return strings.TrimSpace(value.String())
		default:
			return strings.TrimSpace(fmt.Sprint(value))
		}
	}
	return ""
}

func headerValue(headers map[string][]string, name string) string {
	for key, values := range headers {
		if !strings.EqualFold(strings.TrimSpace(key), name) {
			continue
		}
		for _, value := range values {
			if value = strings.TrimSpace(value); value != "" {
				return value
			}
		}
	}
	return ""
}

func hashRoutingSession(value string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return hex.EncodeToString(sum[:])
}
