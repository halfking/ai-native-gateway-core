package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// keyReq 发一个带业务 key 的控制面请求（Authorization: Bearer）。
func keyReq(t *testing.T, srvURL, method, path, secret, body string) (int, map[string]any, string) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, srvURL+path, reader)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return resp.StatusCode, out, string(raw)
}

// keyReqWithHeader 发一个带业务 key + 额外请求头的控制面请求。
func keyReqWithHeader(t *testing.T, srvURL, method, path, secret, header, headerValue, body string) (int, map[string]any, string) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, srvURL+path, reader)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	if header != "" {
		req.Header.Set(header, headerValue)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return resp.StatusCode, out, string(raw)
}

// scopeKey 签发一把带指定 scope 的业务 key。
func scopeKey(t *testing.T, g *Gateway, tenant string, scopes ...string) string {
	t.Helper()
	return issueKey(t, g, KeySpec{TenantID: tenant, Name: "scope-test", Scopes: scopes})
}

// ---------------------------------------------------------------------------
// 一、业务 key 按 scope 走通控制面（注册 / 管理 / 统计分析）
// ---------------------------------------------------------------------------

func TestControlScopeKeyCanRegister(t *testing.T) {
	g, srv := newTestGateway(t, nil)
	secret := scopeKey(t, g, "default", ScopeRegister)

	status, _, _ := keyReq(t, srv.URL, http.MethodPost, "/mcp/control/adapters", secret,
		`{"name":"scoped-adapter","endpoint":"http://127.0.0.1:9101","enabled":true}`)
	if status != http.StatusCreated {
		t.Fatalf("register adapter status=%d, want 201", status)
	}

	status, _, _ = keyReq(t, srv.URL, http.MethodGet, "/mcp/control/adapters", secret, "")
	if status != http.StatusOK {
		t.Fatalf("list adapters status=%d, want 200", status)
	}
}

func TestControlScopeKeyCanManage(t *testing.T) {
	g, srv := newTestGateway(t, nil)
	secret := scopeKey(t, g, "default", ScopeManage)

	// 先用管理令牌注册一个适配器（管理令牌是超集，口径不变）。
	status, _, _ := adminReq(t, srv.URL, http.MethodPost, "/mcp/control/adapters",
		`{"name":"managed-adapter","endpoint":"http://127.0.0.1:9102","enabled":true}`)
	if status != http.StatusCreated {
		t.Fatalf("seed adapter status=%d, want 201", status)
	}

	status, _, _ = keyReq(t, srv.URL, http.MethodDelete, "/mcp/control/adapters/managed-adapter", secret, "")
	if status != http.StatusNoContent {
		t.Fatalf("delete adapter status=%d, want 204", status)
	}
}

func TestControlScopeKeyCanAnalyze(t *testing.T) {
	g, srv := newTestGateway(t, nil)
	secret := scopeKey(t, g, "default", ScopeAnalyze)

	status, _, _ := keyReq(t, srv.URL, http.MethodGet, "/mcp/control/audit", secret, "")
	if status != http.StatusOK {
		t.Fatalf("audit status=%d, want 200", status)
	}
	status, body, _ := keyReq(t, srv.URL, http.MethodGet, "/mcp/control/routes", secret, "")
	if status != http.StatusOK {
		t.Fatalf("routes status=%d, want 200", status)
	}
	if _, ok := body["routes"]; !ok {
		t.Fatalf("routes body missing 'routes': %v", body)
	}
}

// ---------------------------------------------------------------------------
// 二、无权限的 key 被拒（403），且失败口径可断言
// ---------------------------------------------------------------------------

func TestControlScopeDeniedWithoutScope(t *testing.T) {
	g, srv := newTestGateway(t, nil)
	// 只有数据面 scope（mcp.read / mcp.execute）⇒ 控制面一律无权。
	secret := scopeKey(t, g, "default", ScopeRead, ScopeExecute)

	status, body, raw := keyReq(t, srv.URL, http.MethodGet, "/mcp/control/tools", secret, "")
	if status != http.StatusForbidden {
		t.Fatalf("status=%d, want 403 (body=%s)", status, raw)
	}
	if body["error"] != "forbidden" {
		t.Fatalf("error field=%v, want forbidden (body=%s)", body["error"], raw)
	}
	if body["required_scope"] != ScopeRegister {
		t.Fatalf("required_scope=%v, want %s (body=%s)", body["required_scope"], ScopeRegister, raw)
	}
}

