// Package e2e - extpoints_e2e_test.go
//
// End-to-end tests for ExtensionPoint architecture in real HTTP request flow.
//
// 测试范围：
//   - 完整 HTTP 请求 → Pipeline → ExtensionPoint 插件执行
//   - 多 Phase 插件顺序执行验证
//   - 插件 Metadata 同步到 PipelineRequest
//   - Fail-open 策略在真实请求中的行为
//
// 与集成测试的区别：
//   - 集成测试：直接调用 Pipeline.Execute()，模拟 PipelineRequest
//   - E2E测试：通过 HTTP handler，真实请求生命周期
package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domains/pipeline"
	"github.com/kaixuan/llm-gateway-go/extpoints"
	"github.com/kaixuan/llm-gateway-go/plugin-runtime/demo"
)

// TestExtensionPoint_E2E_MultiPhaseExecution 验证多个 Phase 的插件在真实 HTTP 请求中按顺序执行。
//
// 测试流程：
//   1. 注册 demo 插件到 4 个 Phase（Auth, PreRoute, PreUpstream, PostUpstream）
//   2. 构建完整 Pipeline（模拟生产环境）
//   3. 创建 HTTP handler 包装 Pipeline
//   4. 发送 HTTP POST 请求
//   5. 验证所有插件被执行且元数据正确同步
func TestExtensionPoint_E2E_MultiPhaseExecution(t *testing.T) {
	t.Parallel()

	// Step 1: 创建 ExtensionPoint Registry 并注册 demo 插件
	registry := extpoints.NewExtensionPointRegistry()
	
	// 注册 4 个 Phase 的插件（模拟生产场景）
	authPlugin := demo.NewAuthenticationPlugin()
	headerPlugin := demo.NewHeaderInjectorPlugin()
	requestModPlugin := demo.NewRequestModifierPlugin()
	responseMetricsPlugin := demo.NewResponseMetricsPlugin()
	
	if err := registry.Register(extpoints.PhaseAuth, authPlugin); err != nil {
		t.Fatalf("Failed to register auth plugin: %v", err)
	}
	if err := registry.Register(extpoints.PhasePreRoute, headerPlugin); err != nil {
		t.Fatalf("Failed to register header plugin: %v", err)
	}
	if err := registry.Register(extpoints.PhasePreUpstream, requestModPlugin); err != nil {
		t.Fatalf("Failed to register request modifier plugin: %v", err)
	}
	if err := registry.Register(extpoints.PhasePostUpstream, responseMetricsPlugin); err != nil {
		t.Fatalf("Failed to register response metrics plugin: %v", err)
	}

	// Step 2: 构建 Pipeline（模拟生产配置）
	p := pipeline.NewRequestPipeline()
	
	// Authentication stage
	p.AddStage(&pipeline.PipelineStage{
		Name:  "authentication",
		Phase: pipeline.PhaseAuthentication,
		Mode:  pipeline.ModeSequential,
		Hooks: []pipeline.Hook{
			extpoints.NewExtensionPointHook(registry, "authentication"),
		},
	})
	
	// Pre-routing stage
	p.AddStage(&pipeline.PipelineStage{
		Name:  "pre_routing",
		Phase: pipeline.PhasePreRouting,
		Mode:  pipeline.ModeSequential,
		Hooks: []pipeline.Hook{
			extpoints.NewExtensionPointHook(registry, "pre_routing"),
		},
	})
	
	// Routing stage (空 hook，仅占位)
	p.AddStage(&pipeline.PipelineStage{
		Name:  "routing",
		Phase: pipeline.PhaseRouting,
		Mode:  pipeline.ModeSequential,
		Hooks: []pipeline.Hook{
			&passthroughHook{name: "routing"},
		},
	})
	
	// Pre-upstream stage
	p.AddStage(&pipeline.PipelineStage{
		Name:  "pre_upstream",
		Phase: pipeline.PhasePreUpstream,
		Mode:  pipeline.ModeSequential,
		Hooks: []pipeline.Hook{
			extpoints.NewExtensionPointHook(registry, "pre_upstream"),
		},
	})
	
	// Upstream stage (模拟 LLM 调用)
	p.AddStage(&pipeline.PipelineStage{
		Name:  "upstream",
		Phase: pipeline.PhaseUpstream,
		Mode:  pipeline.ModeSequential,
		Hooks: []pipeline.Hook{
			&mockUpstreamHook{},
		},
	})
	
	// Post-upstream stage
	p.AddStage(&pipeline.PipelineStage{
		Name:  "post_upstream",
		Phase: pipeline.PhasePostUpstream,
		Mode:  pipeline.ModeSequential,
		Hooks: []pipeline.Hook{
			extpoints.NewExtensionPointHook(registry, "post_upstream"),
		},
	})

	// Step 3: 创建 HTTP handler
	handler := createPipelineHandler(p)
	
	// Step 4: 创建测试服务器
	server := httptest.NewServer(handler)
	defer server.Close()

	// Step 5: 发送 HTTP 请求
	reqBody := `{"model": "gpt-4", "messages": [{"role": "user", "content": "Hello"}]}`
	resp, err := http.Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("HTTP request failed: %v", err)
	}
	defer resp.Body.Close()

	// Step 6: 验证响应
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	// 验证响应头中的插件元数据
	if pluginHeader := resp.Header.Get("X-Plugin-Processed"); pluginHeader == "" {
		t.Error("Expected X-Plugin-Processed header from plugins")
	}

	// 解析响应体
	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	// 验证插件元数据在响应中
	metadata, ok := result["metadata"].(map[string]interface{})
	if !ok {
		t.Fatal("Expected metadata in response")
	}

	// 验证各个插件的元数据
	expectedKeys := []string{
		"extpoint_plugin_demo-authenticator_user_id",
		"extpoint_plugin_demo-header-injector_headers_added",
		"extpoint_plugin_demo-request-modifier_modifications_applied",
		"extpoint_plugin_demo-response-metrics_metrics_collected",
	}

	for _, key := range expectedKeys {
		if _, exists := metadata[key]; !exists {
			t.Errorf("Expected metadata key %s not found", key)
		}
	}

	t.Logf("E2E test passed: %d metadata keys found", len(metadata))
}

