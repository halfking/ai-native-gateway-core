package sanitize

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"
)

// TestSessionSanitizeRedisKeyMatches pins the SC-1 cross-layer key contract:
// compression.SessionSanitizeRedisKey mirrors SanitizeRedisKey by necessity
// (sanitize imports compression; the reverse would cycle). If either side
// drifts, Invalidate cascades would delete the wrong keys and the L3
// SanitizeMapRef would point at nothing.
//
// T11-P0 v2: keys now include tenantHash; pin with explicit tenant to ensure
// both sides hash the same way.
func TestSessionSanitizeRedisKeyMatches(t *testing.T) {
	if got, want := compression.SessionSanitizeRedisKey("tA", "s1"), SanitizeRedisKey("tA", "s1"); got != want {
		t.Fatalf("map key drift: compression=%q sanitize=%q", got, want)
	}
	if got, want := compression.SessionSanitizeOffsetRedisKey("tA", "s1"), SanitizeOffsetRedisKey("tA", "s1"); got != want {
		t.Fatalf("offset key drift: compression=%q sanitize=%q", got, want)
	}
}

// TestMiddlewarePublishesSanitizeInfo verifies the SC-1 bridge: when the
// input middleware rewrites the body, the downstream handler must see BOTH
// the placeholder map (legacy context value) and the compression.SanitizeInfo
// bridge payload used to populate SessionState v8 L3 fields.
//
// T11-P0 v2: MapRef 必须包含 tenantHash；测试在 request 上设 X-Gw-Tenant-Id，
// 期望 middleware 与 mirror 函数一致。
func TestMiddlewarePublishesSanitizeInfo(t *testing.T) {
	mw, err := NewSanitizeInputMiddleware(mustSanitizer(t), nil, 0)
	if err != nil {
		t.Fatalf("middleware: %v", err)
	}

	var gotInfo compression.SanitizeInfo
	var infoOK bool
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotInfo, infoOK = compression.SanitizeInfoFromContext(r.Context())
		_, mapOK := SanitizeMapFromContext(r.Context())
		if !mapOK {
			t.Error("sanitize map missing from context")
		}
	})

	body := `{"model":"m","messages":[{"role":"user","content":"call me at 13800138000 or mail a@b.com"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("X-Gw-Session-Id", "sess-sc1")
	req.Header.Set("X-Gw-Tenant-Id", "tenant-sc1")
	rec := httptest.NewRecorder()
	mw.Wrap(next).ServeHTTP(rec, req)

	if !infoOK {
		t.Fatal("SanitizeInfo missing from context after sanitization")
	}
	wantRef := compression.SessionSanitizeRedisKey(HashTenant("tenant-sc1"), "sess-sc1")
	if gotInfo.MapRef != wantRef {
		t.Fatalf("MapRef = %q, want %q", gotInfo.MapRef, wantRef)
	}
	if gotInfo.Stats.PlaceholderCount != 2 {
		t.Fatalf("PlaceholderCount = %d, want 2 (phone + email)", gotInfo.Stats.PlaceholderCount)
	}
	if gotInfo.Stats.PhoneCount != 1 || gotInfo.Stats.EmailCount != 1 {
		t.Fatalf("typed counts = phone:%d email:%d, want 1/1", gotInfo.Stats.PhoneCount, gotInfo.Stats.EmailCount)
	}
	if gotInfo.Stats.SanitizedAt == 0 {
		t.Fatal("SanitizedAt must be stamped")
	}
}

// TestMiddlewareNoInfoWithoutSanitization: clean requests carry no info, so
// the compressor keeps the previous state's ref (forwarded by
// buildSessionState).
func TestMiddlewareNoInfoWithoutSanitization(t *testing.T) {
	mw, err := NewSanitizeInputMiddleware(mustSanitizer(t), nil, 0)
	if err != nil {
		t.Fatalf("middleware: %v", err)
	}
	var infoOK bool
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, infoOK = compression.SanitizeInfoFromContext(r.Context())
	})
	body := `{"model":"m","messages":[{"role":"user","content":"nothing sensitive here"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("X-Gw-Session-Id", "sess-clean")
	mw.Wrap(next).ServeHTTP(httptest.NewRecorder(), req)
	if infoOK {
		t.Fatal("SanitizeInfo must be absent when nothing was sanitized")
	}
}

func mustSanitizer(t *testing.T) *Sanitizer {
	t.Helper()
	s, err := NewSanitizer(NewPatternDetector())
	if err != nil {
		t.Fatalf("sanitizer: %v", err)
	}
	return s
}
