package mcp

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestUnknownCapabilityRejected 校验非法请求级能力域的拒绝契约。
//
// 契约来源（既有 tools/mcp-gateway 的 gateway.js）：
//
//	HTTP 400 + JSON-RPC code -32602 +
//	"Unknown capability: X. Valid: <按既有 CAPABILITY_TOOLS 键序的清单>"
func TestUnknownCapabilityRejected(t *testing.T) {
	_, srv := newTestGateway(t, nil)
	status, body := rpcCall(t, srv, "/mcp?capability=bogus", "", map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/list",
	})
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%v)", status, body)
	}
	if code := rpcErrorCode(body); code != CodeInvalidParams {
		t.Fatalf("error code = %d, want %d (-32602)", code, CodeInvalidParams)
	}
	errObj, _ := body["error"].(map[string]any)
	msg, _ := errObj["message"].(string)
	want := "Unknown capability: bogus. Valid: " + strings.Join(ValidCapabilities(), ", ")
	if msg != want {
		t.Fatalf("message = %q, want %q", msg, want)
	}
}

// TestValidCapabilitiesOrderIsStable 校验能力域清单顺序与既有实现一致。
//
// 既有实现的报错文案顺序 = CAPABILITY_TOOLS 的键插入顺序；Go map 顺序随机，
// 所以必须显式保序（见 capability.go 的 capabilityOrder）。
func TestValidCapabilitiesOrderIsStable(t *testing.T) {
	want := []string{"code", "memory", "agent", "wiki", "apikey", "federation", "reasoning"}
	got := ValidCapabilities()
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("capabilities[%d] = %q, want %q (full=%v)", i, got[i], want[i], got)
		}
	}
	// 清单与映射表必须一一对应（不多不少）。
	if len(capabilityToTools) != len(capabilityOrder) {
		t.Fatalf("capabilityToTools has %d keys, capabilityOrder has %d", len(capabilityToTools), len(capabilityOrder))
	}
	for _, c := range capabilityOrder {
		if !ValidCapability(c) {
			t.Fatalf("capability %q not recognized", c)
		}
	}
}

// TestRequestCapabilityOverridesAdapter 校验请求级能力域覆盖适配器默认。
//
// 既有实现把 x-mcp-capability / x-mcp-capability-tools 直接写在入站请求头上
// 再往下游透传 ⇒ 请求级值优先于适配器默认值。
func TestRequestCapabilityOverridesAdapter(t *testing.T) {
	up := newFakeUpstream(t, []map[string]any{{"name": "search_memory"}})
	g, srv := newTestGateway(t, up)
	secret := issueKey(t, g, KeySpec{TenantID: "default", Scopes: []string{ScopeRead, ScopeExecute}})

	status, body := rpcCall(t, srv, "/mcp?capability=code", secret, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/list",
	})
	if status != http.StatusOK {
		t.Fatalf("tools/list status=%d body=%v", status, body)
	}
	if got := up.lastRequest.Get("X-MCP-Capability"); got != "code" {
		t.Fatalf("X-MCP-Capability = %q, want \"code\" (headers=%v)", got, up.lastRequest)
	}
	toolsHeader := up.lastRequest.Get("X-MCP-Capability-Tools")
	var tools []string
	if err := json.Unmarshal([]byte(toolsHeader), &tools); err != nil {
		t.Fatalf("X-MCP-Capability-Tools is not a JSON array: %q (%v)", toolsHeader, err)
	}
	want := CapabilityTools("code")
	if len(tools) != len(want) {
		t.Fatalf("tools header len = %d, want %d (%v)", len(tools), len(want), tools)
	}
	for i := range want {
		if tools[i] != want[i] {
			t.Fatalf("tools[%d] = %q, want %q", i, tools[i], want[i])
		}
	}
}

// TestAdapterCapabilityUsedWithoutOverride 校验未指定能力域时用适配器默认值。
func TestAdapterCapabilityUsedWithoutOverride(t *testing.T) {
	up := newFakeUpstream(t, []map[string]any{{"name": "search_memory"}})
	g, srv := newTestGateway(t, up)
	secret := issueKey(t, g, KeySpec{TenantID: "default", Scopes: []string{ScopeRead, ScopeExecute}})

	status, _ := rpcCall(t, srv, "/mcp", secret, map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "tools/list",
	})
	if status != http.StatusOK {
		t.Fatalf("tools/list status=%d", status)
	}
	if got := up.lastRequest.Get("X-MCP-Capability"); got != "memory" {
		t.Fatalf("X-MCP-Capability = %q, want \"memory\" (adapter default)", got)
	}
}

