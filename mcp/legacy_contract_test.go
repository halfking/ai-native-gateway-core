package mcp

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// 本文件是「与既有 tools/mcp-gateway **逐字一致**的契约」的守护用例。
//
// ★ 每个用例都对应既有仓的一处字面契约（错误码 / 响应头 / 指标族名），
//   目的：既有实现改动时，这里先红，而不是让线上客户端静默失配。
//
// ★ 既有已占用的码（必须逐字沿用，改号＝破坏既有客户端的分支）：
//   -32000 上游错误、-32001 bindings 拒绝、-32002 限流、-32003 tool-allow 拒绝、
//   -32004 请求体超限(413)。
//   新增能力只能落在既有**未使用**的取值上（-32005 工具不存在、-32006 熔断、
//   -32007 审批缺失）。

// TestLegacyBindingDeniedUsesLegacyCode 校验 bindings 拒绝用既有码 -32001。
//
// 既有出处：tools/mcp-gateway/src/bindings.js 的
// `jsonRpcError(res, null, -32001, ...)`（inactive 401 / binding disabled 403）。
func TestLegacyBindingDeniedUsesLegacyCode(t *testing.T) {
	up := newFakeUpstream(t, []map[string]any{{"name": "search_memory"}})
	g, srv := newTestGateway(t, up)
	secret := issueKey(t, g, KeySpec{
		TenantID: "default",
		Scopes:   []string{ScopeRead, ScopeExecute},
		Bindings: map[string]bool{"memora": false}, // 单边禁用该后端
	})

	status, body := rpcCall(t, srv, "/mcp", secret, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "memora__search_memory", "arguments": map[string]any{"q": "x"}},
	})
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body=%v)", status, body)
	}
	if code := rpcErrorCode(body); code != CodeBindingDenied {
		t.Fatalf("error code = %d, want %d (-32001 legacy bindings code)", code, CodeBindingDenied)
	}
}

// TestLegacyInactiveCredentialUsesLegacyCode 校验凭证失效用既有码 -32001。
//
// 既有出处：src/bindings.js 的 `API key is inactive or revoked`（401 + -32001）。
func TestLegacyInactiveCredentialUsesLegacyCode(t *testing.T) {
	g, srv := newTestGateway(t, nil)
	secret := issueKey(t, g, KeySpec{TenantID: "default"})
	keys, err := g.Store().ListKeys(context.Background(), "default")
	if err != nil || len(keys) != 1 {
		t.Fatalf("list keys: %v %d", err, len(keys))
	}
	if err := g.RevokeKey(context.Background(), "default", keys[0].ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	status, body := rpcCall(t, srv, "/mcp", secret, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/list",
	})
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body=%v)", status, body)
	}
	if code := rpcErrorCode(body); code != CodeBindingDenied {
		t.Fatalf("error code = %d, want %d (-32001 legacy inactive code)", code, CodeBindingDenied)
	}
}

// TestLegacyPayloadTooLarge 校验请求体超限用既有码 -32004 + HTTP 413。
//
// 既有出处：src/gateway.js 的 tool-allow 预读体超限（413 + code -32004）。
// ★ 本实现此前用 LimitReader **静默截断** ⇒ 表现为 JSON 解析失败（-32700），
//
//	把真因掩盖 ⇒ 既有客户端按 -32004 分支永远命中不到。
func TestLegacyPayloadTooLarge(t *testing.T) {
	g, srv := newTestGateway(t, nil, func(c *Config) { c.MaxBodyBytes = 32 })
	secret := issueKey(t, g, KeySpec{TenantID: "default"})

	big := map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "tools/list",
		"params": map[string]any{"padding": strings.Repeat("x", 512)},
	}
	status, body := rpcCall(t, srv, "/mcp", secret, big)
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 (body=%v)", status, body)
	}
	if code := rpcErrorCode(body); code != CodePayloadTooLarge {
		t.Fatalf("error code = %d, want %d (-32004 legacy payload-too-large code)", code, CodePayloadTooLarge)
	}
}

// TestLegacyToolNotFoundUsesUnusedCode 校验「工具不存在」落在既有**未使用**的码上。
//
// ★ 既有实现没有「工具不存在」分支（工具集由上游决定）⇒ 新增能力；
//
//	但 -32001/-32004 既有已占用 ⇒ 不能占用 ⇒ 用 -32005。
func TestLegacyToolNotFoundUsesUnusedCode(t *testing.T) {
	up := newFakeUpstream(t, []map[string]any{{"name": "search_memory"}})
	g, srv := newTestGateway(t, up)
	secret := issueKey(t, g, KeySpec{TenantID: "default", Scopes: []string{ScopeRead, ScopeExecute}})

	// ★ 归属适配器不存在 ⇒ 网关侧 404 + -32005。
	//   （适配器存在但工具名对不上时，网关按 `<adapter>__<tool>` 规范化名把请求
	//     交给上游，由上游裁定 —— 这与既有的职责划分一致。）
	status, body := rpcCall(t, srv, "/mcp", secret, map[string]any{
		"jsonrpc": "2.0", "id": 4, "method": "tools/call",
		"params": map[string]any{"name": "ghost__no_such_tool", "arguments": map[string]any{}},
	})
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body=%v)", status, body)
	}
	if code := rpcErrorCode(body); code != CodeToolNotFound {
		t.Fatalf("error code = %d, want %d (-32005)", code, CodeToolNotFound)
	}
	// 既有占用的码不得被新增能力占用。
	for _, occupied := range []int{-32000, -32001, -32002, -32003, -32004} {
		if CodeToolNotFound == occupied {
			t.Fatalf("CodeToolNotFound (%d) collides with a legacy-occupied code", occupied)
		}
	}
}

