// Package extpoints - benchmark_test.go
//
// 性能基准测试，测量 ExtensionPoint 架构的开销。
//
// 测试指标：
//   - 无插件时的 Hook 开销（Enabled() 检查）
//   - 单插件执行时间
//   - 多插件执行时间
//   - 插件优先级排序开销
//
// 运行方式：
//
//	go test -bench=. -benchmem ./extpoints
//	go test -bench=BenchmarkExtensionPointHook -benchtime=10s ./extpoints
package extpoints

import (
	"context"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domains/pipeline"
)

// BenchmarkExtensionPointHook_NoPlugins 测量无插件时的开销。
//
// 期望结果：< 100 ns/op（仅 Enabled() 检查）
func BenchmarkExtensionPointHook_NoPlugins(b *testing.B) {
	registry := NewExtensionPointRegistry()
	hook := NewExtensionPointHook(registry, "authentication")

	ctx := context.Background()
	envelope := &domain.PipelineRequest{
		TenantID: "bench-tenant",
		Metadata: make(map[string]interface{}),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = hook.Execute(ctx, envelope)
	}
}

// BenchmarkExtensionPointHook_SinglePlugin 测量单个插件的执行开销。
//
// 期望结果：< 10 µs/op（包含插件逻辑）
func BenchmarkExtensionPointHook_SinglePlugin(b *testing.B) {
	registry := NewExtensionPointRegistry()

	// 注册一个轻量级测试插件
	plugin := &benchPlugin{
		metadata: &PluginMetadata{
			PluginID: "bench-plugin-1",
			Phase:    PhaseAuth,
			Priority: PriorityNormal,
		},
	}
	registry.Register(PhaseAuth, plugin)

	hook := NewExtensionPointHook(registry, "authentication")

	ctx := context.Background()
	envelope := &domain.PipelineRequest{
		TenantID: "bench-tenant",
		Metadata: make(map[string]interface{}),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = hook.Execute(ctx, envelope)
	}
}

// BenchmarkExtensionPointHook_MultiplePlugins 测量多个插件的执行开销。
//
// 测试场景：5 个插件按优先级顺序执行
func BenchmarkExtensionPointHook_MultiplePlugins(b *testing.B) {
	registry := NewExtensionPointRegistry()

	// 注册 5 个不同优先级的插件
	priorities := []Priority{
		PriorityHighest,
		PriorityHigh,
		PriorityNormal,
		PriorityLow,
		PriorityLowest,
	}

	for i, priority := range priorities {
		plugin := &benchPlugin{
			metadata: &PluginMetadata{
				PluginID: string(rune('A' + i)),
				Phase:    PhaseAuth,
				Priority: priority,
			},
		}
		registry.Register(PhaseAuth, plugin)
	}

	hook := NewExtensionPointHook(registry, "authentication")

	ctx := context.Background()
	envelope := &domain.PipelineRequest{
		TenantID: "bench-tenant",
		Metadata: make(map[string]interface{}),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = hook.Execute(ctx, envelope)
	}
}

// BenchmarkExtensionPointHook_Pipeline 测量在完整 Pipeline 中的开销。
//
// 测试场景：4 个 Phase，每个 Phase 1 个插件
func BenchmarkExtensionPointHook_Pipeline(b *testing.B) {
	registry := NewExtensionPointRegistry()

	// 注册 4 个 Phase 的插件
	phases := []Phase{PhaseAuth, PhasePreRoute, PhaseRoute, PhasePostRoute}
	for _, phase := range phases {
		plugin := &benchPlugin{
			metadata: &PluginMetadata{
				PluginID: "bench-" + string(phase),
				Phase:    phase,
				Priority: PriorityNormal,
			},
		}
		registry.Register(phase, plugin)
	}

	// 构建 Pipeline
	p := pipeline.NewRequestPipeline()

	p.AddStage(&pipeline.PipelineStage{
		Name:  "authentication",
		Phase: pipeline.PhaseAuthentication,
		Mode:  pipeline.ModeSequential,
		Hooks: []pipeline.Hook{
			NewExtensionPointHook(registry, "authentication"),
		},
	})

	p.AddStage(&pipeline.PipelineStage{
		Name:  "pre_routing",
		Phase: pipeline.PhasePreRouting,
		Mode:  pipeline.ModeSequential,
		Hooks: []pipeline.Hook{
			NewExtensionPointHook(registry, "pre_routing"),
		},
	})

	p.AddStage(&pipeline.PipelineStage{
		Name:  "routing",
		Phase: pipeline.PhaseRouting,
		Mode:  pipeline.ModeSequential,
		Hooks: []pipeline.Hook{
			NewExtensionPointHook(registry, "routing"),
		},
	})

	p.AddStage(&pipeline.PipelineStage{
		Name:  "post_routing",
		Phase: pipeline.PhasePostRouting,
		Mode:  pipeline.ModeSequential,
		Hooks: []pipeline.Hook{
			NewExtensionPointHook(registry, "post_routing"),
		},
	})

	ctx := context.Background()
	envelope := &domain.PipelineRequest{
		TenantID: "bench-tenant",
		Metadata: make(map[string]interface{}),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = p.Execute(ctx, envelope)
	}
}