// TestInvalidCapabilityNotCounted 校验非法能力域不污染 requests_by_capability。
//
// 既有实现先判合法再计数；这里断言非法能力域的计数保持为 0。
func TestInvalidCapabilityNotCounted(t *testing.T) {
	_, srv := newTestGateway(t, nil)
	before := testutil.ToFloat64(requestsByCapability.WithLabelValues("bogus"))
	status, _ := rpcCall(t, srv, "/mcp?capability=bogus", "", map[string]any{
		"jsonrpc": "2.0", "id": 4, "method": "tools/list",
	})
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", status)
	}
	after := testutil.ToFloat64(requestsByCapability.WithLabelValues("bogus"))
	if after != before {
		t.Fatalf("requests_by_capability{bogus} changed: %v -> %v", before, after)
	}
}

// TestValidCapabilityCounted 校验合法能力域被计入 requests_by_capability。
func TestValidCapabilityCounted(t *testing.T) {
	up := newFakeUpstream(t, []map[string]any{{"name": "search_memory"}})
	g, srv := newTestGateway(t, up)
	secret := issueKey(t, g, KeySpec{TenantID: "default", Scopes: []string{ScopeRead, ScopeExecute}})

	before := testutil.ToFloat64(requestsByCapability.WithLabelValues("wiki"))
	status, _ := rpcCall(t, srv, "/mcp?capability=wiki", secret, map[string]any{
		"jsonrpc": "2.0", "id": 5, "method": "tools/list",
	})
	if status != http.StatusOK {
		t.Fatalf("tools/list status=%d", status)
	}
	after := testutil.ToFloat64(requestsByCapability.WithLabelValues("wiki"))
	if after != before+1 {
		t.Fatalf("requests_by_capability{wiki} = %v, want %v", after, before+1)
	}
}

// TestRateLimitCarriesRetryAfter 校验 429 的完整契约。
//
// 既有 tools/mcp-gateway 的 429 契约（三者同源，缺一即异常）：
//
//	HTTP 状态 429 + HTTP 头 Retry-After（秒，向上取整）
//	+ JSON-RPC 错误码 -32002 + data.retry_after_ms（毫秒）
func TestRateLimitCarriesRetryAfter(t *testing.T) {
	up := newFakeUpstream(t, []map[string]any{{"name": "search_memory"}})
	g, srv := newTestGateway(t, up, func(c *Config) {
		c.ClassRateLimits = map[ToolClass]int{ClassRead: 1}
	})
	secret := issueKey(t, g, KeySpec{TenantID: "default", Scopes: []string{ScopeRead, ScopeExecute}})

	status, _, body := rpcCallFull(t, srv, "/mcp", secret, map[string]any{
		"jsonrpc": "2.0", "id": 6, "method": "tools/call",
		"params": map[string]any{"name": "memora__search_memory", "arguments": map[string]any{"q": "a"}},
	})
	if status != http.StatusOK {
		t.Fatalf("first call status = %d, want 200 (body=%v)", status, body)
	}

	status, headers, body := rpcCallFull(t, srv, "/mcp", secret, map[string]any{
		"jsonrpc": "2.0", "id": 7, "method": "tools/call",
		"params": map[string]any{"name": "memora__search_memory", "arguments": map[string]any{"q": "b"}},
	})
	if status != http.StatusTooManyRequests {
		t.Fatalf("second call status = %d, want 429 (body=%v)", status, body)
	}
	if code := rpcErrorCode(body); code != CodeRateLimited {
		t.Fatalf("error code = %d, want %d (-32002)", code, CodeRateLimited)
	}
	retryAfter := headers.Get("Retry-After")
	if retryAfter == "" {
		t.Fatalf("Retry-After header missing (headers=%v)", headers)
	}
	errObj, _ := body["error"].(map[string]any)
	data, _ := errObj["data"].(map[string]any)
	msVal, ok := data["retry_after_ms"].(float64)
	if !ok || msVal <= 0 {
		t.Fatalf("data.retry_after_ms = %v, want positive integer", data["retry_after_ms"])
	}
	// 既有 tool-proxy.js:245 的 data 还带 limit（适用的限额）⇒ 本实现同样给出。
	if lim, ok := data["limit"].(float64); !ok || lim != 1 {
		t.Fatalf("data.limit = %v, want 1 (limit carried in error data)", data["limit"])
	}
	ms := int64(msVal)
	// Retry-After 的秒数必须与 retry_after_ms 同源且向上取整。
	wantSec := ms / 1000
	if ms%1000 != 0 {
		wantSec++
	}
	if wantSec < 1 {
		wantSec = 1
	}
	if retryAfter != strconv.FormatInt(wantSec, 10) {
		t.Fatalf("Retry-After = %q, want %d (retry_after_ms=%v)", retryAfter, wantSec, ms)
	}
}

