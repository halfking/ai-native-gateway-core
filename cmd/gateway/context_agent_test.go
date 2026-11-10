package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/contextagent"
	"github.com/kaixuan/llm-gateway-go/mcp"
)

// TestLoopbackChatURLDerivation —— 回灌端点推导支点。
//
// ★ 回灌必须落在网关**自己**的在役链路上（鉴权 → 路由 → 凭据 → 上游 → 计费），
//
//	端点地址推导错了就会回灌到别的东西上（或回灌到自己都到不了的地址）。
func TestLoopbackChatURLDerivation(t *testing.T) {
	cases := []struct {
		listen string
		want   string
	}{
		{":8781", "http://127.0.0.1:8781/v1/chat/completions"},
		{"0.0.0.0:9000", "http://127.0.0.1:9000/v1/chat/completions"},
		{"127.0.0.1:18081", "http://127.0.0.1:18081/v1/chat/completions"},
		{"", "http://127.0.0.1:8781/v1/chat/completions"},
	}
	for _, c := range cases {
		got := loopbackChatURL(c.listen)
		if got != c.want {
			t.Fatalf("listen=%q: got %q, want %q", c.listen, got, c.want)
		}
	}
}

// TestEnvBoolStrictIsStrict —— 开关解析支点。
//
// ★ 新能力开关刻意比仓内宽松解析更严：宁可「配了没开」，不要「没配却开了」。
func TestEnvBoolStrictIsStrict(t *testing.T) {
	const key = "LLM_GATEWAY_TEST_CTX_AGENT_SWITCH"
	t.Setenv(key, "")
	if envBoolStrict(key) {
		t.Fatal("empty must be false")
	}
	for _, v := range []string{"1", "true", "TRUE", "yes", "on"} {
		t.Setenv(key, v)
		if !envBoolStrict(key) {
			t.Fatalf("%q must be true", v)
		}
	}
	for _, v := range []string{"0", "false", "no", "off", "enabled", " "} {
		t.Setenv(key, v)
		if envBoolStrict(key) {
			t.Fatalf("%q must be false", v)
		}
	}
}

// TestContextAgentDisabledByDefault —— 默认关支点。
//
// ★ 这是**新增能力面**（新端点 + 新 MCP 工具）：默认开启等于改变现网行为面。
func TestContextAgentDisabledByDefault(t *testing.T) {
	t.Setenv(envContextAgentEnabled, "")
	mux := http.NewServeMux()
	bundle := registerContextAgent(mux, nil, contextAgentDeps{})
	if bundle != nil {
		t.Fatal("context agent must stay disabled by default")
	}
	if contextAgentEnabled() {
		t.Fatal("contextAgentEnabled must be false by default")
	}
}

// TestContextAgentEnabledMountsFace —— 开启时挂载 HTTP 门面。
func TestContextAgentEnabledMountsFace(t *testing.T) {
	t.Setenv(envContextAgentEnabled, "1")
	mux := http.NewServeMux()
	bundle := registerContextAgent(mux, nil, contextAgentDeps{Listen: ":8781"})
	if bundle == nil {
		t.Fatal("context agent must be assembled when enabled")
	}
	if bundle.Agent == nil || bundle.Service == nil {
		t.Fatal("agent and service must be present")
	}
	// 未配记录目录 ⇒ 不装记录器（不落盘 ⇒ 无隐私外泄面）。
	if bundle.Agent.Recorder != nil {
		t.Fatal("recorder must stay off when no record dir is configured")
	}
	// 门面路由必须能匹配 /api/system/context-agent/...
	req := httptest.NewRequest(http.MethodPost, "/api/system/context-agent/resolve",
		strings.NewReader(`{"body":"{}"}`))
	_, pat := mux.Handler(req)
	if pat == "" {
		t.Fatal("context agent face is not mounted")
	}
}

