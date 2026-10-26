package pluginruntime

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCapabilityRegistry_DefaultsAndRequired(t *testing.T) {
	reg := NewCapabilityRegistry()
	cases := []struct {
		method, path, want string
	}{
		{"GET", "/_gateway/plugin/v1/sessions", "session.read"},
		{"GET", "/_gateway/plugin/v1/sessions/sess-1", "session.read"},
		{"GET", "/_gateway/plugin/v1/analytics", "analytics.read"},
		{"GET", "/_gateway/plugin/v1/bodies/abc", "body.fetch"},
		{"GET", "/_gateway/plugin/v1/interception-policies", "policy.serve"},
		{"POST", "/_gateway/plugin/v1/events", "events.ingest"},
		{"PUT", "/_gateway/plugin/v1/events", ""}, // method mismatch
		{"GET", "/_gateway/plugin/v1/unknown", ""},
		{"POST", "/_gateway/plugin/v1/sessions", ""},
	}
	for _, tc := range cases {
		got := reg.Required(tc.method, tc.path)
		if got != tc.want {
			t.Errorf("Required(%q,%q) = %q, want %q", tc.method, tc.path, got, tc.want)
		}
	}
}

func TestCapabilityRegistry_RegisterManifest_AllowsAndDenies(t *testing.T) {
	reg := NewCapabilityRegistry()
	// ★ 2026-10-24 P0-2：manifest 声明**未实现**的能力 ⇒ 声明即拒。
	//   本用例覆盖三条：一条已实现（session.read）+ 一条未实现（events.ingest），
	//   以及"只声明了未实现能力"（body.fetch）。
	err := reg.RegisterManifest(&Manifest{PluginID: "p1", Capabilities: []string{"session.read", "events.ingest"}})
	if err == nil {
		t.Error("manifest 声明未实现能力 events.ingest 竟被接受 —— 声明即拒的判据失效")
	}
	err = reg.RegisterManifest(&Manifest{PluginID: "p2", Capabilities: []string{"body.fetch"}})
	if err == nil {
		t.Error("manifest 声明未实现能力 body.fetch 竟被接受 —— 声明即拒的判据失效")
	}

	if !reg.Allows("p1", "session.read") {
		t.Error("p1 should allow session.read")
	}
	if reg.Allows("p1", "body.fetch") {
		t.Error("p1 should NOT allow body.fetch (not declared)")
	}
	if reg.Allows("p2", "session.read") {
		t.Error("p2 should NOT allow session.read (not declared)")
	}
	// ★ 行为变更（2026-10-24 P0-2）：p2 声明了 body.fetch，但 body.fetch
	//   **没有实现** ⇒ 能力永远不被授予（失败关闭）。
	//   此前这里是 "p2 should allow body.fetch" —— 那正是"声明了就能用、
	//   但运行时根本没有这个路由"的空洞所在。
	if reg.Allows("p2", "body.fetch") {
		t.Error("p2 should NOT allow body.fetch —— 未实现的能力永远不被授予（P0-2）")
	}
	// 已实现能力不受牵连。
	if !reg.Allows("p1", "session.read") {
		t.Error("p1 的已实现能力不应被未实现能力的声明牵连")
	}
	if reg.Allows("unknown", "session.read") {
		t.Error("unknown plugin should not be allowed")
	}
	if !reg.Allows("any", "") {
		t.Error("empty capability should be treated as allow (passthrough)")
	}
}

// TestCapabilityRegistry_RejectUnimplementedCapabilities 断言"声明即拒"的挑选逻辑。
func TestCapabilityRegistry_RejectUnimplementedCapabilities(t *testing.T) {
	rejected := RejectUnimplementedCapabilities([]string{
		CapabilitySessionRead,
		CapabilityBodyFetch,
		CapabilityEventsIngest,
		"  " + CapabilityPolicyServe + "  ",
	})
	want := []string{CapabilityBodyFetch, CapabilityEventsIngest, CapabilityPolicyServe}
	if len(rejected) != len(want) {
		t.Fatalf("被拒能力 = %v，want %v", rejected, want)
	}
	for i := range want {
		if rejected[i] != want[i] {
			t.Fatalf("被拒能力 = %v，want %v（已排序）", rejected, want)
		}
	}
	// 已实现的能力不该被拒。
	if got := RejectUnimplementedCapabilities([]string{CapabilitySessionRead, CapabilityAnalyticsRead}); len(got) != 0 {
		t.Fatalf("已实现能力不该被拒，got %v", got)
	}
}

