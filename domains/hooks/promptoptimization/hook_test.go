package promptoptimization

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"
)

// newTestEnv 构造带 OpenAI 格式请求体的 PipelineRequest。
func newTestEnv(t *testing.T, body string) *domain.PipelineRequest {
	t.Helper()
	return &domain.PipelineRequest{
		TenantID:          "tenant-a",
		TransformedRequest: []byte(body),
	}
}

// testRequestBody 标准测试请求体。
const testRequestBody = `{
  "model": "gpt-4o",
  "messages": [
    {"role": "system", "content": "You are helpful."},
    {"role": "user", "content": "Write code."},
    {"role": "assistant", "content": "Sure."}
  ],
  "temperature": 0.7,
  "stream": false
}`

// okOptimizer 返回固定优化结果的优化服务 mock（system 改写，user 不变）。
func okOptimizer(t *testing.T, calls *atomic.Int64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls != nil {
			calls.Add(1)
		}
		if r.URL.Path != "/api/v1/prompts/optimize" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var req OptimizeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		resp := OptimizeResult{
			OptimizationID:  "opt_test0000000001",
			OriginalTokens:  100,
			OptimizedTokens: 80,
			LatencyMs:       5,
		}
		for _, p := range req.Prompts {
			changed := p.Role == "system"
			content := p.Content
			if changed {
				content = "Optimized: " + p.Content
			}
			resp.Prompts = append(resp.Prompts, OptimizedPrompt{
				Role: p.Role, Content: content, Changed: changed,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

// enabledConfig 返回指向 mock 优化服务的启用配置。
func enabledConfig(t *testing.T, serverURL string) *Config {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.OptimizerURL = serverURL
	cfg.Timeout = 2 * time.Second
	cfg.CacheTTL = time.Minute
	return cfg
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Enabled {
		t.Error("Enabled must default to false (opt-in)")
	}
	if cfg.OptimizerURL != "http://prompt-optimizer:8090" {
		t.Errorf("OptimizerURL default = %q", cfg.OptimizerURL)
	}
	if cfg.CacheTTL != 24*time.Hour {
		t.Errorf("CacheTTL default = %v", cfg.CacheTTL)
	}
	if cfg.Timeout != 5*time.Second {
		t.Errorf("Timeout default = %v", cfg.Timeout)
	}
	if cfg.Mode != ModeBoth {
		t.Errorf("Mode default = %q", cfg.Mode)
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv(EnvEnabled, "true")
	t.Setenv(EnvOptimizerURL, "http://optimizer:9999/")
	t.Setenv(EnvCacheTTL, "1h")
	t.Setenv(EnvTimeout, "3s")
	t.Setenv(EnvMode, "system")
	t.Setenv(EnvModelWhitelist, "gpt-4o, claude-3")
	t.Setenv(EnvModelBlacklist, "gpt-3.5-turbo")
	t.Setenv(EnvTenantAllowlist, "tenant-a")

	cfg := FromEnv()
	if !cfg.Enabled {
		t.Error("Enabled should be true")
	}
	if cfg.OptimizerURL != "http://optimizer:9999" {
		t.Errorf("OptimizerURL = %q (trailing slash should be trimmed)", cfg.OptimizerURL)
	}
	if cfg.CacheTTL != time.Hour || cfg.Timeout != 3*time.Second {
		t.Errorf("durations = %v/%v", cfg.CacheTTL, cfg.Timeout)
	}
	if cfg.Mode != ModeSystem {
		t.Errorf("Mode = %q", cfg.Mode)
	}
	if !cfg.modelAllowed("gpt-4o") || cfg.modelAllowed("gpt-3.5-turbo") || cfg.modelAllowed("other") {
		t.Error("model whitelist/blacklist semantics broken")
	}
	if !cfg.tenantAllowed("tenant-a") || cfg.tenantAllowed("tenant-b") {
		t.Error("tenant allowlist semantics broken")
	}
}

func TestModeCovers(t *testing.T) {
	if !ModeSystem.Covers("system") || ModeSystem.Covers("user") {
		t.Error("ModeSystem coverage wrong")
	}
	if !ModeUser.Covers("user") || ModeUser.Covers("system") {
		t.Error("ModeUser coverage wrong")
	}
	if !ModeBoth.Covers("system") || !ModeBoth.Covers("user") || ModeBoth.Covers("assistant") {
		t.Error("ModeBoth coverage wrong")
	}
}

func TestCacheKeyStableAndDistinct(t *testing.T) {
	a := []PromptItem{{Role: "system", Content: "hello"}}
	b := []PromptItem{{Role: "system", Content: "hello"}}
	c := []PromptItem{{Role: "system", Content: "world"}}
	if CacheKey("m", ModeBoth, a) != CacheKey("m", ModeBoth, b) {
		t.Error("same inputs must produce same key")
	}
	if CacheKey("m", ModeBoth, a) == CacheKey("m", ModeBoth, c) {
		t.Error("different content must produce different key")
	}
	if CacheKey("m1", ModeBoth, a) == CacheKey("m2", ModeBoth, a) {
		t.Error("different model must produce different key")
	}
	if CacheKey("m", ModeSystem, a) == CacheKey("m", ModeUser, a) {
		t.Error("different mode must produce different key")
	}
}

func TestCacheSetGetExpire(t *testing.T) {
	cache := NewOptimizationCache(10*time.Millisecond, 16, NewMetrics())
	res := &OptimizeResult{OptimizationID: "opt_x"}
	cache.Set("k", res)
	if got, ok := cache.Get("k"); !ok || got.OptimizationID != "opt_x" {
		t.Fatal("cache get after set failed")
	}
	time.Sleep(15 * time.Millisecond)
	if _, ok := cache.Get("k"); ok {
		t.Fatal("expired entry must not be returned")
	}
}

func TestOptimizerClientRoundTrip(t *testing.T) {
	server := okOptimizer(t, nil)
	defer server.Close()
	client := NewOptimizerClient(server.URL, time.Second)
	res, err := client.Optimize(context.Background(), &OptimizeRequest{
		TenantID: "t",
		Model:    "gpt-4o",
		Prompts:  []PromptItem{{Role: "system", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Optimize failed: %v", err)
	}
	if len(res.Prompts) != 1 || !res.Prompts[0].Changed {
		t.Errorf("unexpected result: %+v", res)
	}
}

func TestHookDisabledByDefault(t *testing.T) {
	hook := NewHook(nil, nil) // DefaultConfig: Enabled=false
	env := newTestEnv(t, testRequestBody)
	if hook.Enabled(context.Background(), env) {
		t.Fatal("hook must be disabled by default")
	}
	if err := hook.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute on disabled hook must be a no-op, got %v", err)
	}
	if string(env.TransformedRequest) != testRequestBody {
		t.Error("disabled hook must not modify request")
	}
}

func TestHookDisabledWithoutBody(t *testing.T) {
	cfg := enabledConfig(t, "http://127.0.0.1:1") // 不可达地址也行：Enabled 先拦截
	hook := NewHook(cfg, nil)
	env := &domain.PipelineRequest{TenantID: "t", TransformedRequest: nil}
	if hook.Enabled(context.Background(), env) {
		t.Fatal("hook must be disabled without request body")
	}
}

func TestHookAppliesOptimization(t *testing.T) {
	var calls atomic.Int64
	server := okOptimizer(t, &calls)
	defer server.Close()

	hook := NewHook(enabledConfig(t, server.URL), nil)
	env := newTestEnv(t, testRequestBody)
	if err := hook.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("optimizer should be called once, got %d", calls.Load())
	}

	// 解析写回后的请求体
	var body map[string]any
	if err := json.Unmarshal(env.TransformedRequest, &body); err != nil {
		t.Fatalf("rewritten body invalid: %v", err)
	}
	msgs := body["messages"].([]any)
	sys := msgs[0].(map[string]any)
	usr := msgs[1].(map[string]any)
	if sys["content"] != "Optimized: You are helpful." {
		t.Errorf("system content not optimized: %v", sys["content"])
	}
	if usr["content"] != "Write code." {
		t.Errorf("user content must stay unchanged (mock only changes system): %v", usr["content"])
	}
	// 非目标字段必须保留
	if body["temperature"] != 0.7 {
		t.Errorf("temperature lost: %v", body["temperature"])
	}
	if _, ok := body["stream"]; !ok {
		t.Error("stream field lost")
	}

	// 审计 metadata
	meta, ok := env.Metadata[MetaKeyPromptOptimization].(map[string]any)
	if !ok {
		t.Fatalf("metadata %v missing", MetaKeyPromptOptimization)
	}
	if meta["cache_hit"] != false {
		t.Error("first call must be cache miss")
	}
	if meta["original_sha256"] == meta["optimized_sha256"] {
		t.Error("original/optimized hash must differ when content changed")
	}
}

func TestHookCacheHitAvoidsSecondCall(t *testing.T) {
	var calls atomic.Int64
	server := okOptimizer(t, &calls)
	defer server.Close()

	hook := NewHook(enabledConfig(t, server.URL), nil)
	ctx := context.Background()

	env1 := newTestEnv(t, testRequestBody)
	if err := hook.Execute(ctx, env1); err != nil {
		t.Fatalf("first Execute failed: %v", err)
	}
	env2 := newTestEnv(t, testRequestBody)
	if err := hook.Execute(ctx, env2); err != nil {
		t.Fatalf("second Execute failed: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("optimizer should be called exactly once (cache hit), got %d", calls.Load())
	}
	meta := env2.Metadata[MetaKeyPromptOptimization].(map[string]any)
	if meta["cache_hit"] != true {
		t.Error("second identical call must be a cache hit")
	}
}

func TestHookFallsBackWhenOptimizerFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	hook := NewHook(enabledConfig(t, server.URL), nil)
	env := newTestEnv(t, testRequestBody)
	err := hook.Execute(context.Background(), env)
	if err != nil {
		t.Fatalf("optimizer failure must not propagate, got %v", err)
	}
	if string(env.TransformedRequest) != testRequestBody {
		t.Error("request must stay unchanged on optimizer failure")
	}
	meta := env.Metadata[MetaKeyPromptOptimization].(map[string]any)
	if meta["fallback"] != true {
		t.Error("metadata must record fallback")
	}
	if hook.metrics.Fallbacks.Value() == 0 {
		t.Error("fallback counter must increment")
	}
}

func TestHookSkipsDisallowedModel(t *testing.T) {
	var calls atomic.Int64
	server := okOptimizer(t, &calls)
	defer server.Close()

	cfg := enabledConfig(t, server.URL)
	cfg.ModelBlacklist = []string{"gpt-4o"}
	hook := NewHook(cfg, nil)

	env := newTestEnv(t, testRequestBody)
	if err := hook.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if calls.Load() != 0 {
		t.Error("blacklisted model must not trigger optimizer call")
	}
	if string(env.TransformedRequest) != testRequestBody {
		t.Error("request must stay unchanged")
	}
}

func TestHookSkipsDisallowedTenant(t *testing.T) {
	var calls atomic.Int64
	server := okOptimizer(t, &calls)
	defer server.Close()

	cfg := enabledConfig(t, server.URL)
	cfg.TenantAllowlist = []string{"tenant-good"}
	hook := NewHook(cfg, nil)

	env := newTestEnv(t, testRequestBody) // tenant-a
	if err := hook.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if calls.Load() != 0 {
		t.Error("non-allowlisted tenant must not trigger optimizer call")
	}
}

func TestHookModeSystemOnly(t *testing.T) {
	var gotPromptCount atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req OptimizeRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotPromptCount.Add(int64(len(req.Prompts)))
		resp := OptimizeResult{OptimizationID: "opt_s"}
		for _, p := range req.Prompts {
			resp.Prompts = append(resp.Prompts, OptimizedPrompt{
				Role: p.Role, Content: "S:" + p.Content, Changed: true,
			})
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := enabledConfig(t, server.URL)
	cfg.Mode = ModeSystem
	hook := NewHook(cfg, nil)

	env := newTestEnv(t, testRequestBody)
	if err := hook.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if gotPromptCount.Load() != 1 {
		t.Errorf("mode=system must only optimize 1 prompt, got %d", gotPromptCount.Load())
	}
	var body map[string]any
	_ = json.Unmarshal(env.TransformedRequest, &body)
	msgs := body["messages"].([]any)
	if msgs[1].(map[string]any)["content"] != "Write code." {
		t.Error("user content must be untouched in mode=system")
	}
}

func TestHookSkipsMultimodalAndEmptyContent(t *testing.T) {
	body := `{"model":"m","messages":[
		{"role":"system","content":[{"type":"text","text":"parts"}]},
		{"role":"user","content":"   "}
	]}`
	var calls atomic.Int64
	server := okOptimizer(t, &calls)
	defer server.Close()

	hook := NewHook(enabledConfig(t, server.URL), nil)
	env := newTestEnv(t, body)
	if err := hook.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if calls.Load() != 0 {
		t.Error("multimodal/empty contents must not trigger optimizer")
	}
	if string(env.TransformedRequest) != body {
		t.Error("request must stay unchanged")
	}
}

func TestHookCountMismatchFallsBack(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 返回比请求少一条的 prompts
		_ = json.NewEncoder(w).Encode(OptimizeResult{
			OptimizationID: "opt_bad",
			Prompts:        []OptimizedPrompt{{Role: "system", Content: "x", Changed: true}},
		})
	}))
	defer server.Close()

	hook := NewHook(enabledConfig(t, server.URL), nil)
	env := newTestEnv(t, testRequestBody)
	if err := hook.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if string(env.TransformedRequest) != testRequestBody {
		t.Error("count mismatch must fall back to original request")
	}
}

func TestOnErrorSuppressesError(t *testing.T) {
	hook := NewHook(enabledConfig(t, "http://127.0.0.1:1"), nil)
	env := &domain.PipelineRequest{TenantID: "t"}
	got := hook.OnError(context.Background(), env, context.DeadlineExceeded)
	if got != nil {
		t.Fatalf("OnError must suppress errors, got %v", got)
	}
	if env.Metadata["prompt_optimization_error"] != context.DeadlineExceeded.Error() {
		t.Error("OnError must record error into metadata")
	}
}

func TestJSONRoundTripPreservesNumbers(t *testing.T) {
	// 大整数经 UseNumber + map 往返必须保持字面量
	body := `{"model":"m","max_tokens":9007199254740993,"messages":[{"role":"user","content":"hi"}]}`
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.UseNumber()
	var parsed map[string]any
	if err := decoder.Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(parsed)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "9007199254740993") {
		t.Errorf("large int literal not preserved: %s", out)
	}
}
