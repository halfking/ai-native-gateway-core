package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/installer/internal/launcher/store"
)

func do(t *testing.T, api *API, method, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	var bodyReader *strings.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	} else {
		bodyReader = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, bodyReader)
	if token != "" {
		req.Header.Set("X-Launcher-Token", token)
	}
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, req)
	return rec
}

func TestStatusRequiresToken(t *testing.T) {
	api := New(Config{
		TokenProvider:  func() string { return "secret" },
		StatusProvider: func() Status { return Status{ActiveAddr: "127.0.0.1:8782"} },
	})

	// No token → 401
	rec := do(t, api, "GET", "/launcher/api/status", "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", rec.Code)
	}

	// Wrong token → 401
	rec = do(t, api, "GET", "/launcher/api/status", "", "wrong")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with wrong token, got %d", rec.Code)
	}

	// Right token → 200 + body
	rec = do(t, api, "GET", "/launcher/api/status", "", "secret")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var s Status
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	if s.ActiveAddr != "127.0.0.1:8782" {
		t.Fatalf("expected active 127.0.0.1:8782, got %s", s.ActiveAddr)
	}
}

func TestAuthDisabledWhenTokenEmpty(t *testing.T) {
	api := New(Config{
		TokenProvider:  func() string { return "" },
		StatusProvider: func() Status { return Status{ActiveAddr: "x"} },
	})
	rec := do(t, api, "GET", "/launcher/api/status", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 when token disabled, got %d", rec.Code)
	}
}

func TestApplyRequiresConfirmFlag(t *testing.T) {
	called := false
	var confirmedSeen bool
	api := New(Config{
		TokenProvider:  func() string { return "secret" },
		StatusProvider: func() Status { return Status{} },
		PlanProvider: func() *store.Plan {
			return &store.Plan{ID: "p1", State: store.StatePrepared}
		},
		ApplyFunc: func(planID string, confirmed bool) error {
			called = true
			confirmedSeen = confirmed
			return nil
		},
	})

	// No confirm → 200 with confirm_required, ApplyFunc NOT called
	rec := do(t, api, "POST", "/launcher/api/plan/p1/apply", `{}`, "secret")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (confirm prompt), got %d", rec.Code)
	}
	if called {
		t.Fatal("ApplyFunc should not be called without confirm")
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["status"] != "confirm_required" {
		t.Fatalf("expected confirm_required, got %v", resp["status"])
	}

	// With confirm → ApplyFunc called
	rec = do(t, api, "POST", "/launcher/api/plan/p1/apply", `{"confirm":true}`, "secret")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !called || !confirmedSeen {
		t.Fatal("ApplyFunc should be called with confirmed=true")
	}
}

func TestPrepareCallsPrepareFunc(t *testing.T) {
	var gotPlanID string
	api := New(Config{
		TokenProvider: func() string { return "secret" },
		PrepareFunc: func(planID string) (*store.Plan, error) {
			gotPlanID = planID
			return &store.Plan{ID: planID, State: store.StatePreparing}, nil
		},
	})
	rec := do(t, api, "POST", "/launcher/api/plan/abc/prepare", ``, "secret")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
	}
	if gotPlanID != "abc" {
		t.Fatalf("expected planID abc, got %s", gotPlanID)
	}
}

func TestPlanEndpointReturnsNilWhenNoPlan(t *testing.T) {
	api := New(Config{
		TokenProvider: func() string { return "secret" },
		PlanProvider:  func() *store.Plan { return nil },
	})
	rec := do(t, api, "GET", "/launcher/api/plan", "", "secret")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	// Should be valid JSON (null or {})
	if rec.Body.String() != "null\n" {
		t.Fatalf("expected null, got %q", rec.Body.String())
	}
}

func TestExtractPlanID(t *testing.T) {
	cases := []struct {
		path, suffix, want string
	}{
		{"/launcher/api/plan/p1/apply", "/apply", "p1"},
		{"/launcher/api/plan/p1/rollback", "/rollback", "p1"},
		{"/launcher/api/plan/abc-def_123/prepare", "/prepare", "abc-def_123"},
	}
	for _, c := range cases {
		got := extractPlanID(c.path, c.suffix)
		if got != c.want {
			t.Errorf("extractPlanID(%q, %q) = %q, want %q", c.path, c.suffix, got, c.want)
		}
	}
}

func TestUIPathNotAuthRequired(t *testing.T) {
	api := New(Config{
		TokenProvider:  func() string { return "secret" },
		StatusProvider: func() Status { return Status{} },
	})
	// /launcher/ should serve UI without token (operator loads page, enters token in JS)
	rec := do(t, api, "GET", "/launcher/", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for UI without token, got %d", rec.Code)
	}
}

func TestNotFound(t *testing.T) {
	api := New(Config{
		TokenProvider:  func() string { return "secret" },
		StatusProvider: func() Status { return Status{} },
	})
	rec := do(t, api, "GET", "/launcher/api/nonexistent", "", "secret")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}
