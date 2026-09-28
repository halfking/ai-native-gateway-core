package streaming

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/security/sanitize"
	"github.com/redis/go-redis/v9"
)

type countingSanitizeVerifier struct {
	durableEndpointVerifier
	calls int
}

func (v *countingSanitizeVerifier) Verify(context.Context, string) (*authentication.KeyInfo, error) {
	v.calls++
	return v.key, nil
}

// The capture executor is the last boundary before a provider HTTP request.
// It verifies the real protocol handlers dispatch only sanitized payloads.
func TestProtocolHandlersSanitizeBeforeExecutor(t *testing.T) {
	cases := []struct {
		name, path, body string
		wrap             func(*ChatHandler) http.Handler
		refs             int
	}{
		{"chat blocks", "/v1/chat/completions", `{"model":"m","stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"call 13800138000"},{"type":"image_url","image_url":{"url":"data:image/png;base64,abcdef"}}]}]}`, func(h *ChatHandler) http.Handler { return h }, 1},
		{"completions prompt", "/v1/completions", `{"model":"m","stream":true,"prompt":"call 13800138000","messages":[{"role":"user","content":"hello"}]}`, func(h *ChatHandler) http.Handler { return h }, 1},
		{"messages blocks", "/v1/messages", `{"model":"m","max_tokens":16,"stream":true,"system":"mail a@b.com","messages":[{"role":"user","content":[{"type":"text","text":"call 13800138000"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"abcdef"}}]}]}`, func(h *ChatHandler) http.Handler { return NewMessagesHandler(h) }, 1},
		{"responses blocks", "/v1/responses", `{"model":"m","stream":true,"instructions":"mail a@b.com","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"call 13800138000"},{"type":"input_image","image_url":"data:image/png;base64,abcdef"}]}]}`, func(h *ChatHandler) http.Handler { return NewResponsesHandler(h) }, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mini, err := miniredis.Run()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(mini.Close)
			rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
			t.Cleanup(func() { _ = rdb.Close() })
			s, err := sanitize.NewSanitizer(sanitize.NewPatternDetector())
			if err != nil {
				t.Fatal(err)
			}
			mw, err := sanitize.NewSanitizeInputMiddleware(s, rdb, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			capture := &correlationCaptureExecutor{}
			h := NewChatHandler(nil, nil, nil, nil, nil, nil)
			h.SetSanitizeInputMiddleware(mw.Wrap)
			h.setRequestKeyVerifierForTest(durableEndpointVerifier{key: &authentication.KeyInfo{ID: 42, TenantID: "tenant-1", ApplicationID: 7}})
			h.provider = durableEndpointResolver{}
			h.executor = &executors.Executor{}
			h.SetRequestSurvival(func(string) bool { return true }, SurvivalOptions{})
			h.survivalAttemptExec = capture
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer sk-test")
			req.Header.Set("X-Gw-Session-Id", "gw_sanitize_integration")
			req.Header.Set("X-Gw-Tenant-Id", "tenant-1")
			rec := httptest.NewRecorder()
			tc.wrap(h).ServeHTTP(rec, req)
			if capture.params == nil {
				t.Fatalf("request never reached executor: status=%d body=%s", rec.Code, rec.Body.String())
			}
			p := capture.params
			if strings.Contains(string(p.BodyBytes), "13800138000") || strings.Contains(string(p.BodyBytes), "a@b.com") {
				t.Fatalf("raw PII reached provider body: %s", p.BodyBytes)
			}
			if !strings.Contains(string(p.BodyBytes), "{SENSITIVE:") {
				t.Fatalf("provider body missing sanitized placeholder: %s", p.BodyBytes)
			}
			if tc.path == "/v1/responses" && (strings.Contains(string(p.ResponsesBodyBytes), "13800138000") || strings.Contains(string(p.ResponsesBodyBytes), "a@b.com")) {
				t.Fatalf("native Responses body retained PII: %s", p.ResponsesBodyBytes)
			}
			info, ok := compression.SanitizeInfoFromContext(p.R.Context())
			if !ok || len(info.MessageRefs) != tc.refs {
				t.Fatalf("sanitize context/ref missing: ok=%v info=%+v", ok, info)
			}
			values, err := rdb.HGetAll(context.Background(), sanitize.SanitizeRedisKey(sanitize.HashTenant("tenant-1"), "gw_sanitize_integration")).Result()
			if err != nil || len(values) == 0 {
				t.Fatalf("session sanitize map missing: values=%v err=%v", values, err)
			}
		})
	}
}

