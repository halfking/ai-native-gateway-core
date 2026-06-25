// Package integration 把所有领域 Hook 拼装成完整 Pipeline。
//
// 设计动机：
//   - 路由/转换/流式三个领域（routing / transformation / streaming）
//     各有自己的 Hook 抽象；调用方需要一种"开箱即用"的拼装方式。
//   - BuildFullPipeline 把这些 Hook 串成 5-stage Pipeline。
//   - 不引入对 authentication / session / identity 等其他领域的依赖
//     （这些领域的实现尚未完成；当前 task 只交付路由/转换/流式组）。
//
// 使用示例：
//
//	p := integration.BuildFullPipeline(integration.MinimalDeps{})
//	err := p.Execute(ctx, env)
//
// 阶段顺序：
//  1. PhaseRouting       路由决策（粘性 → 轮询）
//  2. PhaseTransform     转换链（sanitize → compress）
//  3. PhasePostUpstream  流式处理（SSE 包装）
//
// 注：未使用所有 15 个 Phase 是有意为之——其他 Phase（auth / identity /
// session）由后续 task 负责。本包专注于已交付领域的串联。
package integration

import (
	"github.com/kaixuan/llm-gateway-go/domains/pipeline"
	"github.com/kaixuan/llm-gateway-go/domains/routing"
	"github.com/kaixuan/llm-gateway-go/domains/streaming"
	"github.com/kaixuan/llm-gateway-go/domains/transformation"
)

// MinimalDeps 最小依赖。
//
// 路由/转换/流式三个领域**当前**不需要外部依赖（都是纯算法）。
// 保留 Deps 类型是为未来扩展（注入真实凭据存储 / session 存储 /
// Redis 客户端）做准备。
type MinimalDeps struct {
	// CompressMaxTokens Compressor 阈值（<=0 用默认 4096）
	CompressMaxTokens int
	// 未来扩展字段：SessionStore / IdentityBuilder / APIKeyVerifier
	// 当前未启用，留位避免破坏现有调用方。
}

// BuildFullPipeline 构造 3-stage 路由/转换/流式 Pipeline。
//
// 阶段组成：
//  1. routing       (PhaseRouting, sequential)
//  2. transform     (PhaseTransform, sequential)
//  3. post_upstream (PhasePostUpstream, sequential)
//
// 当前实现仅启用已交付的 3 个阶段；其他阶段（auth / identity / session /
// pre-routing / pre-transform 等）由后续 task 负责实现。
func BuildFullPipeline(deps MinimalDeps) *pipeline.RequestPipeline {
	p := pipeline.NewRequestPipeline()

	compressMax := deps.CompressMaxTokens
	if compressMax <= 0 {
		compressMax = 4096
	}

	// Phase 3: Routing（粘性 → 轮询）
	p.AddStage(&pipeline.PipelineStage{
		Name:  "routing",
		Phase: pipeline.PhaseRouting,
		Mode:  pipeline.ModeSequential,
		Hooks: []pipeline.Hook{
			routing.NewRoutingHook(
				routing.NewStickyRouter(routing.NewRoundRobinRouter()),
			),
		},
	})

	// Phase 4: Transform（sanitize → compress）
	p.AddStage(&pipeline.PipelineStage{
		Name:  "transform",
		Phase: pipeline.PhaseTransform,
		Mode:  pipeline.ModeSequential,
		Hooks: []pipeline.Hook{
			transformation.NewTransformHook(
				transformation.NewSanitizer(),
				transformation.NewCompressor(compressMax),
			),
		},
	})

	// Phase 5: Post-Upstream（流式）
	p.AddStage(&pipeline.PipelineStage{
		Name:  "post_upstream",
		Phase: pipeline.PhasePostUpstream,
		Mode:  pipeline.ModeSequential,
		Hooks: []pipeline.Hook{
			streaming.NewStreamHook(streaming.NewSSEStreamer()),
		},
	})

	return p
}

// BuildRoutingOnlyPipeline 构造仅含 routing 的 Pipeline（用于路由单测）。
func BuildRoutingOnlyPipeline() *pipeline.RequestPipeline {
	p := pipeline.NewRequestPipeline()
	p.AddStage(&pipeline.PipelineStage{
		Name:  "routing",
		Phase: pipeline.PhaseRouting,
		Mode:  pipeline.ModeSequential,
		Hooks: []pipeline.Hook{
			routing.NewRoutingHook(
				routing.NewStickyRouter(routing.NewRoundRobinRouter()),
			),
		},
	})
	return p
}

// BuildTransformOnlyPipeline 构造仅含 transform 的 Pipeline。
func BuildTransformOnlyPipeline(maxTokens int) *pipeline.RequestPipeline {
	p := pipeline.NewRequestPipeline()
	if maxTokens <= 0 {
		maxTokens = 4096
	}
	p.AddStage(&pipeline.PipelineStage{
		Name:  "transform",
		Phase: pipeline.PhaseTransform,
		Mode:  pipeline.ModeSequential,
		Hooks: []pipeline.Hook{
			transformation.NewTransformHook(
				transformation.NewSanitizer(),
				transformation.NewCompressor(maxTokens),
			),
		},
	})
	return p
}

// BuildStreamingOnlyPipeline 构造仅含 streaming 的 Pipeline。
func BuildStreamingOnlyPipeline() *pipeline.RequestPipeline {
	p := pipeline.NewRequestPipeline()
	p.AddStage(&pipeline.PipelineStage{
		Name:  "post_upstream",
		Phase: pipeline.PhasePostUpstream,
		Mode:  pipeline.ModeSequential,
		Hooks: []pipeline.Hook{
			streaming.NewStreamHook(streaming.NewSSEStreamer()),
		},
	})
	return p
}
