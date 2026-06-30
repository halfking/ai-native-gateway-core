package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// captureSlog swaps slog.Default() with a JSON handler writing
// to a buffer for the duration of the test, then restores the
// original. Returns the buffer so the test can decode the
// captured records.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	leveler := new(slog.LevelVar)
	leveler.Set(slog.LevelDebug)
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: leveler,
	})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// decodeRecords parses newline-delimited JSON records from the
// capture buffer. Order matches emission order.
func decodeRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("invalid JSON in log buffer: %v\nline=%s", err, line)
		}
		out = append(out, rec)
	}
	return out
}

// TestLoggingMiddleware_HappyPath covers the operator-spec fields
// for a 2xx request: request_id, method, path, route, status,
// status_text, duration_ms, remote, user_agent, response_bytes.
func TestLoggingMiddleware_HappyPath(t *testing.T) {
	buf := captureSlog(t)
	mw := NewLoggingMiddleware()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions?model=gpt-4o", strings.NewReader("hello"))
	req.Header.Set("X-Request-Id", "req-123")
	req.Header.Set("X-Gw-Client-Request-Id", "client-456")
	req.Header.Set("User-Agent", "test-client/1.0")
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = 5

	rec := httptest.NewRecorder()
	mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	})).ServeHTTP(rec, req)

	records := decodeRecords(t, buf)
	if len(records) != 1 {
		t.Fatalf("want 1 log record, got %d: %v", len(records), records)
	}
	r := records[0]
	if r["msg"] != "http_request" {
		t.Errorf("msg = %v, want http_request", r["msg"])
	}
	if r["level"] != "INFO" {
		t.Errorf("level = %v, want INFO", r["level"])
	}
	if r["request_id"] != "req-123" {
		t.Errorf("request_id = %v, want req-123", r["request_id"])
	}
	if r["client_request_id"] != "client-456" {
		t.Errorf("client_request_id = %v, want client-456", r["client_request_id"])
	}
	if r["method"] != "POST" {
		t.Errorf("method = %v, want POST", r["method"])
	}
	if r["path"] != "/v1/chat/completions" {
		t.Errorf("path = %v", r["path"])
	}
	if r["route"] != "/v1/chat/completions" {
		t.Errorf("route = %v", r["route"])
	}
	if r["query"] != "model=gpt-4o" {
		t.Errorf("query = %v", r["query"])
	}
	if r["status"].(float64) != 200 {
		t.Errorf("status = %v, want 200", r["status"])
	}
	if r["status_text"] != "OK" {
		t.Errorf("status_text = %v, want OK", r["status_text"])
	}
	if r["response_bytes"].(float64) != 11 {
		t.Errorf("response_bytes = %v, want 11 (len(`{\"ok\":true}`))", r["response_bytes"])
	}
	if r["user_agent"] != "test-client/1.0" {
		t.Errorf("user_agent = %v", r["user_agent"])
	}
	if r["request_bytes"].(float64) != 5 {
		t.Errorf("request_bytes = %v, want 5 (Content-Length)", r["request_bytes"])
	}
}

// TestLoggingMiddleware_4xxIsWarn verifies that 4xx responses
// produce WARN records with an `error.kind` so log queries
// can pivot on kind directly.
func TestLoggingMiddleware_4xxIsWarn(t *testing.T) {
	buf := captureSlog(t)
	mw := NewLoggingMiddleware()

	req := httptest.NewRequest(http.MethodGet, "/api/missing", nil)
	rec := httptest.NewRecorder()
	mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})).ServeHTTP(rec, req)

	records := decodeRecords(t, buf)
	if len(records) != 1 {
		t.Fatalf("want 1 record, got %d", len(records))
	}
	r := records[0]
	if r["level"] != "WARN" {
		t.Errorf("level = %v, want WARN", r["level"])
	}
	if r["status"].(float64) != 404 {
		t.Errorf("status = %v, want 404", r["status"])
	}
	if r["error.kind"] != "not_found" {
		t.Errorf("error.kind = %v, want not_found", r["error.kind"])
	}
}

// TestLoggingMiddleware_5xxIsError verifies that 5xx responses
// produce ERROR records (the recovery middleware additionally
// emits the panic stack; this test covers the "no panic, just
// 500" case).
func TestLoggingMiddleware_5xxIsError(t *testing.T) {
	buf := captureSlog(t)
	mw := NewLoggingMiddleware()

	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", nil)
	rec := httptest.NewRecorder()
	mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})).ServeHTTP(rec, req)

	records := decodeRecords(t, buf)
	if len(records) != 1 {
		t.Fatalf("want 1 record, got %d", len(records))
	}
	r := records[0]
	if r["level"] != "ERROR" {
		t.Errorf("level = %v, want ERROR", r["level"])
	}
	if r["error.kind"] != "server_error" {
		t.Errorf("error.kind = %v, want server_error", r["error.kind"])
	}
}

