package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	optimizer "github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer"
	"github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer/harness"
	"github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer/strategies"
)

// newTestService 构造一个不带模型通道的服务（规则优化，确定性）。
func newTestService() *service {
	structural := strategies.NewStructural(strategies.DefaultStructuralConfig())
	fewshot := strategies.NewFewShot(strategies.DefaultFewShotConfig(), nil)
	opt := optimizer.NewOptimizer(
		optimizer.WithStrategies(structural, fewshot),
		optimizer.WithHarnessInjector(harness.NewInjector(
			harness.ProductionInjectorConfig(),
			harness.NewMockToolRegistry(),
			harness.NewMockSafetyRules(),
		)),
	)
	return &service{opt: opt, hookCache: newHookCache(time.Minute)}
}

func postJSON(t *testing.T, handler http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	buf, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestHookOptimizeContract(t *testing.T) {
	svc := newTestService()
	handler := svc.routes()

	rec := postJSON(t, handler, "/api/v1/prompts/optimize", HookOptimizeRequest{
		TenantID: "t1",
		Model:    "gpt-4o",
		Prompts: []HookPromptIn{
			{Role: "system", Content: "你是一个助手。"},
			{Role: "user", Content: "帮我把下面三条商品转成 JSON 数组"},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}

	var res HookOptimizeResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, rec.Body.String())
	}

	// 网关 Hook 依赖的字段必须存在且类型正确。
	if !strings.HasPrefix(res.OptimizationID, "opt_") {
		t.Errorf("optimization_id = %q, want opt_ prefix", res.OptimizationID)
	}
	if res.CacheHit {
		t.Error("first call must not be a cache hit")
	}
	if len(res.Prompts) != 2 {
		t.Fatalf("prompts len = %d, want 2 (hook rejects count mismatch)", len(res.Prompts))
	}
	// role 必须与请求一一对应。
	if res.Prompts[0].Role != "system" || res.Prompts[1].Role != "user" {
		t.Errorf("roles = %q/%q", res.Prompts[0].Role, res.Prompts[1].Role)
	}
	if res.OriginalTokens <= 0 || res.OptimizedTokens <= 0 {
		t.Errorf("token estimates = %d/%d, want > 0", res.OriginalTokens, res.OptimizedTokens)
	}
	if res.OptimizedTokens < res.OriginalTokens {
		t.Logf("note: optimized prompt is shorter than original (%d < %d)",
			res.OptimizedTokens, res.OriginalTokens)
	}
}

// 网关 Hook 的写回逻辑：只写回 changed=true 的 content。
// 因此 "changed" 必须真实反映内容是否变化，不能恒为 true 或恒为 false。
func TestHookOptimizeChangedFlagIsHonest(t *testing.T) {
	svc := newTestService()
	handler := svc.routes()

	rec := postJSON(t, handler, "/api/v1/prompts/optimize", HookOptimizeRequest{
		Prompts: []HookPromptIn{{Role: "user", Content: "你好"}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var res HookOptimizeResult
	_ = json.Unmarshal(rec.Body.Bytes(), &res)

	changed := res.Prompts[0].Changed
	sameContent := res.Prompts[0].Content == "你好"
	if changed == sameContent {
		t.Errorf("changed=%v but content same=%v — 标志与事实矛盾", changed, sameContent)
	}
}

func TestHookOptimizeCacheHit(t *testing.T) {
	svc := newTestService()
	handler := svc.routes()

	reqBody := HookOptimizeRequest{
		Model:   "m",
		Prompts: []HookPromptIn{{Role: "system", Content: "缓存测试提示词"}},
	}

	rec1 := postJSON(t, handler, "/api/v1/prompts/optimize", reqBody)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first status = %d", rec1.Code)
	}
	rec2 := postJSON(t, handler, "/api/v1/prompts/optimize", reqBody)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second status = %d", rec2.Code)
	}

	var r1, r2 HookOptimizeResult
	_ = json.Unmarshal(rec1.Body.Bytes(), &r1)
	_ = json.Unmarshal(rec2.Body.Bytes(), &r2)
	if r1.CacheHit {
		t.Error("first call should not be cache hit")
	}
	if !r2.CacheHit {
		t.Error("second identical call should be cache hit")
	}
	if r1.OptimizationID != r2.OptimizationID {
		t.Errorf("cached result should keep optimization_id: %q vs %q", r1.OptimizationID, r2.OptimizationID)
	}
}

func TestHookOptimizeRejectsBadInput(t *testing.T) {
	svc := newTestService()
	handler := svc.routes()

	rec := postJSON(t, handler, "/api/v1/prompts/optimize", HookOptimizeRequest{})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("empty prompts status = %d, want 400", rec.Code)
	}

	rec = postJSON(t, handler, "/api/v1/prompts/optimize", HookOptimizeRequest{
		Prompts: []HookPromptIn{{Role: "system", Content: "   "}},
	})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("blank content status = %d, want 400", rec.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/prompts/optimize", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", rec.Code)
	}
}

func TestEstimateTokens(t *testing.T) {
	if estimateTokens("") != 0 {
		t.Error("empty should be 0 tokens")
	}
	if estimateTokens("中文") != 2 {
		t.Errorf("2 CJK chars = %d, want 2", estimateTokens("中文"))
	}
	if estimateTokens("abcd") != 1 {
		t.Errorf("4 ascii = %d, want 1", estimateTokens("abcd"))
	}
	if estimateTokens("abcde") != 2 {
		t.Errorf("5 ascii = %d, want 2", estimateTokens("abcde"))
	}
}

func TestHookCacheDisabledWhenTTLErrorZero(t *testing.T) {
	c := newHookCache(0)
	c.Put("k", HookOptimizeResult{OptimizationID: "x"})
	if _, ok := c.Get("k"); ok {
		t.Error("cache with ttl=0 must not store")
	}
}
