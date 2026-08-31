# Agent 4: 错误处理/可观测性审计报告

**审计日期**: 2026-08-31  
**审计范围**: LLM Gateway错误处理、供应商质量度量与可观测性  
**审计基准**: 最近24-48小时提交 (commit f1ae3c71e, d558ec334, f13c47ce7等)

---

## 审计范围

### 审计文件清单
- **错误分类**: `errorsx/classify.go` (1351行，42种ErrorKind)
- **供应商错误聚合**: `bg/provider_error_aggregator.go` (283行)
- **请求链路追踪**: `domains/requestjourney/retention.go` (155行)
- **观测sink**: `cmd/gateway/main_dispatch_observation.go` (446行)
- **供应商诊断**: `admin/provider_diagnose.go` (545行)
- **调度错误处理**: `domains/streaming/executors/executor_dispatch.go`
- **候选失败日志**: `domains/routing/candidate_failure_logger.go` (243行)
- **凭据状态写入**: `domains/credential/writer.go` (300+行)
- **熔断器**: `domains/credential/breaker.go` (200+行)
- **错误恢复投影**: `errorsx/recovery_projection.go` (52行)
- **Failover逻辑**: `domains/dispatch/failover.go` (250+行)

### 审计维度
1. **供应商错误处理闭环**: 错误分类 → 状态记录 → 聚合统计 → 恢复探测
2. **错误备用方案**: 路由重试 → 凭据切换 → 模型降级 → 兜底失败
3. **可观测性数据完整性**: trace事件 → snapshot → journal → 持久化
4. **供应商质量度量**: 错误统计 → 健康评分 → SLA监控

---

## 审计发现

### P0级问题（立即修复）

#### P0-1: ✅ 已修复 - 错误聚合的租户隔离问题
- **位置**: `bg/provider_error_aggregator.go:113-121`
- **状态**: 已通过事务级`app.bypass_rls`修复
- **验证**: 聚合器使用事务级别的RLS旁路，不污染连接池
```go
for _, statement := range []string{
    "SELECT set_config('app.current_role', 'super_admin', true)",
    "SELECT set_config('app.bypass_rls', 'true', true)",
} {
    if _, err := tx.Exec(timeoutCtx, statement); err != nil {
        return
    }
}
```
- **闭环确认**: 配置是事务级(`true`参数)，pooled连接不会保留提升的可见性

#### P0-2: ✅ 已修复 - Journal Snapshot幂等性与防重
- **位置**: `cmd/gateway/main_dispatch_observation.go:185-206`
- **状态**: 已实现数据库receipt机制 + 本地内存去重
- **验证**: 
  - 数据库层: `JournalSnapshotReceiptStore.ClaimWithProjectionBase` (基于hash的幂等性)
  - 内存层: `journalSnapshotReceiptKey` map去重
  - 容量控制: `journalSnapshotReceiptCapacity = 10000`，自动LRU淘汰
- **闭环确认**: 双层防御，数据库租约跨进程，内存去重跨goroutine

#### P0-3: ⚠️ 需关注 - 错误聚合watermark单调性保证
- **位置**: `bg/provider_error_aggregator.go:233-240`
- **当前实现**:
```sql
UPDATE provider_error_aggregator_state
SET last_source_id = COALESCE(
      (SELECT MAX(aggregation_id) FROM new_source_rows),
      provider_error_aggregator_state.last_source_id),
    updated_at = NOW()
WHERE id = 1
```
- **分析**: 
  - ✅ 使用`COALESCE`确保watermark只增不减
  - ✅ 事务内`FOR UPDATE`锁保证单worker写入
  - ✅ `aggregation_id`为单调递增序列（migration 622/627引入）
- **建议**: 当前实现正确，无需修复

---

### P1级问题（下一迭代）

#### P1-1: 错误恢复时间精度丢失
- **位置**: `errorsx/classify.go:1060-1108` (`scanQuotaResetTimestamp`)
- **问题**: 
  - 旧实现使用固定窗口长度，短于layout的时间戳会被跳过
  - 例如`2026-08-10T12:34:56.789Z` (24 chars)无法匹配`time.RFC3339Nano` (35 chars)
