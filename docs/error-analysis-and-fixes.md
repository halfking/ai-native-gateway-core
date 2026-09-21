# LLM Gateway 错误分析与修复方案

## 日期：2026-09-04

## 概述
本文档分析了网关中频繁出现的14类错误，确定根本原因，并提供修复方案。

## 错误分类与根本原因分析

### 1. empty_model_response (KindEmptyResponse)
**错误现象**：provider返回HTTP 200，但响应体为空或无有效内容
**根本原因**：
- 上游模型返回了空的choices数组或choices[0].message无内容
- 分类在 `errorsx/classify.go:KindEmptyResponse`
- 处理在 `domains/streaming/executors/empty_response.go`

**当前策略**：
- 被标记为可重试 (`IsRetryable` 返回 false，但由专门的failover路径处理)
- 不触发credential冷却 (不在 `IsCredentialFatal` 中)
- 应该触发转移到下一个节点

**问题**：
- 当前分类正确，但需要确认重试逻辑是否正确执行

### 2. gateway_survival_resume_blocked (committed_output)
**错误现象**：流式响应已提交部分输出后发生错误，无法透明重试
**根本原因**：
- 在 `domains/streaming/attempt_outcome.go:208-209` 和 `:337-340`
- 当 `CommitState >= CommitStateContent` 且遇到可恢复错误时触发
- 这是**预期行为**，防止重复输出到客户端

**当前策略**：
```go
if committed && (hasRetry || hasWait) {
    return TaskDecision{Action: TaskActionResumeBlocked, Reason: "committed_output"}
}
```

**问题**：
- 这个不是bug，是正确的保护机制
- 但频繁出现说明上游不稳定
- **需要分析：为什么会频繁在已提交后才失败？**

### 3. Network connection failed
**错误现象**：网络连接失败
**根本原因**：
- 分类为 `KindNetwork` (errorsx/classify.go:716-721)
- 包含 "connection", "refused", "no such host", "reset" 等错误

**当前策略**：
- `IsRetryable` 返回 true (classify.go:1293)
- 应该立即重试下一个节点

**问题**：
- 需要确认是否正确触发了节点切换
- 需要检查节点健康度标记

### 4. Our servers are currently overloaded
**错误现象**：上游返回 502 "servers are currently overloaded"
**根本原因**：
- 分类为 `KindUpstreamOverloaded` (errorsx/classify.go:158)
- 在 `classify.go:594-601` 的 `overloadKindForStatus` 中处理

**当前策略**：
```go
case http.StatusInternalServerError, http.StatusBadGateway:
    return KindUpstreamOverloaded  // NOT KindConcurrent
```

**问题**：
- 应该被视为可重试 (`IsRetryable` 中包含 `KindUpstreamOverloaded`)
- **需要检查：是否正确标记了节点状态？**
- **需要检查：breaker是否正确处理？**

### 5. Upstream service temporarily unavailable
**错误现象**：上游服务暂时不可用
**根本原因**：
- HTTP 5xx 错误，分类为 `KindUpstreamDown` (classify.go:793-794)

**当前策略**：
- `IsRetryable` 返回 true
- 应该触发 `TaskActionWaitRecovery`

**问题**：
- 需要确认恢复窗口时间设置
- 需要检查是否正确写入了 `availability_recover_at`

### 6. Tool result is missing
**错误现象**：tool_use 没有对应的 tool_result
**根本原因**：
- 新增的验证器 `domains/streaming/tool_call_validator.go`
- 检测到 tool_use 但流在 tool_result 到达前中断

**当前策略**：
```go
func classifyIncompleteToolCall(streamReceivedDone bool) (kind errorsx.ErrorKind, resumable bool, reason string) {
    if streamReceivedDone {
        return errorsx.KindUpstreamDown, true, "incomplete_tool_call_after_done"
    }
    return errorsx.KindUpstreamDown, true, "incomplete_tool_call_interrupted"
}
```

**问题**：
- 分类正确 (KindUpstreamDown, resumable=true)
- **需要检查：是否正确触发了重试？**

### 7. prompt exceeds gateway budget
**错误现象**：提示词超过网关预算限制
**根本原因**：
- 在请求验证阶段触发
- 限制：`LLM_GATEWAY_MAX_PROMPT_TOKENS=1048576`

**当前策略**：
- 这是客户端错误，应该直接返回 400

**问题**：
- **不应该重试或切换节点**
- 需要确认返回的错误码和消息是否清晰

### 8. 错误识别工具输出 (minimax)
**错误现象**：将 tools 当作文本输出，如 `<tool_call>` 标签
**根本原因**：
- Minimax 模型的输出格式问题
- 转换层未正确处理 tool_calls

**当前策略**：
- 应该被视为转换错误或内容过滤

**问题**：
- **需要增强 Minimax 的输出验证**
- **可能需要添加专门的 pattern 检测**

### 9. stream_read_error
**错误现象**：流读取错误
**根本原因**：
- 在 `domains/streaming/handler.go` 中定义
- 包括格式错误的 SSE、读取失败等

**当前策略**：
```go
"read_error", "stream_read_error":
    return "stream_read_error"
```

