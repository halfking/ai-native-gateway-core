package sanitize

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"
	"github.com/stretchr/testify/require"
)

func TestInputSanitizerProtocolTextAndOpaqueMedia(t *testing.T) {
	cases := []struct {
		name, path, body string
		wantRefs         int
		wantPlaceholders int
	}{
		{
			name: "chat content blocks", path: "/v1/chat/completions",
			body:     `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"call 13800138000"},{"type":"image_url","image_url":{"url":"data:image/png;base64,13800138000"}}]},{"role":"assistant","content":"prior 13800138000"}]}`,
			wantRefs: 2, wantPlaceholders: 1,
		},
		{
			name: "anthropic system and user blocks", path: "/v1/messages",
			body:     `{"model":"m","max_tokens":16,"system":[{"type":"text","text":"mail a@b.com"}],"messages":[{"role":"user","content":[{"type":"text","text":"call 13800138000"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"13800138000"}}]}]}`,
			wantRefs: 1, wantPlaceholders: 2,
		},
		{
			name: "responses instructions and tool output", path: "/v1/responses",
			body:     `{"model":"m","instructions":"mail a@b.com","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"call 13800138000"},{"type":"input_image","image_url":"data:image/png;base64,13800138000"}]},{"type":"function_call_output","call_id":"c1","output":"phone 13912345678"}]}`,
			wantRefs: 2, wantPlaceholders: 3,
		},
		{
			name: "completions prompt array", path: "/v1/completions",
			body:     `{"model":"m","prompt":["call 13800138000","mail a@b.com"]}`,
			wantRefs: 2, wantPlaceholders: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rdb := setupSaniGuardRedis(t)
			s, err := NewSanitizer(NewPatternDetector())
			require.NoError(t, err)
			mw, err := NewSanitizeInputMiddleware(s, rdb, time.Minute)
			require.NoError(t, err)
			var forwarded []byte
			var info compression.SanitizeInfo
			var hasInfo bool
			handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				forwarded, _ = io.ReadAll(r.Body)
				info, hasInfo = compression.SanitizeInfoFromContext(r.Context())
			}))
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("X-Gw-Session-Id", "session-protocols")
			req = req.WithContext(WithAuthenticatedTenant(req.Context(), "tenant-protocols"))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.True(t, hasInfo)
			require.Len(t, info.MessageRefs, tc.wantRefs)
			require.Equal(t, tc.wantPlaceholders, info.Stats.PlaceholderCount)
			require.NotContains(t, string(forwarded), `"text":"call 13800138000"`)
			if tc.path == "/v1/completions" {
				require.NotContains(t, string(forwarded), "13800138000")
				require.NotContains(t, string(forwarded), "a@b.com")
			}
			require.Contains(t, string(forwarded), "{SENSITIVE:")
			var decoded any
			require.NoError(t, json.Unmarshal(forwarded, &decoded))
			// Opaque media bytes remain intact; replayed assistant text is sanitized.
			if strings.Contains(tc.body, "data:image/png;base64,13800138000") {
				require.Contains(t, string(forwarded), "data:image/png;base64,13800138000")
			}
			if strings.Contains(tc.body, `"data":"13800138000"`) {
				require.Contains(t, string(forwarded), `"data":"13800138000"`)
			}
			if tc.path == "/v1/chat/completions" {
				require.Contains(t, string(forwarded), "prior {SENSITIVE:phone:1}")
			}
			vals, err := rdb.HGetAll(context.Background(), sanitizeMapKey("tenant-protocols", "session-protocols")).Result()
			require.NoError(t, err)
			require.Len(t, vals, tc.wantPlaceholders)
		})
	}
}

type unavailableInputDetector struct{}

func (unavailableInputDetector) Name() string { return "unavailable" }
func (unavailableInputDetector) Detect(context.Context, string) ([]SensitiveFragment, error) {
	return nil, errors.New("detector unavailable")
}

func TestInputSanitizerDetectorFailureStopsDispatch(t *testing.T) {
	s, err := NewSanitizer(unavailableInputDetector{})
	require.NoError(t, err)
	mw, err := NewSanitizeInputMiddleware(s, nil, time.Minute)
	require.NoError(t, err)
	called := false
	handler := mw.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"messages":[{"role":"user","content":"call 13800138000"}]}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.False(t, called)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.NotContains(t, rec.Body.String(), "13800138000")
	require.Contains(t, rec.Body.String(), `"type":"error"`)
}

func TestInputSanitizerMappingFailureStopsDispatch(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	require.NoError(t, rdb.Close())
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	mw, err := NewSanitizeInputMiddleware(s, rdb, time.Minute)
	require.NoError(t, err)
	called := false
	handler := mw.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"input":"call 13800138000"}`))
	req.Header.Set("X-Gw-Session-Id", "session-fail")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.False(t, called)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.NotContains(t, rec.Body.String(), "13800138000")
}

func TestInputSanitizerOversizedBodyStopsDispatch(t *testing.T) {
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	mw, err := NewSanitizeInputMiddleware(s, nil, time.Minute)
	require.NoError(t, err)
	called := false
	handler := mw.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"messages":[]}`))
	req.ContentLength = maxSanitizeBodySize + 1
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.False(t, called)
	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	require.Contains(t, rec.Body.String(), "Request body too large")

	// Unknown/chunked length must still stop after a one-byte overread.
	unknown := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("123456789"))
	unknown.ContentLength = -1
	_, err = readBodyLimit(unknown, 8)
	require.ErrorIs(t, err, errSanitizeBodyTooLarge)
	within := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("12345678"))
	within.ContentLength = -1
	body, err := readBodyLimit(within, 8)
	require.NoError(t, err)
	require.Equal(t, "12345678", string(body))
}