- **状态**: ✅ 已修复 (2026-08-22 audit round-2 §5.3)
- **修复方案**: 先用regex提取候选时间戳，再逐个layout尝试解析
```go
for _, m := range timestampCandidateRe.FindAllString(detail, -1) {
    for _, layout := range []string{time.RFC3339Nano, time.RFC3339, ...} {
        t, err := time.Parse(layout, m)
        if err == nil && !t.Before(now.Add(-1*time.Minute)) {
            return t.UTC(), true
        }
    }
}
```
- **影响**: 配额恢复时间可精确到毫秒级，避免5h/24h窗口的过早/过晚恢复

#### P1-2: Circuit Breaker的probe slot泄漏防御
- **位置**: `domains/credential/breaker.go:82`
- **当前防御**: `halfOpenProbeTimeout = 5 * time.Minute`
- **问题**: probe被limiter阻塞时，5分钟超时可能过长
- **建议**: 
  1. 在limiter/fpslot层记录probe标识，拒绝时立即释放probe slot
  2. 或缩短超时到2分钟（平衡probe完整性与恢复速度）
- **优先级**: P1（当前5分钟仍可自愈，但影响恢复速度）

#### P1-3: 供应商诊断的错误分类不一致
- **位置**: `admin/provider_diagnose.go:242-252`
- **问题**: 错误分类逻辑使用字符串匹配而非`errorsx.ClassifyError`
```go
switch {
case strings.Contains(kind, "auth") || strings.Contains(kind, "401") || strings.Contains(kind, "403"):
    ec.AuthErrors += cnt
case strings.Contains(kind, "rate") || strings.Contains(kind, "429"):
    ec.RateLimitErrors += cnt
// ...
}
```
- **风险**: 
  - `KindQuotaPeriodic`/`KindQuotaPermanent`被归入`OtherErrors`而非专门的quota bucket
  - `KindUpstreamOverloaded`与`KindUpstreamDown`无法区分
- **建议**: 
  1. 引入`errorsx.ClassifyErrorCategory(kind ErrorKind) string`统一分类
  2. 或增加quota/circuit专属bucket
- **优先级**: P1（影响UI展示准确性，不影响路由决策）

---

### P2级问题（技术债）

#### P2-1: 候选失败日志的body截断策略
- **位置**: `domains/routing/candidate_failure_logger.go:178-189`
- **当前策略**: 
  - 全量body截断到1KB
  - preview截断到320 chars
- **潜在问题**: 
  - JSON body可能在中间被截断，导致非法JSON
  - 320 chars可能不足以包含完整错误message
- **建议**: 
  1. 按JSON字段边界截断（提取`error.message`/`error.code`）
  2. 或增加preview到512 chars
- **优先级**: P2（已足够诊断，优化体验）

#### P2-2: Journal Snapshot清理的TTL硬编码
- **位置**: `cmd/gateway/main_dispatch_observation.go:138`
- **当前实现**: `adapter.startCleanup(24 * time.Hour)`
- **问题**: TTL硬编码，无法根据流量动态调整
- **建议**: 
  1. 从配置读取TTL (默认24h)
  2. 或根据内存压力自适应调整
- **优先级**: P2（当前10000容量足够，极端流量下可能需要调整）

#### P2-3: 错误聚合的10分钟bucket对齐
- **位置**: `bg/provider_error_aggregator.go:164-165`
```sql
(date_trunc('hour', c.ts) +
 floor(extract(minute FROM c.ts) / 10) * interval '10 minutes') AS aggregation_bucket
```
- **当前行为**: 按小时对齐后取10分钟桶（00/10/20/30/40/50）
- **潜在问题**: 边界误差可能导致同一错误跨越两个bucket
- **建议**: 考虑使用`date_bin('10 minutes', c.ts, '2020-01-01'::timestamptz)`对齐到epoch
- **优先级**: P2（当前逻辑已稳定运行，优化一致性）

---

