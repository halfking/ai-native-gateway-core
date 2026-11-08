package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// issueKey 签发一条测试凭证并返回明文。
func issueKey(t *testing.T, g *Gateway, spec KeySpec) string {
	t.Helper()
	_, secret, err := g.IssueKey(context.Background(), spec)
	if err != nil {
		t.Fatalf("issue key: %v", err)
	}
	return secret
}

func TestInitializeReportsProtocolAndStateless(t *testing.T) {
	g, srv := newTestGateway(t, nil)
	_ = g
	status, body := rpcCall(t, srv, "/mcp", "", map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
	})
	if status != http.StatusUnauthorized {
		t.Fatalf("expected 401 without credential, got %d", status)
	}
	secret := issueKey(t, g, KeySpec{TenantID: "default", Scopes: []string{ScopeRead, ScopeExecute}})
	status, body = rpcCall(t, srv, "/mcp", secret, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
	})
	if status != http.StatusOK {
		t.Fatalf("initialize status=%d body=%v", status, body)
	}
	res := rpcResult(t, body)
	if res["protocolVersion"] != ProtocolVersion {
		t.Fatalf("protocolVersion = %v, want %s", res["protocolVersion"], ProtocolVersion)
	}
	info, _ := res["serverInfo"].(map[string]any)
	if info["name"] == "" {
		t.Fatalf("serverInfo.name missing: %v", res)
	}
}

func TestUnauthorizedAndUnknownKeyRejected(t *testing.T) {
	_, srv := newTestGateway(t, nil)

	status, _ := rpcCall(t, srv, "/mcp", "sk-mcp-nope", map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/list",
	})
	if status != http.StatusUnauthorized {
		t.Fatalf("unknown key status=%d, want 401", status)
	}

	status, _ = rpcCall(t, srv, "/mcp", "", map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/list",
	})
	if status != http.StatusUnauthorized {
		t.Fatalf("missing key status=%d, want 401", status)
	}
}

func TestRevokedKeyRejected(t *testing.T) {
	g, srv := newTestGateway(t, nil)
	secret := issueKey(t, g, KeySpec{TenantID: "default"})
	keys, err := g.Store().ListKeys(context.Background(), "default")
	if err != nil || len(keys) != 1 {
		t.Fatalf("list keys: %v %d", err, len(keys))
	}
	if err := g.RevokeKey(context.Background(), "default", keys[0].ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	status, _ := rpcCall(t, srv, "/mcp", secret, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/list",
	})
	if status != http.StatusUnauthorized {
		t.Fatalf("revoked key status=%d, want 401", status)
	}
}

func TestExpiredKeyRejected(t *testing.T) {
	g, srv := newTestGateway(t, nil)
	past := time.Now().Add(-time.Hour)
	secret := issueKey(t, g, KeySpec{TenantID: "default", ExpiresAt: &past})
	status, _ := rpcCall(t, srv, "/mcp", secret, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/list",
	})
	if status != http.StatusUnauthorized {
		t.Fatalf("expired key status=%d, want 401", status)
	}
}

func TestToolsListAggregatesAndNamespaces(t *testing.T) {
	up := newFakeUpstream(t, []map[string]any{
		{"name": "search_memory", "description": "检索记忆"},
		{"name": "add_memory", "description": "写入记忆"},
	})
	g, srv := newTestGateway(t, up)
	secret := issueKey(t, g, KeySpec{TenantID: "default"})

	status, body := rpcCall(t, srv, "/mcp", secret, map[string]any{
		"jsonrpc": "2.0", "id": 7, "method": "tools/list",
	})
	if status != http.StatusOK {
		t.Fatalf("tools/list status=%d body=%v", status, body)
	}
	tools, _ := rpcResult(t, body)["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d (%v)", len(tools), body)
	}
	first, _ := tools[0].(map[string]any)
	if first["name"] != "memora__add_memory" && first["name"] != "memora__search_memory" {
		t.Fatalf("tool not namespaced: %v", first["name"])
	}
	// 能力路由头必须注入上游。
	if up.lastRequest.Get("X-MCP-Capability") != "memory" {
		t.Fatalf("capability header missing: %v", up.lastRequest)
	}
	if up.lastRequest.Get("X-MCP-Capability-Tools") == "" {
		t.Fatalf("capability tools header missing: %v", up.lastRequest)
	}
	if up.lastRequest.Get("MCP-Protocol-Version") != ProtocolVersion {
		t.Fatalf("protocol version header missing: %v", up.lastRequest)
	}
}

