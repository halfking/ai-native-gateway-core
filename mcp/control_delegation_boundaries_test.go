package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestControlDelegationCannotWidenRestrictions(t *testing.T) {
	g, srv := newTestGateway(t, nil)
	issuer := KeySpec{TenantID: "tenant-a", OrgID: "team-a", UserID: "user-a", AgentID: "agent-a", Scopes: []string{ScopeManage, ScopeRead, ScopeExecute}, AllowedTools: []string{"memora__*"}, DeniedTools: []string{"memora__delete"}, AllowedAdapters: []string{"memora"}, Bindings: map[string]bool{"memora": false}, ToolAllowEnforce: true}
	secret := issueKey(t, g, issuer)
	request := KeySpec{TenantID: issuer.TenantID, Scopes: []string{ScopeRead, ScopeExecute}}
	raw, _ := json.Marshal(request)
	status, _, response := keyReq(t, srv.URL, http.MethodPost, "/mcp/control/keys", secret, string(raw))
	if status != http.StatusForbidden {
		t.Fatalf("unrestricted delegation status=%d want 403; body=%s", status, response)
	}
	// A delegate preserving all restrictions can still be issued.
	request = issuer
	request.Scopes = []string{ScopeRead, ScopeExecute}
	raw, _ = json.Marshal(request)
	status, _, response = keyReq(t, srv.URL, http.MethodPost, "/mcp/control/keys", secret, string(raw))
	if status != http.StatusCreated {
		t.Fatalf("restricted delegation status=%d want 201; body=%s", status, response)
	}
}

func TestControlDelegationChecksEachRestriction(t *testing.T) {
	expires := time.Now().Add(time.Hour)
	base := func() KeySpec {
		return KeySpec{
			TenantID: "tenant-a", OrgID: "team-a", UserID: "user-a", AgentID: "agent-a",
			Scopes:       []string{ScopeManage, ScopeRead, ScopeExecute},
			AllowedTools: []string{"memora__*"}, DeniedTools: []string{"memora__delete"},
			AllowedAdapters: []string{"memora"}, Bindings: map[string]bool{"memora": false},
			ToolAllowEnforce: true, ExpiresAt: &expires,
		}
	}
	for _, tc := range []struct {
		name  string
		widen func(*KeySpec)
	}{
		{"missing_tool_allow", func(s *KeySpec) { s.AllowedTools = nil }},
		{"wider_tool_allow", func(s *KeySpec) { s.AllowedTools = []string{"*"} }},
		{"missing_tool_deny", func(s *KeySpec) { s.DeniedTools = nil }},
		{"missing_adapter_allow", func(s *KeySpec) { s.AllowedAdapters = nil }},
		{"wider_adapter_allow", func(s *KeySpec) { s.AllowedAdapters = []string{"*"} }},
		{"missing_disabled_binding", func(s *KeySpec) { s.Bindings = nil }},
		{"enabled_binding", func(s *KeySpec) { s.Bindings = map[string]bool{"memora": true} }},
		{"org", func(s *KeySpec) { s.OrgID = "team-b" }},
		{"user", func(s *KeySpec) { s.UserID = "user-b" }},
		{"agent", func(s *KeySpec) { s.AgentID = "agent-b" }},
		{"expiry_removed", func(s *KeySpec) { s.ExpiresAt = nil }},
		{"expiry_extended", func(s *KeySpec) { v := expires.Add(time.Hour); s.ExpiresAt = &v }},
		{"enforcement_removed", func(s *KeySpec) { s.ToolAllowEnforce = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, srv := newTestGateway(t, nil)
			secret := issueKey(t, g, base())
			child := base()
			tc.widen(&child)
			raw, err := json.Marshal(child)
			if err != nil {
				t.Fatal(err)
			}
			status, _, _ := keyReq(t, srv.URL, http.MethodPost, "/mcp/control/keys", secret, string(raw))
			if status != http.StatusForbidden {
				t.Fatalf("issue status=%d, want 403", status)
			}
			target, _, err := g.IssueKey(context.Background(), child)
			if err != nil {
				t.Fatal(err)
			}
			status, _, _ = keyReq(t, srv.URL, http.MethodPost, "/mcp/control/keys/"+target.ID+"/rotate", secret, "")
			if status != http.StatusForbidden {
				t.Fatalf("rotate status=%d, want 403", status)
			}
		})
	}
}

func TestControlDelegationTreatsGlobsConservatively(t *testing.T) {
	for _, tc := range []struct {
		name                              string
		parentDeny, childDeny, childAllow []string
		want                              int
	}{
		{"deny_universal_not_double_star", []string{"*"}, []string{"**"}, []string{"memora__*"}, http.StatusForbidden},
		{"allow_concrete_narrowing", []string{"memora__delete"}, []string{"memora__delete"}, []string{"memora__read"}, http.StatusCreated},
		{"allow_unknown_glob_narrowing", []string{"memora__delete"}, []string{"memora__delete"}, []string{"memora__read*"}, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, srv := newTestGateway(t, nil)
			spec := KeySpec{TenantID: "default", Scopes: []string{ScopeManage, ScopeExecute}, AllowedTools: []string{"memora__*"}, DeniedTools: tc.parentDeny}
			secret := issueKey(t, g, spec)
			spec.AllowedTools = tc.childAllow
			spec.DeniedTools = tc.childDeny
			raw, err := json.Marshal(spec)
			if err != nil {
				t.Fatal(err)
			}
			status, _, _ := keyReq(t, srv.URL, http.MethodPost, "/mcp/control/keys", secret, string(raw))
			if status != tc.want {
				t.Fatalf("delegation status=%d want %d", status, tc.want)
			}
		})
	}
}
