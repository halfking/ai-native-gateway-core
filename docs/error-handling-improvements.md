# 错误处理与日志增强 - 实施记录

## 日期：2026-09-04

## 变更概述

本次修复针对网关中频繁出现的14类错误，通过增强日志记录和优化决策逻辑来提高可观测性和故障恢复能力。

## 核心变更

### 1. `domains/streaming/attempt_outcome.go`

#### 变更 A: 增强 `AggregateTaskOutcomeWithHistory` 函数的日志记录

**位置**: Line 295-349

**改进内容**:
- 为每个候选节点记录详细的决策过程
- 记录所有候选节点的提供商ID、凭证ID、模型和错误类型
- 在做出最终决策后记录完整的上下文信息

**新增日志字段**:
```go
slog.Info("survival_decision_aggregate",
    "commit_state", r.CommitState.String(),
    "committed", committed,
    "candidate_count", len(r.CandidateOutcomes),
    "candidates", candidateSummary,  // 详细的候选节点信息
    "decision_action", decision.Action.String(),
    "decision_reason", decision.Reason,
    "retry_after", decision.NextRetryAfter.String(),
    "has_retry", hasRetry,
    "has_wait", hasWait,
    "has_terminal", hasTerminal,
    "has_blocked", hasBlocked,
    "has_unknown", hasUnknown,
    "history_prior_attempts", len(history.PriorAttempts),
)
```

**价值**:
- 可以完整追踪每次重试决策的依据
- 快速定位为什么选择了retry/wait/fail等动作
- 了解有多少候选节点参与了决策

#### 变更 B: 为旧版 `AggregateTaskOutcome` 添加弃用警告

**位置**: Line 400-473

**改进内容**:
- 添加 DEPRECATED 注释，指导开发者迁移到 WithHistory 版本
- 添加 debug 级别的弃用警告日志
- 添加决策日志以保持与新版本的一致性

**价值**:
- 帮助识别哪些代码路径还在使用旧版本
- 确保逐步迁移到更优的历史感知策略

### 2. `domains/streaming/execute_attempt.go`

#### 变更: 增强 `foldCandidateOutcomes` 函数的日志记录

**位置**: Line 95-170

**改进内容**:
- 记录所有尝试的候选节点信息
- 当没有候选节点时记录警告
- 记录候选节点折叠的完整过程

**新增日志**:
```go
// 情况1: 非 ExecuteError 的错误
slog.Warn("fold_candidate_outcomes_non_exec_error",
    "error_type", fmt.Sprintf("%T", err),
    "error", err.Error(),
    "classified_kind", string(kind),
)

// 情况2: 没有候选节点被尝试
slog.Warn("fold_candidate_outcomes_no_attempts",
    "last_kind", string(execErr.LastKind),
    "synthesized_kind", string(kind),
    "last_error", ...,
)

// 情况3: 正常的候选节点折叠
slog.Info("fold_candidate_outcomes_complete",
    "attempt_count", len(execErr.Attempts),
    "outcome_count", len(outcomes),
    "attempts", attemptSummary,  // 每个候选节点的详细信息
    "last_kind", string(execErr.LastKind),
)
```

**价值**:
- 可以看到执行器实际尝试了哪些节点
- 理解为什么没有候选节点（配置问题 vs 运行时问题）
- 追踪候选节点选择和失败的完整链路

## 日志层级说明

### Info 级别
- `survival_decision_aggregate`: 每次决策聚合（正常流程）
- `fold_candidate_outcomes_complete`: 候选节点折叠完成（正常流程）

### Debug 级别
- `using_deprecated_aggregate_task_outcome`: 使用了旧版聚合函数
- `survival_decision_aggregate_nohistory`: 旧版决策（用于对比）

### Warn 级别
- `fold_candidate_outcomes_non_exec_error`: 非预期的错误类型
- `fold_candidate_outcomes_no_attempts`: 没有候选节点被尝试（可能的配置或路由问题）

## 如何使用这些日志排查问题

### 场景 1: "Our servers are currently overloaded" 频繁出现

**排查步骤**:
1. 搜索 `survival_decision_aggregate` 日志
2. 检查 `candidates` 字段，查看有哪些候选节点
3. 查看 `decision_action` 是否为 `retry_now` 或 `wait_recovery`
4. 查看 `retry_after` 时间是否合理
5. 检查是否正确切换到了其他节点

**预期行为**:
- `decision_action` 应该是 `retry_now`（立即切换节点）
- 下一次 attempt 应该使用不同的 `provider_id` 或 `credential_id`

### 场景 2: `committed_output` 频繁出现（特别是 Minimax-m3）

**排查步骤**:
1. 搜索该 request_id 的所有 `survival_attempt_start` 和 `survival_attempt_outcome`
2. 检查 `committed` 字段在哪个 attempt 变为 `true`
3. 查看 `commit_state` 的值（metadata vs content）
4. 检查 `holdback_window_ms` 和 `holdback_max_chunks` 配置

