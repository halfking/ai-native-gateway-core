package extpoints_test

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domains/pipeline"
	"github.com/kaixuan/llm-gateway-go/extpoints"
)

// TestIntegration_ExtensionPointInPipeline 验证 ExtensionPointHook 在真实 Pipeline 中的集成。
//
// 这个测试模拟了 cmd/gateway/main_pipeline.go 中的集成场景：
//   1. 创建一个 ExtensionPointRegistry
//   2. 注册一个测试插件
//   3. 创建 ExtensionPointHook 并添加到 Pipeline
//   4. 执行 Pipeline 并验证插件被调用
func TestIntegration_ExtensionPointInPipeline(t *testing.T) {
	// Step 1: 创建注册表并注册插件
	registry := extpoints.NewExtensionPointRegistry()
	
	// 创建一个简单的测试插件
	plugin := &testPlugin{
		metadata: &extpoints.PluginMetadata{
			PluginID: "test-auth-plugin",
			Phase:    extpoints.PhaseAuth,
			Priority: extpoints.PriorityNormal,
		},
		executed: false,
	}
	
	err := registry.Register(extpoints.PhaseAuth, plugin)
	if err != nil {
		t.Fatalf("Failed to register plugin: %v", err)
	}
	
	// Step 2: 创建 Pipeline 并添加 ExtensionPointHook
	p := pipeline.NewRequestPipeline()
	
	authHook := extpoints.NewExtensionPointHook(registry, "authentication")
	
	p.AddStage(&pipeline.PipelineStage{
		Name:  "authentication",
		Phase: pipeline.PhaseAuthentication,
		Mode:  pipeline.ModeSequential,
		Hooks: []pipeline.Hook{authHook},
	})
	
	// Step 3: 创建测试请求并执行 Pipeline
	ctx := context.Background()
	envelope := &domain.PipelineRequest{
		TenantID:  "test-tenant",
		SessionID: "test-session",
		Metadata: map[string]interface{}{
			"method": "POST",
			"path":   "/v1/chat/completions",
			"model":  "gpt-4",
		},
		Envelope: &domain.RequestEnvelope{
			RequestID: "test-request-123",
		},
	}
	
	err = p.Execute(ctx, envelope)
	if err != nil {
		t.Errorf("Pipeline execution failed: %v", err)
	}
	
	// Step 4: 验证插件被执行了
	if !plugin.executed {
		t.Error("Expected plugin to be executed in pipeline")
	}
	
	// 验证插件接收到正确的上下文
	if plugin.lastContext == nil {
		t.Error("Plugin did not receive execution context")
	} else {
		if plugin.lastContext.RequestID == "" {
			t.Errorf("Plugin did not receive RequestID, context: %+v", plugin.lastContext)
		}
	}
}

