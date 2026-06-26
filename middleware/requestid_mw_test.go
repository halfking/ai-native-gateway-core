package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRequestIDMiddleware_AlwaysGeneratesFreshServerID is the regression
// guard for the 2026-06-26 cross-request pollution bug: the gateway used
// to reuse a client-supplied X-Request-Id verbatim, so a client that
// retried 5× with the same id produced 5 audit rows sharing one
// request_id (see bug report for 3875431e-9ba6-4e90-8b43-0d234f90d85d).
//
// The new contract: the middleware ALWAYS writes a server-generated
// UUID into X-Request-Id. The client value is preserved on
// X-Client-Request-Id for cross-system tracing only.
func TestRequestIDMiddleware_AlwaysGeneratesFreshServerID(t *testing.T) {
	mw := NewRequestIDMiddleware()

	// Scenario 1: client sends no X-Request-Id — server must generate one.
	req1 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	rec1 := httptest.NewRecorder()
	mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec1, req1)

	if got := rec1.Header().Get("X-Request-Id"); got == "" {
		t.Fatal("server should always set X-Request-Id on the response")
	}
	if got := req1.Header.Get("X-Request-Id"); got == "" {
		t.Fatal("server should set X-Request-Id on the inbound request for downstream handlers")
	}
	if len(req1.Header.Get("X-Request-Id")) != 32 {
		t.Fatalf("server id should be 32-hex (UUID-like), got %q (len=%d)",
			req1.Header.Get("X-Request-Id"), len(req1.Header.Get("X-Request-Id")))
	}

	// Scenario 2: client sends a custom X-Request-Id — server must IGNORE
	// it (a different id is written) and preserve the client value on
	// X-Client-Request-Id.
	req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req2.Header.Set("X-Request-Id", "3875431e-9ba6-4e90-8b43-0d234f90d85d")
	rec2 := httptest.NewRecorder()
	mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})).ServeHTTP(rec2, req2)

	serverID := req2.Header.Get("X-Request-Id")
	if serverID == "3875431e-9ba6-4e90-8b43-0d234f90d85d" {
		t.Fatal("middleware must NOT reuse client-supplied X-Request-Id as the primary request id")
	}
	if serverID == "" {
		t.Fatal("server should set X-Request-Id even when client provides one")
	}
	if rec2.Header().Get("X-Client-Request-Id") != "3875431e-9ba6-4e90-8b43-0d234f90d85d" {
		t.Fatalf("client id should be preserved on X-Client-Request-Id response header, got %q",
			rec2.Header().Get("X-Client-Request-Id"))
	}
	if req2.Header.Get("X-Gw-Client-Request-Id") != "3875431e-9ba6-4e90-8b43-0d234f90d85d" {
		t.Fatalf("client id should be forwarded on X-Gw-Client-Request-Id request header, got %q",
			req2.Header.Get("X-Gw-Client-Request-Id"))
	}
}

// TestRequestIDMiddleware_FiveRetriesProduceFiveDistinctIDs is the
// end-to-end shape of the original bug: 5 successive HTTP requests
// carrying the same client X-Request-Id must come out the back end
// as 5 different server ids. Without this fix, the 5 server ids were
// identical and the request_logs rows collapsed into 5 entries with
// the same primary request_id (the source of the cross-request
// pollution symptom).
func TestRequestIDMiddleware_FiveRetriesProduceFiveDistinctIDs(t *testing.T) {
	mw := NewRequestIDMiddleware()
	const clientID = "client-retry-1234"
	seen := map[string]int{}

	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		req.Header.Set("X-Request-Id", clientID)
		rec := httptest.NewRecorder()
		mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})).ServeHTTP(rec, req)

		serverID := req.Header.Get("X-Request-Id")
		if serverID == clientID {
			t.Fatalf("retry %d: server id %q must not equal client id", i, serverID)
		}
		if rec.Header().Get("X-Client-Request-Id") != clientID {
			t.Fatalf("retry %d: client id should be preserved on response", i)
		}
		seen[serverID]++
	}

	if len(seen) != 5 {
		t.Fatalf("expected 5 distinct server ids across 5 retries, got %d (counts=%v)", len(seen), seen)
	}
}

// TestRequestIDMiddleware_EmptyClientIDIsIgnored covers the edge case
// where a client (or proxy) sends X-Request-Id with only whitespace.
// Whitespace-only values must be treated as if no client id was sent.
func TestRequestIDMiddleware_EmptyClientIDIsIgnored(t *testing.T) {
	mw := NewRequestIDMiddleware()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("X-Request-Id", "   ")
	rec := httptest.NewRecorder()
	mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})).ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Client-Request-Id"); got != "" {
		t.Fatalf("whitespace-only client id must not be echoed on X-Client-Request-Id, got %q", got)
	}
	if got := req.Header.Get("X-Gw-Client-Request-Id"); got != "" {
		t.Fatalf("whitespace-only client id must not be forwarded, got %q", got)
	}
	if !strings.HasPrefix(req.Header.Get("X-Request-Id"), "") || req.Header.Get("X-Request-Id") == "" {
		t.Fatal("server id must always be set")
	}
}

// TestGenerateRequestID_UniqueUnderConcurrency sanity-checks the
// underlying generator under the workload that triggered the bug:
// concurrent retries from many goroutines. crypto/rand should give
// us effectively-unique ids even under high concurrency.
func TestGenerateRequestID_UniqueUnderConcurrency(t *testing.T) {
	const N = 1000
	seen := make(map[string]struct{}, N)
	ch := make(chan string, N)
	for i := 0; i < N; i++ {
		go func() { ch <- generateRequestID() }()
	}
	for i := 0; i < N; i++ {
		id := <-ch
		if _, dup := seen[id]; dup {
			t.Fatalf("collision after %d ids: %s", i, id)
		}
		seen[id] = struct{}{}
	}
}