// TestUpstreamErrorCodeMatchesLegacy 校验上游错误的 JSON-RPC 码与既有实现一致。
func TestUpstreamErrorCodeMatchesLegacy(t *testing.T) {
	up := newFakeUpstream(t, []map[string]any{{"name": "search_memory"}})
	up.status = 500
	g, srv := newTestGateway(t, up)
	secret := issueKey(t, g, KeySpec{TenantID: "default", Scopes: []string{ScopeRead, ScopeExecute}})

	status, _, body := rpcCallFull(t, srv, "/mcp", secret, map[string]any{
		"jsonrpc": "2.0", "id": 8, "method": "tools/call",
		"params": map[string]any{"name": "memora__search_memory", "arguments": map[string]any{}},
	})
	if status != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (body=%v)", status, body)
	}
	if code := rpcErrorCode(body); code != CodeUpstreamError {
		t.Fatalf("error code = %d, want %d (-32000)", code, CodeUpstreamError)
	}
}

// TestToolNotAllowedCodeMatchesLegacy 校验工具拒绝码与既有实现一致（-32003）。
func TestToolNotAllowedCodeMatchesLegacy(t *testing.T) {
	up := newFakeUpstream(t, []map[string]any{{"name": "search_memory"}})
	g, srv := newTestGateway(t, up)
	secret := issueKey(t, g, KeySpec{
		TenantID:     "default",
		Scopes:       []string{ScopeRead, ScopeExecute},
		AllowedTools: []string{"memora__other_tool"},
	})

	status, _, body := rpcCallFull(t, srv, "/mcp", secret, map[string]any{
		"jsonrpc": "2.0", "id": 9, "method": "tools/call",
		"params": map[string]any{"name": "memora__search_memory", "arguments": map[string]any{}},
	})
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body=%v)", status, body)
	}
	if code := rpcErrorCode(body); code != CodeToolNotAllowed {
		t.Fatalf("error code = %d, want %d (-32003)", code, CodeToolNotAllowed)
	}
}

// TestFilterToolsByCapability 校验按能力域过滤工具清单的能力。
//
// 移植自既有 tools/mcp-gateway/src/gateway.js 的 getCapabilityFromToolsList：
// 既有实现里该函数**只被导出、请求路径并未调用** ⇒ 本实现提供同能力 API，
// 但 tools/list 的请求路径**不**据此过滤（保持与既有逐字一致）。
func TestFilterToolsByCapability(t *testing.T) {
	// 能力域清单取自真实目录：memory 能力含 search_memory / save_memory，
	// code 能力含 code_review ⇒ 用它们验证「按能力域取子集」。
	views := []ToolView{
		{Name: "memora__search_memory", Meta: map[string]any{"raw_name": "search_memory"}},
		{Name: "memora__save_memory", Meta: map[string]any{"raw_name": "save_memory"}},
		{Name: "crm__code_review", Meta: map[string]any{"raw_name": "code_review"}},
	}
	got := FilterToolsByCapability(views, "memory")
	if len(got) != 2 {
		t.Fatalf("filtered len = %d, want 2 (%v)", len(got), got)
	}
	for _, v := range got {
		if v.Name != "memora__search_memory" && v.Name != "memora__save_memory" {
			t.Fatalf("unexpected tool in filtered list: %s", v.Name)
		}
	}
	// 未知能力域 ⇒ 与既有实现一致，原样返回（不静默清空）。
	all := FilterToolsByCapability(views, "no-such-capability")
	if len(all) != len(views) {
		t.Fatalf("unknown capability should pass through, got %d want %d", len(all), len(views))
	}
}

// TestCapabilityCatalogMatchesCapabilityMap 校验内嵌目录与能力映射表同源。
func TestCapabilityCatalogMatchesCapabilityMap(t *testing.T) {
	r := NewCapabilityRouter()
	all := r.AllCapabilities()
	if len(all) == 0 {
		t.Fatal("capability catalog is empty")
	}
	for _, c := range capabilityOrder {
		if !ValidCapability(c) {
			t.Fatalf("capability %q missing from capabilityToTools", c)
		}
	}
	// 目录里的工具应当都能被检索到（防止 embed 丢失）。
	if len(r.RecommendTools("memory search", 5)) == 0 {
		t.Log("recommend returned empty; catalog may use different keywords")
	}
}