// TestLoggingMiddleware_BypassHealthz verifies that probe paths
// do NOT produce a log record — otherwise /healthz and /metrics
// would dominate the log file and burn the 1GB rotation budget
// in minutes.
func TestLoggingMiddleware_BypassHealthz(t *testing.T) {
	buf := captureSlog(t)
	mw := NewLoggingMiddleware()

	for _, path := range []string{"/healthz", "/metrics", "/"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})).ServeHTTP(rec, req)
	}
	if buf.Len() != 0 {
		t.Fatalf("bypass paths must not log; got %d bytes: %s", buf.Len(), buf.String())
	}
}

// TestLoggingMiddleware_CompactQuery truncates oversized query
// strings to the 512-byte cap so a single record cannot blow
// the JSON log line into multiple physical lines.
func TestLoggingMiddleware_CompactQuery(t *testing.T) {
	buf := captureSlog(t)
	mw := NewLoggingMiddleware()

	// Build a query string just over the 512-byte cap.
	big := strings.Repeat("a", 600)
	req := httptest.NewRequest(http.MethodGet, "/v1/models?"+big, nil)
	rec := httptest.NewRecorder()
	mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)

	records := decodeRecords(t, buf)
	if len(records) != 1 {
		t.Fatalf("want 1 record, got %d", len(records))
	}
	q := records[0]["query"].(string)
	// The cap is 512 BYTES plus the 3-byte "…" suffix. The input
	// is pure ASCII, so the result is exactly 515 bytes.
	if len(q) != 515 {
		t.Errorf("compact query byte-length = %d, want 515 (512 + 3-byte '…')", len(q))
	}
	if !strings.HasSuffix(q, "…") {
		t.Errorf("compact query should end with …, got %q", q)
	}
}

// TestRecoveryMiddleware_PanicIsLogged ensures a recovered
// panic emits an ERROR record with the full stack and request
// context, so ops can pivot from "5xx at 02:14" to the panic
// cause in one log query.
func TestRecoveryMiddleware_PanicIsLogged(t *testing.T) {
	buf := captureSlog(t)
	mw := NewRecoveryMiddleware()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions?stream=true", nil)
	req.Header.Set("X-Request-Id", "rid-99")
	req.Header.Set("X-Gw-Client-Request-Id", "cid-99")
	rec := httptest.NewRecorder()

	mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("kaboom")
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("panic should produce 500, got %d", rec.Code)
	}

	records := decodeRecords(t, buf)
	if len(records) != 1 {
		t.Fatalf("want 1 record, got %d", len(records))
	}
	r := records[0]
	if r["msg"] != "panic_recovered" {
		t.Errorf("msg = %v, want panic_recovered", r["msg"])
	}
	if r["level"] != "ERROR" {
		t.Errorf("level = %v, want ERROR", r["level"])
	}
	if r["error.message"] != "kaboom" {
		t.Errorf("error.message = %v, want kaboom", r["error.message"])
	}
	if r["error.kind"] != "panic" {
		t.Errorf("error.kind = %v, want panic", r["error.kind"])
	}
	if !strings.Contains(r["stack"].(string), "panic") {
		t.Errorf("stack should mention panic, got %q", r["stack"])
	}
	if r["request_id"] != "rid-99" {
		t.Errorf("request_id = %v", r["request_id"])
	}
	if r["method"] != "POST" {
		t.Errorf("method = %v", r["method"])
	}
	if r["path"] != "/v1/chat/completions" {
		t.Errorf("path = %v", r["path"])
	}
}

// TestRecoveryMiddleware_NoPanicIsSilent ensures the recovery
// middleware does NOT emit any record on the happy path — the
// logging middleware already covers that.
func TestRecoveryMiddleware_NoPanicIsSilent(t *testing.T) {
	buf := captureSlog(t)
	mw := NewRecoveryMiddleware()

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)

	if buf.Len() != 0 {
		t.Fatalf("happy path should not log; got %s", buf.String())
	}
}

// TestLoggingMiddleware_PropagatesContext ensures slog records
// carry the request's context (so future helpers can attach
// trace/tenant attributes via slog.Default).
func TestLoggingMiddleware_PropagatesContext(t *testing.T) {
	buf := captureSlog(t)
	mw := NewLoggingMiddleware()

	type ctxKey struct{}
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	ctx := context.WithValue(req.Context(), ctxKey{}, "marker")
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Context().Value(ctxKey{}) != "marker" {
			t.Errorf("context not propagated through middleware chain")
		}
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)

	if buf.Len() == 0 {
		t.Fatalf("no log record emitted")
	}
}