// BenchmarkPluginExecution_Direct 测量插件直接执行的开销（baseline）。
//
// 用于对比：插件本身的开销 vs 框架开销
func BenchmarkPluginExecution_Direct(b *testing.B) {
	plugin := &benchPlugin{
		metadata: &PluginMetadata{
			PluginID: "bench-direct",
			Phase:    PhaseAuth,
			Priority: PriorityNormal,
		},
	}

	ctx := &ExecutionContext{
		Context:   context.Background(),
		Phase:     PhaseAuth,
		PluginID:  "bench-direct",
		State:     make(map[string]interface{}),
		RequestID: "bench-req",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = plugin.Execute(ctx)
	}
}

// BenchmarkRegistry_Get 测量 Registry.Get() 的性能。
//
// 测试场景：从含有 10 个插件的 Phase 中获取插件列表
func BenchmarkRegistry_Get(b *testing.B) {
	registry := NewExtensionPointRegistry()

	// 注册 10 个插件
	for i := 0; i < 10; i++ {
		plugin := &benchPlugin{
			metadata: &PluginMetadata{
				PluginID: string(rune('A' + i)),
				Phase:    PhaseAuth,
				Priority: Priority(i * 100),
			},
		}
		registry.Register(PhaseAuth, plugin)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = registry.Get(PhaseAuth)
	}
}

// BenchmarkRegistry_Register 测量插件注册的性能。
func BenchmarkRegistry_Register(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		registry := NewExtensionPointRegistry()
		plugin := &benchPlugin{
			metadata: &PluginMetadata{
				PluginID: "bench-register",
				Phase:    PhaseAuth,
				Priority: PriorityNormal,
			},
		}
		b.StartTimer()

		_ = registry.Register(PhaseAuth, plugin)
	}
}

// BenchmarkEnabled_Check 测量 Enabled() 检查的开销。
func BenchmarkEnabled_Check(b *testing.B) {
	registry := NewExtensionPointRegistry()
	hook := NewExtensionPointHook(registry, "authentication")

	ctx := context.Background()
	envelope := &domain.PipelineRequest{
		TenantID: "bench-tenant",
		Metadata: make(map[string]interface{}),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = hook.Enabled(ctx, envelope)
	}
}

// benchPlugin 是用于性能测试的轻量级插件。
type benchPlugin struct {
	metadata *PluginMetadata
}

func (p *benchPlugin) Metadata() *PluginMetadata             { return p.metadata }
func (p *benchPlugin) Initialize(ctx context.Context) error  { return nil }
func (p *benchPlugin) Shutdown(ctx context.Context) error    { return nil }
func (p *benchPlugin) HealthCheck(ctx context.Context) error { return nil }

func (p *benchPlugin) Execute(ctx *ExecutionContext) (*ExecutionResult, error) {
	// 模拟轻量级插件逻辑：简单的 map 操作
	if ctx.State != nil {
		ctx.State["executed"] = true
		ctx.State["plugin_id"] = p.metadata.PluginID
	}

	return &ExecutionResult{
		Success: true,
		Latency: time.Microsecond,
		Metadata: map[string]interface{}{
			"executed": true,
		},
	}, nil
}
