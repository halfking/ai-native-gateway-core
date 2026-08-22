package session

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestWithSession_GwHeaderPreferred covers scenario G-ID-1 v2
// wrapper priority: when BOTH X-Gw-Session-Id and X-Session-Id are
// supplied, the canonical X-Gw-Session-Id wins. Both header values
// must point at real sessions in the manager so we can prove the
// resolution follows the canonical id, not the legacy one.
func TestWithSession_GwHeaderPreferred(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()

	gwSess, err := mgr.Create(ctx, 1, "t", "device-gw")
	if err != nil {
		t.Fatalf("create gw session: %v", err)
	}
	legacySess, err := mgr.Create(ctx, 1, "t", "device-legacy")
	if err != nil {
		t.Fatalf("create legacy session: %v", err)
	}

	var seenSessionID string
	h := WithSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s := SessionFromContext(r.Context())
		if s == nil {
			t.Fatal("session not injected into context")
			return
		}
		seenSessionID = s.SessionID
	}), mgr)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Gw-Session-Id", gwSess.SessionID)
	r.Header.Set("X-Session-Id", legacySess.SessionID)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, r)

	if seenSessionID != gwSess.SessionID {
		t.Fatalf("X-Gw-Session-Id must win; got %q want %q", seenSessionID, gwSess.SessionID)
	}
	if seenSessionID == legacySess.SessionID {
		t.Fatalf("legacy X-Session-Id should NOT have been used: %q", seenSessionID)
	}
	if got := w.Header().Get("X-Gw-Session-Id"); got != gwSess.SessionID {
		t.Fatalf("response X-Gw-Session-Id: got %q want %q", got, gwSess.SessionID)
	}
}

// TestWithSession_LegacyFallback covers the legacy X-Session-Id
// fallback path: when X-Gw-Session-Id is empty and X-Session-Id
// points at a real session, the middleware MUST resolve and inject
// it. Both header values being absent is exercised by
// TestWithSession_EmptyHeader in middleware_test.go — here we focus
// on the legacy hit path.
func TestWithSession_LegacyFallback(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()

	legacySess, err := mgr.Create(ctx, 1, "t", "legacy-device")
	if err != nil {
		t.Fatalf("create legacy session: %v", err)
	}

	var seenSessionID string
	h := WithSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s := SessionFromContext(r.Context())
		if s == nil {
			t.Fatal("legacy session not injected into context")
			return
		}
		seenSessionID = s.SessionID
	}), mgr)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Session-Id", legacySess.SessionID)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, r)

	if seenSessionID != legacySess.SessionID {
		t.Fatalf("legacy X-Session-Id fallback failed: got %q want %q",
			seenSessionID, legacySess.SessionID)
	}
	// 2026-07-28 (request-flow audit §10 Step 2): legacy hit must also
	// surface X-Gw-Session-Id-Resume so operators can see the resolved
	// id on the response.
	if got := w.Header().Get("X-Gw-Session-Id-Resume"); got != legacySess.SessionID {
		t.Fatalf("legacy hit should write X-Gw-Session-Id-Resume: got %q want %q",
			got, legacySess.SessionID)
	}
	if got := w.Header().Get("X-Gw-Session-Id"); got != legacySess.SessionID {
		t.Fatalf("response X-Gw-Session-Id should mirror legacy hit: got %q want %q",
			got, legacySess.SessionID)
	}
}

// TestWithSession_ResumeHeaderOnValidHit locks down the new
// behaviour: when a valid session id is supplied (canonical OR
// legacy), the middleware writes X-Gw-Session-Id-Resume equal to
// the resolved id. This is the v2 wrapper priority change requested
// in the audit spec.
func TestWithSession_ResumeHeaderOnValidHit(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()

	sess, err := mgr.Create(ctx, 1, "t", "device-x")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	t.Run("canonical_header_writes_resume", func(t *testing.T) {
		h := WithSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), mgr)
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("X-Gw-Session-Id", sess.SessionID)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)

		if got := w.Header().Get("X-Gw-Session-Id-Resume"); got != sess.SessionID {
			t.Fatalf("canonical hit should set X-Gw-Session-Id-Resume: got %q want %q",
				got, sess.SessionID)
		}
	})

	t.Run("legacy_header_writes_resume", func(t *testing.T) {
		h := WithSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), mgr)
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("X-Session-Id", sess.SessionID)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)

		if got := w.Header().Get("X-Gw-Session-Id-Resume"); got != sess.SessionID {
			t.Fatalf("legacy hit should set X-Gw-Session-Id-Resume: got %q want %q",
				got, sess.SessionID)
		}
	})
}

// TestWithSession_NoResumeHeaderOnUnknown verifies the negative:
// when neither header points at a real session, X-Gw-Session-Id-Resume
// must NOT be written. This protects against accidentally surfacing
// an empty / stale id.
func TestWithSession_NoResumeHeaderOnUnknown(t *testing.T) {
	mgr, _ := newTestManager(t)

	h := WithSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), mgr)

	t.Run("gw_header_unknown", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("X-Gw-Session-Id", "does-not-exist")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if got := w.Header().Get("X-Gw-Session-Id-Resume"); got != "" {
			t.Fatalf("unknown X-Gw-Session-Id must NOT write resume header: got %q", got)
		}
	})

	t.Run("legacy_unknown_without_apikey", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("X-Session-Id", "does-not-exist")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if got := w.Header().Get("X-Gw-Session-Id-Resume"); got != "" {
			t.Fatalf("unknown legacy id without apikey must NOT write resume header: got %q", got)
		}
	})
}

// TestWithSession_FallbackResumeHeader covers the existing fallback
// path: X-Session-Id is supplied but does not exist, api key + tenant
// are in context, so the middleware creates a fresh session and
// surfaces the NEW id on X-Gw-Session-Id-Resume. This is unchanged
// from the previous behaviour but is locked down by a dedicated test
// so the v2 wrapper priority change does not regress it.
func TestWithSession_FallbackResumeHeader(t *testing.T) {
	mgr, _ := newTestManager(t)

	var seenSessionID string
	h := WithSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s := SessionFromContext(r.Context())
		if s == nil {
			t.Fatal("fresh session not injected")
			return
		}
		seenSessionID = s.SessionID
	}), mgr)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Session-Id", "missing-id")
	r = r.WithContext(SetAPIKeyID(r.Context(), 1))
	r = r.WithContext(SetTenantID(r.Context(), "tenant-x"))
	r.Header.Set("X-Device-Seed", "device-y")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, r)

	if seenSessionID == "" {
		t.Fatal("fresh session id should be non-empty")
	}
	if seenSessionID == "missing-id" {
		t.Fatal("fresh session id must NOT equal the legacy input id")
	}
	if got := w.Header().Get("X-Gw-Session-Id-Resume"); got != seenSessionID {
		t.Fatalf("X-Gw-Session-Id-Resume should mirror fresh session: got %q want %q",
			got, seenSessionID)
	}
	if got := w.Header().Get("Deprecation"); got != "true" {
		t.Fatalf("legacy fallback should set Deprecation header: got %q", got)
	}
}
