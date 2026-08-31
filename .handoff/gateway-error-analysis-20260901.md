# 网关错误分析与修复方案

## 问题概述

在154和245环境中频繁出现以下错误：

1. **empty_model_response** - GLM-5.2、Minimax-m3等模型返回空响应
2. **gateway_survival_resume_blocked** - 存活协调器因committed_output阻止恢复
3. **committed_output** - 部分输出后流中断，无法重试
4. **Network connection failed** - 网络连接失败
5. **Our servers are currently overloaded** - 上游过载但节点状态标注不合理
6. **Upstream service temporarily unavailable** - 上游临时不可用

## 根因分析

### 1. Empty Response 问题

**现象**：
- GLM-5.2、Minimax-m3 频繁返回 HTTP 200 但内容为空
- 错误码：`empty_model_response`

**根因**：
从代码分析发现：
- `errorsx/classify.go` 定义了 `KindEmptyResponse` 
- `domains/streaming/stream.go` 有空响应检测机制（`runEmptyStreamGate`）
- `domains/streaming/survival_coordinator.go` 的 L1 holdback window 可以缓存前N个chunk

**问题**：
1. 空响应检测可能触发太晚，部分字节已发送到客户端
2. Survival coordinator 的 holdback window 配置可能不合理
3. 日志记录不足，无法追踪完整流程

### 2. Survival Resume Blocked 问题

**现象**：
```
Turn execution failed
provider=ef7bed64-de6f-42d8-86f2-eab4b62d9812 provider_code=gateway_survival_resume_blocked 
model=minimax-m3 request=499a5fb6-bdba-43bc-9b78-729c75cd5592 reason=unknown retryable=false
```

**根因**：
从 `survival_coordinator.go` 分析：
- L329-370: TaskActionSucceed 分支会 flush 和 commit gate
- L372-529: Retry分支检查 `CommitState >= CommitStateContent`
- L531-551: ResumeBlocked/FailTerminal 分支记录错误

**问题**：
1. Minimax-m3 流式响应在发送少量字节后中断
2. AttemptCommitGate 已标记为 committed（`CommitStateContent`）
3. Survival coordinator 无法 discard 已提交的尝试
4. 最终返回 `gateway_survival_resume_blocked`

**关键代码**：
```go
// survival_coordinator.go:337-349
if err := gw.Finish(); err != nil {
    res.Decision = TaskDecision{Action: TaskActionFailClosed, Reason: "client_disconnected"}
    // ...
}
if err := gate.Commit(); err != nil {
    res.Decision = TaskDecision{Action: TaskActionFailClosed, Reason: "client_disconnected"}
    // ...
}
```

### 3. Holdback Window 配置问题

**当前配置**（从代码推断）：
```go
// survival_coordinator.go:232-233
hbWindow, hbChunks := RecoveryHoldbackFromEnv()
// 默认值可能是 HoldbackWindow=0（禁用）或很小
```

**问题**：
- Minimax-m3 和 GLM-5.2 经常在前几个chunk就出问题
- 如果 holdback window 太小或未启用，无法捕获早期故障
- 需要调整 `HoldbackWindow` 和 `HoldbackMaxChunks` 参数

### 4. 日志不足问题

**缺失的关键日志**：
1. 每个chunk的接收和发送时间戳
2. Gate状态转换的完整记录
3. 网络错误的详细上下文（重试次数、节点信息）
4. Survival coordinator 的决策路径
5. Empty response 检测的触发条件

## 修复方案

### Phase 1: 增强日志记录（优先级：P0）

#### 1.1 在 survival_coordinator.go 增强日志

**位置**: `survival_coordinator.go:264-328`

**修改**：
```go
// 在每个attempt开始时记录详细信息
log.Info("survival_attempt_start", 
    "request_id", params.RequestID,
    "attempt", res.Attempts,
    "holdback_window_ms", hbWindow.Milliseconds(),
    "holdback_max_chunks", hbChunks,
    "gate_mode", "buffered",
)

// 在gate操作时增加日志
if err := gw.Finish(); err != nil {
    log.Error("survival_gate_finish_failed",
        "request_id", params.RequestID,
        "attempt", res.Attempts,
        "error", err,
        "buffer_bytes", gate.Snapshot()[0],
        "commit_state", gate.State().String(),
    )
}
```

#### 1.2 在 attempt_commit_gate.go 增强状态转换日志

**位置**: `attempt_commit_gate.go:604-629`