func TestToolsCallHappyPathAndAudit(t *testing.T) {
	up := newFakeUpstream(t, []map[string]any{{"name": "search_memory"}})
	g, srv := newTestGateway(t, up)
	secret := issueKey(t, g, KeySpec{TenantID: "default"})

	status, body := rpcCall(t, srv, "/mcp", secret, map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": map[string]any{"name": "memora__search_memory", "arguments": map[string]any{"q": "x"}},
	})
	if status != http.StatusOK {
		t.Fatalf("tools/call status=%d body=%v", status, body)
	}
	if _, ok := body["result"]; !ok {
		t.Fatalf("expected result, got %v", body)
	}
	events := g.Audit().Query(AuditFilter{Kind: AuditKindCall})
	if len(events) == 0 {
		t.Fatalf("expected call audit event")
	}
	if events[0].Tool != "memora__search_memory" {
		t.Fatalf("audit tool = %q", events[0].Tool)
	}
	if events[0].Adapter != "memora" {
		t.Fatalf("audit adapter = %q", events[0].Adapter)
	}
}

func TestToolNotFoundReturns404(t *testing.T) {
	up := newFakeUpstream(t, []map[string]any{{"name": "search_memory"}})
	g, srv := newTestGateway(t, up)
	secret := issueKey(t, g, KeySpec{TenantID: "default"})

	status, body := rpcCall(t, srv, "/mcp", secret, map[string]any{
		"jsonrpc": "2.0", "id": 4, "method": "tools/call",
		"params": map[string]any{"name": "nope__missing", "arguments": map[string]any{}},
	})
	if status != http.StatusNotFound {
		t.Fatalf("status=%d, want 404 (body=%v)", status, body)
	}
	if code := rpcErrorCode(body); code != CodeToolNotFound {
		t.Fatalf("error code=%d, want %d", code, CodeToolNotFound)
	}
}

func TestKeyAllowListDeniesTool(t *testing.T) {
	up := newFakeUpstream(t, []map[string]any{{"name": "search_memory"}})
	g, srv := newTestGateway(t, up)
	secret := issueKey(t, g, KeySpec{TenantID: "default", AllowedTools: []string{"memora__add_memory"}})

	status, body := rpcCall(t, srv, "/mcp", secret, map[string]any{
		"jsonrpc": "2.0", "id": 5, "method": "tools/call",
		"params": map[string]any{"name": "memora__search_memory", "arguments": map[string]any{}},
	})
	if status != http.StatusForbidden {
		t.Fatalf("status=%d, want 403 (body=%v)", status, body)
	}
	// 与既有 tools/mcp-gateway 的 tool-allow 错误码保持一致（-32003）。
	if code := rpcErrorCode(body); code != CodeToolNotAllowed {
		t.Fatalf("error code=%d, want %d", code, CodeToolNotAllowed)
	}
}

func TestDenyListWinsOverAllowList(t *testing.T) {
	up := newFakeUpstream(t, []map[string]any{{"name": "search_memory"}})
	g, srv := newTestGateway(t, up)
	secret := issueKey(t, g, KeySpec{
		TenantID:     "default",
		AllowedTools: []string{"memora__*"},
		DeniedTools:  []string{"memora__search_memory"},
	})
	status, _ := rpcCall(t, srv, "/mcp", secret, map[string]any{
		"jsonrpc": "2.0", "id": 6, "method": "tools/call",
		"params": map[string]any{"name": "memora__search_memory", "arguments": map[string]any{}},
	})
	if status != http.StatusForbidden {
		t.Fatalf("status=%d, want 403", status)
	}
}