func TestCapabilityRegistry_NilSafe(t *testing.T) {
	var reg *CapabilityRegistry
	if reg.Required("GET", "/x") != "" {
		t.Error("nil registry: Required should return empty string")
	}
	if !reg.Allows("p", "") {
		t.Error("nil registry: Allows(empty capability) should be true")
	}
	if !reg.Allows("p", "x") {
		t.Error("nil registry: Allows should be permissive (true) — caller is expected to handle missing registry upstream")
	}
	// No-panic: register on nil registry
	reg.RegisterManifest(nil)
	reg.RegisterManifest(&Manifest{})
	reg.RegisterRoute(CapabilityRoute{})
}

func TestCapabilityRegistry_CustomRoute_MostSpecificWins(t *testing.T) {
	reg := NewCapabilityRegistry()
	reg.RegisterRoute(CapabilityRoute{Method: "GET", PathPrefix: "/_gateway/plugin/v1/sessions", Capability: "session.read"})
	reg.RegisterRoute(CapabilityRoute{Method: "GET", PathPrefix: "/_gateway/plugin/v1/sessions/special", Capability: "session.special"})
	if got := reg.Required("GET", "/_gateway/plugin/v1/sessions/special"); got != "session.special" {
		t.Errorf("most-specific lookup = %q, want session.special", got)
	}
	if got := reg.Required("GET", "/_gateway/plugin/v1/sessions/abc"); got != "session.read" {
		t.Errorf("general lookup = %q, want session.read", got)
	}
}

func TestCapabilityRegistry_RegisterManifest_NilSafe(t *testing.T) {
	var reg *CapabilityRegistry
	// should not panic
	reg.RegisterManifest(nil)
	reg.RegisterManifest(&Manifest{PluginID: ""})
	reg.RegisterManifest(&Manifest{PluginID: "p", Capabilities: nil})
}

func TestCapabilityDenied_Response(t *testing.T) {
	rec := httptest.NewRecorder()
	CapabilityDenied(rec, "session.read")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json: %v", err)
	}
	if body["code"] != "capability_denied" {
		t.Errorf("code = %q", body["code"])
	}
	if body["capability"] != "session.read" {
		t.Errorf("capability = %q", body["capability"])
	}
}

func TestRequireCapability(t *testing.T) {
	reg := NewCapabilityRegistry()
	reg.RegisterManifest(&Manifest{PluginID: "p1", Capabilities: []string{"session.read"}})
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	h := RequireCapability(reg, "session.read", inner)
	cases := []struct {
		name       string
		pluginID   string
		wantStatus int
		wantBody   string
	}{
		{"allowed-header", "p1", 200, "ok"},
		{"denied-other-plugin", "p2", 403, ""},
		{"denied-empty-plugin", "", 403, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("GET", "/_gateway/plugin/v1/sessions", nil)
			if tc.pluginID != "" {
				req.Header.Set("X-Gateway-Plugin-ID", tc.pluginID)
			}
			h.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body=%q)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantBody != "" && !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Errorf("body = %q, want contains %q", rec.Body.String(), tc.wantBody)
			}
		})
	}
}

func TestRequireRouteCapability_PassesThroughUnmatchedRoutes(t *testing.T) {
	reg := NewCapabilityRegistry()
	reg.RegisterManifest(&Manifest{PluginID: "p1", Capabilities: []string{"session.read"}})
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := RequireRouteCapability(reg, inner)
	// path not matched by route table => passes through
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/some/other/path", nil)
	req.Header.Set("X-Gateway-Plugin-ID", "p1")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected passthrough, got %d", rec.Code)
	}
}
