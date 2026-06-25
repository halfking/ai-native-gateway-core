package session

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWithSession_EmptyHeader(t *testing.T) {
	mgr, _ := newTestManager(t)
	called := false
	h := WithSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}), mgr)
	r, _ := http.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if !called {
		t.Fatal("downstream handler not called")
	}
}

func TestWithSession_ValidSession(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()
	sess, _ := mgr.Create(ctx, 1, "t", "d")

	got := false
	h := WithSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = true
		// session ID should be set in context
		if SessionFromContext(r.Context()) == nil {
			t.Error("session not in context")
		}
	}), mgr)
	r, _ := http.NewRequest("GET", "/", nil)
	r.Header.Set("X-Session-Id", sess.SessionID)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if !got {
		t.Fatal("downstream not called")
	}
}

func TestWithSession_NotFoundNoAPIKey(t *testing.T) {
	mgr, _ := newTestManager(t)
	called := false
	h := WithSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}), mgr)
	r, _ := http.NewRequest("GET", "/", nil)
	r.Header.Set("X-Session-Id", "nonexistent")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if !called {
		t.Fatal("downstream not called (should fall through)")
	}
}

func TestWithSession_NotFoundWithAPIKey_Fallback(t *testing.T) {
	mgr, _ := newTestManager(t)
	called := false
	var resumeHeader string
	h := WithSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		resumeHeader = w.Header().Get("X-Gw-Session-Id-Resume")
	}), mgr)
	r, _ := http.NewRequest("GET", "/", nil)
	r.Header.Set("X-Session-Id", "old-id")
	// 注入 api key + tenant 到 ctx
	r = r.WithContext(SetAPIKeyID(r.Context(), 1))
	r = r.WithContext(SetTenantID(r.Context(), "t"))
	r.Header.Set("X-Device-Seed", "dev")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if !called {
		t.Fatal("downstream not called")
	}
	if resumeHeader == "" {
		t.Fatal("X-Gw-Session-Id-Resume header should be set on fallback")
	}
}

func TestWithSession_NotFoundWithAPIKey_NoDeviceSeed(t *testing.T) {
	mgr, _ := newTestManager(t)
	called := false
	h := WithSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}), mgr)
	r, _ := http.NewRequest("GET", "/", nil)
	r.Header.Set("X-Session-Id", "old-id")
	r = r.WithContext(SetAPIKeyID(r.Context(), 1))
	r = r.WithContext(SetTenantID(r.Context(), "t"))
	r.Header.Set("X-Machine-Id", "mach-1")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if !called {
		t.Fatal("downstream not called")
	}
}