## 闭环验证

### ✅ 数据闭环: 通过

#### 错误记录 → 聚合 → 展示
1. **记录层**: `candidate_failure_logs_hot` 由 `CandidateFailureWriter.LogFailure` 写入
   - 包含: request_id, credential_id, error_kind, upstream_status_code, body preview
   - 触发点: `executor.go` 每次候选失败后立即调用

2. **聚合层**: `provider_error_aggregator` 每10分钟聚合
   - 输入: `candidate_failure_logs_unified` (hot + historical 分区视图)
   - 输出: `provider_error_details` (10分钟bucket聚合)
   - Watermark: `aggregation_id` 单调递增，防止重复聚合

3. **展示层**: `admin/provider_diagnose.go` 查询
   - 数据源: `request_logs_hot` (24h窗口) + `provider_error_details`
   - 分类: AuthErrors, RateLimitErrors, TimeoutErrors, ModelNotFoundErrors, OtherErrors
   - 健康评分: circuit_state, health_status, consecutive_failures综合计算

**验证结论**: 闭环完整，watermark机制保证exactly-once聚合

---

### ✅ 流程闭环: 通过

#### 错误 → 熔断 → 恢复 → 探测
```
[1] 上游失败
    ↓
[2] errorsx.ClassifyError → ErrorKind
    ↓
[3] Breaker.RecordFailure(kind)
    ├─ 2次失败 → OPEN (cooling)
    ├─ cooling期到 → HALF_OPEN (probe)
    └─ probe成功 → CLOSED
    ↓
[4] Writer.WriteOnError(credentialID, model, failure)
    ├─ KindAuth → availability_state='auth_failed', recover_at=now+15min
    ├─ KindQuotaPeriodic → quota_state='periodic_exhausted', recover_at=inferred
    ├─ KindQuotaPermanent → quota_state='permanently_exhausted', recover_at=NULL
    └─ Transient/Network → per-model binding unavailable
    ↓
[5] Dispatch Failover
    ├─ NextActionRetrySameCred (同凭据重试)
    ├─ NextActionSwitchCred (切换凭据)
    ├─ NextActionSwitchModel (切换模型)
    └─ NextActionFailed (exhaustion)
    ↓
[6] bg/credential_recovery.go (60s tick)
    ├─ availability_recover_at < now → flip to 'ready'
    └─ quota_recover_at < now → reset quota_state
    ↓
[7] bg/active_probe_executor.go
    └─ 定期探测 auth_failed/unreachable 凭据
```

**验证结论**: 流程闭环完整，每个状态都有明确的恢复路径

---

### ✅ 反馈闭环: 通过

#### 可观测性 → 运维决策
1. **实时反馈**: 
   - `liveactions.ActionEvent` (WebSocket推送)
   - `DispatchNotice` (think模式SSE通知)
   - 用户可见错误kind + 重试/切换原因

2. **历史分析**:
   - `request_state_transitions` (journey事件序列，7天保留)
   - `journal_snapshot_receipts` (attempt trace，7天保留)
   - `provider_error_details` (10分钟聚合，永久保留)

3. **运维接口**:
   - `GET /admin/providers/:id/diagnose` (深度健康检查)
   - `provider_error_details` 表可直接SQL查询
   - `candidate_failure_logs_hot` 可追溯单个request的完整failover链

**验证结论**: 三层反馈体系完整，从实时到历史全覆盖

---

## 错误分类完整性分析