func TestControlScopeDeniedIsNotUnauthorized(t *testing.T) {
	g, srv := newTestGateway(t, nil)
	secret := scopeKey(t, g, "default", ScopeAnalyze)

	// analyze 不含 register ⇒ 注册面拒绝；且**不是** 401（身份本身是有效的）。
	status, _, raw := keyReq(t, srv.URL, http.MethodPost, "/mcp/control/adapters", secret,
		`{"name":"x","endpoint":"http://127.0.0.1:9103"}`)
	if status != http.StatusForbidden {
		t.Fatalf("status=%d, want 403 (body=%s)", status, raw)
	}
}

// ---------------------------------------------------------------------------
// 三、拓扑面单独口径：/health 需要 mcp.topology
// ---------------------------------------------------------------------------

func TestControlScopeHealthNeedsTopology(t *testing.T) {
	g, srv := newTestGateway(t, nil)
	analyzeOnly := scopeKey(t, g, "default", ScopeAnalyze)

	status, body, raw := keyReq(t, srv.URL, http.MethodGet, "/mcp/control/health", analyzeOnly, "")
	if status != http.StatusForbidden {
		t.Fatalf("health status=%d, want 403 (body=%s)", status, raw)
	}
	if body["required_scope"] != ScopeTopology {
		t.Fatalf("required_scope=%v, want %s (body=%s)", body["required_scope"], ScopeTopology, raw)
	}

	withTopology := scopeKey(t, g, "default", ScopeAnalyze, ScopeTopology)
	status, _, raw = keyReq(t, srv.URL, http.MethodGet, "/mcp/control/health", withTopology, "")
	if status != http.StatusOK {
		t.Fatalf("health with topology status=%d, want 200 (body=%s)", status, raw)
	}
}

// ---------------------------------------------------------------------------
// 四、统计出口（/stats）按 mcp.analyze 分级
// ---------------------------------------------------------------------------

func TestControlScopeStatsEndpoint(t *testing.T) {
	g, srv := newTestGateway(t, nil)
	secret := scopeKey(t, g, "default", ScopeAnalyze)

	status, body, raw := keyReq(t, srv.URL, http.MethodGet, "/mcp/control/stats", secret, "")
	if status != http.StatusOK {
		t.Fatalf("stats status=%d, want 200 (body=%s)", status, raw)
	}
	families, ok := body["families"].([]any)
	if !ok || len(families) != 0 || body["scope"] != "tenant_audit_only" {
		t.Fatalf("tenant key received unscoped process metrics: %s", raw)
	}
	status, body, _ = adminReq(t, srv.URL, http.MethodGet, "/mcp/control/stats", "")
	if families, ok := body["families"].([]any); status != http.StatusOK || !ok || len(families) == 0 {
		t.Fatalf("management token lost process metrics: status=%d", status)
	}

	// 无 analyze 权限 ⇒ 统计面拒绝。
	plain := scopeKey(t, g, "default", ScopeRegister)
	status, _, raw = keyReq(t, srv.URL, http.MethodGet, "/mcp/control/stats", plain, "")
	if status != http.StatusForbidden {
		t.Fatalf("stats without analyze status=%d, want 403 (body=%s)", status, raw)
	}
}

// ---------------------------------------------------------------------------
// 五、权限放大防护：签不出比自己权限更大的 key
// ---------------------------------------------------------------------------