**修改**：
```go
func (g *AttemptCommitGate) advanceStateLocked(class FrameClass) bool {
    oldState := g.state
    // ... 现有逻辑 ...
    if next > g.state {
        g.state = next
        slog.Debug("gate_state_advanced",
            "request_id", g.requestID,
            "from", oldState.String(),
            "to", next.String(),
            "frame_class", class,
            "buffer_bytes", g.bufferLen,
            "committed", g.committed,
        )
        return true
    }
    return false
}
```

#### 1.3 在 stream.go 增强空响应检测日志

**位置**: `stream.go:224-244`

**修改**：
```go
observeEarlyEmptyDelta := func(payload string) *StreamOutcome {
    if earlyEmptyChunks == 0 {
        return nil
    }
    if chunkHasContent(payload) {
        if consecutiveEmptyDeltas > 0 {
            slog.Debug("early_empty_reset_by_content",
                "request_id", params.RequestID,
                "consecutive_empty", consecutiveEmptyDeltas,
                "payload_preview", truncateString(payload, 100),
            )
        }
        consecutiveEmptyDeltas = 0
        return nil
    }
    // ... 检测逻辑 ...
    consecutiveEmptyDeltas++
    slog.Debug("early_empty_delta_detected",
        "request_id", params.RequestID,
        "consecutive", consecutiveEmptyDeltas,
        "threshold", earlyEmptyChunks,
        "payload_preview", truncateString(payload, 100),
    )
    if consecutiveEmptyDeltas >= earlyEmptyChunks {
        slog.Warn("early_empty_threshold_reached",
            "request_id", params.RequestID,
            "consecutive", consecutiveEmptyDeltas,
            "threshold", earlyEmptyChunks,
        )
        return earlyEmptyOutcome(capture)
    }
    return nil
}
```

### Phase 2: 调整 Holdback Window 配置（优先级：P0）

#### 2.1 检查当前环境变量

需要确认 154 和 245 的配置：
```bash
# 检查这些环境变量
LLM_GATEWAY_RECOVERY_HOLDBACK_WINDOW
LLM_GATEWAY_RECOVERY_HOLDBACK_MAX_CHUNKS
```

#### 2.2 推荐配置

针对 Minimax-m3 和 GLM-5.2 的不稳定特性：
```bash
# 保持前20个chunk或前5秒的内容
export LLM_GATEWAY_RECOVERY_HOLDBACK_WINDOW=5000  # 5秒
export LLM_GATEWAY_RECOVERY_HOLDBACK_MAX_CHUNKS=20
```

### Phase 3: 优化错误分类和恢复逻辑（优先级：P1）

#### 3.1 改进 empty response 的早期检测

**位置**: `stream.go:220-280`

**问题**：当前逻辑在读取多个chunk后才能确定是否为空响应

**改进**：
1. 降低 `earlyEmptyChunks` 阈值（从可能的10降到5）
2. 增加对特定模型的快速检测路径
3. 在第一个chunk就判断是否为已知的空响应模式

#### 3.2 增强 Survival Coordinator 的 committed 状态处理

**位置**: `survival_coordinator.go:337-370`

**当前问题**：gate.Finish() 失败后，decision 是 FailClosed，但没有记录原始错误

**改进**：
```go
if err := gw.Finish(); err != nil {
    lastProviderID := 0
    lastRawModel := ""
    if res.FinalAttempt != nil && res.FinalAttempt.ExecResult != nil {
        lastProviderID = res.FinalAttempt.ExecResult.Candidate.ProviderID
        lastRawModel = res.FinalAttempt.ExecResult.Candidate.RawModel
    }
    res.Decision = TaskDecision{Action: TaskActionFailClosed, Reason: "client_disconnected"}
    recordSurvivalTransition(survivalStateRunning, survivalTerminalToState(res.Decision), res.Decision.Reason)
    recordSurvivalRequestTerminal(c.Protocol, res.Decision)
    log.Error("survival_task_ended_gate_finish_error",
        "attempt", res.Attempts,
        "action", res.Decision.Action.String(),
        "reason", res.Decision.Reason,
        "committed", res.FinalAttempt.CommitState >= CommitStateContent,
        "succeed", false,
        "finish_error", err.Error(),
        "provider_id", lastProviderID,
        "raw_model", lastRawModel,
    )
    return res
}
```

### Phase 4: 节点状态标注优化（优先级：P1）

#### 4.1 检查 "Our servers are currently overloaded" 的分类

**位置**: `errorsx/classify.go:131-158`