### 42种ErrorKind覆盖度
| 分类 | Kind | 路由行为 | 凭据影响 | 恢复策略 |
|------|------|----------|----------|----------|
| **可重试** | Transient, Timeout, Network, UpstreamDown, UpstreamOverloaded, Concurrent, StreamTimeout | IsRetryable=true, 继续尝试 | 无 | 自动恢复 |
| **凭据致命** | Auth, AuthRevoked, Quota, QuotaBalance, QuotaPeriodic, QuotaPermanent | IsRetryable=false, 立即切换 | 标记cooling/suspended | 探测恢复 |
| **客户端错误** | ClientBug, ToolCallIdMismatch, UnsupportedFeature, Canceled | IsClientBug=true, 短路不重试 | 无 | N/A |
| **内容策略** | ContentFilter | 短路不重试 | 无 | 用户修改prompt |
| **上下文长度** | ContextLength | trim一次后重试 | 无 | 客户端缩减 |
| **模型相关** | ModelNotFound, ModelDeprecated | 切换模型/凭据 | per-model标记 | 管理员配置 |
| **特殊failover** | EmptyResponse, NoAvailableChannel | CandidateFailover=true | 软降级 | 透明切换 |
| **准入控制** | CircuitOpen, FpSlotSaturated | 网关侧拒绝 | 无 | 容量恢复 |
| **数据完整性** | UpstreamContextLoss, Conversion | 记录质量问题 | 软降级 | 供应商修复 |

**覆盖度评估**: ✅ 完整
- 上游HTTP错误: 4xx/5xx全覆盖
- 网络层错误: timeout/connection/EOF
- 业务层错误: quota/auth/content-filter
- 网关层错误: circuit/limiter/fpslot

---

## Failover完整性分析

### 5级Failover梯度

```
Level 1: Same-Cred Retry (同凭据重试)
├─ 触发条件: !IsCredentialFatal(kind) && qr.CredRetryCount < budget
├─ 冷却策略: NextRetryDelay(retryCount, out.RetryAfter)
├─ 失败兜底: → Level 2
└─ 观测: ObservationRetryScheduled + NoticeKindRetry

Level 2: Switch-Cred (同模型切换凭据)
├─ 触发条件: 当前凭据exhausted，sibling credentials available
├─ 选择策略: Router.PlanCandidatesPinned (bandit排序)
├─ 失败兜底: → Level 3
└─ 观测: ObservationNodeSwitched + NoticeKindNodeSwitch

Level 3: Cross-Provider Switch (跨供应商切换)
├─ 触发条件: Level 2无可用凭据 && policy允许跨provider
├─ 选择策略: 同Level 2，但不限provider_id
├─ 失败兜底: → Level 4
└─ 观测: 同Level 2

Level 4: Model-Change (模型降级)
├─ 触发条件: 当前模型exhausted && DispatchAllowModelChange=true
├─ 选择策略: dispatchModelRecommender.RecommendModelAlternatives
├─ 失败兜底: → Level 5
└─ 观测: ObservationModelSwitched

Level 5: Terminal (终止)
├─ 触发条件: 所有组合exhausted || attempt_cap reached
├─ 返回错误: AggregateExhaustionError || 最后上游错误
└─ 观测: ObservationRequestFailed
```

**代码映射**:
- Level 1: `dispatch/failover.go:58-85` (`NextActionRetrySameCred`)
- Level 2-3: `dispatch/failover.go:92-169` (`NextActionSwitchCred`, `PlanSwitchCred`)
- Level 4: `dispatch/failover.go:172-175` + `planner.go` (`tryModelChangeOutcome`)
- Level 5: `dispatch/planner.go` (`NextActionFailed`, `terminateOnAttemptCap`)

**验证结论**: ✅ 5级梯度完整，每级都有明确的失败兜底

---

## 可观测性数据流分析

### 数据流图
```
[Executor.Execute]
    ├─ ObservationSink.ObserveDispatch()
    │   └─ requestjourney.Recorder.Apply(JourneyEvent)
    │       └─ request_state_transitions (seq单调)
    │
    ├─ JournalSink.ApplyJournalSnapshot()
    │   ├─ JournalSnapshotReceiptStore.ClaimWithProjectionBase (幂等)
    │   ├─ JournalSnapshotStore.Store (可选文件存储)
    │   └─ requestjourney.Recorder.Apply(journal entries → JourneyEvent)
    │
    ├─ CandidateFailureWriter.LogFailure()
    │   └─ candidate_failure_logs_hot
    │       └─ [10min tick] provider_error_aggregator
    │           └─ provider_error_details
    │
    └─ Writer.WriteOnError()
        └─ credentials (availability_state, quota_state)
            └─ credential_model_bindings (per-model unavailable)
```