func TestControlScopeEscalationBlockedOnIssue(t *testing.T) {
	g, srv := newTestGateway(t, nil)

	// ① 只有注册权限的 key 连「签发」这个动作都够不着（签发属管理面）。
	registerSecret := scopeKey(t, g, "default", ScopeRegister)
	status, body, raw := keyReq(t, srv.URL, http.MethodPost, "/mcp/control/keys", registerSecret,
		`{"tenant_id":"default","name":"escalated","scopes":["mcp.register"]}`)
	if status != http.StatusForbidden {
		t.Fatalf("issue by register-only key status=%d, want 403 (body=%s)", status, raw)
	}
	if body["required_scope"] != ScopeManage {
		t.Fatalf("required_scope=%v, want %s (body=%s)", body["required_scope"], ScopeManage, raw)
	}

	// ② 有管理权限的 key 试图签出**超出自身**权限的 key ⇒ 权限放大被挡。
	manageSecret := scopeKey(t, g, "default", ScopeManage)
	status, body, raw = keyReq(t, srv.URL, http.MethodPost, "/mcp/control/keys", manageSecret,
		`{"tenant_id":"default","name":"escalated","scopes":["mcp.analyze"]}`)
	if status != http.StatusForbidden {
		t.Fatalf("escalate status=%d, want 403 (body=%s)", status, raw)
	}
	if body["reason"] != "scope_escalation" {
		t.Fatalf("reason=%v, want scope_escalation (body=%s)", body["reason"], raw)
	}
	if body["exceeded_scope"] != ScopeAnalyze {
		t.Fatalf("exceeded_scope=%v, want %s (body=%s)", body["exceeded_scope"], ScopeAnalyze, raw)
	}

	// ③ 签出**不超出自身**的 key ⇒ 允许。
	status, _, raw = keyReq(t, srv.URL, http.MethodPost, "/mcp/control/keys", manageSecret,
		`{"tenant_id":"default","name":"delegated","scopes":["mcp.manage"]}`)
	if status != http.StatusCreated {
		t.Fatalf("delegate status=%d, want 201 (body=%s)", status, raw)
	}

	// ④ 省略 scopes 时沿用既有默认档 [mcp.read, mcp.execute]；manage 权限的 key
	//    不持有 read/execute ⇒ 默认档同样被挡（不能借「省略」绕过收口）。
	status, body, raw = keyReq(t, srv.URL, http.MethodPost, "/mcp/control/keys", manageSecret,
		`{"tenant_id":"default","name":"default-scopes"}`)
	if status != http.StatusForbidden {
		t.Fatalf("default scopes status=%d, want 403 (body=%s)", status, raw)
	}
	if body["reason"] != "scope_escalation" {
		t.Fatalf("reason=%v, want scope_escalation (body=%s)", body["reason"], raw)
	}
}

func TestControlScopeEscalationBlockedOnRotate(t *testing.T) {
	g, srv := newTestGateway(t, nil)
	// 一把 admin 权限的 key（超集）。
	adminSecret := scopeKey(t, g, "default", ScopeAdmin)
	// 一把只有 manage 的 key。
	manageSecret := scopeKey(t, g, "default", ScopeManage)

	// 取出 admin key 的 ID。
	keys, err := g.store.ListKeys(context.Background(), "default")
	if err != nil {
		t.Fatalf("list keys: %v", err)
	}
	var adminKeyID string
	for i := range keys {
		if keys[i].Hash == HashSecret(adminSecret) {
			adminKeyID = keys[i].ID
		}
	}
	if adminKeyID == "" {
		t.Fatal("admin key not found in store")
	}

	// manage 权限的 key 试图轮换 admin 权限的 key ⇒ 拒绝（否则等于提权）。
	status, body, raw := keyReq(t, srv.URL, http.MethodPost, "/mcp/control/keys/"+adminKeyID+"/rotate", manageSecret, "")
	if status != http.StatusForbidden {
		t.Fatalf("rotate status=%d, want 403 (body=%s)", status, raw)
	}
	if body["reason"] != "scope_escalation" {
		t.Fatalf("reason=%v, want scope_escalation (body=%s)", body["reason"], raw)
	}
}

// ---------------------------------------------------------------------------
// 六、租户收口：业务 key 只能操作自己所属租户
// ---------------------------------------------------------------------------

func TestControlScopeTenantConfinement(t *testing.T) {
	g, srv := newTestGateway(t, nil)
	secret := scopeKey(t, g, "tenant-a", ScopeRegister)

	// 显式指定别的租户 ⇒ 拒绝。
	status, body, raw := keyReqWithHeader(t, srv.URL, http.MethodGet, "/mcp/control/adapters",
		secret, "X-Tenant-Id", "tenant-b", "")
	if status != http.StatusForbidden {
		t.Fatalf("status=%d, want 403 (body=%s)", status, raw)
	}
	if body["reason"] != "tenant_mismatch" {
		t.Fatalf("reason=%v, want tenant_mismatch (body=%s)", body["reason"], raw)
	}

	// 自己的租户 ⇒ 放行。
	status, _, raw = keyReq(t, srv.URL, http.MethodGet, "/mcp/control/adapters", secret, "")
	if status != http.StatusOK {
		t.Fatalf("status=%d, want 200 (body=%s)", status, raw)
	}
}

// ---------------------------------------------------------------------------
// 七、既有口径保持：管理令牌全权、未配置管理令牌 ⇒ 503、无效凭据 ⇒ 401
// ---------------------------------------------------------------------------