**问题**：
- 需要区分是客户端断开还是上游问题
- 应该根据具体原因决定是否重试

## 关键发现

### 1. 错误分类总体正确
代码中的错误分类逻辑（errorsx/classify.go）是完善的，包含：
- 正确的正则表达式匹配
- 合理的重试策略映射
- 适当的节点状态标记

### 2. 核心问题在于执行层
问题主要在以下几个方面：

#### A. 重试决策执行不一致
```go
// attempt_outcome.go 中有两个聚合函数
AggregateTaskOutcome()           // 旧版，无历史
AggregateTaskOutcomeWithHistory() // 新版，有历史
```
需要确认所有调用点都使用了正确的版本。

#### B. 节点状态写入可能不完整
需要检查 `domains/credential/writer.go` 中的状态写入逻辑。

#### C. 日志记录不充分
当前日志可能缺少：
- 完整的候选节点列表
- 每次重试的详细决策过程
- 节点状态变更记录

### 3. Minimax-m3 特别问题
Minimax-m3 频繁出现 `committed_output` 错误，可能原因：
1. 模型响应不稳定
2. 流式输出在中途更容易断开
3. 需要调整该模型的超时配置

## 修复方案

### Phase 1: 增强日志记录（优先级：最高）

#### 1.1 在 attempt_outcome.go 中添加详细日志
```go
func AggregateTaskOutcomeWithHistory(r *AttemptResult, history errorsx.DecisionHistory) TaskDecision {
    // 添加完整的决策日志
    slog.Info("survival_decision",
        "request_id", extractRequestID(history),
        "attempt_no", len(history.PriorAttempts),
        "committed", r.CommitState >= CommitStateContent,
        "candidate_count", len(r.CandidateOutcomes),
        "candidates", formatCandidates(r.CandidateOutcomes),
    )
    // ... existing logic
}
```

#### 1.2 在 executor.go 中记录候选节点选择
需要在候选节点选择时记录：
- 可用节点总数
- 每个节点的健康状态
- 最终选择的节点及原因

#### 1.3 在 credential/writer.go 中记录状态变更
记录每次写入 credential 状态的完整上下文。

### Phase 2: 修复重试逻辑（优先级：高）

#### 2.1 统一使用 WithHistory 版本
确保所有调用点都使用 `AggregateTaskOutcomeWithHistory`。

#### 2.2 修复 "Our servers are currently overloaded" 处理
确保 KindUpstreamOverloaded 正确触发节点切换：

```go
// 在 errorsx/failover_policy.go 中
case KindUpstreamOverloaded:
    return FailoverDecision{
        ShouldMove: true,
        Scope: ScopeNode,  // 切换节点
        ProbeFirst: false,
        RetryAfter: DefaultFrontendWait,
    }
```

#### 2.3 增强 empty_response 的处理
确保 empty_response 正确触发 resumable failover。

### Phase 3: Minimax 专项修复（优先级：中）

#### 3.1 添加 Minimax tool_call 格式验证
```go
// 在 transformation/minimax 中添加
func validateMinimaxToolOutput(body []byte) error {
    // 检测 <tool_call> 等标签泄露
    if bytes.Contains(body, []byte("<tool_call>")) {
        return errors.New("minimax tool format leak detected")
    }
    return nil
}
```

#### 3.2 调整 Minimax-m3 超时配置
增加该模型的超时时间或减少单次请求的 token 数。

### Phase 4: 监控与告警（优先级：中）

#### 4.1 添加错误率指标
为每种错误类型添加 Prometheus 指标。

#### 4.2 添加节点健康度仪表板
可视化每个节点的：
- 成功率
- 平均响应时间
- 错误分布

## 实施计划

### Step 1: 增强日志（1-2小时）
1. 修改 attempt_outcome.go
2. 修改 executor.go  
3. 修改 credential/writer.go
4. 设置 `LLM_GATEWAY_RAW_LOG_ENABLED=true` 启用原始日志

### Step 2: 本地验证（30分钟）
1. 运行本地测试
2. 检查日志输出是否完整
3. 模拟几种错误场景

### Step 3: 部署到 154 测试（1小时）
1. 使用部署脚本更新 154
2. 观察日志输出
3. 收集真实错误案例

### Step 4: 分析并修复（2-3小时）
1. 基于 154 的日志确定根本原因
2. 实施具体修复
3. 再次部署验证

### Step 5: 审计和提交（1小时）
1. 代码审查
2. 编写测试
3. 提交和推送

## 预期结果

修复后应该达到：
1. **日志完整**：每个请求都有完整的候选节点选择、重试、失败原因记录
2. **策略正确**：每种错误都按预期触发正确的重试/切换/报错动作
3. **可观测**：通过日志可以回溯任何请求的完整流程
4. **错误率下降**：特别是 "Our servers are currently overloaded" 和 empty_response 错误

## 附录：关键代码位置

- 错误分类：`errorsx/classify.go`
- 重试决策：`domains/streaming/attempt_outcome.go`
- 执行器：`domains/streaming/executors/executor.go`
- 状态写入：`domains/credential/writer.go`
- 流处理：`domains/streaming/handler.go`
- Survival 协调：`domains/streaming/survival_coordinator.go`