### Observation采集点完整性
| 事件类型 | 采集点 | 字段 | 持久化 |
|---------|--------|------|--------|
| RequestReceived | executor入口 | tenant_id, request_id, model | ✅ |
| CredentialSelected | Router.Plan | credential_id, provider_id | ✅ |
| UpstreamRequest | beginUpstreamAttempt | attempt_ref, latency | ✅ |
| RetryScheduled | failover.scheduleSameCredRetry | retry_at, retry_reason | ✅ |
| NodeSwitched | failover.move (L2/L3) | from_cred → to_cred | ✅ |
| ModelSwitched | tryModelChangeOutcome | from_model → to_model | ✅ |
| RequestSucceeded | terminal成功 | outcome=success | ✅ |
| RequestFailed | terminal失败 | error_kind, http_status | ✅ |
| ObservationDegraded | journal未知action | degraded标记 | ✅ |

**缺失观测**: 无（所有关键决策点都有对应observation）

---

## 供应商质量度量体系

### 1. 实时指标 (内存)
- **Circuit Breaker状态**: closed/open/half_open/quarantined
- **Consecutive Failures**: 连续失败次数
- **Cooling Until**: 冷却期结束时间
- **Health Status**: healthy/warning/degraded/unknown

### 2. 短期指标 (24h窗口, request_logs_hot)
- **Error Classification**: auth/rate_limit/timeout/model_not_found/other
- **Success Rate**: 成功请求 / 总请求
- **Avg Latency**: 平均响应延迟
- **P95/P99 Latency**: 分位数延迟

### 3. 中期指标 (7天窗口, candidate_failure_logs)
- **Per-Model Failure Rate**: 每个模型的失败率
- **Error Distribution**: 各error_kind的分布
- **Retry Count Distribution**: 重试次数分布
- **Failover Chain Length**: 平均failover链长度

### 4. 长期指标 (永久, provider_error_details)
- **10-Min Bucket Aggregation**: 时序错误趋势
- **Error Pattern Detection**: 周期性错误检测
- **SLA Compliance**: 可用性SLA计算
- **Quarterly Report**: 季度质量报告

### Health Score计算公式
```go
// admin/provider_diagnose.go:518-531
score := 100.0
if cd.CircuitState != "closed" {
    score -= 30  // 熔断中严重降级
}
if cd.HealthStatus != "healthy" {
    score -= 20  // 健康状态异常
}
if cd.ConsecutiveFailures > 0 {
    score -= 10  // 有失败记录
    score -= math.Min(20, float64(cd.ConsecutiveFailures)*5)  // 连续失败累加惩罚
}
score = math.Max(0, math.Min(100, score))
```

**评估**: ✅ 四层指标体系完整，从实时到长期全覆盖

---

## 错误处理的边界案例审计

### Case 1: 供应商中继节点余额耗尽
**场景**: apigpt/apiclaude.cc节点余额耗尽，返回"节点费用已用完"

**修复历史**:
- 2026-08-20 P0 fix: `budgetExceededRe` 增加中文"费用用完"模式
- `concurrentOverloadRe` 移除"quota exhausted"避免冲突
- `ClassifyErrorWithBody` CJK检查前置

**当前行为**:
1. 错误分类: `KindQuotaPermanent`
2. Failover动作: 立即ejected，切换到sibling节点
3. 凭据状态: `quota_state='permanently_exhausted', recover_at=NULL`
4. 恢复路径: 需管理员充值后手动reset或探测恢复

**验证**: ✅ 正确

---

### Case 2: 5小时配额窗口重置
**场景**: 智谱AI GLM Coding Plan 5h窗口限额，"usage limit exceeded ... every 5 hours"

**修复历史**:
- 2026-08-18 fix: `quotaResetsRe` 增加5小时窗口模式
- `NextQuotaReset` 增加`nextFiveHourBoundaryUTC8`对齐UTC+8时区