// TestContextMemoryToolsRegistered —— MCP 本地工具注册支点。
//
// ★ 这两个工具是「网关自己的记忆能力」对外的唯一暴露面：
//
//	列不出来 ⇒ 客户端不知道有这条路；调不动 ⇒ 等于没有。
func TestContextMemoryToolsRegistered(t *testing.T) {
	reg := mcp.NewLocalToolRegistry()
	agent := &contextagent.Agent{}
	registerContextMemoryTools(reg, agent)

	list := reg.List()
	if len(list) != 2 {
		t.Fatalf("want 2 local tools, got %d", len(list))
	}
	byName := map[string]mcp.LocalTool{}
	for _, lt := range list {
		byName[lt.Name] = lt
	}
	for _, name := range contextagent.ToolNames {
		lt, ok := byName[name]
		if !ok {
			t.Fatalf("tool %q not registered", name)
		}
		if lt.Handler == nil {
			t.Fatalf("tool %q has no handler", name)
		}
		if lt.Description == "" {
			t.Fatalf("tool %q has no description (模型无从判断何时该用)", name)
		}
		if len(lt.InputSchema) == 0 {
			t.Fatalf("tool %q has no input schema", name)
		}
	}
	// 参数白名单必须含必填参数，否则治理链的 validateParams 会拒掉合法调用。
	stepTool := byName[contextagent.ToolLoadStepDetail]
	if !containsStr(stepTool.AllowedParams, "step_index") {
		t.Fatalf("load_step_detail allowed params must include step_index: %v", stepTool.AllowedParams)
	}
	searchTool := byName[contextagent.ToolSearchMemory]
	if !containsStr(searchTool.AllowedParams, "query") {
		t.Fatalf("search_memory allowed params must include query: %v", searchTool.AllowedParams)
	}
}

// TestContextMemoryToolFailsSoftly —— 记忆工具失败不打断调用方。
//
// ★ 没有加载器时（未启用 PG / Memora），工具必须回一句可读说明，
//
//	而不是把整个 MCP 调用打成错误。
func TestContextMemoryToolFailsSoftly(t *testing.T) {
	reg := mcp.NewLocalToolRegistry()
	registerContextMemoryTools(reg, &contextagent.Agent{})

	lt, ok := reg.Get(contextagent.ToolLoadStepDetail)
	if !ok {
		t.Fatal("load_step_detail not registered")
	}
	out, err := lt.Handler(context.Background(), &mcp.Identity{TenantID: "t1"}, map[string]any{
		"step_index": float64(3),
	})
	if err != nil {
		t.Fatalf("local tool must not hard-fail: %v", err)
	}
	text, _ := out.(string)
	if text == "" {
		t.Fatal("local tool must return a readable explanation")
	}
	if !strings.Contains(text, "步3") {
		t.Fatalf("explanation should mention the step: %q", text)
	}
}

// TestContextIdentityFromMCPKeepsTenant —— 租户边界支点。
//
// ★ 租户来自**凭证**，不来自参数 ⇒ 调用方无法靠参数把租户改掉。
func TestContextIdentityFromMCPKeepsTenant(t *testing.T) {
	id := contextIdentityFromMCP(&mcp.Identity{TenantID: "tenant-A"}, map[string]any{
		"tenant_id":  "tenant-B",
		"session_id": "sess-1",
	})
	if id.TenantID != "tenant-A" {
		t.Fatalf("tenant must come from the credential, got %q", id.TenantID)
	}
	if id.Session != "sess-1" {
		t.Fatalf("session must come from the arguments, got %q", id.Session)
	}
}

// containsStr 判断字符串切片是否含某元素。
func containsStr(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// TestEnvBoolStrictIgnoresUnset —— 环境变量缺失时读到的必须是空串。
func TestEnvBoolStrictIgnoresUnset(t *testing.T) {
	const key = "LLM_GATEWAY_TEST_CTX_AGENT_UNSET"
	_ = os.Unsetenv(key)
	if envBoolStrict(key) {
		t.Fatal("unset must be false")
	}
}