func TestBindingDisabledDeniesBackend(t *testing.T) {
	up := newFakeUpstream(t, []map[string]any{{"name": "search_memory"}})
	g, srv := newTestGateway(t, up)
	secret := issueKey(t, g, KeySpec{TenantID: "default", Bindings: map[string]bool{"memora": false}})

	status, body := rpcCall(t, srv, "/mcp", secret, map[string]any{
		"jsonrpc": "2.0", "id": 8, "method": "tools/call",
		"params": map[string]any{"name": "memora__search_memory", "arguments": map[string]any{}},
	})
	if status != http.StatusForbidden {
		t.Fatalf("status=%d, want 403 (body=%v)", status, body)
	}
}

func TestAllowedAdaptersRestrictsAccess(t *testing.T) {
	up := newFakeUpstream(t, []map[string]any{{"name": "search_memory"}})
	g, srv := newTestGateway(t, up)
	secret := issueKey(t, g, KeySpec{TenantID: "default", AllowedAdapters: []string{"other"}})

	status, body := rpcCall(t, srv, "/mcp", secret, map[string]any{
		"jsonrpc": "2.0", "id": 9, "method": "tools/list",
	})
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%v", status, body)
	}
	tools, _ := rpcResult(t, body)["tools"].([]any)
	if len(tools) != 0 {
		t.Fatalf("expected no visible tools, got %d", len(tools))
	}
}

func TestPrivateToolHiddenFromOtherTenants(t *testing.T) {
	up := newFakeUpstream(t, []map[string]any{{"name": "search_memory"}})
	g, srv := newTestGateway(t, up)
	ctx := context.Background()
	if err := g.Store().PutTool(ctx, Tool{
		Name:       "memora__search_memory",
		RawName:    "search_memory",
		Adapter:    "memora",
		TenantID:   "tenant-a",
		Visibility: VisibilityPrivate,
		Enabled:    true,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}); err != nil {
		t.Fatalf("put tool: %v", err)
	}
	if err := g.LoadCatalog(ctx, "tenant-b"); err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	secret := issueKey(t, g, KeySpec{TenantID: "tenant-b"})
	status, body := rpcCall(t, srv, "/mcp", secret, map[string]any{
		"jsonrpc": "2.0", "id": 10, "method": "tools/list",
	})
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%v", status, body)
	}
	tools, _ := rpcResult(t, body)["tools"].([]any)
	if len(tools) != 0 {
		t.Fatalf("private tool leaked to other tenant: %v", tools)
	}
}

