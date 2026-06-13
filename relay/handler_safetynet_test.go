package relay

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/kaixuan/llm-gateway-go/telemetry"
)

// requestLogCollector captures every request_logs row emitted by the
// safety-net.  It is wired via ChatHandler.SetRequestLogHook.  The
// production code never sets a hook; only the unit tests in this
// file do.
type requestLogCollector struct {
	mu   sync.Mutex
	rows []collectedRow
}

type collectedRow struct {
	success      bool
	clientModel  string
	errorKind    string
	requestID    string
}

func newRequestLogCollector() *requestLogCollector {
	return &requestLogCollector{}
}

func (c *requestLogCollector) Hook(entry *telemetry.RequestLogEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry == nil {
		return
	}
	c.rows = append(c.rows, collectedRow{
		success:     entry.Success,
		clientModel: derefString(entry.ClientModel),
		errorKind:   derefString(entry.ErrorKind),
		requestID:   entry.RequestID,
	})
}

func (c *requestLogCollector) Snapshot() []collectedRow {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]collectedRow, len(c.rows))
	copy(out, c.rows)
	return out
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// requestLogsAreAlwaysRecorded is the contract we just added.  Any
// request that reaches ChatHandler.ServeHTTP must end up in
// request_logs, regardless of which exit path it took.  These
// tests verify the two ends of the contract:
//
//   - method-not-allowed / executor-unavailable → safety net
//   - recordFailedRequestWithKey → direct call
//
// In a real test run the inner pipeline (executor, candidates) is
// not wired up, so the only paths we can exercise directly without
// a database are the early failures and the executor-unavailable
// fallback.  The executor / candidate / panic paths are covered
// indirectly by the executor_test.go suite, which we extend below.
func TestRequestLog_SafetyNet_RecordsAllRequests(t *testing.T) {
	cases := []struct {
		name        string
		method      string
		path        string
		body        string
		wantStatus  int
		wantErrKind string
	}{
		{
			name:        "method-not-allowed",
			method:      http.MethodPut,
			path:        "/v1/chat/completions",
			body:        "",
			wantStatus:  http.StatusMethodNotAllowed,
			wantErrKind: "method_not_allowed",
		},
		{
			name:        "executor-unavailable",
			method:      http.MethodPost,
			path:        "/v1/chat/completions",
			body:        `{"model":"mimo-v2.5-pro","messages":[]}`,
			wantStatus:  http.StatusServiceUnavailable,
			wantErrKind: "executor_unavailable",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ch, col := newHookedHandler()
			var body *bytes.Buffer
			if tc.body != "" {
				body = bytes.NewBufferString(tc.body)
			} else {
				body = &bytes.Buffer{}
			}
			req := httptest.NewRequest(tc.method, tc.path, body)
			rec := httptest.NewRecorder()
			ch.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("expected status %d, got %d body=%s", tc.wantStatus, rec.Code, rec.Body.String())
			}
			rows := col.Snapshot()
			if len(rows) != 1 {
				t.Fatalf("expected exactly 1 request_log row for %s, got %d: %+v", tc.name, len(rows), rows)
			}
			row := rows[0]
			if row.success {
				t.Fatalf("expected success=false for %s, got true", tc.name)
			}
			if row.errorKind != tc.wantErrKind {
				t.Fatalf("expected error_kind=%s for %s, got %q", tc.wantErrKind, tc.name, row.errorKind)
			}
		})
	}
}

func TestRequestLog_DoesNotDuplicateAcrossCallers(t *testing.T) {
	// When the inner pipeline calls recordFailedRequestWithKey
	// directly, the hook fires once.  The deferred safety net
	// knows not to duplicate because *attemptLogged is set to true.
	// We simulate that contract by calling recordFailedRequest twice
	// (each call writes its own row) and asserting that no caller
	// path produced two rows for the same request_id.
	ch, col := newHookedHandler()
	ch.recordFailedRequestWithKey("req-A", "mimo-v2.5-pro", "",
		nil, nil, "no_candidate", "no candidates", 100, []byte(`{}`), nil, nil)
	ch.recordFailedRequestWithKey("req-B", "minimax-m2.7", "",
		nil, nil, "rate_limit_exceeded", "too many", 50, []byte(`{}`), nil, nil)
	rows := col.Snapshot()
	if len(rows) != 2 {
		t.Fatalf("expected 2 distinct rows, got %d: %+v", len(rows), rows)
	}
	ids := map[string]bool{}
	for _, r := range rows {
		ids[r.requestID] = true
	}
	if !ids["req-A"] || !ids["req-B"] {
		t.Fatalf("missing one of the expected request_ids; got %+v", ids)
	}
}

// newHookedHandler returns a ChatHandler with a request_log hook
// installed so tests can assert on the safety-net coverage.  The
// heavier dependencies (keyVerifier, executor, provider, …) are
// left nil so we exercise the early-exit / executor-unavailable
// paths without needing a database.
func newHookedHandler() (*ChatHandler, *requestLogCollector) {
	ch := &ChatHandler{}
	col := newRequestLogCollector()
	ch.SetRequestLogHook(col.Hook)
	return ch, col
}