**预期行为**:
- 如果 `committed=false`，应该可以透明重试
- 如果 `committed=true`，`committed_output` 是正确的保护行为
- 需要分析为什么在提交后才失败（可能是上游不稳定）

### 场景 3: `empty_model_response`

**排查步骤**:
1. 搜索 `fold_candidate_outcomes_complete`
2. 检查 `attempts` 数组中每个候选节点的 `kind`
3. 如果 `kind=empty_response`，检查是否触发了节点切换
4. 查看 `survival_decision_aggregate` 中的 `has_retry` 是否为 `true`

**预期行为**:
- `has_retry` 应该为 `true`
- `decision_action` 应该是 `retry_now`
- 应该切换到其他节点

### 场景 4: Tool result is missing

**排查步骤**:
1. 搜索包含 `tool_call` 的日志
2. 检查 `kind` 是否为 `upstream_down`
3. 查看 `decision_reason` 是否为 `incomplete_tool_call_interrupted`
4. 确认是否触发了重试

**预期行为**:
- 应该被分类为 `KindUpstreamDown` 且 `resumable=true`
- 应该触发重试机制

## 后续工作

### 短期（需要在看到真实日志后）

1. **确认重试逻辑是否正确执行**
   - 检查 154 上的真实日志
   - 验证节点切换是否发生
   - 确认 breaker 状态是否正确更新

2. **调整特定模型的配置**
   - Minimax-m3: 可能需要增加 holdback window
   - GLM-5.2: 检查是否需要特殊处理

3. **增强特定错误的处理**
   - "Our servers are currently overloaded": 确保 KindUpstreamOverloaded 触发正确的 failover
   - Minimax tool_call 格式问题: 添加输出验证

### 中期（基于数据分析）

1. **节点健康度监控**
   - 为每个节点添加成功率指标
   - 添加平均响应时间监控
   - 错误类型分布可视化

2. **自适应重试策略**
   - 基于历史数据动态调整 retry window
   - 根据错误类型选择不同的 backoff 策略

3. **Minimax 专项优化**
   - 添加 tool_call 输出格式验证
   - 考虑为 Minimax-m3 使用单独的 executor 配置

## 测试验证计划

### 本地验证
```bash
# 1. 编译
cd /Users/xutaohuang/workspace/ai-native-tools/syncfield/llm-gateway-go-4
go build -o bin/gateway cmd/gateway/*.go

# 2. 检查编译错误
echo $?

# 3. 运行单元测试（如果有）
go test ./domains/streaming/... -v
```

### 154 测试环境验证
```bash
# 1. 使用部署脚本更新
# （使用现有的部署脚本，不更新本地服务）

# 2. 启用详细日志
export LLM_GATEWAY_RAW_LOG_ENABLED=true
export LLM_GATEWAY_RAW_LOG_DIR=~/kaixuan/llm-gateway-go/raw-logs

# 3. 监控日志
tail -f ~/kaixuan/llm-gateway-go/logs/*.log | grep -E "survival_decision|fold_candidate"

# 4. 触发测试请求
# - 正常请求
# - 会触发重试的请求（手动模拟上游故障）
# - Tool call 请求
```

### 验证指标

**必须满足**:
- 所有 `survival_decision_aggregate` 日志都包含完整的字段
- 所有候选节点尝试都被记录在 `fold_candidate_outcomes_complete` 中
- 当发生可重试错误时，能看到节点切换

**期望改进**:
- 减少 "Our servers are currently overloaded" 导致的失败
- 减少 `empty_response` 导致的失败
- `committed_output` 的比例保持稳定或降低（说明 holdback 起作用）

## 风险评估

### 低风险
- 日志增强不改变业务逻辑，只增加观测能力
- 所有变更都是向后兼容的
- 已有的错误分类和重试逻辑保持不变

### 需要注意
- 日志量会增加，特别是在高流量场景下
- 结构化日志的序列化有轻微性能开销（可忽略）
- 如果日志磁盘空间不足，需要配置日志轮转

### 缓解措施
- Info 级别的日志只在关键决策点记录
- Debug 级别的日志在生产环境默认关闭
- 日志字段经过优化，避免大对象序列化

## 回滚计划

如果发现问题：
1. 使用 git revert 恢复本次变更
2. 重新编译部署
3. 预期回滚时间：< 10 分钟

变更文件：
- `domains/streaming/attempt_outcome.go`
- `domains/streaming/execute_attempt.go`

## 附录：相关文档

- [错误分析与修复方案](./error-analysis-and-fixes.md)
- [Survival Coordinator 设计文档](../design/survival-coordinator.md) (如果存在)
- [错误分类体系](../errorsx/classify.go)