**当前逻辑**：
```go
// KindUpstreamOverloaded: 500/502 with overload body
if concurrentOverloadRe.Match(body) {
    return overloadKindForStatus(status)
}
```

**问题验证**：
需要确认这个错误是否正确映射到 `KindUpstreamOverloaded`，以及是否正确触发了节点降级

#### 4.2 确认节点状态更新逻辑

**位置**: `domains/streaming/executors/executor_nodehealth.go`

需要确认：
1. `KindUpstreamOverloaded` 是否正确写入节点状态
2. 节点恢复时间是否合理
3. 是否有正确的探测机制

## 实施计划

### Step 1: 增加日志（立即执行）
1. 修改 `survival_coordinator.go` - 增强 attempt 生命周期日志
2. 修改 `attempt_commit_gate.go` - 增强状态转换日志  
3. 修改 `stream.go` - 增强空响应检测日志

### Step 2: 配置调整（立即执行）
1. 在 `.env.example` 添加推荐的 holdback 配置
2. 更新 154 和 245 的环境变量
3. 重启服务验证

### Step 3: 代码修复（次优先）
1. 优化 empty response 早期检测
2. 改进 gate.Finish() 错误处理
3. 验证节点状态标注逻辑

### Step 4: 测试验证
1. 本地复现 Minimax-m3 / GLM-5.2 错误
2. 验证新日志是否完整记录流程
3. 部署到 154 测试
4. 观察 24 小时，收集新日志
5. 分析是否解决问题

### Step 5: 生产部署
1. 合并到 main 分支
2. 部署到 245（当前主服务）
3. 持续监控

## 测试用例

### Test Case 1: Minimax-m3 空响应
```bash
curl -X POST https://154-server/v1/chat/completions \
  -H "Authorization: Bearer xxx" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "minimax-m3",
    "messages": [{"role": "user", "content": "test"}],
    "stream": true
  }'
```

**预期**：
- 日志记录完整的 attempt 流程
- 如果空响应，应该在 holdback window 内检测并重试
- 最终返回有效响应或明确的错误信息

### Test Case 2: GLM-5.2 流中断
```bash
curl -X POST https://154-server/v1/chat/completions \
  -H "Authorization: Bearer xxx" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "glm-5.2",
    "messages": [{"role": "user", "content": "写一篇长文章"}],
    "stream": true
  }'
```

**预期**：
- 如果在前几个chunk后中断，holdback window 应该捕获
- 日志记录 gate 状态、buffer 大小、discard 原因
- 自动重试其他候选节点

## 关键指标

部署后需要监控：

1. **empty_model_response 发生率**
   - 目标：降低 80%
   - 指标：`request_logs_hot.error_kind = 'empty_response'`

2. **gateway_survival_resume_blocked 发生率**
   - 目标：降低 90%
   - 指标：`request_logs_hot.provider_code = 'gateway_survival_resume_blocked'`

3. **Survival coordinator 重试成功率**
   - 目标：> 85%
   - 指标：`survival_attempt_outcome.action = 'succeed'` 比例

4. **Holdback window 命中率**
   - 新指标：记录有多少请求在 holdback window 内被 discard 并重试成功

## 风险评估

### 低风险
- 增加日志：只读操作，不影响业务逻辑
- 环境变量调整：可以动态回滚

### 中风险
- Holdback window 增大：可能增加少量延迟（最多5秒）
- 空响应检测优化：需要充分测试避免误判

### 高风险
- 无

## 回滚方案

如果部署后出现问题：

1. **立即回滚**：
   ```bash
   # 恢复环境变量
   export LLM_GATEWAY_RECOVERY_HOLDBACK_WINDOW=0
   export LLM_GATEWAY_RECOVERY_HOLDBACK_MAX_CHUNKS=0
   ```

2. **代码回滚**：
   ```bash
   git revert <commit-hash>
   git push origin main
   ```

3. **服务重启**：
   ```bash
   systemctl restart llm-gateway-go
   ```

## 后续优化方向

1. **自适应 Holdback Window**
   - 根据模型历史表现动态调整
   - GLM-5.2: 更长的 window
   - 稳定模型: 更短的 window

2. **模型特征库**
   - 记录每个模型的失败模式
   - 针对性的检测和恢复策略

3. **智能重试**
   - 基于失败类型选择重试策略
   - 避免对同一节点的无效重试

---

**文档版本**: 1.0  
**创建日期**: 2026-09-01  
**作者**: ZCode Agent  
**状态**: 待审核