func TestInvalidStaticKeyCannotPersistSanitizeMap(t *testing.T) {
	mini, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mini.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	defer rdb.Close()
	s, err := sanitize.NewSanitizer(sanitize.NewPatternDetector())
	if err != nil {
		t.Fatal(err)
	}
	mw, err := sanitize.NewSanitizeInputMiddleware(s, rdb, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	h.SetSanitizeInputMiddleware(mw.Wrap)
	h.SetStaticDataPlaneKey("sk-valid")
	h.provider = durableEndpointResolver{}
	h.executor = &executors.Executor{}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"call 13800138000"}]}`))
	req.Header.Set("Authorization", "Bearer sk-invalid")
	req.Header.Set("X-Gw-Session-Id", "gw_static_invalid")
	req.Header.Set("X-Gw-Tenant-Id", "victim")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	for _, tenantID := range []string{"default", "victim"} {
		values, err := rdb.HGetAll(context.Background(), sanitize.SanitizeRedisKey(sanitize.HashTenant(tenantID), "gw_static_invalid")).Result()
		if err != nil || len(values) != 0 {
			t.Fatalf("unauthorized request persisted map for %s: %v %v", tenantID, values, err)
		}
	}
}

func TestHandlerGeneratedSessionPersistsSanitizeMap(t *testing.T) {
	mini, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mini.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	defer rdb.Close()
	s, err := sanitize.NewSanitizer(sanitize.NewPatternDetector())
	if err != nil {
		t.Fatal(err)
	}
	mw, err := sanitize.NewSanitizeInputMiddleware(s, rdb, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	capture := &correlationCaptureExecutor{}
	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	h.SetSanitizeInputMiddleware(mw.Wrap)
	h.setRequestKeyVerifierForTest(durableEndpointVerifier{key: &authentication.KeyInfo{ID: 42, TenantID: "tenant-1", ApplicationID: 7}})
	h.provider = durableEndpointResolver{}
	h.executor = &executors.Executor{}
	h.SetRequestSurvival(func(string) bool { return true }, SurvivalOptions{})
	h.survivalAttemptExec = capture
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"m","stream":true,"input":"call 13800138000"}`))
	req.Header.Set("Authorization", "Bearer sk-test")
	rec := httptest.NewRecorder()
	NewResponsesHandler(h).ServeHTTP(rec, req)
	if capture.params == nil {
		t.Fatalf("request never reached executor: status=%d body=%s", rec.Code, rec.Body.String())
	}
	sessionID := rec.Header().Get("X-Gw-Session-Id")
	if sessionID == "" {
		t.Fatal("handler did not allocate a session ID")
	}
	values, err := rdb.HGetAll(context.Background(), sanitize.SanitizeRedisKey(sanitize.HashTenant("tenant-1"), sessionID)).Result()
	if err != nil || values["{SENSITIVE:phone:1}"] != "13800138000" {
		t.Fatalf("generated session map: %v %v", values, err)
	}
}

func TestProtocolHandlersBindSanitizeMapToVerifiedTenant(t *testing.T) {
	mini, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mini.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	defer rdb.Close()
	s, err := sanitize.NewSanitizer(sanitize.NewPatternDetector())
	if err != nil {
		t.Fatal(err)
	}
	mw, err := sanitize.NewSanitizeInputMiddleware(s, rdb, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	const sessionID = "gw_shared_sanitize_session"
	requests := []struct {
		path, tenant, spoofedTenant, phone, body string
		wrap                                     func(*ChatHandler) http.Handler
	}{
		{"/v1/messages", "tenant-A", "tenant-B", "13800138000", `{"model":"m","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"call 13800138000"}]}`, func(h *ChatHandler) http.Handler { return NewMessagesHandler(h) }},
		{"/v1/responses", "tenant-B", "", "13912345678", `{"model":"m","stream":true,"input":"call 13912345678"}`, func(h *ChatHandler) http.Handler { return NewResponsesHandler(h) }},
	}
	for _, tc := range requests {
		verifier := &countingSanitizeVerifier{durableEndpointVerifier: durableEndpointVerifier{key: &authentication.KeyInfo{ID: 42, TenantID: tc.tenant, ApplicationID: 7}}}
		capture := &correlationCaptureExecutor{}
		h := NewChatHandler(nil, nil, nil, nil, nil, nil)
		h.SetSanitizeInputMiddleware(mw.Wrap)
		h.setRequestKeyVerifierForTest(verifier)
		h.provider = durableEndpointResolver{}
		h.executor = &executors.Executor{}
		h.SetRequestSurvival(func(string) bool { return true }, SurvivalOptions{})
		h.survivalAttemptExec = capture
		req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer sk-test")
		req.Header.Set("X-Gw-Session-Id", sessionID)
		if tc.spoofedTenant != "" {
			req.Header.Set("X-Gw-Tenant-Id", tc.spoofedTenant)
		}
		rec := httptest.NewRecorder()
		tc.wrap(h).ServeHTTP(rec, req)
		if capture.params == nil || verifier.calls != 1 {
			t.Fatalf("tenant %s dispatch/verify: params=%v calls=%d status=%d", tc.tenant, capture.params, verifier.calls, rec.Code)
		}
		if strings.Contains(string(capture.params.BodyBytes), tc.phone) {
			t.Fatalf("raw phone reached executor for %s", tc.tenant)
		}
		key := sanitize.SanitizeRedisKey(sanitize.HashTenant(tc.tenant), sessionID)
		values, err := rdb.HGetAll(context.Background(), key).Result()
		if err != nil || values["{SENSITIVE:phone:1}"] != tc.phone {
			t.Fatalf("tenant-scoped map %q = %v, err=%v", key, values, err)
		}
	}
	// The same placeholder number must restore to the value owned by the
	// authenticated tenant, even when a caller forged the other tenant header.
	restore, err := sanitize.NewSanitizeRestoreInterceptor(s, rdb, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	chunk := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"{SENSITIVE:phone:1}\"}}]}\n\n")
	for _, tc := range requests {
		result, err := restore.InterceptStreamChunk(context.Background(), chunk, &response.StreamMeta{SessionID: sessionID, TenantID: tc.tenant})
		if err != nil || result == nil || !strings.Contains(string(result.ModifiedChunk), tc.phone) {
			t.Fatalf("tenant %s restore = %+v, err=%v", tc.tenant, result, err)
		}
	}
}
