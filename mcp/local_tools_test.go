package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// ★ 本文件钉住「网关本地工具」这条新通路：
//
//	① 本地工具能被 tools/list 列出（否则客户端不知道有这条路）；
//	② 本地工具能被 tools/call 调用，且**不经上游**；
//	③ 治理链不被绕过：scope / 允许表（deny-wins）/ 审批 / 参数校验都生效；
//	④ 零本地工具时行为与引入本特性前完全一致。

// registerMemoryLikeLocalTool 注册一个测试用的本地工具。
func registerMemoryLikeLocalTool(reg *LocalToolRegistry, called *int) {
	reg.Register(LocalTool{
		Name:        "load_step_detail",
		Description: "按步骤号取回该步骤的完整历史明细",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"step_index":{"type":"integer","minimum":1}},"required":["step_index"]}`),
		Handler: func(_ context.Context, _ *Identity, args map[string]any) (any, error) {
			*called++
			idx, _ := args["step_index"].(float64)
			return map[string]any{"step_index": int(idx), "detail": "internal/auth/client.go"}, nil
		},
	})
}

// registerGroupedLocalTool 注册一个带 group 的本地工具（分组过滤用）。
func registerGroupedLocalTool(reg *LocalToolRegistry, name, group string) {
	reg.Register(LocalTool{
		Name:        name,
		Description: "分组过滤用的本地工具",
		Group:       group,
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		Handler: func(_ context.Context, _ *Identity, _ map[string]any) (any, error) {
			return "ok", nil
		},
	})
}

// TestLocalTool_GroupFilterApplies —— tools/list?group=… 的过滤必须
// **对本地工具同样生效**。
//
// ★ 审计轮的缺陷：上游分支按 `meta.Group != group` 过滤，而本地分支
//
//	无条件全列 ⇒ 按 group 拉清单的客户端会拿到不属于该 group 的本地工具。
func TestLocalTool_GroupFilterApplies(t *testing.T) {
	reg := NewLocalToolRegistry()
	registerGroupedLocalTool(reg, "memory_probe", "memory")
	registerGroupedLocalTool(reg, "other_probe", "other")

	g, srv := newTestGateway(t, nil, func(c *Config) { c.LocalTools = reg })
	secret := issueKey(t, g, KeySpec{TenantID: "default", Scopes: []string{ScopeRead, ScopeExecute}})

	// 无 group ⇒ 全列。
	status, body := rpcCall(t, srv, "/mcp", secret, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/list",
	})
	if status != http.StatusOK {
		t.Fatalf("tools/list status=%d body=%v", status, body)
	}
	res := rpcResult(t, body)
	tools, _ := res["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("without group both local tools must be listed, got %d", len(tools))
	}

	// group=other ⇒ 只有 other_probe。
	status, body = rpcCall(t, srv, "/mcp?group=other", secret, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/list",
	})
	if status != http.StatusOK {
		t.Fatalf("tools/list?group=other status=%d body=%v", status, body)
	}
	res = rpcResult(t, body)
	tools, _ = res["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("group=other must list exactly one local tool, got %d (%v)", len(tools), tools)
	}
	m, _ := tools[0].(map[string]any)
	if m["name"] != "other_probe" {
		t.Fatalf("group=other must list other_probe, got %v", m["name"])
	}
}

// TestLocalTool_ListedInToolsList —— 本地工具必须出现在清单里。
func TestLocalTool_ListedInToolsList(t *testing.T) {
	reg := NewLocalToolRegistry()
	called := 0
	registerMemoryLikeLocalTool(reg, &called)

	g, srv := newTestGateway(t, nil, func(c *Config) { c.LocalTools = reg })
	secret := issueKey(t, g, KeySpec{TenantID: "default", Scopes: []string{ScopeRead, ScopeExecute}})

	status, body := rpcCall(t, srv, "/mcp", secret, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/list",
	})
	if status != http.StatusOK {
		t.Fatalf("tools/list status=%d body=%v", status, body)
	}
	res := rpcResult(t, body)
	tools, _ := res["tools"].([]any)
	found := false
	for _, tv := range tools {
		m, _ := tv.(map[string]any)
		if m["name"] == "load_step_detail" {
			found = true
			if m["description"] == "" {
				t.Fatal("local tool must carry a description (the model needs to know it exists)")
			}
		}
	}
	if !found {
		t.Fatalf("local tool must be listed, got %v", tools)
	}
}

// TestLocalTool_CallExecutesLocally —— 本地工具被调用时不碰上游。
func TestLocalTool_CallExecutesLocally(t *testing.T) {
	reg := NewLocalToolRegistry()
	called := 0
	registerMemoryLikeLocalTool(reg, &called)

	// upstream 传 nil：若本地工具被错误地转发到上游，这里会 404。
	g, srv := newTestGateway(t, nil, func(c *Config) { c.LocalTools = reg })
	secret := issueKey(t, g, KeySpec{TenantID: "default", Scopes: []string{ScopeRead, ScopeExecute}})

	status, body := rpcCall(t, srv, "/mcp", secret, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{
			"name":      "load_step_detail",
			"arguments": map[string]any{"step_index": 7},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("tools/call status=%d body=%v", status, body)
	}
	if called != 1 {
		t.Fatalf("local handler must run exactly once, got %d", called)
	}
	res := rpcResult(t, body)
	content, _ := res["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("expected one content block, got %v", res)
	}
	blk, _ := content[0].(map[string]any)
	if blk["type"] != "text" {
		t.Fatalf("content block type = %v", blk["type"])
	}
	text, _ := blk["text"].(string)
	if !strings.Contains(text, "internal/auth/client.go") {
		t.Fatalf("content missing the tool result: %q", text)
	}
}

// TestLocalTool_RequiresExecuteScope —— scope 闸对本地工具同样生效。
func TestLocalTool_RequiresExecuteScope(t *testing.T) {
	reg := NewLocalToolRegistry()
	called := 0
	registerMemoryLikeLocalTool(reg, &called)

	g, srv := newTestGateway(t, nil, func(c *Config) { c.LocalTools = reg })
	secret := issueKey(t, g, KeySpec{TenantID: "default", Scopes: []string{ScopeRead}})

	status, body := rpcCall(t, srv, "/mcp", secret, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{
			"name":      "load_step_detail",
			"arguments": map[string]any{"step_index": 7},
		},
	})
	if status != http.StatusForbidden {
		t.Fatalf("expected 403 without execute scope, got %d body=%v", status, body)
	}
	if called != 0 {
		t.Fatal("the handler must not run when the scope gate rejects")
	}
}

// TestLocalTool_DeniedByToolAllowList —— 允许表（deny-wins）对本地工具同样生效。
func TestLocalTool_DeniedByToolAllowList(t *testing.T) {
	reg := NewLocalToolRegistry()
	called := 0
	registerMemoryLikeLocalTool(reg, &called)

	g, srv := newTestGateway(t, nil, func(c *Config) { c.LocalTools = reg })
	secret := issueKey(t, g, KeySpec{
		TenantID:    "default",
		Scopes:      []string{ScopeRead, ScopeExecute},
		DeniedTools: []string{"load_step_detail"},
	})

	status, body := rpcCall(t, srv, "/mcp", secret, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{
			"name":      "load_step_detail",
			"arguments": map[string]any{"step_index": 7},
		},
	})
	if status != http.StatusForbidden {
		t.Fatalf("expected 403 for a denied tool, got %d body=%v", status, body)
	}
	if called != 0 {
		t.Fatal("the handler must not run when the allow-list denies the tool")
	}
}

// TestLocalTool_InvalidParamsRejected —— 参数校验对本地工具同样生效。
func TestLocalTool_InvalidParamsRejected(t *testing.T) {
	reg := NewLocalToolRegistry()
	called := 0
	registerMemoryLikeLocalTool(reg, &called)

	g, srv := newTestGateway(t, nil, func(c *Config) { c.LocalTools = reg })
	secret := issueKey(t, g, KeySpec{TenantID: "default", Scopes: []string{ScopeRead, ScopeExecute}})

	// step_index 缺失 ⇒ 不满足 required。
	status, body := rpcCall(t, srv, "/mcp", secret, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{
			"name":      "load_step_detail",
			"arguments": map[string]any{"wrong": 1},
		},
	})
	if status != http.StatusForbidden {
		t.Fatalf("expected 403 for invalid params, got %d body=%v", status, body)
	}
	if called != 0 {
		t.Fatal("the handler must not run when params are invalid")
	}
}

// TestLocalTool_ApprovalRequired —— 敏感度审批闸对本地工具同样生效。
func TestLocalTool_ApprovalRequired(t *testing.T) {
	reg := NewLocalToolRegistry()
	called := 0
	reg.Register(LocalTool{
		Name:            "load_step_detail",
		Description:     "sensitive",
		Sensitivity:     SensitivityHigh,
		RequireApproval: true,
		Handler: func(_ context.Context, _ *Identity, _ map[string]any) (any, error) {
			called++
			return "ok", nil
		},
	})

	g, srv := newTestGateway(t, nil, func(c *Config) { c.LocalTools = reg })
	secret := issueKey(t, g, KeySpec{TenantID: "default", Scopes: []string{ScopeRead, ScopeExecute}})

	status, body := rpcCall(t, srv, "/mcp", secret, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{
			"name":      "load_step_detail",
			"arguments": map[string]any{"step_index": 7},
		},
	})
	if status != http.StatusForbidden {
		t.Fatalf("expected 403 without an approval, got %d body=%v", status, body)
	}
	if called != 0 {
		t.Fatal("the handler must not run when the approval gate rejects")
	}
}

// TestLocalTool_ZeroRegistryLeavesListUnchanged —— 零本地工具 ⇒ 清单里没有本地条目
// （⇒ 引入本特性前后行为一致）。
func TestLocalTool_ZeroRegistryLeavesListUnchanged(t *testing.T) {
	g, srv := newTestGateway(t, nil)
	secret := issueKey(t, g, KeySpec{TenantID: "default", Scopes: []string{ScopeRead, ScopeExecute}})

	status, body := rpcCall(t, srv, "/mcp", secret, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/list",
	})
	if status != http.StatusOK {
		t.Fatalf("tools/list status=%d", status)
	}
	res := rpcResult(t, body)
	tools, _ := res["tools"].([]any)
	for _, tv := range tools {
		m, _ := tv.(map[string]any)
		if strings.HasPrefix(strings.ToLower(toString(m["name"])), "load_") {
			t.Fatalf("unexpected local tool in an empty registry: %v", m["name"])
		}
	}
}

// toString 辅助。
func toString(v any) string {
	s, _ := v.(string)
	return s
}

// TestLocalToolRegistry_RegisterAndList —— 注册表自身的基本契约。
func TestLocalToolRegistry_RegisterAndList(t *testing.T) {
	reg := NewLocalToolRegistry()
	reg.Register(LocalTool{Name: "b_tool", Handler: func(context.Context, *Identity, map[string]any) (any, error) { return "x", nil }})
	reg.Register(LocalTool{Name: "a_tool", Handler: func(context.Context, *Identity, map[string]any) (any, error) { return "x", nil }})
	// 无效注册（无 handler / 无名）必须被忽略。
	reg.Register(LocalTool{Name: "c_tool"})
	reg.Register(LocalTool{Handler: func(context.Context, *Identity, map[string]any) (any, error) { return "x", nil }})

	list := reg.List()
	if len(list) != 2 {
		t.Fatalf("expected 2 registered tools, got %d", len(list))
	}
	if list[0].Name != "a_tool" || list[1].Name != "b_tool" {
		t.Fatalf("listing must be sorted by name: %+v", list)
	}
	if _, ok := reg.Get("c_tool"); ok {
		t.Fatal("a tool without a handler must not be registered")
	}
}
