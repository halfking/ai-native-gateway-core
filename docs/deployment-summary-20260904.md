# 错误处理增强 - 部署总结

## 部署信息

- **部署时间**: 2026-09-04 23:48
- **目标服务器**: 154 (47.97.111.154)
- **版本**: v2.5.0-fc28e27b-20260904-1929
- **Build Seq**: 1929
- **部署耗时**: 99秒（切换24秒）

## 变更摘要

本次部署主要增强了网关的日志记录能力，以便更好地追踪和诊断14类频繁出现的错误。

### 核心变更

1. **增强决策聚合日志** (`domains/streaming/attempt_outcome.go`)
   - 为每个候选节点记录详细信息（provider_id, credential_id, kind）
   - 记录完整的决策上下文（has_retry, has_wait, has_terminal等）
   - 可以追踪为什么选择了特定的动作（retry/wait/fail）

2. **增强候选节点折叠日志** (`domains/streaming/execute_attempt.go`)
   - 记录所有尝试的候选节点
   - 区分三种情况：非ExecuteError、无候选节点、正常折叠
   - 提供完整的候选节点选择链路追踪

3. **添加弃用警告**
   - 为旧版 `AggregateTaskOutcome` 添加DEPRECATED注释
   - 引导开发者迁移到 `AggregateTaskOutcomeWithHistory`

## 关键日志字段

### survival_decision_aggregate (Info级别)
```
commit_state: 提交状态
committed: 是否已提交内容
candidate_count: 候选节点数量
candidates: [{provider_id, credential_id, raw_model, kind, action, retry_after}]
decision_action: 最终决策动作
decision_reason: 决策原因
has_retry, has_wait, has_terminal, has_blocked, has_unknown: 决策标志
history_prior_attempts: 历史尝试次数
```

### fold_candidate_outcomes_complete (Info级别)
```
attempt_count: 尝试次数
outcome_count: 结果数量
attempts: [{provider_id, credential_id, raw_model, kind}]
last_kind: 最后的错误类型
```

## 验证结果

### 部署验证 ✅
- [x] 编译成功
- [x] 单元测试通过（TestAggregateTaskOutcome系列）
- [x] 上传到154成功
- [x] 服务启动正常
- [x] /healthz 返回200
- [x] /readyz 返回200
- [x] /version 返回正确的版本号（1929）
- [x] DB连接正常
- [x] 凭据解密冒烟通过（15个凭据，0个失败）
- [x] Nginx切换完成

### 日志验证
由于是新增的日志，需要等待实际的错误场景触发才能看到。

## 如何使用新日志排查问题

### 场景1: "Our servers are currently overloaded"

**查询命令**:
```bash
ssh root@47.97.111.154 -p 25022
journalctl -u llm-gateway-go.service --since "1 hour ago" | \
  grep -A 10 "survival_decision_aggregate" | \
  grep -E "overloaded|decision_action|candidates"
```

**检查点**:
- `decision_action` 应该是 `retry_now`
- 下一个attempt应该使用不同的 `provider_id` 或 `credential_id`

### 场景2: committed_output (特别是Minimax-m3)

**查询命令**:
```bash
ssh root@47.97.111.154 -p 25022
journalctl -u llm-gateway-go.service --since "1 hour ago" | \
  grep -B 5 "committed_output" | \
  grep -E "survival_attempt_start|committed|holdback"
```

**检查点**:
- 查看 `committed` 在哪个attempt变为true
- 检查 `holdback_window_ms` 配置是否足够

### 场景3: empty_model_response

**查询命令**:
```bash
ssh root@47.97.111.154 -p 25022
journalctl -u llm-gateway-go.service --since "1 hour ago" | \
  grep -A 5 "fold_candidate_outcomes_complete" | \
  grep -E "empty_response|decision_action"
```

**检查点**:
- `kind` 应该是 `empty_response`
- `decision_action` 应该是 `retry_now`

## 后续观察计划

### 第1天（今天）
- [x] 部署完成
- [ ] 观察是否有新日志产生
- [ ] 收集至少5个错误案例的完整日志

### 第2-3天
- [ ] 分析收集的日志，确认错误分类是否正确
- [ ] 检查重试逻辑是否按预期执行
- [ ] 确认节点切换是否发生

### 第4-7天
- [ ] 统计错误率变化
- [ ] 识别需要进一步优化的错误类型
- [ ] 基于数据调整特定模型配置（如Minimax-m3的holdback window）

## 潜在问题和缓解

### 日志量增加
- **影响**: 结构化日志会增加I/O
- **缓解**: 
  - Info级别仅在关键决策点记录
  - Debug级别默认关闭
  - 已配置journald轮转

### 性能影响
- **影响**: 序列化结构化日志有轻微开销
- **缓解**: 
  - 日志字段经过优化
  - 避免大对象序列化
  - 预期影响 < 1ms per request

## 回滚方案

如果发现问题，执行：
```bash
cd /Users/xutaohuang/workspace/ai-native-tools/syncfield/llm-gateway-go-4
bash scripts/deploy-seamless.sh rollback 154
```

回滚到版本: 1917-7189ccac

## 下一步

1. **监控日志**（24-48小时）
   - 观察新日志是否按预期产生
   - 收集真实的错误案例

2. **数据分析**
   - 统计各类错误的出现频率
   - 分析重试成功率
   - 识别问题节点

3. **针对性优化**
   - 基于数据调整Minimax-m3配置
   - 优化"Our servers are currently overloaded"的处理
   - 改进empty_response的检测逻辑

4. **文档更新**
   - 根据实际观察更新排查手册
   - 补充常见错误案例和解决方案

## 相关文档

- [错误分析与修复方案](./error-analysis-and-fixes.md)
- [错误处理改进实施记录](./error-handling-improvements.md)
- 变更的文件:
  - `domains/streaming/attempt_outcome.go`
  - `domains/streaming/execute_attempt.go`

## 联系人

- 实施者: AI Assistant
- 审核者: 待定
- 部署日期: 2026-09-04