**当前行为**:
1. 错误分类: `KindQuotaPeriodic` (有reset信号)
2. 恢复时间: 下一个UTC+8的0/5/10/15/20点
3. 凭据状态: `quota_state='periodic_exhausted', recover_at=计算时间`
4. 恢复路径: `credential_recovery` 60s tick自动翻回'ready'

**验证**: ✅ 正确，时区对齐避免了过早/过晚恢复

---

### Case 3: OneAPI分发器无可用通道
**场景**: OneAPI relay返回"No available channel for model X under group Y"，HTTP 503

**修复历史**:
- 2026-08-09: 引入`KindNoAvailableChannel`
- `noAvailableChannelRe` 匹配"for model"限定词
- 分类器前置到5xx检查，避免被concurrent/model_not_found劫持

**当前行为**:
1. 错误分类: `KindNoAvailableChannel`
2. 路由行为: `CandidateFailover=true`, 不进入`IsRetryable`
3. Failover动作: 透明切换到sibling凭据（可能不同分发器或直连）
4. 凭据状态: 不写cooling状态（transient failure）

**验证**: ✅ 正确，避免了旧版的同凭据重试循环

---

### Case 4: Tool Call ID不匹配
**场景**: 客户端框架丢失tool_call_id，MiniMax返回code 2013

**修复历史**:
- 2026-07-11: 引入`KindToolCallIdMismatch`
- `toolCallIdMismatchRe` 匹配MiniMax/Anthropic/OpenAI变体
- 分类为`IsClientBug=true`

**当前行为**:
1. 错误分类: `KindToolCallIdMismatch`
2. 路由行为: 短路，不重试其他凭据
3. 凭据状态: 不写状态（客户端错误）
4. 用户反馈: 返回400 + 原始upstream错误

**验证**: ✅ 正确，避免了对健康凭据的误惩罚

---

### Case 5: NVIDIA NIM空响应
**场景**: NIM返回HTTP 200，stream发送1-3个empty chunks，0 completion_tokens

**修复历史**:
- 引入`KindEmptyResponse`
- `ProjectRecovery`: `CandidateFailover=true, TransparentResume=true`
- 不在`IsCredentialFatal`中（允许重试），不在`freeCredentialsTolerateTransient`（触发circuit降级）

**当前行为**:
1. 错误分类: `KindEmptyResponse`
2. 路由行为: 触发candidate failover（content-gate返回Resumable=true）
3. 凭据状态: circuit记录失败（降低recent_success_rate），但不标记unavailable
4. 恢复路径: 后续请求可再次尝试，多次失败后自然降级

**验证**: ✅ 正确，平衡了transient tolerance与质量降级

---

## 建议的修复方案

### 短期修复 (P1)

#### 1. 统一错误分类API
**目标**: 消除`admin/provider_diagnose.go`的字符串匹配分类

**方案**:
```go
// errorsx/classify.go
func ClassifyErrorCategory(kind ErrorKind) string {
    switch kind {
    case KindAuth, KindAuthRevoked:
        return "auth"
    case KindRateLimit, KindConcurrent:
        return "rate_limit"
    case KindQuota, KindQuotaBalance, KindQuotaPeriodic, KindQuotaPermanent:
        return "quota"
    case KindTimeout, KindNetwork, KindStreamTimeout:
        return "timeout"
    case KindModelNotFound, KindModelDeprecated:
        return "model"
    case KindCircuitOpen:
        return "circuit"
    default:
        return "other"
    }
}
```

**改动文件**: `errorsx/classify.go`, `admin/provider_diagnose.go:242-252`

---

#### 2. Circuit Breaker Probe释放优化
**目标**: 减少probe slot泄漏导致的恢复延迟

**方案A** (推荐): Limiter层感知probe
```go
// domains/credential/limiter.go (假设)
func (l *Limiter) TryAcquire(isProbe bool) bool {
    if !l.acquire() {
        if isProbe {
            // 立即通知breaker释放probe slot
            breaker.ReleaseProbe()
        }
        return false
    }
    return true
}
```

