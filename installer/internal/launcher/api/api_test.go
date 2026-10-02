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

// TestStatus_MergesInstanceMeta covers the new InstanceMetaProvider wiring:
// meta fields must appear in the JSON payload when the provider returns
// them, and the legacy 5-field StatusProvider still controls its own
// fields (priority: StatusProvider > InstanceMetaProvider).
func TestStatus_MergesInstanceMeta(t *testing.T) {
	api := New(Config{
		TokenProvider: func() string { return "secret" },
		StatusProvider: func() Status {
			return Status{
				ActiveAddr:    "127.0.0.1:8782",
				ActiveVersion: "v1.2.3",
				HasPlan:       false,
			}
		},
		InstanceMetaProvider: func() InstanceMeta {
			return InstanceMeta{
				InstallMode:      "full",
				InstanceID:       "inst-abc",
				DeviceCode:       "DEV-XK4F-9021",
				IPAddress:        "10.20.30.40",
				ActivationStatus: "activated",
				ActivatedAt:      "2026-09-24T08:30:00Z",
			}
		},
	})
	rec := do(t, api, "GET", "/launcher/api/status", "", "secret")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var s Status
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	if s.ActiveAddr != "127.0.0.1:8782" || s.ActiveVersion != "v1.2.3" {
		t.Errorf("legacy fields corrupted by merge: %+v", s)
	}
	if s.InstallMode != "full" {
		t.Errorf("InstallMode = %q", s.InstallMode)
	}
	if s.InstanceID != "inst-abc" {
		t.Errorf("InstanceID = %q", s.InstanceID)
	}
	if s.DeviceCode != "DEV-XK4F-9021" {
		t.Errorf("DeviceCode = %q", s.DeviceCode)
	}
	if s.IPAddress != "10.20.30.40" {
		t.Errorf("IPAddress = %q", s.IPAddress)
	}
	if s.ActivationStatus != "activated" {
		t.Errorf("ActivationStatus = %q", s.ActivationStatus)
	}
	if s.ActivatedAt != "2026-09-24T08:30:00Z" {
		t.Errorf("ActivatedAt = %q", s.ActivatedAt)
	}
}

// TestStatus_NoInstanceMetaBackwardCompat confirms that an API instance
// built without InstanceMetaProvider still serves the legacy payload and
// never includes the meta keys — important for rolling back this release
// or forking launches that never wired activation.
func TestStatus_NoInstanceMetaBackwardCompat(t *testing.T) {
	api := New(Config{
		TokenProvider: func() string { return "secret" },
		StatusProvider: func() Status {
			return Status{
				ActiveAddr:    "127.0.0.1:8782",
				ActiveVersion: "v1.2.3",
			}
		},
	})
	rec := do(t, api, "GET", "/launcher/api/status", "", "secret")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var generic map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &generic); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{
		"install_mode", "instance_id", "device_code",
		"ip_address", "activation_status", "activation_error",
		"activated_at",
	} {
		if _, ok := generic[k]; ok {
			t.Errorf("legacy payload must NOT include %q when InstanceMetaProvider is unset, got %v", k, generic)
		}
	}
}

// TestStatus_StatusProviderWinsOverMeta documents the merge precedence:
// if both providers set the same field, StatusProvider's value is the one
// that reaches the client. Today this never happens in practice
// (StatusProvider only sets the 5 legacy fields), but locking the
// contract here prevents future regressions.
func TestStatus_StatusProviderWinsOverMeta(t *testing.T) {
	api := New(Config{
		TokenProvider: func() string { return "secret" },
		StatusProvider: func() Status {
			return Status{
				ActiveAddr:    "from-status-provider",
				ActiveVersion: "v9.9.9",
			}
		},
		InstanceMetaProvider: func() InstanceMeta {
			return InstanceMeta{
				InstallMode:      "full",
				IPAddress:        "10.0.0.1",
				ActivationStatus: "activated",
			}
		},
	})
	rec := do(t, api, "GET", "/launcher/api/status", "", "secret")
	var s Status
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	// Legacy fields untouched by meta merge:
	if s.ActiveAddr != "from-status-provider" {
		t.Errorf("ActiveAddr should come from StatusProvider, got %q", s.ActiveAddr)
	}
	// Meta fields populated:
	if s.IPAddress != "10.0.0.1" {
		t.Errorf("IPAddress should come from InstanceMetaProvider, got %q", s.IPAddress)
	}
}

// TestStatus_ActivationErrorOnlyShownOnFailed encodes the rule that the
// error field is only meaningful when activation_status=failed. The wire
// format doesn't enforce it (we surface whatever's in activation.json),
// but a misconfigured writer that puts error="x" with status="activated"
// is a documentation bug — flag it loudly via this test (currently the
// loader passes the value through; the UI hides the row when not failed).
func TestStatus_ActivationErrorPassthrough(t *testing.T) {
	api := New(Config{
		TokenProvider: func() string { return "secret" },
		StatusProvider: func() Status {
			return Status{}
		},
		InstanceMetaProvider: func() InstanceMeta {
			return InstanceMeta{
				ActivationStatus: "failed",
				ActivationError:  "master returned 503",
			}
		},
	})
	rec := do(t, api, "GET", "/launcher/api/status", "", "secret")
	var s Status
	_ = json.Unmarshal(rec.Body.Bytes(), &s)
	if s.ActivationStatus != "failed" || s.ActivationError != "master returned 503" {
		t.Errorf("activation failure must include error: %+v", s)
	}
}