func TestHighSensitivityRequiresApproval(t *testing.T) {
	up := newFakeUpstream(t, []map[string]any{{"name": "delete_everything"}})
	g, srv := newTestGateway(t, up)
	ctx := context.Background()
	if err := g.Store().PutTool(ctx, Tool{
		Name:            "memora__delete_everything",
		RawName:         "delete_everything",
		Adapter:         "memora",
		TenantID:        "default",
		Visibility:      VisibilityPublic,
		Sensitivity:     SensitivityHigh,
		RequireApproval: true,
		Enabled:         true,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}); err != nil {
		t.Fatalf("put tool: %v", err)
	}
	if err := g.LoadCatalog(ctx, "default"); err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	secret := issueKey(t, g, KeySpec{TenantID: "default", UserID: "u1"})

	status, body := rpcCall(t, srv, "/mcp", secret, map[string]any{
		"jsonrpc": "2.0", "id": 11, "method": "tools/call",
		"params": map[string]any{"name": "memora__delete_everything", "arguments": map[string]any{}},
	})
	if status != http.StatusForbidden {
		t.Fatalf("status=%d, want 403 without approval (body=%v)", status, body)
	}
	// ★ 审批缺失有**专属码** -32007（新增、既有未使用）⇒ 不再借用 -32003
	//   （既有语义是 tool-allow 拒绝）：审批闸与允许表闸必须能分开分支。
	if code := rpcErrorCode(body); code != CodeApprovalRequired {
		t.Fatalf("error code=%d, want %d (-32007 approval required)", code, CodeApprovalRequired)
	}

	// 审批通过后放行。
	approval := Approval{
		ID: "apr-1", TenantID: "default", Tool: "memora__delete_everything",
		Requester: "u1", Status: ApprovalApproved, CreatedAt: time.Now(),
	}
	if err := g.Store().PutApproval(ctx, approval); err != nil {
		t.Fatalf("put approval: %v", err)
	}
	req := map[string]any{
		"jsonrpc": "2.0", "id": 12, "method": "tools/call",
		"params": map[string]any{"name": "memora__delete_everything", "arguments": map[string]any{}},
	}
	raw, _ := json.Marshal(req)
	httpReq, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp", bytesReader(raw))
	httpReq.Header.Set("Authorization", "Bearer "+secret)
	httpReq.Header.Set("X-MCP-Approval", "apr-1")
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("approved call status=%d, want 200", resp.StatusCode)
	}
}

func TestParameterWhitelistRejectsExtraParam(t *testing.T) {
	up := newFakeUpstream(t, []map[string]any{{"name": "search_memory"}})
	g, srv := newTestGateway(t, up)
	ctx := context.Background()
	if err := g.Store().PutTool(ctx, Tool{
		Name:          "memora__search_memory",
		RawName:       "search_memory",
		Adapter:       "memora",
		TenantID:      "default",
		Visibility:    VisibilityPublic,
		Sensitivity:   SensitivityRead,
		AllowedParams: []string{"q"},
		Enabled:       true,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}); err != nil {
		t.Fatalf("put tool: %v", err)
	}
	if err := g.LoadCatalog(ctx, "default"); err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	secret := issueKey(t, g, KeySpec{TenantID: "default"})
	status, _ := rpcCall(t, srv, "/mcp", secret, map[string]any{
		"jsonrpc": "2.0", "id": 13, "method": "tools/call",
		"params": map[string]any{
			"name":      "memora__search_memory",
			"arguments": map[string]any{"q": "ok", "evil": "drop table"},
		},
	})
	if status != http.StatusForbidden {
		t.Fatalf("status=%d, want 403 for non-whitelisted param", status)
	}
}

func TestSchemaValidationRejectsWrongType(t *testing.T) {
	up := newFakeUpstream(t, []map[string]any{{"name": "search_memory"}})
	g, srv := newTestGateway(t, up)
	ctx := context.Background()
	schema := json.RawMessage(`{"type":"object","properties":{"n":{"type":"integer"}},"required":["n"]}`)
	if err := g.Store().PutTool(ctx, Tool{
		Name:        "memora__search_memory",
		RawName:     "search_memory",
		Adapter:     "memora",
		TenantID:    "default",
		Visibility:  VisibilityPublic,
		Sensitivity: SensitivityRead,
		InputSchema: schema,
		Enabled:     true,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}); err != nil {
		t.Fatalf("put tool: %v", err)
	}
	if err := g.LoadCatalog(ctx, "default"); err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	secret := issueKey(t, g, KeySpec{TenantID: "default"})
	status, _ := rpcCall(t, srv, "/mcp", secret, map[string]any{
		"jsonrpc": "2.0", "id": 14, "method": "tools/call",
		"params": map[string]any{
			"name":      "memora__search_memory",
			"arguments": map[string]any{"n": "not-a-number"},
		},
	})
	if status != http.StatusForbidden {
		t.Fatalf("status=%d, want 403 for schema violation", status)
	}
}

