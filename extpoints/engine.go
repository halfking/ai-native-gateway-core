package extpoints

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// ExecutionOrder 是 16 阶段管线的**执行顺序**（真源）。
//
// ★ PhaseError 不在其中：它是**失败时**被调用的阶段，不是常规推进的一环。
var ExecutionOrder = []Phase{
	PhasePreAuth,
	PhaseAuth,
	PhasePostAuth,
	PhasePreRoute,
	PhaseRoute,
	PhasePostRoute,
	PhasePreUpstream,
	PhaseUpstream,
	PhasePostUpstream,
	PhasePreResponse,
	PhaseResponse,
	PhasePostResponse,
	PhasePreLog,
	PhaseLog,
	PhasePostLog,
}

// ExecutionStats 是执行统计。
type ExecutionStats struct {
	// TotalExecutions 是 Execute 被调用的次数。
	TotalExecutions int64
	// SuccessfulPhases 是**成功**完成的阶段数（一个阶段里多个插件都算这一个阶段）。
	SuccessfulPhases int64
	// FailedPhases 是失败的阶段数。
	FailedPhases int64
	// TotalLatency 是全部阶段耗时之和。
	TotalLatency time.Duration
}

// ExecutionEngine 按 16 阶段顺序驱动已注册的扩展点。
//
// ★ 2026-11-17（12h 审计）：同窗口的 demo 引用了 `NewExecutionEngine`，
//
//	但本包此前**只有接口** ⇒ demo 所在包编译不过 ⇒ 主分支的
//	`go test ./...` / `go vet ./...` 两道门是红的。这里补上具体实现。
type ExecutionEngine struct {
	registry ExtensionPointRegistry

	mu    sync.Mutex
	stats ExecutionStats
}

// NewExecutionEngine 基于一个注册表构造执行引擎。
func NewExecutionEngine(registry ExtensionPointRegistry) *ExecutionEngine {
	if registry == nil {
		registry = NewExtensionPointRegistry()
	}
	return &ExecutionEngine{registry: registry}
}

// Execute 跑完整条管线。
//
// ★ 失败语义：某阶段的插件返回错误 ⇒ 该阶段算**失败**，
//
//	**继续**跑后续阶段（插件框架的常见做法：日志/观测阶段仍要跑），
//	但把第一个错误记在响应上（Response.Error / Response.ErrorPhase）⇒
//	调用方能同时拿到「出了什么事」和「流水线跑完了」。
//	★ 与 demo 的期望一致（demo 允许 Execute 返回 error，且要求 resp != nil）。
func (e *ExecutionEngine) Execute(ctx context.Context, req *Request) (*Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	start := time.Now()

	e.mu.Lock()
	e.stats.TotalExecutions++
	e.mu.Unlock()

	resp := &Response{
		StatusCode:  200,
		Headers:     http.Header{},
		Body:        req.Body,
		ContentType: req.Headers.Get("Content-Type"),
		Attributes:  map[string]any{},
	}
	if resp.ContentType == "" {
		resp.ContentType = "application/json"
	}
	resp.ContentLength = int64(len(resp.Body))

	// 共享状态：同一次执行内所有插件可见（ExecutionContext.State 的契约）。
	state := map[string]any{}

	var firstErr error
	var firstErrPhase Phase

	for _, phase := range ExecutionOrder {
		plugins := e.registry.Get(phase)
		if len(plugins) == 0 {
			continue
		}
		phaseStart := time.Now()
		phaseOK := true

		for _, p := range plugins {
			exec, ok := p.(PhaseExecutor)
			if !ok {
				// 不是 PhaseExecutor 的扩展点在本阶段不参与执行。
				continue
			}
			md := p.Metadata()
			execCtx := &ExecutionContext{
				Context:     ctx,
				Phase:       phase,
				PluginID:    md.PluginID,
				State:       state,
				RequestID:   req.RequestID,
				RequestTime: start,
				Timeout:     md.Timeout,
			}
			res, err := exec.Execute(execCtx)
			if err != nil {
				phaseOK = false
				if firstErr == nil {
					firstErr = fmt.Errorf("extpoints: phase %s plugin %s: %w", phase, md.PluginID, err)
					firstErrPhase = phase
				}
				continue
			}
			if res != nil && !res.Success {
				phaseOK = false
				if firstErr == nil {
					var cause error = res.Error
					if cause == nil {
						cause = fmt.Errorf("plugin reported failure")
					}
					firstErr = fmt.Errorf("extpoints: phase %s plugin %s: %w", phase, md.PluginID, cause)
					firstErrPhase = phase
				}
			}
		}

		elapsed := time.Since(phaseStart)
		e.mu.Lock()
		e.stats.TotalLatency += elapsed
		if phaseOK {
			e.stats.SuccessfulPhases++
		} else {
			e.stats.FailedPhases++
		}
		e.mu.Unlock()
	}

	resp.TotalTime = time.Since(start)
	if firstErr != nil {
		resp.Error = firstErr
		resp.ErrorPhase = firstErrPhase
		resp.StatusCode = 500
	}
	return resp, firstErr
}

// GetStats 返回执行统计快照。
func (e *ExecutionEngine) GetStats() ExecutionStats {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.stats
}
