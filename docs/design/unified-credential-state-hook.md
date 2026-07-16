# 统一 Credential 状态更新 Hook 设计

## 问题现状

### 当前架构的缺陷
1. **状态更新分散**：在 executor_chat.go 的多个分支中手动调用 `writeCredentialStateOnError`
2. **容易遗漏**：某些 failover 路径、重试路径、成功路径可能忘记更新状态
3. **不一致**：不同错误类型的处理逻辑不统一
4. **无法追踪**：难以确认每个请求是否都正确更新了状态

### 具体问题案例
从 2026-07-16 minimax-m3 超时事件看到：
- cred 19 在 10:22:41 触发 active_probe（consecutive=2）
- 之后 10:23:13 又失败，但 consecutive 重置为 1
- **原因**：中间可能有其他请求成功但未正确更新 LastSuccessAt，或者失败未正确累加

## 设计方案：Post-Execution Hook

### 核心思想
**在每个上游请求结束后（无论成功/失败/fallback/超时），强制调用统一的状态更新 hook。**

### Hook 触发时机
```
Client Request
    ↓
Executor.Execute()
    ↓
for each candidate:
    ├─ HTTP Request to Upstream
    ├─ Response Processing
    └─ **[HOOK] RecordExecutionOutcome()**  ← 统一入口
        ├─ 成功 → WriteOnSuccess
        ├─ 失败 → WriteOnError
        ├─ 超时 → WriteOnError(stream_timeout)
        ├─ Fallback → WriteOnError(transient)
        └─ Update metrics
    ↓
Return to client
```

### Hook 接口设计

```go
// domains/streaming/executors/credential_state_hook.go

package executors

import (
	"context"
	"time"
	"github.com/kaixuan/llm-gateway-go/domains/credential"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// ExecutionOutcome 统一的请求结果描述
type ExecutionOutcome struct {
	// 身份信息
	CredentialID int
	ProviderID   int
	RawModel     string
	CanonicalModel string
	RequestID    string
	TenantID     string
	
	// 结果
	Success      bool
	ErrorKind    errorsx.ErrorKind  // stream_timeout, network, concurrent, etc.
	ErrorDetail  string
	
	// 延迟
	LatencyMs    int64
	TTFB_Ms      int64
	
	// 流式相关
	IsStream     bool
	ChunkCount   int
	Resumable    bool
	
	// 重试/Fallback
	IsRetry      bool
	IsFallback   bool
	AttemptNum   int
	
	// 时间戳
	StartedAt    time.Time
	CompletedAt  time.Time
}

// CredentialStateHook 统一的状态更新 hook
type CredentialStateHook interface {
	// RecordOutcome 记录单次执行结果
	// 必须在每个 candidate 尝试后调用（无论成功/失败）
	RecordOutcome(ctx context.Context, outcome ExecutionOutcome) error
	
	// RecordFinalOutcome 记录最终结果（所有 candidates 尝试完毕）
	// 用于整体请求级别的统计
	RecordFinalOutcome(ctx context.Context, outcomes []ExecutionOutcome) error
}

// DefaultCredentialStateHook 默认实现
type DefaultCredentialStateHook struct {
	StateWriter  credential.Writer
	CircuitBreaker CircuitBreaker
	ActiveProbe  ActiveProbeSubmitter
	MetricsEmitter MetricsEmitter
	
	// 配置
	ActiveProbeThreshold int // 连续失败多少次触发主动探测
	FlashGuardWindow     time.Duration // 闪断保护窗口（默认 2s）
}

func (h *DefaultCredentialStateHook) RecordOutcome(ctx context.Context, outcome ExecutionOutcome) error {
	if outcome.Success {
		// 1. 更新成功状态
		if err := h.StateWriter.WriteOnSuccess(ctx, outcome.CredentialID, outcome.CanonicalModel); err != nil {
			slog.Warn("state_hook: WriteOnSuccess failed", "error", err)
		}
		
		// 2. 记录熔断器成功
		h.CircuitBreaker.RecordSuccess(outcome.ProviderID, outcome.CredentialID)
		
		// 3. 更新指标
		h.MetricsEmitter.RecordSuccess(outcome)
		
	} else {
		// 1. 更新失败状态
		failure := credential.Failure{
			Kind:   outcome.ErrorKind,
			Detail: outcome.ErrorDetail,
		}
		if err := h.StateWriter.WriteOnError(ctx, outcome.CredentialID, outcome.CanonicalModel, failure); err != nil {
			slog.Warn("state_hook: WriteOnError failed", "error", err)
		}
		
		// 2. 记录熔断器失败
		h.CircuitBreaker.RecordFailure(outcome.ProviderID, outcome.CredentialID, outcome.ErrorKind)
		
		// 3. 检查是否触发主动探测
		consecutiveFails := h.CircuitBreaker.GetConsecutiveFailures(outcome.ProviderID, outcome.CredentialID)
		if consecutiveFails >= h.ActiveProbeThreshold {
			// 闪断保护：检查最近 2s 内是否有成功
			lastSuccess := h.StateWriter.GetLastSuccessAt(ctx, outcome.CredentialID, outcome.CanonicalModel)
			if lastSuccess == nil || time.Since(*lastSuccess) > h.FlashGuardWindow {
				h.ActiveProbe.Submit(outcome.CredentialID, outcome.CanonicalModel, outcome.TenantID, outcome.RequestID)
			}
		}
		
		// 4. 更新指标
		h.MetricsEmitter.RecordFailure(outcome)
	}
	
	// 5. 统一日志（可配置为 debug 级别）
	slog.Debug("state_hook: outcome recorded",
		"credential_id", outcome.CredentialID,
		"model", outcome.CanonicalModel,
		"success", outcome.Success,
		"error_kind", outcome.ErrorKind,
		"latency_ms", outcome.LatencyMs,
		"is_fallback", outcome.IsFallback,
	)
	
	return nil
}
```