func TestRateLimitReturns429(t *testing.T) {
	up := newFakeUpstream(t, []map[string]any{{"name": "search_memory"}})
	g, srv := newTestGateway(t, up, func(cfg *Config) {
		cfg.ClassRateLimits = map[ToolClass]int{ClassRead: 1}
	})
	secret := issueKey(t, g, KeySpec{TenantID: "default"})

	status, _ := rpcCall(t, srv, "/mcp", secret, map[string]any{
		"jsonrpc": "2.0", "id": 15, "method": "tools/call",
		"params": map[string]any{"name": "memora__search_memory", "arguments": map[string]any{}},
	})
	if status != http.StatusOK {
		t.Fatalf("first call status=%d, want 200", status)
	}
	status, body := rpcCall(t, srv, "/mcp", secret, map[string]any{
		"jsonrpc": "2.0", "id": 16, "method": "tools/call",
		"params": map[string]any{"name": "memora__search_memory", "arguments": map[string]any{}},
	})
	if status != http.StatusTooManyRequests {
		t.Fatalf("second call status=%d, want 429 (body=%v)", status, body)
	}
	if code := rpcErrorCode(body); code != CodeRateLimited {
		t.Fatalf("error code=%d, want %d", code, CodeRateLimited)
	}
}

func TestUpstreamErrorSurfaces502(t *testing.T) {
	up := newFakeUpstream(t, []map[string]any{{"name": "search_memory"}})
	g, srv := newTestGateway(t, up)
	secret := issueKey(t, g, KeySpec{TenantID: "default"})
	up.status = 500
	status, body := rpcCall(t, srv, "/mcp", secret, map[string]any{
		"jsonrpc": "2.0", "id": 17, "method": "tools/call",
		"params": map[string]any{"name": "memora__search_memory", "arguments": map[string]any{}},
	})
	if status != http.StatusBadGateway {
		t.Fatalf("status=%d, want 502 (body=%v)", status, body)
	}
}

func TestOutputRedactionMasksSecrets(t *testing.T) {
	up := newFakeUpstream(t, []map[string]any{{"name": "get_secret"}})
	up.callResult = map[string]any{
		"content": []any{map[string]any{
			"type": "text",
			"text": "token sk-ant-abcdefghijklmnopqrstuvwxyz123456 and mail a@b.com",
		}},
	}
	g, srv := newTestGateway(t, up)
	secret := issueKey(t, g, KeySpec{TenantID: "default"})

	status, body := rpcCall(t, srv, "/mcp", secret, map[string]any{
		"jsonrpc": "2.0", "id": 18, "method": "tools/call",
		"params": map[string]any{"name": "memora__get_secret", "arguments": map[string]any{}},
	})
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%v", status, body)
	}
	raw, _ := json.Marshal(body)
	if containsSubstring(string(raw), "sk-ant-abcdefghijklmnopqrstuvwxyz123456") {
		t.Fatalf("secret leaked to client: %s", raw)
	}
	if !containsSubstring(string(raw), "REDACTED") {
		t.Fatalf("expected redaction marker: %s", raw)
	}
}

func TestMethodNotFound(t *testing.T) {
	g, srv := newTestGateway(t, nil)
	secret := issueKey(t, g, KeySpec{TenantID: "default"})
	status, body := rpcCall(t, srv, "/mcp", secret, map[string]any{
		"jsonrpc": "2.0", "id": 19, "method": "does/not/exist",
	})
	if status != http.StatusOK {
		t.Fatalf("status=%d, want 200 (JSON-RPC level error)", status)
	}
	if code := rpcErrorCode(body); code != CodeMethodNotFound {
		t.Fatalf("error code=%d, want %d", code, CodeMethodNotFound)
	}
}

func TestStatelessRoutingViaHeaderMethod(t *testing.T) {
	g, srv := newTestGateway(t, nil)
	secret := issueKey(t, g, KeySpec{TenantID: "default"})
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 20})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp", bytesReader(raw))
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Mcp-Method", "ping")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
}
