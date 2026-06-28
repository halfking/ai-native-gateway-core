// Package pipeline 实现 Hook Pipeline 执行引擎。
// 这是领域驱动重构的核心组件：所有请求通过一系列可组合的 Hook 流转。
//
// 设计要点：
//   - Hook 接口 5 个方法（Name/Execute/Priority/Enabled/OnError）
//   - 15 个 Phase 常量（覆盖请求全生命周期）
//   - 阶段内支持 sequential / parallel 两种执行模式
//   - parallel 模式使用 errgroup 风格（sync.WaitGroup + cancel + buffered errCh）
//   - 不依赖 errgroup（go.mod 中无此子包），纯标准库实现
package pipeline

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/kaixuan/llm-gateway-go/domain"
)

// Phase 管道阶段标识
type Phase string

const (
	PhasePreAuthentication  Phase = "pre_authentication"
	PhaseAuthentication     Phase = "authentication"
	PhasePostAuthentication Phase = "post_authentication"
	PhasePreRouting         Phase = "pre_routing"
	PhaseRouting            Phase = "routing"
	PhasePostRouting        Phase = "post_routing"
	PhaseGovernance         Phase = "governance" // V4 NEW (PR-V4-02)
	PhasePreTransform       Phase = "pre_transform"
	PhaseTransform          Phase = "transform"
	PhasePostTransform      Phase = "post_transform"
	PhasePreUpstream        Phase = "pre_upstream"
	PhaseUpstream           Phase = "upstream"
	PhasePostUpstream       Phase = "post_upstream"
	PhasePreResponse        Phase = "pre_response"
	PhaseResponse           Phase = "response"
	PhasePostResponse       Phase = "post_response"
)

// AllPhases 返回所有 Phase 的顺序列表。
func AllPhases() []Phase {
	return []Phase{
		PhasePreAuthentication, PhaseAuthentication, PhasePostAuthentication,
		PhasePreRouting, PhaseRouting, PhasePostRouting,
		PhaseGovernance,
		PhasePreTransform, PhaseTransform, PhasePostTransform,
		PhasePreUpstream, PhaseUpstream, PhasePostUpstream,
		PhasePreResponse, PhaseResponse, PhasePostResponse,
	}
}

// ExecutionMode Hook 执行模式
type ExecutionMode string

const (
	// ModeSequential 串行执行（前一个失败则中断）
	ModeSequential ExecutionMode = "sequential"
	// ModeParallel 并行执行（独立运行，errgroup 风格）
	ModeParallel ExecutionMode = "parallel"
)

// Hook 是所有 Pipeline 处理单元的接口。
// 一个 Hook 通常完成单一职责（如：注入 RequestID、解析 API key、压缩请求体等）。
type Hook interface {
	// Name 返回 Hook 名称（用于日志/调试）
	Name() string

	// Execute 执行 Hook 逻辑。
	// 返回 error 会触发 OnError 和后续中断（取决于执行模式）。
	Execute(ctx context.Context, envelope *domain.PipelineRequest) error

	// Priority 优先级（小值先执行；同 Phase 内排序）。
	// 推荐范围: 0-1000
	Priority() int

	// Enabled 是否启用此 Hook。
	// 动态控制：根据 envelope 状态/特性跳过某些 Hook。
	Enabled(ctx context.Context, envelope *domain.PipelineRequest) bool

	// OnError 当 Execute 返回 error 时的错误处理。
	// 返回 nil 表示"已处理"（吞掉错误继续），非 nil 表示向上传递。
	OnError(ctx context.Context, envelope *domain.PipelineRequest, err error) error
}

// PipelineStage 管道阶段。
// 一个 Pipeline 由多个 Stage 组成，每个 Stage 包含一组 Hook 和执行模式。
type PipelineStage struct {
	// Name 阶段名称（用于日志）
	Name string

	// Phase 阶段标识
	Phase Phase

	// Hooks 此阶段包含的 Hook
	Hooks []Hook

	// Mode 执行模式（sequential / parallel）
	Mode ExecutionMode
}

// RequestPipeline 请求管道
type RequestPipeline struct {
	stages []*PipelineStage
}

// NewRequestPipeline 创建一个新的空 Pipeline
func NewRequestPipeline() *RequestPipeline {
	return &RequestPipeline{
		stages: make([]*PipelineStage, 0),
	}
}