func TestControlScopeAdminTokenStillFullPower(t *testing.T) {
	_, srv := newTestGateway(t, nil)
	for _, path := range []string{"/mcp/control/tools", "/mcp/control/audit", "/mcp/control/health", "/mcp/control/stats"} {
		status, _, raw := adminReq(t, srv.URL, http.MethodGet, path, "")
		if status != http.StatusOK {
			t.Fatalf("%s status=%d, want 200 (body=%s)", path, status, raw)
		}
	}
}

func TestControlScopeInvalidCredentialIs401(t *testing.T) {
	_, srv := newTestGateway(t, nil)
	status, _, raw := keyReq(t, srv.URL, http.MethodGet, "/mcp/control/tools", "sk-mcp-not-a-real-key", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401 (body=%s)", status, raw)
	}
}

func TestControlScopeNoAdminTokenStillDisabled(t *testing.T) {
	// ★ 既有口径：管理令牌未配置 ⇒ 控制面整体 503（不得出现任何可用入口）。
	_, srv := newTestGateway(t, nil, func(cfg *Config) { cfg.AdminToken = "" })
	status, body, raw := keyReq(t, srv.URL, http.MethodGet, "/mcp/control/tools", "", "")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want 503 (body=%s)", status, raw)
	}
	if body["error"] != "control plane disabled: admin token not configured" {
		t.Fatalf("error=%v (body=%s)", body["error"], raw)
	}
}

// ---------------------------------------------------------------------------
// 八、熔断开关：MCP_GATEWAY_CONTROL_SCOPE=off ⇒ 回退到「只认管理令牌」
// ---------------------------------------------------------------------------

func TestControlScopeDisabledFallback(t *testing.T) {
	g, srv := newTestGateway(t, nil, func(cfg *Config) { cfg.ControlScopeDisabled = true })
	secret := scopeKey(t, g, "default", ScopeRegister)

	status, body, raw := keyReq(t, srv.URL, http.MethodGet, "/mcp/control/adapters", secret, "")
	if status != http.StatusForbidden {
		t.Fatalf("status=%d, want 403 (body=%s)", status, raw)
	}
	if body["reason"] != "control_scope_disabled" {
		t.Fatalf("reason=%v, want control_scope_disabled (body=%s)", body["reason"], raw)
	}

	// 管理令牌仍然全权。
	status, _, raw = adminReq(t, srv.URL, http.MethodGet, "/mcp/control/adapters", "")
	if status != http.StatusOK {
		t.Fatalf("admin status=%d, want 200 (body=%s)", status, raw)
	}
}

// ---------------------------------------------------------------------------
// 九、scope 常量与超集语义的守护
// ---------------------------------------------------------------------------

func TestControlScopeConstantsAndSuperset(t *testing.T) {
	// 既有三位逐字不动。
	if ScopeRead != "mcp.read" || ScopeExecute != "mcp.execute" || ScopeAdmin != "mcp.admin" {
		t.Fatalf("既有 scope 常量被改动：%q/%q/%q", ScopeRead, ScopeExecute, ScopeAdmin)
	}
	// 新增四位互不重名。
	seen := map[string]bool{}
	for _, s := range ControlScopes() {
		if seen[s] {
			t.Fatalf("scope 常量重复：%s", s)
		}
		seen[s] = true
	}
	// mcp.admin 是超集：持有它 ⇒ 具备全部 scope。
	admin := &Identity{Scopes: []string{ScopeAdmin}}
	for _, s := range ControlScopes() {
		if !admin.HasScope(s) {
			t.Fatalf("mcp.admin 应隐含 %s", s)
		}
	}
	// 其余各位互相不派生。
	registerOnly := &Identity{Scopes: []string{ScopeRegister}}
	for _, s := range []string{ScopeManage, ScopeAnalyze, ScopeTopology} {
		if registerOnly.HasScope(s) {
			t.Fatalf("mcp.register 不应隐含 %s", s)
		}
	}
}

// TestControlScopeRFC9728ScopesSupported 断言受保护资源元数据列出了新能力位。
func TestControlScopeRFC9728ScopesSupported(t *testing.T) {
	_, srv := newTestGateway(t, nil)
	resp, err := http.Get(srv.URL + "/.well-known/oauth-protected-resource")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	raw2, _ := json.Marshal(body["scopes_supported"])
	if !bytes.Contains(raw2, []byte(ScopeRegister)) {
		t.Fatalf("scopes_supported 未含 %s：%s", ScopeRegister, string(raw2))
	}
}