### Executor 集成

```go
// domains/streaming/executors/executor_chat.go

func (e *Executor) executeChatRequest(...) (*ExecuteResult, error) {
	// ... 现有逻辑 ...
	
	for attemptNum, cand := range candidates {
		outcomeStartAt := time.Now()
		
		// 发起 HTTP 请求
		resp, err := httpClient.Do(req)
		
		// ========= 关键：立即调用 hook =========
		outcome := ExecutionOutcome{
			CredentialID:   cand.CredentialID,
			ProviderID:     cand.ProviderID,
			RawModel:       cand.RawModel,
			CanonicalModel: params.Model,
			RequestID:      params.RequestID,
			TenantID:       params.TenantID,
			Success:        err == nil && resp.StatusCode == 200,
			ErrorKind:      inferErrorKind(err, resp),
			ErrorDetail:    inferErrorDetail(err, resp),
			LatencyMs:      time.Since(outcomeStartAt).Milliseconds(),
			IsStream:       params.Stream,
			IsRetry:        attemptNum > 0,
			IsFallback:     attemptNum > 0,
			AttemptNum:     attemptNum,
			StartedAt:      outcomeStartAt,
			CompletedAt:    time.Now(),
		}
		
		// 强制调用 hook（defer 也行，但立即调用更明确）
		if e.StateHook != nil {
			if err := e.StateHook.RecordOutcome(params.R.Context(), outcome); err != nil {
				slog.Warn("state_hook: RecordOutcome failed", "error", err)
			}
		}
		// ========= hook 调用结束 =========
		
		// 继续处理响应、failover 等逻辑
		if err != nil || resp.StatusCode != 200 {
			continue // 尝试下一个 candidate
		}
		
		// 成功，返回
		return &ExecuteResult{...}, nil
	}
	
	// 所有 candidates 失败
	return nil, errors.New("all candidates failed")
}
```

### 优势

1. **强制执行**：每个 candidate 尝试后**必须**调用 hook，不会遗漏
2. **统一逻辑**：所有状态更新、熔断器、主动探测逻辑集中在一处
3. **可观测**：统一日志，易于审计和调试
4. **可测试**：hook 可以 mock，便于单元测试
5. **可扩展**：未来可以在 hook 中添加更多逻辑（如 A/B 测试、限流等）

## 实施计划

### Phase 1: Hook 基础设施（1 天）
- [ ] 定义 `ExecutionOutcome` 结构
- [ ] 实现 `DefaultCredentialStateHook`
- [ ] 添加单元测试

### Phase 2: Executor 集成（2 天）
- [ ] executor_chat.go: 在每个 candidate 尝试后调用 hook
- [ ] executor_anthropic.go: 同上
- [ ] executor_common.go: 统一 hook 接口

### Phase 3: 验证与监控（1 天）
- [ ] 添加 hook 调用计数指标（Prometheus）
- [ ] 对比 hook 调用次数 vs 实际请求次数，确保 1:1
- [ ] 部署到 245 测试环境验证

### Phase 4: 生产部署（1 天）
- [ ] 部署到 154 生产
- [ ] 监控 active_probe 触发频率
- [ ] 验证 consecutive_fails 计数正确性

## 遗留问题修复

### 1. active_probe 未执行问题
**现象**：10:22:41 触发 probe，但没有后续执行日志

**可能原因**：
- queue 满了（但日志里没有 "queue full" 警告）
- worker 没启动（但启动日志显示正常）
- **推测**：`node_probe_worker: submit` 后，runLoop 没有及时消费 queue

**修复方案**：
1. 增加 queue 大小（当前可能太小）
2. 增加 worker goroutine 数量（并发处理 probe）
3. 添加 queue 深度监控指标

### 2. consecutive_fails 重置问题
**现象**：10:22:41 consecutive=2，10:23:13 又变回 1

**可能原因**：
- 中间有其他请求成功（但没更新 LastSuccessAt）
- 状态更新有竞态条件
- WriteOnError 中的 consecutive_fails 计数逻辑有 bug

**修复方案**：
- 引入 hook 后，强制所有请求都更新状态，避免遗漏
- 在 DB 层面使用 `CASE` 语句原子更新 consecutive_fails

### 3. 首字节超时无重连
**现象**：30s 首字节超时后直接 failover，没有重连尝试

**应该做什么**：
- **短暂延迟后重试同一个 credential**（如 1s 后重试 1 次）
- **并行请求**：同时向 2 个 credentials 发起请求，使用先返回的结果
- **降级超时阈值**：第一次 30s，重试时降低到 10s

**实施建议**：
```go
// executor_chat.go
if isFirstByteTimeout && !hasRetried {
	// 短暂延迟后重试同一个 credential
	time.Sleep(1 * time.Second)
	hasRetried = true
	goto retry_same_credential // 重试，不计入 attemptNum
}
```

## 总结

**你的建议（统一 hook）是正确的**，可以从根本上解决：
1. 状态更新遗漏
2. consecutive_fails 计数不准
3. active_probe 未触发

配合其他修复（queue 大小、重连机制），可以彻底解决 minimax-m3 超时问题。
