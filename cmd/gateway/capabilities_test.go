// capabilities_test.go pins the GW-0.2 contract for GET /api/v2/capabilities
// (docs/全面优化v1/API-DETAILS.md §5): response shape, honest feature
// labelling, and GET-only semantics.
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCapabilitiesHandler_Get(t *testing.T) {
	handler := NewCapabilitiesHandler("2.5.0-test", ":8781")

	req := httptest.NewRequest(http.MethodGet, "/api/v2/capabilities", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}

	var body struct {
		Service  string            `json:"service"`
		Version  string            `json:"version"`
		Status   string            `json:"status"`
		Features map[string]string `json:"features"`
		Ports    map[string]int    `json:"ports"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v; body: %s", err, rec.Body.String())
	}

	if body.Service != "llm-gateway-go" {
		t.Errorf("service = %q, want llm-gateway-go", body.Service)
	}
	if body.Version != "2.5.0-test" {
		t.Errorf("version = %q, want 2.5.0-test", body.Version)
	}
	if body.Status != "native" {
		t.Errorf("status = %q, want native", body.Status)
	}

	// Feature labels must mirror the audit matrix in
	// docs/全面优化v1/README.md §二 — current facts only, no target inflation.
	wantFeatures := map[string]string{
		"data_plane":           "current",
		"sticky_session":       "current",
		"tenant_quota":         "current",
		"durable_outbox":       "partial",
		"plugin_runtime":       "partial",
		"webhook_subscription": "planned",
	}
	for feature, want := range wantFeatures {
		if got, ok := body.Features[feature]; !ok {
			t.Errorf("features missing %q", feature)
		} else if got != want {
			t.Errorf("features[%q] = %q, want %q", feature, got, want)
		}
	}
	if len(body.Features) != len(wantFeatures) {
		t.Errorf("features has %d entries, want %d (unexpected extras: %v)", len(body.Features), len(wantFeatures), body.Features)
	}

	if body.Ports["primary"] != 8781 {
		t.Errorf("ports.primary = %d, want 8781", body.Ports["primary"])
	}
	if _, ok := body.Ports["gateway_v2_demo"]; !ok {
		t.Error("ports.gateway_v2_demo missing (demo plane must be labelled separately from primary)")
	}
}

func TestCapabilitiesHandler_MethodNotAllowed(t *testing.T) {
	handler := NewCapabilitiesHandler("v", ":8781")

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(method, "/api/v2/capabilities", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s status = %d, want %d", method, rec.Code, http.StatusMethodNotAllowed)
		}
	}
}

func TestParseListenPort(t *testing.T) {
	cases := []struct {
		listen string
		want   int
	}{
		{":8781", 8781},
		{"0.0.0.0:8781", 8781},
		{"127.0.0.1:9000", 9000},
		{"", 8781},          // default primary port
		{"garbage", 8781},   // unparseable → documented default
		{":notaport", 8781}, // unparseable → documented default
	}
	for _, tc := range cases {
		if got := parseListenPort(tc.listen); got != tc.want {
			t.Errorf("parseListenPort(%q) = %d, want %d", tc.listen, got, tc.want)
		}
	}
}
