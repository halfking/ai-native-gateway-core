
package relay

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/_to-be-deprecated/sessions"
)

func TestExtractSessionIDFromHeaders_Priority(t *testing.T) {
	tests := []struct {
		name     string
		headers  map[string]string
		expected string
	}{
		{
			name: "X-Gw-Session-Id takes priority",
			headers: map[string]string{
				"X-Gw-Session-Id":   "gw_session_1",
				"X-Session-Id":      "session_2",
				"X-Conversation-Id": "conv_3",
				"X-Chat-Session-Id": "chat_4",
				"X-Thread-Id":       "thread_5",
			},
			expected: "gw_session_1",
		},
		{
			name: "X-Session-Id when X-Gw-Session-Id missing",
			headers: map[string]string{
				"X-Session-Id":      "session_2",
				"X-Conversation-Id": "conv_3",
				"X-Chat-Session-Id": "chat_4",
				"X-Thread-Id":       "thread_5",
			},
			expected: "session_2",
		},
		{
			name: "X-Conversation-Id when first two missing",
			headers: map[string]string{
				"X-Conversation-Id": "conv_3",
				"X-Chat-Session-Id": "chat_4",
				"X-Thread-Id":       "thread_5",
			},
			expected: "conv_3",
		},
		{
			name: "X-Chat-Session-Id when first three missing",
			headers: map[string]string{
				"X-Chat-Session-Id": "chat_4",
				"X-Thread-Id":       "thread_5",
			},
			expected: "chat_4",
		},
		{
			name: "X-Thread-Id when all others missing",
			headers: map[string]string{
				"X-Thread-Id": "thread_5",
			},
			expected: "thread_5",
		},
		{
			name:     "empty when no headers",
			headers:  map[string]string{},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			for k, v := range tt.headers {
				r.Header.Set(k, v)
			}

			result := extractSessionIDFromHeaders(r)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestExtractSessionIDFromHeaders_XGwSessionIdSanitized(t *testing.T) {
	// X-Gw-Session-Id should be sanitized
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-Gw-Session-Id", "gw_abc123")

	result := extractSessionIDFromHeaders(r)
	// Should pass through sanitizeGwSessionHeader
	assert.NotEmpty(t, result)
}

func TestGenerateSystemSessionID(t *testing.T) {
	id1 := generateSystemSessionID()
	id2 := generateSystemSessionID()

	assert.NotEmpty(t, id1)
	assert.NotEmpty(t, id2)
	assert.NotEqual(t, id1, id2)
	assert.Contains(t, id1, "gw_")
	assert.Contains(t, id2, "gw_")
}

func TestSessionHeadersPriority_Order(t *testing.T) {
	expected := []string{
		"X-Gw-Session-Id",
		"X-Session-Id",
		"X-Conversation-Id",
		"X-Chat-Session-Id",
		"X-Thread-Id",
	}
	assert.Equal(t, expected, SessionHeadersPriority)
}

func TestResolveSession_HeaderSessionID(t *testing.T) {
	// Test that header session ID is extracted correctly
	r := httptest.NewRequest("POST", "/v1/chat", nil)
	r.Header.Set("X-Gw-Session-Id", "gw_test_session_123")

	// Without sessionGetter, should return the session ID from header
	sessionID, _, _, _ := resolveSession(
		r.Context(),
		r,
		httptest.NewRecorder(),
		nil, // sessionGetter
		nil, // lastSystemSessionIdx
		nil, // keyInfo
	)

	assert.Equal(t, "gw_test_session_123", sessionID)
}

func TestResolveSession_NoID_NoKeyInfo(t *testing.T) {
	// When no headers and no keyInfo, should return empty
	r := httptest.NewRequest("POST", "/v1/chat", nil)

	sessionID, _, _, _ := resolveSession(
		r.Context(),
		r,
		httptest.NewRecorder(),
		nil,
		nil,
		nil,
	)

	assert.Empty(t, sessionID)
}

func TestResolveSession_HeaderPriorityOverFallback(t *testing.T) {
	// When header is present, it should be used (not auto-create)
	r := httptest.NewRequest("POST", "/v1/chat", nil)
	r.Header.Set("X-Gw-Session-Id", "gw_explicit_session")

	sessionID, _, _, _ := resolveSession(
		r.Context(),
		r,
		httptest.NewRecorder(),
		nil, // sessionGetter (no lookup)
		nil,
		&struct {
			ID       int
			TenantID string
		}{ID: 123, TenantID: "tenant-A"},
	)

	assert.Equal(t, "gw_explicit_session", sessionID)
}

// ── detectAndHandleModelSwitch tests (V3.1, 2026-06-26) ──────────────

func setupRedisTest(t *testing.T) (*redis.Client, func()) {
	mr, err := miniredis.Run()
	require.NoError(t, err)

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cleanup := func() {
		client.Close()
		mr.Close()
	}
	return client, cleanup
}

func TestDetectModelSwitch_NoPreference(t *testing.T) {
	client, cleanup := setupRedisTest(t)
	defer cleanup()

	sp := sessions.NewSessionPreference(client)
	ctx := context.Background()

	changed, prev := detectAndHandleModelSwitch(ctx, sp, "session-nonexistent", "gpt-4")
	assert.False(t, changed)
	assert.Equal(t, "", prev)
}

func TestDetectModelSwitch_SameModel(t *testing.T) {
	client, cleanup := setupRedisTest(t)
	defer cleanup()

	sp := sessions.NewSessionPreference(client)
	ctx := context.Background()

	err := sp.Set(ctx, "session-same", 100, "gpt-4")
	require.NoError(t, err)

	changed, prev := detectAndHandleModelSwitch(ctx, sp, "session-same", "gpt-4")
	assert.False(t, changed)
	assert.Equal(t, "", prev)

	// Preference should still exist
	val, found := sp.Get(ctx, "session-same")
	require.True(t, found)
	assert.Equal(t, 100, val.CredentialID)
}

func TestDetectModelSwitch_DifferentModel(t *testing.T) {
	client, cleanup := setupRedisTest(t)
	defer cleanup()

	sp := sessions.NewSessionPreference(client)
	ctx := context.Background()

	err := sp.Set(ctx, "session-diff", 100, "gpt-4")
	require.NoError(t, err)

	changed, prev := detectAndHandleModelSwitch(ctx, sp, "session-diff", "claude-3-opus")
	assert.True(t, changed)
	assert.Equal(t, "gpt-4", prev)

	// Preference should be cleared
	_, found := sp.Get(ctx, "session-diff")
	assert.False(t, found)
}

func TestDetectModelSwitch_NoPreviousModelStored(t *testing.T) {
	client, cleanup := setupRedisTest(t)
	defer cleanup()

	sp := sessions.NewSessionPreference(client)
	ctx := context.Background()

	// Legacy format: no model stored
	err := sp.Set(ctx, "session-legacy", 100, "")
	require.NoError(t, err)

	changed, prev := detectAndHandleModelSwitch(ctx, sp, "session-legacy", "gpt-4")
	assert.False(t, changed)
	assert.Equal(t, "", prev)
}

func TestDetectModelSwitch_NilPref(t *testing.T) {
	changed, prev := detectAndHandleModelSwitch(context.Background(), nil, "session-any", "gpt-4")
	assert.False(t, changed)
	assert.Equal(t, "", prev)
}

func TestDetectModelSwitch_EmptySessionID(t *testing.T) {
	client, cleanup := setupRedisTest(t)
	defer cleanup()

	sp := sessions.NewSessionPreference(client)

	changed, prev := detectAndHandleModelSwitch(context.Background(), sp, "", "gpt-4")
	assert.False(t, changed)
	assert.Equal(t, "", prev)
}

func TestDetectModelSwitch_EmptyClientModel(t *testing.T) {
	client, cleanup := setupRedisTest(t)
	defer cleanup()

	sp := sessions.NewSessionPreference(client)

	changed, prev := detectAndHandleModelSwitch(context.Background(), sp, "session-id", "")
	assert.False(t, changed)
	assert.Equal(t, "", prev)
}
