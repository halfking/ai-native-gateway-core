package streaming

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestInitializeRequestIdentityMiddlewareFilled covers scenario A:
// the RequestIDMiddleware chain has already populated X-Request-Id
// (and the canonical client request id). InitializeRequestIdentity
// MUST honour the inbound values verbatim and still mirror
// X-Request-Id onto the response header so the client can correlate.
func TestInitializeRequestIdentityMiddlewareFilled(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("X-Request-Id", "server-issued-123")
	req.Header.Set("X-Gw-Client-Request-Id", "client-corr-abc")

	w := httptest.NewRecorder()

	got := InitializeRequestIdentity(req, w)

	if got.RequestID != "server-issued-123" {
		t.Fatalf("RequestID overwritten by helper: got %q want %q", got.RequestID, "server-issued-123")
	}
	if got.ClientRequestID != "client-corr-abc" {
		t.Fatalf("ClientRequestID mismatch: got %q want %q", got.ClientRequestID, "client-corr-abc")
	}
	if got.TenantID != "default" {
		t.Fatalf("TenantID default contract broken: got %q", got.TenantID)
	}
	if w.Header().Get("X-Request-Id") != "server-issued-123" {
		t.Fatalf("response X-Request-Id not mirrored: got %q", w.Header().Get("X-Request-Id"))
	}
	if w.Header().Get("X-Client-Request-Id") != "client-corr-abc" {
		t.Fatalf("response X-Client-Request-Id not surfaced: got %q", w.Header().Get("X-Client-Request-Id"))
	}
}

// TestInitializeRequestIdentityDirectInvocation covers scenario B:
// middleware was bypassed entirely (e.g. direct unit-test invocation
// against the handler). The helper MUST still produce a stable,
// non-empty request_id and surface it on the response header. A
// second call against the same request must keep the same id
// (idempotency under direct invocation).
func TestInitializeRequestIdentityDirectInvocation(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	w := httptest.NewRecorder()

	first := InitializeRequestIdentity(req, w)
	second := InitializeRequestIdentity(req, w)

	if first.RequestID == "" {
		t.Fatalf("RequestID must be generated when middleware is bypassed")
	}
	if first.SessionID == "" {
		t.Fatalf("SessionID must be generated when middleware is bypassed")
	}
	if !startsWith(first.SessionID, "gw_") {
		t.Fatalf("provisional SessionID must be gw_-prefixed: got %q", first.SessionID)
	}
	if first.RequestID != second.RequestID {
		t.Fatalf("RequestID must be stable across calls: first=%q second=%q",
			first.RequestID, second.RequestID)
	}
	if w.Header().Get("X-Request-Id") != first.RequestID {
		t.Fatalf("response X-Request-Id should reflect generated id: got %q want %q",
			w.Header().Get("X-Request-Id"), first.RequestID)
	}
	// Ensure that even on a fresh helper call the response header is
	// consistent — a regression here would mean the helper sometimes
	// forgets to mirror, which external proxies can observe.
	if got := w.Header().Get("X-Request-Id"); got != second.RequestID {
		t.Fatalf("response header drifted on second call: got %q want %q",
			got, second.RequestID)
	}
}

// TestInitializeRequestIdentityClientRequestIDCoexists covers scenario C:
// the client supplies BOTH the canonical X-Gw-Client-Request-Id AND
// the legacy X-Client-Request-Id. The helper must prefer the
// canonical value and surface only that on the response. A separate
// case covers legacy-only requests — the helper must echo the legacy
// value back so old clients still see correlation.
func TestInitializeRequestIdentityClientRequestIDCoexists(t *testing.T) {
	t.Run("canonical_wins_over_legacy", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		req.Header.Set("X-Gw-Client-Request-Id", "canonical-id")
		req.Header.Set("X-Client-Request-Id", "legacy-id")
		w := httptest.NewRecorder()

		got := InitializeRequestIdentity(req, w)

		if got.ClientRequestID != "canonical-id" {
			t.Fatalf("canonical header should win: got %q", got.ClientRequestID)
		}
		if w.Header().Get("X-Client-Request-Id") != "canonical-id" {
			t.Fatalf("response X-Client-Request-Id should mirror canonical: got %q",
				w.Header().Get("X-Client-Request-Id"))
		}
	})

	t.Run("legacy_only_echoed_back", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		req.Header.Set("X-Client-Request-Id", "legacy-only-id")
		w := httptest.NewRecorder()

		got := InitializeRequestIdentity(req, w)

		if got.ClientRequestID != "legacy-only-id" {
			t.Fatalf("legacy fallback not honoured: got %q", got.ClientRequestID)
		}
		if w.Header().Get("X-Client-Request-Id") != "legacy-only-id" {
			t.Fatalf("response should echo legacy id: got %q",
				w.Header().Get("X-Client-Request-Id"))
		}
	})

	t.Run("empty_when_client_omits_both", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		w := httptest.NewRecorder()

		got := InitializeRequestIdentity(req, w)

		if got.ClientRequestID != "" {
			t.Fatalf("ClientRequestID should be empty when no client header sent: got %q",
				got.ClientRequestID)
		}
		if w.Header().Get("X-Client-Request-Id") != "" {
			t.Fatalf("response should NOT contain X-Client-Request-Id when client sent nothing: got %q",
				w.Header().Get("X-Client-Request-Id"))
		}
	})
}

// TestInitializeRequestIdentityProvisionalSessionDoesNotWriteResponse
// locks down the explicit non-write contract: even when the helper
// generates a provisional gw_<uuid>, it MUST NOT surface it on the
// response header. The full session assignment pipeline owns that
// decision.
func TestInitializeRequestIdentityProvisionalSessionDoesNotWriteResponse(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	w := httptest.NewRecorder()

	got := InitializeRequestIdentity(req, w)

	if got.SessionID == "" {
		t.Fatalf("provisional session id must be generated")
	}
	if w.Header().Get("X-Gw-Session-Id") != "" {
		t.Fatalf("helper must NOT write X-Gw-Session-Id to response: got %q",
			w.Header().Get("X-Gw-Session-Id"))
	}
}

// startsWith is a tiny local helper to avoid importing strings just
// for one prefix check.
func startsWith(s, prefix string) bool {
	if len(prefix) > len(s) {
		return false
	}
	return s[:len(prefix)] == prefix
}
