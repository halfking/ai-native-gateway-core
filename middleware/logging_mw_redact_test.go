package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestLoggingMiddleware_RedactsCredentialQueryParameters pins the log
// hygiene that the Gemini credential form forced.
//
// The Gemini native credential shape is `?key=<API_KEY>`
// (docs/vendor-formats/gemini.md §HTTP 形态). The access log records the
// raw query string ⇒ without redaction the gateway would write the client
// API key in cleartext into its rotated log files (and into the panic log
// via recovery_mw.go).
//
// The redaction must be surgical: every non-credential parameter stays
// byte-identical, otherwise operators lose the ability to read the log.
func TestLoggingMiddleware_RedactsCredentialQueryParameters(t *testing.T) {
	buf := captureSlog(t)
	mw := NewLoggingMiddleware()

	const secret = "AIzaSySUPERSECRET-VALUE-1234567890"
	req := httptest.NewRequest(http.MethodPost,
		"/v1beta/models/gemini-2.5-pro:generateContent?key="+secret+"&alt=sse", nil)
	rec := httptest.NewRecorder()
	mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)

	records := decodeRecords(t, buf)
	if len(records) != 1 {
		t.Fatalf("want 1 record, got %d", len(records))
	}
	q, ok := records[0]["query"].(string)
	if !ok {
		t.Fatalf("query field missing: %#v", records[0])
	}
	if strings.Contains(q, secret) {
		t.Fatalf("access log leaked the credential in cleartext: %q", q)
	}
	if !strings.Contains(q, "key=REDACTED") {
		t.Errorf("query = %q, want the key parameter redacted", q)
	}
	if !strings.Contains(q, "alt=sse") {
		t.Errorf("query = %q, want non-credential parameters preserved", q)
	}
}

// TestLoggingMiddleware_NonCredentialQueryIsUnchanged is the reverse nail:
// the redaction must not touch ordinary queries.
func TestLoggingMiddleware_NonCredentialQueryIsUnchanged(t *testing.T) {
	buf := captureSlog(t)
	mw := NewLoggingMiddleware()

	req := httptest.NewRequest(http.MethodGet, "/v1/models?model=gpt-4o&limit=10", nil)
	rec := httptest.NewRecorder()
	mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)

	records := decodeRecords(t, buf)
	if len(records) != 1 {
		t.Fatalf("want 1 record, got %d", len(records))
	}
	if q := records[0]["query"].(string); q != "model=gpt-4o&limit=10" {
		t.Errorf("query = %q, want %q", q, "model=gpt-4o&limit=10")
	}
}
