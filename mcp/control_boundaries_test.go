package mcp

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestControlBodyTenantCannotCrossBoundary(t *testing.T) {
	for _, tc := range []struct{ path, fields string }{
		{"adapters", `"name":"foreign","endpoint":"http://127.0.0.1:9100"`},
		{"tools", `"name":"foreign","adapter":"memora"`},
		{"agents", `"name":"foreign"`},
		{"skills", `"name":"foreign"`},
		{"approvals", `"tool":"foreign"`},
	} {
		t.Run(tc.path, func(t *testing.T) {
			g, srv := newTestGateway(t, nil)
			secret := scopeKey(t, g, "tenant-a", ScopeRegister, ScopeManage)
			status, _, raw := keyReq(t, srv.URL, http.MethodPost, "/mcp/control/"+tc.path, secret,
				`{"tenant_id":"tenant-b",`+tc.fields+`}`)
			if status != http.StatusForbidden {
				t.Fatalf("cross-tenant creation status=%d, want 403; body=%s", status, raw)
			}
			status, _, raw = adminReq(t, srv.URL, http.MethodPost, "/mcp/control/"+tc.path,
				`{"tenant_id":"tenant-b",`+tc.fields+`}`)
			if status != http.StatusCreated {
				t.Fatalf("admin creation status=%d, want 201; body=%s", status, raw)
			}
		})
	}
}

func TestControlRegisterCannotOverwrite(t *testing.T) {
	for _, tc := range []struct{ path, original, replacement string }{
		{"adapters", `{"name":"protected","endpoint":"http://127.0.0.1:9100"}`, `{"name":"protected","endpoint":"http://127.0.0.1:9101"}`},
		{"tools", `{"name":"protected","adapter":"memora","sensitivity":"high"}`, `{"name":"protected","adapter":"memora","sensitivity":"read"}`},
	} {
		t.Run(tc.path, func(t *testing.T) {
			g, srv := newTestGateway(t, nil)
			secret := scopeKey(t, g, "default", ScopeRegister)
			status, _, _ := adminReq(t, srv.URL, http.MethodPost, "/mcp/control/"+tc.path, tc.original)
			if status != http.StatusCreated {
				t.Fatalf("seed status=%d", status)
			}
			status, _, raw := keyReq(t, srv.URL, http.MethodPost, "/mcp/control/"+tc.path, secret, tc.replacement)
			if status != http.StatusForbidden {
				t.Fatalf("register overwrite status=%d, want 403; body=%s", status, raw)
			}
			status, _, raw = adminReq(t, srv.URL, http.MethodGet, "/mcp/control/"+tc.path+"/protected", "")
			if status != http.StatusOK || strings.Contains(raw, "9101") || tc.path == "tools" && !strings.Contains(raw, `"sensitivity":"high"`) {
				t.Fatalf("original changed: %s", raw)
			}
		})
	}
}

func TestControlTenantCannotMutateForeignOrShared(t *testing.T) {
	g, srv := newTestGateway(t, nil)
	ctx := context.Background()
	secret := scopeKey(t, g, "tenant-a", ScopeManage)
	if err := g.store.PutAgent(ctx, Agent{ID: "other-agent", TenantID: "tenant-b"}); err != nil {
		t.Fatal(err)
	}
	if err := g.store.PutSkill(ctx, Skill{ID: "other-skill", TenantID: "tenant-b"}); err != nil {
		t.Fatal(err)
	}
	if err := g.store.PutAdapter(ctx, Adapter{Name: "shared", Endpoint: "http://127.0.0.1:9100"}); err != nil {
		t.Fatal(err)
	}
	if err := g.store.PutTool(ctx, Tool{Name: "shared-tool", Adapter: "shared"}); err != nil {
		t.Fatal(err)
	}
	if err := g.store.PutApproval(ctx, Approval{ID: "other-approval", TenantID: "tenant-b", Tool: "protected", Status: ApprovalPending}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodDelete, "agents/other-agent", ""},
		{http.MethodDelete, "skills/other-skill", ""},
		{http.MethodDelete, "adapters/shared", ""},
		{http.MethodPut, "adapters/shared", `{"endpoint":"http://127.0.0.1:9101"}`},
		{http.MethodDelete, "tools/shared-tool", ""},
		{http.MethodPut, "tools/shared-tool", `{"sensitivity":"read"}`},
		{http.MethodPost, "approvals/other-approval", `{"decision":"approved"}`},
	} {
		t.Run(tc.method+"/"+tc.path, func(t *testing.T) {
			status, _, raw := keyReq(t, srv.URL, tc.method, "/mcp/control/"+tc.path, secret, tc.body)
			if status != http.StatusForbidden && status != http.StatusNotFound {
				t.Fatalf("status=%d, want 403/404; body=%s", status, raw)
			}
		})
	}
	if a, _ := g.store.GetAgent(ctx, "tenant-b", "other-agent"); a == nil {
		t.Error("foreign agent deleted")
	}
	if s, _ := g.store.GetSkill(ctx, "tenant-b", "other-skill"); s == nil {
		t.Error("foreign skill deleted")
	}
	if a, _ := g.store.GetApproval(ctx, "other-approval"); a.Status != ApprovalPending {
		t.Error("foreign approval changed")
	}
}

func TestControlAuditIsTenantScoped(t *testing.T) {
	g, srv := newTestGateway(t, nil)
	secret := scopeKey(t, g, "tenant-a", ScopeAnalyze)
	g.audit.Record(AuditEvent{TenantID: "tenant-a", Tool: "own-marker", Outcome: OutcomeAllowed})
	g.audit.Record(AuditEvent{TenantID: "tenant-b", Tool: "foreign-marker", Outcome: OutcomeAllowed})
	for _, path := range []string{"/mcp/control/audit", "/mcp/control/audit/export"} {
		status, _, raw := keyReq(t, srv.URL, http.MethodGet, path, secret, "")
		if status != http.StatusOK || !strings.Contains(raw, "own-marker") || strings.Contains(raw, "foreign-marker") {
			t.Fatalf("tenant audit status=%d body=%s", status, raw)
		}
		status, _, raw = adminReq(t, srv.URL, http.MethodGet, path, "")
		if status != http.StatusOK || !strings.Contains(raw, "foreign-marker") {
			t.Fatalf("admin audit missing event: %s", raw)
		}
	}
}