**方案B**: 缩短超时
```go
// domains/credential/breaker.go:82
const halfOpenProbeTimeout = 2 * time.Minute  // 从5分钟改为2分钟
```

**改动文件**: `domains/credential/breaker.go`, `domains/credential/limiter.go`

---

### 中期优化 (P2)

#### 3. Journal Snapshot清理配置化
**目标**: 允许根据流量动态调整TTL

**方案**:
```go
// settings/spec.go
type DispatchV2Settings struct {
    // ...
    JournalSnapshotReceiptTTL time.Duration `json:"journal_snapshot_receipt_ttl"`
}

// cmd/gateway/main_dispatch_observation.go
ttl := dispatchSettings.JournalSnapshotReceiptTTL
if ttl <= 0 {
    ttl = 24 * time.Hour
}
adapter.startCleanup(ttl)
```

**改动文件**: `settings/spec.go`, `cmd/gateway/main_dispatch_observation.go`

---

#### 4. 候选失败日志JSON截断优化
**目标**: 避免截断破坏JSON结构

**方案**:
```go
// domains/routing/candidate_failure_logger.go
func trimBodyJSON(body string, maxLen int) string {
    if len(body) <= maxLen {
        return body
    }
    // 尝试提取error字段
    var parsed struct {
        Error struct {
            Message string `json:"message"`
            Code    string `json:"code"`
            Type    string `json:"type"`
        } `json:"error"`
    }
    if json.Unmarshal([]byte(body), &parsed) == nil {
        compact := fmt.Sprintf(`{"error":{"message":%q,"code":%q,"type":%q}}`,
            parsed.Error.Message, parsed.Error.Code, parsed.Error.Type)
        if len(compact) <= maxLen {
            return compact
        }
    }
    // 回退：按字符截断
    return body[:maxLen]
}
```

**改动文件**: `domains/routing/candidate_failure_logger.go:178-189`

---

## 总结

### 整体评估: ✅ 优秀

LLM Gateway的错误处理与可观测性架构经过2年迭代，已达到生产级成熟度：

**优势**:
1. ✅ **错误分类完整**: 42种ErrorKind覆盖所有上游/网络/业务场景
2. ✅ **Failover梯度清晰**: 5级failover (retry → switch-cred → cross-provider → model-change → terminal)
3. ✅ **状态闭环严密**: 错误记录 → 聚合 → 展示 → 恢复全链路可追溯
4. ✅ **可观测性丰富**: 实时(liveactions) + 短期(hot表) + 中期(7天) + 长期(聚合)四层体系
5. ✅ **幂等性保证**: Journal snapshot双层去重（DB租约 + 内存map）
6. ✅ **租户隔离**: 事务级RLS旁路，不污染连接池

**近期修复亮点**:
- 2026-08-20: apigpt节点余额耗尽分类修复（P0）
- 2026-08-22: 配额恢复时间精度修复（P1）
- 2026-08-28: Journal snapshot幂等性机制（P0）
- 2026-08-30: Circuit breaker防泄漏超时（P1）

**遗留技术债**:
- P1-3: 供应商诊断错误分类不一致（UI展示）
- P2-1: 候选失败日志JSON截断（诊断体验）
- P2-2: Journal清理TTL硬编码（配置灵活性）

### 风险评估: 低
- 无P0级未修复问题
- P1级问题均有workaround，不影响核心功能
- P2级问题为体验优化，无功能影响

### 建议优先级
1. **立即执行**: 无（所有P0已修复）
2. **下一迭代** (1-2周):
   - P1-1: 统一错误分类API
   - P1-2: Circuit probe释放优化
3. **技术债清理** (1-2月):
   - P2级优化按需排期

---

**审计结论**: LLM Gateway错误处理与可观测性架构设计合理，实现完整，闭环严密。近期修复已覆盖所有P0级问题，可继续稳定运行。建议按优先级逐步优化P1/P2级问题，进一步提升诊断体验与运维效率。

---

**审计人**: Agent 4  
**审计完成时间**: 2026-08-31 23:59 UTC  
**下次审计建议**: 2026-09-30 (月度审计)