// AddStage 添加一个阶段到 Pipeline 末尾。
// 注意：阶段按添加顺序执行，不重新排序。
func (p *RequestPipeline) AddStage(stage *PipelineStage) {
	if stage == nil {
		return
	}
	p.stages = append(p.stages, stage)
}

// Stages 返回所有阶段（只读副本）。
func (p *RequestPipeline) Stages() []*PipelineStage {
	out := make([]*PipelineStage, len(p.stages))
	copy(out, p.stages)
	return out
}

// Execute 执行整个 Pipeline。
// 任一阶段失败立即返回 error（除非该阶段内 OnError 吞掉）。
func (p *RequestPipeline) Execute(ctx context.Context, envelope *domain.PipelineRequest) error {
	if envelope == nil {
		return fmt.Errorf("pipeline: nil envelope")
	}

	for _, stage := range p.stages {
		if err := p.executeStage(ctx, stage, envelope); err != nil {
			return fmt.Errorf("pipeline: stage '%s' (phase=%s) failed: %w",
				stage.Name, stage.Phase, err)
		}

		// 如果 envelope 上有 error（非 nil）且阶段未处理，停止 pipeline
		if envelope.Error != nil {
			return fmt.Errorf("pipeline: envelope error after stage '%s': %w",
				stage.Name, envelope.Error)
		}
	}
	return nil
}

// executeStage 执行单个阶段。
// 1. 过滤启用的 Hook
// 2. 按 Priority 排序
// 3. 根据 Mode 串行/并行执行
func (p *RequestPipeline) executeStage(ctx context.Context, stage *PipelineStage, envelope *domain.PipelineRequest) error {
	// 过滤启用的 Hook
	enabledHooks := make([]Hook, 0, len(stage.Hooks))
	for _, hook := range stage.Hooks {
		if hook.Enabled(ctx, envelope) {
			enabledHooks = append(enabledHooks, hook)
		}
	}

	if len(enabledHooks) == 0 {
		return nil
	}

	// 按 Priority 升序排序（小值先执行）
	sort.SliceStable(enabledHooks, func(i, j int) bool {
		return enabledHooks[i].Priority() < enabledHooks[j].Priority()
	})

	// 根据模式执行
	switch stage.Mode {
	case ModeParallel:
		return p.executeParallel(ctx, enabledHooks, envelope)
	default:
		return p.executeSequential(ctx, enabledHooks, envelope)
	}
}

// executeSequential 串行执行 Hook。
// 第一个失败的 Hook 触发 OnError；若 OnError 返回非 nil，停止后续。
func (p *RequestPipeline) executeSequential(ctx context.Context, hooks []Hook, envelope *domain.PipelineRequest) error {
	for _, hook := range hooks {
		if err := hook.Execute(ctx, envelope); err != nil {
			// 调用 OnError
			if onErr := hook.OnError(ctx, envelope, err); onErr != nil {
				return fmt.Errorf("hook '%s' failed: %w", hook.Name(), onErr)
			}
			// OnError 吞掉错误，继续下一个 hook
		}
	}
	return nil
}

// executeParallel 并行执行 Hook（errgroup 风格的纯标准库实现）。
// 所有 Hook 同时启动；任一失败立即取消其他（通过 ctx）；返回第一个 error。
func (p *RequestPipeline) executeParallel(ctx context.Context, hooks []Hook, envelope *domain.PipelineRequest) error {
	if len(hooks) == 0 {
		return nil
	}

	// errCh 收集错误（buffered, len=1 即可：第一个错误胜出）
	errCh := make(chan error, 1)

	// cancelCtx 用于任一 hook 失败时取消其他
	cancelCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	for _, hook := range hooks {
		hook := hook // 循环变量捕获
		wg.Add(1)
		go func() {
			defer wg.Done()

			// 检查是否已被取消
			if cancelCtx.Err() != nil {
				return
			}

			if err := hook.Execute(cancelCtx, envelope); err != nil {
				// 非阻塞写入第一个 error
				select {
				case errCh <- fmt.Errorf("hook '%s' failed: %w", hook.Name(), err):
					cancel() // 取消其他 goroutine
				default:
				}
			}
		}()
	}

	wg.Wait()
	close(errCh)

	// 返回第一个错误（如果有）
	if err, ok := <-errCh; ok {
		return err
	}
	return nil
}
