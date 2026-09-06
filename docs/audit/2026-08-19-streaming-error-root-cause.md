# Streaming Error Root Cause Analysis

## 核心发现

通过分析最新的commit eee0511f5和代码流程,我发现**日志系统已经完整**,问题的根本原因不是日志缺失,而是:

1. **Empty Response的处理机制正常,但可能遇到所有candidates都返回empty的情况**
2. **Resume Blocked是设计行为,不是bug**

## 错误流程详解

### 1. Empty Model Response (正常的Failover机制)

#### 触发路径
```
domains/streaming/stream.go:167-175, 285-299
  ↓ (检测到empty stream)
domains/streaming/executors/executor_chat.go:1148-1150
  ↓ (分类为 KindEmptyResponse)
domains/streaming/attempt_outcome.go:149
  ↓ (映射到 TaskActionRetryNow)
domains/streaming/survival_coordinator.go:325-386
  ↓ (Discard buffer & retry)
```

#### 日志链路 (已完整)
```
executor: stream interrupted
  ├─ request_id
  ├─ provider_id
  ├─ credential_id
  ├─ raw_model
  ├─ reason: "empty_stream_no_content"
  ├─ kind: "empty_response"
  ├─ chunk_count: 0
  └─ resumable: true

survival_attempt_outcome
  ├─ attempt: N
  ├─ committed: false
  ├─ commit_state: "none" or "metadata"
  ├─ kinds: "empty_response"
  ├─ action: "retry_now"
  └─ reason: "recoverable_candidate"

survival_attempt_discarded
  ├─ buffer_bytes: X
  ├─ holdback_held: 0
  ├─ state: "none"
  ├─ action: "retry_now"
  └─ reason: "recoverable_candidate"
```

#### 问题场景
当**所有candidates**都返回empty时:
1. 第1个candidate: empty → retry → 第2个candidate
2. 第2个candidate: empty → retry → ...
3. 最后一个candidate: empty → **retry budget exhausted** → fail

**这是正确的行为**,但用户看到的是失败,原因可能是:
- Provider真的没有生成内容(模型问题、quota问题)
- 所有credentials都有同样的问题
- Prompt导致模型无法生成

### 2. Gateway Survival Resume Blocked (设计行为,非Bug)

#### 触发条件 (attempt_outcome.go:235-240)
```go
case committed && (hasRetry || hasWait):
    // Recoverable failure, but client-visible output was already
    // committed — a transparent restart would duplicate it.
    return TaskDecision{
        Action: TaskActionResumeBlocked,
        Reason: "committed_output",
    }
```

这是**安全机制**,防止重复输出:

```
Client已经收到的内容:
  "你好,我是AI助手,我可以帮"  ← 已committed

如果允许retry:
  "你好,我是AI助手,我可以帮你好,我是AI助手,我可以帮"  ← 重复!
```

#### 日志链路 (已完整)
```
survival_attempt_outcome
  ├─ committed: true         ← 关键!
  ├─ commit_state: "content"
  ├─ kinds: "empty_response,timeout,..."
  ├─ action: "resume_blocked"
  └─ reason: "committed_output"

survival_resume_blocked
  ├─ provider_id
  ├─ raw_model
  ├─ committed: true
  └─ commit_state: "content"

request_survival_finished
  ├─ succeed: false
  ├─ decision: "resume_blocked"
  └─ reason: "committed_output"
```

## 真正的问题

### 问题1: Commit Gate过早Commit

**现象**: Stream刚开始输出就commit了,然后遇到empty/timeout

**原因**: 
- `GateModeBuffered` 的commit逻辑: **第一个semantic frame就commit**
- 对于不稳定的upstream,这个策略太激进

**位置**: `domains/streaming/attempt_commit_gate.go:345-359`

```go
// Buffered mode, not yet committed.
if isSemanticClass(class) {
    // First semantic frame triggers the normal semantic commit: flush
    // buffered frames in original order, then this frame.
    if err := g.commitLocked(); err != nil {
        return err
    }
    // ... 已经committed,无法回退
}
```

**影响**:
- glm-5.2/minimax-m3这类不稳定模型
- 可能发送1-2个chunks后就中断
- 但已经committed,无法透明failover

### 问题2: Empty Response Detection太晚

**现象**: 要等stream完全结束才知道是empty

**位置**: `domains/streaming/stream.go:285-299`

