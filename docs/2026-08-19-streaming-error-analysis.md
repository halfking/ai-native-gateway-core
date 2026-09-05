# Streaming Error Analysis - glm-5.2 & minimax-m3

## 问题描述

在154服务器上,glm-5.2和minimax-m3频繁出现以下错误:

1. **empty_model_response**: `provider=ef7bed64-de6f-42d8-86f2-eab4b62d9812 model=glm-5.2 reason=empty_model_response`
2. **gateway_survival_resume_blocked**: `provider_code=gateway_survival_resume_blocked model=minimax-m3 request=499a5fb6-bdba-43bc-9b78-729c75cd5592 reason=unknown retryable=false`
3. **gateway request survival ended**: `committed_output`
4. **Partial assistant output was discarded before a streaming retry**

## 错误流程分析

### 1. empty_model_response 流程

#### 触发位置
- **domains/streaming/stream.go**:169, 290
  - 当upstream返回的stream在第一个chunk就是`[DONE]`
  - 或者整个stream没有任何实际内容(sawContent=false)

```go
// stream.go:167-175
if startingPayload == "[DONE]" {
    if capture != nil {
        capture.MarkInterruptedWithReason("empty_stream_no_content")
    }
    return nil, &StreamOutcome{
        Interrupted: true,
        Reason:      "empty_stream_no_content",
        Resumable:   true,
    }
}
```

#### 分类处理
- **domains/streaming/executors/executor_chat.go**:1148
  - `streamOutcome.Reason == "empty_stream_no_content"` 被分类为 `errorsx.KindEmptyResponse`
  - 标记为 `Resumable: true, ChunkCount: 0`

#### 重试机制
- **attempt_outcome.go**:149
  - `errorsx.KindEmptyResponse` 映射到 `TaskActionRetryNow`
  - 这意味着应该立即重试,切换到下一个候选credential

### 2. gateway_survival_resume_blocked 流程

#### 触发条件
- **survival_coordinator.go**:235-240
  - 当 `committed && (hasRetry || hasWait)` 时触发
  - 即: 已经向客户端输出了语义内容,但遇到可恢复错误

```go
case committed && (hasRetry || hasWait):
    // Recoverable failure, but client-visible output was already
    // committed — a transparent restart would duplicate it.
    return TaskDecision{
        Action: TaskActionResumeBlocked,
        Reason: "committed_output",
    }
```

#### CommitState判断
- **attempt_commit_gate.go**
  - `CommitStateContent` 及以上状态表示已经向客户端发送了语义内容
  - 一旦committed,就不能安全地重试,因为会重复输出

### 3. 问题根因分析

#### 空响应问题 (empty_model_response)

**现象**: upstream返回HTTP 200 + 正常stream开头,但没有实际content

**可能原因**:
1. **upstream模型过载**: 接受请求但实际未生成内容
2. **upstream模型bug**: 某些prompt导致模型返回空输出
3. **网络问题**: stream中断在content到达前
4. **credential配额耗尽**: 虽然接受请求但不生成内容

**当前处理**:
- ✅ 正确分类为 `KindEmptyResponse`
- ✅ 标记为 `Resumable: true`
- ✅ 应该触发failover到下一个credential
- ❌ **可能的问题**: 如果所有candidates都返回empty,最终会失败

#### Resume Blocked问题 (committed_output)

**现象**: 已经向客户端发送了部分内容,然后遇到可恢复错误

**流程**:
1. Upstream开始streaming
2. Gateway通过commit gate向客户端发送了第一批chunks
3. Upstream突然中断(empty response / timeout / network error)
4. Survival coordinator判断: 可恢复 + 已committed = resume_blocked
5. 无法透明重试,返回错误给客户端

**关键问题**:
- Commit gate的阈值可能设置不当
- 应该在确认upstream稳定输出后再commit
- 当前commit可能太早

### 4. 日志缺失问题

根据commit eee0511f5,已经增加了以下日志:

✅ **已添加的日志**:
- `survival_attempt_outcome`: 每次attempt的结果
- `survival_attempt_discarded`: 丢弃uncommitted buffer时
- `survival_resume_blocked`: resume被阻止时
- `request_survival_finished`: survival完成时
- `survival_recovery_action`: recovery决策时
- `executor: stream interrupted`: stream中断时
- `executor: non-stream empty response`: 非stream空响应时

❓ **可能缺失的日志**:
- Commit gate状态转换时的日志
- 详细的buffer内容统计(何时开始积累,何时committed)
- 每个candidate尝试前的状态快照

## 修复方案

### Phase 1: 增强日志 (立即实施)

1. **Commit Gate状态转换日志**
   - 在 `attempt_commit_gate.go` 的 `Commit()` 方法中添加日志
   - 记录: request_id, buffer_bytes, holdback_count, gate_state

2. **Candidate选择前置日志**
   - 在executor开始尝试每个candidate前记录
   - 记录: request_id, candidate_index, total_candidates, provider_id, credential_id, raw_model

3. **Empty Response详细诊断**
   - 在stream.go的empty detection处添加更详细的上下文
   - 记录: upstream_headers, first_chunk_content, chunk_count_before_empty

### Phase 2: 调整Commit Gate策略 (需要测试)

1. **增加Commit Holdback窗口**
   - 当前可能在第一个chunk就commit
   - 建议: 至少等待2-3个有效content chunks才commit
   - 参数: `GateOptions.MinChunksBeforeCommit`

2. **Empty Response早期检测**
   - 在commit gate内部检测empty pattern
   - 如果检测到upstream可能返回empty,延迟commit

### Phase 3: 优化Failover策略 (需要评估)

1. **Empty Response快速failover**
   - 当前: 等待stream完成才知道是empty
   - 优化: 检测到empty pattern立即切换

2. **Provider级别的Empty Rate监控**
   - 记录每个provider的empty response rate
   - 自动降低高empty-rate provider的优先级

## 实施计划

### Step 1: 立即增强日志 (今天)
- [ ] 添加commit gate状态转换日志
- [ ] 添加candidate选择前置日志
- [ ] 添加empty response详细诊断日志

### Step 2: 本地测试 (今天)
- [ ] 使用模拟empty response测试
- [ ] 验证日志完整性
- [ ] 验证failover路径

### Step 3: 部署到154验证 (今天)
- [ ] 构建新版本
- [ ] 部署到154
- [ ] 监控日志输出
- [ ] 收集真实故障案例

### Step 4: 分析并优化 (明天)
- [ ] 分析收集的日志
- [ ] 确定commit gate策略调整
- [ ] 实施优化方案

## 预期效果

1. **完整的请求流程可见性**: 从candidate选择 → commit决策 → empty检测 → failover → 最终结果
2. **快速故障定位**: 通过request_id grep完整重现失败路径
3. **优化failover效率**: 减少empty response导致的用户可见错误