// TestIntegration_MultiplePhases 验证多个 Phase 的插件按顺序执行。
func TestIntegration_MultiplePhases(t *testing.T) {
	registry := extpoints.NewExtensionPointRegistry()
	
	executionOrder := []string{}
	
	// 注册 Auth 阶段插件
	authPlugin := &testPlugin{
		metadata: &extpoints.PluginMetadata{
			PluginID: "auth-plugin",
			Phase:    extpoints.PhaseAuth,
			Priority: extpoints.PriorityNormal,
		},
		onExecute: func() {
			executionOrder = append(executionOrder, "auth")
		},
	}
	registry.Register(extpoints.PhaseAuth, authPlugin)
	
	// 注册 PreRoute 阶段插件
	preRoutePlugin := &testPlugin{
		metadata: &extpoints.PluginMetadata{
			PluginID: "pre-route-plugin",
			Phase:    extpoints.PhasePreRoute,
			Priority: extpoints.PriorityNormal,
		},
		onExecute: func() {
			executionOrder = append(executionOrder, "pre-route")
		},
	}
	registry.Register(extpoints.PhasePreRoute, preRoutePlugin)
	
	// 创建 Pipeline
	p := pipeline.NewRequestPipeline()
	
	// 添加 Auth Stage
	p.AddStage(&pipeline.PipelineStage{
		Name:  "authentication",
		Phase: pipeline.PhaseAuthentication,
		Mode:  pipeline.ModeSequential,
		Hooks: []pipeline.Hook{extpoints.NewExtensionPointHook(registry, "authentication")},
	})
	
	// 添加 PreRouting Stage
	p.AddStage(&pipeline.PipelineStage{
		Name:  "pre_routing",
		Phase: pipeline.PhasePreRouting,
		Mode:  pipeline.ModeSequential,
		Hooks: []pipeline.Hook{extpoints.NewExtensionPointHook(registry, "pre_routing")},
	})
	
	// 执行 Pipeline
	ctx := context.Background()
	envelope := &domain.PipelineRequest{
		TenantID: "test-tenant",
		Metadata: make(map[string]interface{}),
	}
	
	err := p.Execute(ctx, envelope)
	if err != nil {
		t.Errorf("Pipeline execution failed: %v", err)
	}
	
	// 验证执行顺序
	if len(executionOrder) != 2 {
		t.Fatalf("Expected 2 plugins to execute, got %d", len(executionOrder))
	}
	
	if executionOrder[0] != "auth" {
		t.Errorf("Expected first execution to be 'auth', got '%s'", executionOrder[0])
	}
	
	if executionOrder[1] != "pre-route" {
		t.Errorf("Expected second execution to be 'pre-route', got '%s'", executionOrder[1])
	}
}