// TestExtensionPoint_E2E_PluginFailure 验证插件失败不阻塞请求（fail-open）。
func TestExtensionPoint_E2E_PluginFailure(t *testing.T) {
	t.Parallel()

	registry := extpoints.NewExtensionPointRegistry()
	
	// 注册一个会失败的插件
	failingPlugin := &failingTestPlugin{
		metadata: &extpoints.PluginMetadata{
			PluginID: "failing-plugin-e2e",
			Phase:    extpoints.PhaseAuth,
			Priority: extpoints.PriorityNormal,
		},
	}
	
	if err := registry.Register(extpoints.PhaseAuth, failingPlugin); err != nil {
		t.Fatalf("Failed to register failing plugin: %v", err)
	}

	// 构建 Pipeline
	p := pipeline.NewRequestPipeline()
	p.AddStage(&pipeline.PipelineStage{
		Name:  "authentication",
		Phase: pipeline.PhaseAuthentication,
		Mode:  pipeline.ModeSequential,
		Hooks: []pipeline.Hook{
			extpoints.NewExtensionPointHook(registry, "authentication"),
		},
	})
	
	// Upstream stage (返回成功)
	p.AddStage(&pipeline.PipelineStage{
		Name:  "upstream",
		Phase: pipeline.PhaseUpstream,
		Mode:  pipeline.ModeSequential,
		Hooks: []pipeline.Hook{
			&mockUpstreamHook{},
		},
	})

	handler := createPipelineHandler(p)
	server := httptest.NewServer(handler)
	defer server.Close()

	// 发送请求
	reqBody := `{"model": "gpt-4", "messages": []}`
	resp, err := http.Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("HTTP request failed: %v", err)
	}
	defer resp.Body.Close()

	// 验证请求成功（fail-open）
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200 (fail-open), got %d", resp.StatusCode)
	}

	t.Log("E2E fail-open test passed: request succeeded despite plugin failure")
}

// createPipelineHandler 创建一个 HTTP handler 包装 Pipeline。
func createPipelineHandler(p *pipeline.RequestPipeline) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 构建 PipelineRequest
		envelope := &domain.PipelineRequest{
			TenantID: "test-tenant-e2e",
			Metadata: map[string]interface{}{
				"method": r.Method,
				"path":   r.URL.Path,
				"model":  "gpt-4",
			},
		}

		// 执行 Pipeline
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		if err := p.Execute(ctx, envelope); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// 构建响应（包含插件元数据）
		response := map[string]interface{}{
			"id":      "chatcmpl-test-e2e",
			"model":   "gpt-4",
			"choices": []map[string]interface{}{},
			"metadata": envelope.Metadata, // 包含插件同步的元数据
		}

		// 写入响应头（从 envelope.Metadata 提取）
		if headers, ok := envelope.Metadata["headers_added"].([]string); ok {
			for _, h := range headers {
				w.Header().Set(h, "plugin-value")
			}
		}
		w.Header().Set("X-Plugin-Processed", "true")

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(response)
	}
}

// passthroughHook 是一个空 hook，仅用于占位。
type passthroughHook struct {
	name string
}

func (h *passthroughHook) Name() string                                                  { return h.name }
func (h *passthroughHook) Execute(ctx context.Context, env *domain.PipelineRequest) error { return nil }
func (h *passthroughHook) Priority() int                                                 { return 500 }
func (h *passthroughHook) Enabled(ctx context.Context, env *domain.PipelineRequest) bool { return true }
func (h *passthroughHook) OnError(ctx context.Context, env *domain.PipelineRequest, err error) error {
	return nil
}

// mockUpstreamHook 模拟上游 LLM 调用，设置响应数据。
type mockUpstreamHook struct{}

func (h *mockUpstreamHook) Name() string { return "mock_upstream" }
func (h *mockUpstreamHook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
	// 模拟 LLM 响应
	if env.Metadata == nil {
		env.Metadata = make(map[string]interface{})
	}
	env.Metadata["response_size"] = int64(256)
	env.Metadata["response_time"] = 100 * time.Millisecond
	return nil
}
func (h *mockUpstreamHook) Priority() int                                                 { return 500 }
func (h *mockUpstreamHook) Enabled(ctx context.Context, env *domain.PipelineRequest) bool { return true }
func (h *mockUpstreamHook) OnError(ctx context.Context, env *domain.PipelineRequest, err error) error {
	return nil
}

// failingTestPlugin 是一个总是失败的测试插件。
type failingTestPlugin struct {
	metadata *extpoints.PluginMetadata
}

func (p *failingTestPlugin) Metadata() *extpoints.PluginMetadata { return p.metadata }
func (p *failingTestPlugin) Initialize(ctx context.Context) error { return nil }
func (p *failingTestPlugin) Shutdown(ctx context.Context) error   { return nil }
func (p *failingTestPlugin) HealthCheck(ctx context.Context) error { return nil }
func (p *failingTestPlugin) Execute(ctx *extpoints.ExecutionContext) (*extpoints.ExecutionResult, error) {
	return &extpoints.ExecutionResult{
		Success: false,
		Error:   context.Canceled,
	}, context.Canceled
}