// TestLegacyToolAllowCounters 校验遗留旧名指标族确实在自增。
//
// ★ 这些计数器是运维判断「收口到底生不生效」的抓手：
//
//	· tool_allow_checks_total      —— 判定次数
//	· tool_allow_not_enforced_total —— **允许表为空 ⇒ 执法跳过**（收口没生效）
//	· tool_allow_denied_total      —— 真的拒了
//	既有出处：tools/mcp-gateway/src/tool-allow.js 的同名指标。
func TestLegacyToolAllowCounters(t *testing.T) {
	up := newFakeUpstream(t, []map[string]any{{"name": "search_memory"}})
	g, srv := newTestGateway(t, up)

	// 空允许表 ⇒ 执法跳过 ⇒ not_enforced 自增。
	before := testutil.ToFloat64(toolAllowNotEnforcedTotal)
	checksBefore := testutil.ToFloat64(toolAllowChecksTotal)
	loose := issueKey(t, g, KeySpec{TenantID: "default", Scopes: []string{ScopeRead, ScopeExecute}})
	status, _ := rpcCall(t, srv, "/mcp", loose, map[string]any{
		"jsonrpc": "2.0", "id": 5, "method": "tools/call",
		"params": map[string]any{"name": "memora__search_memory", "arguments": map[string]any{"q": "x"}},
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if got := testutil.ToFloat64(toolAllowNotEnforcedTotal); got != before+1 {
		t.Fatalf("tool_allow_not_enforced_total = %v, want %v", got, before+1)
	}
	if got := testutil.ToFloat64(toolAllowChecksTotal); got != checksBefore+1 {
		t.Fatalf("tool_allow_checks_total = %v, want %v", got, checksBefore+1)
	}

	// 有允许表但工具不在其中 ⇒ denied 自增，not_enforced 不再增。
	deniedBefore := testutil.ToFloat64(toolAllowDeniedTotal)
	notEnforcedBefore := testutil.ToFloat64(toolAllowNotEnforcedTotal)
	strict := issueKey(t, g, KeySpec{
		TenantID: "default", Scopes: []string{ScopeRead, ScopeExecute},
		AllowedTools: []string{"memora__other_tool"},
	})
	status, _ = rpcCall(t, srv, "/mcp", strict, map[string]any{
		"jsonrpc": "2.0", "id": 6, "method": "tools/call",
		"params": map[string]any{"name": "memora__search_memory", "arguments": map[string]any{"q": "x"}},
	})
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", status)
	}
	if got := testutil.ToFloat64(toolAllowDeniedTotal); got != deniedBefore+1 {
		t.Fatalf("tool_allow_denied_total = %v, want %v", got, deniedBefore+1)
	}
	if got := testutil.ToFloat64(toolAllowNotEnforcedTotal); got != notEnforcedBefore {
		t.Fatalf("tool_allow_not_enforced_total = %v, want unchanged %v", got, notEnforcedBefore)
	}
}

// TestLegacyBindingCounters 校验 bindings 旧名族在自增。
//
// 既有出处：tools/mcp-gateway/src/bindings.js 的同名指标。
func TestLegacyBindingCounters(t *testing.T) {
	up := newFakeUpstream(t, []map[string]any{{"name": "search_memory"}})
	g, srv := newTestGateway(t, up)
	secret := issueKey(t, g, KeySpec{
		TenantID: "default", Scopes: []string{ScopeRead, ScopeExecute},
		Bindings: map[string]bool{"memora": false},
	})
	before := testutil.ToFloat64(bindingsDeniedTotal)
	checksBefore := testutil.ToFloat64(bindingsChecksTotal)
	status, _ := rpcCall(t, srv, "/mcp", secret, map[string]any{
		"jsonrpc": "2.0", "id": 7, "method": "tools/call",
		"params": map[string]any{"name": "memora__search_memory", "arguments": map[string]any{"q": "x"}},
	})
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", status)
	}
	if got := testutil.ToFloat64(bindingsDeniedTotal); got != before+1 {
		t.Fatalf("bindings_denied_total = %v, want %v", got, before+1)
	}
	if got := testutil.ToFloat64(bindingsChecksTotal); got != checksBefore+1 {
		t.Fatalf("bindings_checks_total = %v, want %v", got, checksBefore+1)
	}
}
