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
func TestSessionSanitizeRedisKeyMatches(t *testing.T) {
	if got, want := compression.SessionSanitizeRedisKey("s1"), SanitizeRedisKey("s1"); got != want {
		t.Fatalf("map key drift: compression=%q sanitize=%q", got, want)
	}
	if got, want := compression.SessionSanitizeOffsetRedisKey("s1"), SanitizeOffsetRedisKey("s1"); got != want {
		t.Fatalf("offset key drift: compression=%q sanitize=%q", got, want)
	}
}

// TestMiddlewarePublishesSanitizeInfo verifies the SC-1 bridge: when the
// input middleware rewrites the body, the downstream handler must see BOTH
// the placeholder map (legacy context value) and the compression.SanitizeInfo
// bridge payload used to populate SessionState v8 L3 fields.
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
	rec := httptest.NewRecorder()
	mw.Wrap(next).ServeHTTP(rec, req)

	if !infoOK {
		t.Fatal("SanitizeInfo missing from context after sanitization")
	}
	if gotInfo.MapRef != compression.SessionSanitizeRedisKey("sess-sc1") {
		t.Fatalf("MapRef = %q, want session:sess-sc1:sanitize", gotInfo.MapRef)
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
	if len(gotInfo.MessageRefs) != 1 {
		t.Fatalf("MessageRefs len = %d, want 1", len(gotInfo.MessageRefs))
	}
	ref := gotInfo.MessageRefs[0]
	if ref.RawIndex != 0 || ref.SanitizedIndex != 0 || !ref.Changed || ref.PlaceholderCount != 2 {
		t.Errorf("message provenance = %+v, want index 0/0 changed with 2 placeholders", ref)
	}
	if ref.RawHash == "" || ref.SanitizedHash == "" || ref.RawHash == ref.SanitizedHash {
		t.Errorf("message provenance hashes = raw:%q sanitized:%q, want distinct non-empty hashes", ref.RawHash, ref.SanitizedHash)
	}
	if strings.Contains(ref.RawHash, "13800138000") || strings.Contains(ref.RawHash, "a@b.com") {
		t.Fatal("message provenance hash must not contain original PII")
	}
}

func TestMiddlewarePublishesMessageLevelProvenanceForUnchangedMessages(t *testing.T) {
	mw, err := NewSanitizeInputMiddleware(mustSanitizer(t), nil, 0)
	if err != nil {
		t.Fatalf("middleware: %v", err)
	}

	var got compression.SanitizeInfo
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = compression.SanitizeInfoFromContext(r.Context())
	})
	body := `{"model":"m","messages":[{"role":"system","content":"plain"},{"role":"user","content":"call 13800138000"},{"role":"assistant","content":"reply"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("X-Gw-Session-Id", "sess-provenance")
	mw.Wrap(next).ServeHTTP(httptest.NewRecorder(), req)

	if len(got.MessageRefs) != 3 {
		t.Fatalf("MessageRefs len = %d, want one ref per message", len(got.MessageRefs))
	}
	if got.MessageRefs[0].Changed || got.MessageRefs[2].Changed {
		t.Fatalf("unchanged messages incorrectly marked changed: %+v", got.MessageRefs)
	}
	if !got.MessageRefs[1].Changed || got.MessageRefs[1].PlaceholderCount != 1 {
		t.Fatalf("sanitized message provenance = %+v", got.MessageRefs[1])
	}
	for i, ref := range got.MessageRefs {
		if ref.RawIndex != i || ref.SanitizedIndex != i || ref.RawHash == "" || ref.SanitizedHash == "" {
			t.Errorf("ref[%d] = %+v, want aligned non-empty fingerprints", i, ref)
		}
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