// TestIntegration_PluginFailureDoesNotBlockPipeline 验证插件失败不阻塞 Pipeline（fail-open）。
func TestIntegration_PluginFailureDoesNotBlockPipeline(t *testing.T) {
	registry := extpoints.NewExtensionPointRegistry()
	
	// 创建一个会失败的插件
	failingPlugin := &testPlugin{
		metadata: &extpoints.PluginMetadata{
			PluginID: "failing-plugin",
			Phase:    extpoints.PhaseAuth,
			Priority: extpoints.PriorityNormal,
		},
		shouldFail: true,
	}
	registry.Register(extpoints.PhaseAuth, failingPlugin)
	
	// 创建 Pipeline
	p := pipeline.NewRequestPipeline()
	p.AddStage(&pipeline.PipelineStage{
		Name:  "authentication",
		Phase: pipeline.PhaseAuthentication,
		Mode:  pipeline.ModeSequential,
		Hooks: []pipeline.Hook{extpoints.NewExtensionPointHook(registry, "authentication")},
	})
	
	// 执行 Pipeline - 应该成功（fail-open）
	ctx := context.Background()
	envelope := &domain.PipelineRequest{
		TenantID: "test-tenant",
		Metadata: make(map[string]interface{}),
	}
	
	err := p.Execute(ctx, envelope)
	// fail-open: 插件失败不应导致 Pipeline 失败
	if err != nil {
		t.Errorf("Pipeline should not fail when plugin fails (fail-open), got error: %v", err)
	}
}

	// TestIntegration_PhasePostUpstream 验证 PhasePostUpstream 插件集成。
	func TestIntegration_PhasePostUpstream(t *testing.T) {
		registry := extpoints.NewExtensionPointRegistry()
		
		// 创建 PostUpstream 阶段插件
		postUpstreamPlugin := &testPlugin{
			metadata: &extpoints.PluginMetadata{
				PluginID: "test-post-upstream",
				Phase:    extpoints.PhasePostUpstream,
				Priority: extpoints.PriorityNormal,
			},
			executed: false,
		}
		
		err := registry.Register(extpoints.PhasePostUpstream, postUpstreamPlugin)
		if err != nil {
			t.Fatalf("Failed to register post-upstream plugin: %v", err)
		}
		
		// 创建 Pipeline
		p := pipeline.NewRequestPipeline()
		
		// 添加 PhasePostUpstream Stage
		p.AddStage(&pipeline.PipelineStage{
			Name:  "post_upstream_output_compliance",
			Phase: pipeline.PhasePostUpstream,
			Mode:  pipeline.ModeSequential,
			Hooks: []pipeline.Hook{
				extpoints.NewExtensionPointHook(registry, "post_upstream"),
			},
		})
		
		// 创建测试请求
		ctx := context.Background()
		envelope := &domain.PipelineRequest{
			TenantID:  "test-tenant",
			SessionID: "test-session",
			Metadata: map[string]interface{}{
				"response_size": int64(1024),
				"response_time": 100,
			},
		}
		
		// 执行 Pipeline
		err = p.Execute(ctx, envelope)
		if err != nil {
			t.Errorf("Pipeline execution failed: %v", err)
		}
		
		// 验证插件被执行
		if !postUpstreamPlugin.executed {
			t.Error("Expected post-upstream plugin to be executed")
		}
	}

	// TestIntegration_PhasePreUpstream 验证 PhasePreUpstream 插件集成。
	func TestIntegration_PhasePreUpstream(t *testing.T) {
		registry := extpoints.NewExtensionPointRegistry()
		
		// 创建 PreUpstream 阶段插件
		preUpstreamPlugin := &testPlugin{
			metadata: &extpoints.PluginMetadata{
				PluginID: "test-pre-upstream",
				Phase:    extpoints.PhasePreUpstream,
				Priority: extpoints.PriorityNormal,
			},
			executed: false,
		}
		
		err := registry.Register(extpoints.PhasePreUpstream, preUpstreamPlugin)
		if err != nil {
			t.Fatalf("Failed to register pre-upstream plugin: %v", err)
		}
		
		// 创建 Pipeline
		p := pipeline.NewRequestPipeline()
		
		// 添加 PhasePreUpstream Stage
		p.AddStage(&pipeline.PipelineStage{
			Name:  "pre_upstream",
			Phase: pipeline.PhasePreUpstream,
			Mode:  pipeline.ModeSequential,
			Hooks: []pipeline.Hook{
				extpoints.NewExtensionPointHook(registry, "pre_upstream"),
			},
		})
		
		// 创建测试请求
		ctx := context.Background()
		envelope := &domain.PipelineRequest{
			TenantID:  "test-tenant",
			SessionID: "test-session",
			Metadata: map[string]interface{}{
				"model":       "gpt-4",
				"temperature": 0.5,
			},
		}
		
		// 执行 Pipeline
		err = p.Execute(ctx, envelope)
		if err != nil {
			t.Errorf("Pipeline execution failed: %v", err)
		}
		
		// 验证插件被执行
		if !preUpstreamPlugin.executed {
			t.Error("Expected pre-upstream plugin to be executed")
		}
	}
	
	// testPlugin 是用于集成测试的简单插件实现。
	type testPlugin struct {
		metadata    *extpoints.PluginMetadata
		executed    bool
		lastContext *extpoints.ExecutionContext
		onExecute   func()
		shouldFail  bool
	}

func (p *testPlugin) Metadata() *extpoints.PluginMetadata {
	return p.metadata
}

func (p *testPlugin) Initialize(ctx context.Context) error {
	return nil
}

func (p *testPlugin) Shutdown(ctx context.Context) error {
	return nil
}

func (p *testPlugin) HealthCheck(ctx context.Context) error {
	return nil
}

func (p *testPlugin) Execute(ctx *extpoints.ExecutionContext) (*extpoints.ExecutionResult, error) {
	p.executed = true
	p.lastContext = ctx
	
	if p.onExecute != nil {
		p.onExecute()
	}
	
	if p.shouldFail {
		return &extpoints.ExecutionResult{
			Success: false,
			Error:   context.Canceled,
		}, context.Canceled
	}
	
	return &extpoints.ExecutionResult{
		Success: true,
	}, nil
}