```go
if !sawContent {
    // Empty stream — signal Resumable failover.
    // 但此时stream已经完了,可能已经committed了metadata frames
}
```

## 修复方案

### Phase 1: 调整Commit Gate策略 (建议实施)

#### 方案A: 增加Commit Holdback窗口 (推荐)

已经有FR-12 L1 holdback机制,但可能配置不够:

```go
// GateOptions in survival_wiring.go or handler.go
GateOptions{
    Mode: GateModeBuffered,
    HoldbackWindow: 5 * time.Second,    // 增加到5秒
    HoldbackMaxChunks: 5,               // 至少5个chunks
    BeforeSemanticCommit: ...
}
```

**效果**: 
- 前5个semantic chunks或前5秒内的输出会被hold在buffer
- 如果在这个窗口内遇到empty/timeout,可以完全discard
- 窗口关闭后才真正commit给客户端

#### 方案B: Empty Pattern早期检测

在content gate中增加empty pattern检测:

```go
// domains/streaming/stream.go:167
if startingPayload == "[DONE]" {
    // 已有
}

// 新增: 检测连续N个空chunks
if emptyChunkCount > 3 {
    return nil, &StreamOutcome{
        Interrupted: true,
        Reason:      "early_empty_detection",
        Resumable:   true,
    }
}
```

### Phase 2: 增强诊断日志 (立即实施)

虽然日志已经完整,但可以增加更多上下文:

#### 1. Commit决策日志

```go
// domains/streaming/attempt_commit_gate.go:349
if err := g.commitLocked(); err != nil {
    return err
}
slog.Info("attempt_commit_gate_committed",
    "request_id", extractRequestIDFromContext(ctx),
    "buffer_bytes", g.bufferLen,
    "holdback_held", g.holdbackHeld,
    "state", g.state.String(),
    "first_meta_age_ms", time.Since(g.firstMetaAt).Milliseconds(),
)
```

#### 2. Candidate尝试前置日志

```go
// domains/streaming/executors/executor_chat.go 开始executeOpenAI前
slog.Info("candidate_attempt_starting",
    "request_id", params.RequestID,
    "candidate_index", currentIndex,
    "total_candidates", len(params.Candidates),
    "provider_id", cand.ProviderID,
    "credential_id", cand.CredentialID,
    "raw_model", cand.RawModel,
)
```

#### 3. Empty检测详细日志

```go
// domains/streaming/stream.go:167
slog.Debug("empty_stream_detected",
    "request_id", extractFromContext(ctx),
    "chunk_count_total", chunkCount,
    "buffered_bytes", bufferedBytes,
    "first_payload", startingPayload,
)
```

## 当前状态评估

### 已经完成的工作 (commit eee0511f5)

✅ **Survival coordinator日志完整**
- survival_attempt_outcome
- survival_attempt_discarded
- survival_resume_blocked
- request_survival_finished

✅ **Stream中断日志完整**
- executor: stream interrupted
- empty_stream_no_content reason

✅ **Discard event记录**
- StreamCapture.MarkDiscarded
- request_logs_hot.discard_events JSONB

✅ **Commit state追踪**
- CommitState枚举
- commit_state字段在日志中

### 缺失的部分

❌ **Commit gate状态转换日志**
- 什么时候committed
- committed时buffer有多少内容

❌ **Candidate选择日志**
- 每次尝试前记录候选列表
- 当前是哪个candidate

❌ **Empty pattern早期检测**
- 目前要等到stream结束才知道

## 实施建议

### 立即执行 (今天)

1. **增加commit gate日志**: 3处关键位置
2. **增加candidate前置日志**: executor_chat.go开始处
3. **测试并部署到154**

### 短期优化 (本周)

1. **调整HoldbackWindow配置**: 测试不同的窗口大小
2. **监控empty_response rate**: 按provider统计
3. **分析154的实际日志**: 确认commit timing

### 中期改进 (下周)

1. **实施empty pattern早期检测**
2. **Provider级别的empty rate监控和自动降级**
3. **Commit策略自适应调整**

## 结论

**不是bug,是feature边界**:
- Empty response机制工作正常
- Resume blocked是安全保护
- 真正的问题是commit太早,导致无法利用survival的failover能力

**修复优先级**:
1. **P0**: 增加commit gate和candidate日志 (今天)
2. **P1**: 调整holdback配置 (本周)
3. **P2**: 实施early detection (下周)
