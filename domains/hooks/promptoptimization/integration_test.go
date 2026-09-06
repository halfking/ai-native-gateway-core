// integration_test.go 端到端集成测试：真实 HTTP 客户端 → httptest 优化服务，
// 覆盖「未命中→调用→应用」「命中→不再调用」「服务宕机→回退」三条主链路。
package promptoptimization

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"
)

// TestIntegrationOptimizeCacheFallback 完整链路：
//  1. 首次请求：缓存未命中 → 调用优化服务 → system prompt 被改写
//  2. 相同请求：缓存命中 → 优化服务调用次数不变
//  3. 优化服务宕机后的新请求：回退原始 prompt，请求照常通过
func TestIntegrationOptimizeCacheFallback(t *testing.T) {
	var calls atomic.Int64
	upstream := okOptimizer(t, &calls)
	defer upstream.Close()

	cfg := FromEnv()
	cfg.Enabled = true
	cfg.OptimizerURL = upstream.URL
	cfg.Timeout = 2 * time.Second
	cfg.CacheTTL = time.Minute
	hook := NewHook(cfg, nil)
	ctx := context.Background()

	// ── 1. 未命中 → 优化并写回 ──
	env1 := newTestEnv(t, testRequestBody)
	if err := hook.Execute(ctx, env1); err != nil {
		t.Fatalf("round1 failed: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("round1: optimizer calls = %d, want 1", calls.Load())
	}
	var body1 map[string]any
	if err := json.Unmarshal(env1.TransformedRequest, &body1); err != nil {
		t.Fatal(err)
	}
	sys1 := body1["messages"].([]any)[0].(map[string]any)
	if sys1["content"] != "Optimized: You are helpful." {
		t.Fatalf("round1: system prompt not optimized: %v", sys1["content"])
	}

	// ── 2. 命中 → 不再调用 ──
	env2 := newTestEnv(t, testRequestBody)
	if err := hook.Execute(ctx, env2); err != nil {
		t.Fatalf("round2 failed: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("round2: optimizer calls = %d, want still 1 (cache hit)", calls.Load())
	}
	var body2 map[string]any
	if err := json.Unmarshal(env2.TransformedRequest, &body2); err != nil {
		t.Fatal(err)
	}
	sys2 := body2["messages"].([]any)[0].(map[string]any)
	if sys2["content"] != "Optimized: You are helpful." {
		t.Fatalf("round2: cached optimization not applied: %v", sys2["content"])
	}
	meta2 := env2.Metadata[MetaKeyPromptOptimization].(map[string]any)
	if meta2["cache_hit"] != true {
		t.Fatal("round2 must record cache_hit=true")
	}

	// ── 3. 服务宕机 → 回退 ──
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	dead.Close() // 立即关闭 → 连接拒绝

	hook2 := NewHook(enabledConfig(t, dead.URL), nil)
	env3 := newTestEnv(t, testRequestBody)
	if err := hook2.Execute(ctx, env3); err != nil {
		t.Fatalf("round3 failed (fallback must swallow errors): %v", err)
	}
	if string(env3.TransformedRequest) != testRequestBody {
		t.Fatal("round3: request must stay unchanged after optimizer outage")
	}
	meta3 := env3.Metadata[MetaKeyPromptOptimization].(map[string]any)
	if meta3["fallback"] != true {
		t.Fatal("round3 must record fallback=true")
	}
	if hook2.metrics.OptimizationErrors.Value() == 0 {
		t.Fatal("round3: error counter must increment")
	}
}

// TestIntegrationPipelineStageShape 验证 Hook 满足 pipeline.Hook 接口，
// 并可通过与 main_pipeline.go 相同的 PipelineStage 装配方式执行。
func TestIntegrationPipelineStageShape(t *testing.T) {
	upstream := okOptimizer(t, nil)
	defer upstream.Close()

	cfg := enabledConfig(t, upstream.URL)
	hook := NewHook(cfg, nil)

	if hook.Name() != "prompt_optimization" {
		t.Errorf("Name = %q", hook.Name())
	}
	if hook.Priority() != 90 {
		t.Errorf("Priority = %d, want 90 (before compression=100)", hook.Priority())
	}

	env := &domain.PipelineRequest{
		TenantID:          "tenant-a",
		TransformedRequest: []byte(testRequestBody),
		Metadata:          make(map[string]any),
	}
	if !hook.Enabled(ctx4test(), env) {
		t.Fatal("hook should be enabled with config enabled and body present")
	}
	if err := hook.Execute(ctx4test(), env); err != nil {
		t.Fatalf("Execute via stage shape failed: %v", err)
	}
	if len(env.Metadata[MetaKeyPromptOptimization].(map[string]any)) == 0 {
		t.Fatal("audit metadata missing after stage execution")
	}
}

func ctx4test() context.Context { return context.Background() }
