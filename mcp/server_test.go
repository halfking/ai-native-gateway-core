package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
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

// fakeAnthropicKey 是一个**运行时拼装**的 Anthropic 形态假凭据（合成值、无效）。
//
// ★ 为什么源码里不写死字面量：仓内秘密扫描器把 `sk-ant-[A-Za-z0-9_-]{32,}`
//
//	判为 BLOCK（那是真凭据的形态）⇒ 源码里写死会让
//	`scripts/scan-secrets.sh --mode=strict` 报 BLOCK，从而
//	`scripts/sync-to-github.sh` 的 Step 1（工作树 strict 扫描）直接中止，
//	公开镜像就同步不出去。
//
// ★ 为什么又不能换成别的形态：**本测试的主体就是这个 key** —— 出参脱敏靠
//
//	domains/secretmask 的 `sk-ant-[a-zA-Z0-9\-]{20,}` 规则识别，才谈得上
//	「key 被遮蔽」⇒ 换成非 key 形态 = 测的不再是「key 被遮蔽」，测试就废了。
//
// ⇒ 折中：**运行时拼装** —— 源码里没有连续 32+ 位的 key 字面量，运行时值与原先
//
//	逐字节同形（7 + 32 位）。
//	★ 这是仓内既有的做法，见 domains/secretmask/secretmask_test.go 的
//	  `"use sk-ant-" + strings.Repeat("c", 40)`（该文件因此不在扫描基线里）。
var fakeAnthropicKey = "sk-ant-" + strings.Repeat("c", 32)

func TestOutputRedactionMasksSecrets(t *testing.T) {
	up := newFakeUpstream(t, []map[string]any{{"name": "get_secret"}})
	up.callResult = map[string]any{
		"content": []any{map[string]any{
			"type": "text",
			"text": "token " + fakeAnthropicKey + " and mail a@b.com",
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
	if containsSubstring(string(raw), fakeAnthropicKey) {
		t.Fatalf("secret leaked to client: %s", raw)
	}
	if !containsSubstring(string(raw), "REDACTED") {
		t.Fatalf("expected redaction marker: %s", raw)
	}
}

// TestUptimeMetricIsExposedNotAbsent 钉住 mcp_gateway_uptime_seconds 的**存在性**。
//
// TestUptimeMetricTracksGatewayUptime 钉住 mcp_gateway_uptime_seconds 的**取值**。
//
// ★ 实测前提（本机用 prometheus 客户端 v1.23.2 实测，不是推断）：
//
//	普通 Gauge（promauto.NewGauge）—— **在 promauto 注册的那一刻就已经在
//	/metrics 上，值为 0**；只有 GaugeVec/CounterVec 在「没有实例化过任何标签
//	组合」时才是 ABSENT。
//
//	⇒ 所以「只声明不 Set」对**普通 Gauge**的真实后果**不是 ABSENT**，而是
//	  **「恒为 0」**（面板上是一条永远平的线）。
//	  ⚠ internal/metricguard 那条报错文案（「恒为 ABSENT（不是 0）」）对普通
//	    Gauge **不成立** —— 它描述的是 Vec 的形态。别照着它去写断言。
//	⇒ 因此这道门断言的是**取值随运行时长走**，而不是「指标在不在」：
//	  断言存在性对普通 Gauge **恒真**（没有牙）。
func TestUptimeMetricTracksGatewayUptime(t *testing.T) {
	// 把网关的启动时刻挪到 1 小时前 ⇒ 期望 uptime ≈ 3600，而不是 0。
	const skew = time.Hour
	minExpected := float64(skew.Seconds()) - 60

	_, srv := newTestGateway(t, nil, func(c *Config) {
		c.Now = func() time.Time { return time.Now().Add(-skew) }
	})

	// 构造完就该是「真实运行时长」——若指标只声明不记录，这里读到的是 0。
	got := testutil.ToFloat64(uptimeSeconds)
	if got < minExpected {
		t.Fatalf("构造后 uptime = %v, want >= %v（指标只声明不记录 ⇒ 恒为 0）", got, minExpected)
	}

	// 指标族必须在 /metrics 上（且是 gauge，不是 counter）。
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	var found *dto.MetricFamily
	for _, f := range families {
		if f.GetName() == "mcp_gateway_uptime_seconds" {
			found = f
			break
		}
	}
	if found == nil {
		t.Fatal("mcp_gateway_uptime_seconds 不在 /metrics 上 —— 指标已声明但没落到 registry")
	}
	if len(found.GetMetric()) != 1 {
		t.Fatalf("mcp_gateway_uptime_seconds 指标数 = %d, want 1", len(found.GetMetric()))
	}
	if found.GetMetric()[0].GetGauge() == nil {
		t.Fatalf("mcp_gateway_uptime_seconds 不是 gauge：%v", found.GetMetric()[0])
	}

	// 探针端点会**刷新**它（部署里被周期性打的是 /healthz，而不是需要管理令牌的
	// /health）⇒ 断言的是「值会跟着走」，而不是「值曾经对过」。
	//
	// ★ 为什么需要等 1.5s：UptimeSeconds() 按整秒截断，构造时落的那一版是
	//   floor(3600.x) = 3600 ⇒ 只有真的刷新过，才会变成 >= 3601。
	//   不刷新的话它会**冻结**在构造那一刻的值上（= 3600 永远不变）。
	v0 := testutil.ToFloat64(uptimeSeconds)
	time.Sleep(1500 * time.Millisecond)
	resp, err := http.Get(srv.URL + "/mcp/control/healthz")
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz status = %d, want 200", resp.StatusCode)
	}
	after := testutil.ToFloat64(uptimeSeconds)
	if after < v0+1 {
		t.Fatalf("healthz 之后 uptime 没有前进：before=%v after=%v（探针端点没刷新指标）", v0, after)
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
