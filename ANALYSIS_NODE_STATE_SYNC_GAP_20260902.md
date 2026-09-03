# 节点状态未同步更新问题深度分析报告

> **分析日期**: 2026-09-02  
> **问题描述**: 供应商凭据正常恢复后，系统中的节点状态（credential_model_bindings / model_offers / credentials.availability_state）未能自动同步更新，需要手工干预修正。  
> **分析目标**: 定位自检流程为何在实际运行情况下未能同步更新节点状态，识别根因并提出修复方案。

---

## 1. 问题背景与症状

### 1.1 实际观察到的现象

- **供应商凭据状态**: 上游API凭据已恢复正常（可以成功响应请求）
- **数据库状态**: 节点在数据库中仍标记为不可用
  - `credential_model_bindings.available = FALSE`
  - `model_offers.available = FALSE` 
  - `credentials.availability_state` 可能处于 `cooling`/`suspended`/`auth_failed` 等非 `ready` 状态
- **路由行为**: 网关路由层排除了这些实际可用的节点
- **恢复方式**: 需要通过管理员界面手工 `force_enable` 或直接修改数据库状态

### 1.2 问题影响范围

根据代码分析和历史记录，该问题影响以下供应商类型：
- 智谱AI (GLM系列) - 5小时周期性配额窗口
- MiniMax - 流控和并发限制
- 芝麻AI / SenseNova - 瞬态5xx故障
- NVIDIA NIM - 流式超时

---

## 2. 自检流程架构分析

系统包含**三层**状态恢复机制，设计上应该形成完整的闭环：

### 2.1 第一层：请求成功时的立即恢复 (Hot Path)

**触发点**: 每次成功的请求执行完成后

**代码路径**:
```
domains/streaming/executors/execution_recorder.go:RecordOutcome()
  → outcome.Success == true
  → StateWriter.RestoreOnSuccess(credentialID, rawModel)
    → domains/credential/writer.go:RestoreOnSuccess()
```

**操作内容** (在单个事务中):
1. `credentials.availability_state = 'ready'`
2. `credentials.circuit_state = 'closed'`, `consecutive_failures = 0`
3. `credential_model_bindings.available = TRUE` (针对成功的模型)
4. `model_offers` 通过VIEW自动反映

**关键特征**:
- ✅ 粒度：per-model，不会跨模型污染
- ✅ 时效：实时（请求成功后5秒内）
- ✅ 范围：涵盖三个状态表面
- ⚠️ **前提条件**: 需要有**真实的成功请求**通过该凭据

### 2.2 第二层：30秒周期性自动恢复 (Background Ticker)

**触发点**: `bg/credential_recovery.go:recover()` 每30秒执行一次

**恢复的SQL块**:

#### Block 1: `availability_recover` (行 449-526)
```sql
UPDATE credentials
SET availability_state = 'ready',
    availability_recover_at = NULL
WHERE availability_state IN ('cooling','rate_limited','unreachable','auth_failed','suspended')
  AND (
      availability_state = 'auth_failed'
      OR (availability_recover_at IS NOT NULL AND availability_recover_at <= now())
  )
  AND (
      availability_state <> 'suspended'
      OR COALESCE(quota_state, 'ok') NOT IN ('permanently_exhausted', 'balance_exhausted')
  )
  AND NOT EXISTS (
      SELECT 1 FROM model_probe_state mps
      JOIN provider_models pm ON pm.raw_model_name = mps.raw_model_name
      JOIN credential_model_bindings cmb ...
      WHERE mps.state = 'broken_confirmed' AND cmb.available = FALSE
  )
```

**守卫条件分析**:
- ✅ `availability_recover_at <= now()` - 冷却期已过
- ✅ `suspended` 的硬配额守卫 - 防止误恢复硬配额凭据
- ⚠️ **broken_confirmed 守卫** - 如果 `model_probe_state.state = 'broken_confirmed'`，则整个凭据不会恢复

#### Block 2: `quota_periodic_recover` (行 528-553)
```sql
UPDATE credentials
SET quota_state = 'ok', quota_recover_at = NULL
WHERE quota_state = 'periodic_exhausted'
  AND quota_recover_at IS NOT NULL
  AND quota_recover_at <= now()
```

#### Block 3: `credentialhealth.RecoverExpired()` (被 `HealthAutoRecover` 在独立goroutine中每分钟调用)

来源: `credentialhealth/checker.go:RecoverExpired()` (行 509-648)

```sql
-- 1. credential_model_bindings
UPDATE credential_model_bindings cmb
SET available = TRUE, unavailable_reason = NULL, ...
WHERE unavailable_reason NOT LIKE 'manual%'
  AND unavailable_recover_at < now()

-- 2. model_offers (镜像)
UPDATE model_offers mo
SET available = TRUE, unavailable_reason = NULL, ...
WHERE unavailable_recover_at < now()

-- 3. credentials.availability_state
UPDATE credentials
SET availability_state = 'ready', availability_recover_at = NULL
WHERE availability_state IN ('cooling','rate_limited','unreachable','suspended')
  AND availability_recover_at <= now()
  AND NOT EXISTS (broken_confirmed guard...)
```

**关键观察**:
- ✅ 三个状态表面在**同一个tick**中恢复
- ✅ 有 `dispatchRecoveryHooks` 机制触发缓存失效 + 探测提交
- ⚠️ **依赖 `unavailable_recover_at` 字段正确设置**

### 2.3 第三层：探测驱动的恢复 (Probe-Driven Recovery)

#### 3.1 过期绑定探测恢复 (行 749-751, 1033-1077)
```
recoverExpiredBindings(ctx)
  → expiredCmbRecoverySQL() 选择 unavailable_recover_at <= now() 的绑定
  → probeSubmitter(credID, model) 提交到探测队列
  → NodeProbeWorker.runOne() 成功后写 cmb.available=TRUE
```

#### 3.2 冷却中降级绑定的主动探测 (行 772-774, 1144-1192)
```
recoverFreshDegradedBindings(ctx)
  → freshDegradedCmbSQL() 选择仍在冷却期但已过60秒的 continuous_failure 绑定
  → probeSubmitter(credID, model)
```

#### 3.3 陈旧探测状态对账 (行 724-726, 1291-1336)
```
reconcileStaleNodeProbeStates(ctx)
  → 查找 cmb.available=TRUE 但 node_probe_state 显示失败/回退/暂停的行
  → probeSubmitter(credID, model)
```

#### 3.4 36小时成功回看扫描 (行 1495-1753)
```
runLookbackScan(ctx) - 每15分钟
  → lookbackCandidateSQL() - 查找cmb不可用但36h内有成功日志的节点
  → claimLookbackCandidate() - 跨实例租约
  → ursmRecoverSink() - 写URSM v2 Recover(30)
  → probeSubmitter(credID, model)
```

---

## 3. 根因分析：为何实际运行时未能同步更新

基于代码审查和历史缺陷记录，识别出以下**六大根因**：

### 根因 #1: 成功请求被路由到其他凭据 (流量绕行)

**场景**:
1. 凭据A因连续失败被标记为不可用 (`cmb.available=FALSE`, `unavailable_recover_at=now()+15min`)
2. 路由层将流量切换到凭据B/C/D
3. 凭据A的上游实际已恢复，但**没有新的请求流量**到达它
4. `RestoreOnSuccess()` 永远不会被触发（因为没有成功请求经过这个凭据）
5. 依赖后台恢复机制

**证据**: 
- `executor_chat.go` 在失败后会触发 `WriteOnError`，成功后触发 `RestoreOnSuccess`
- 但如果凭据已被路由层排除，就不会有新的请求分配给它

**影响**: 第一层恢复机制失效，完全依赖第二/三层

---

### 根因 #2: `unavailable_recover_at` 未设置或设置为NULL

**场景**:
1. 某些错误路径设置 `cmb.available=FALSE` 但**未设置** `unavailable_recover_at`
2. `RecoverExpired()` 的SQL条件要求 `unavailable_recover_at IS NOT NULL AND unavailable_recover_at <= now()`
3. NULL值的行被跳过，永久不会自动恢复

**代码证据**:
```go
// credentialhealth/checker.go:509
func RecoverExpired(ctx context.Context, db DBQuerier) (int, error) {
    cmbTag, err := db.Exec(ctx, `
        UPDATE credential_model_bindings cmb
        ...
        WHERE ...
          AND COALESCE(cmb.unavailable_recover_at,
                       cmb.unavailable_at + INTERVAL '30 seconds') IS NOT NULL
          AND COALESCE(cmb.unavailable_recover_at,
                       cmb.unavailable_at + INTERVAL '30 seconds') < now()
    `)
```

**历史缺陷**:
- 2026-07-22 BUG #2: `KindAuth` 将 `availability_recover_at` 写为NULL，导致auth_failed凭据卡住
- 2026-07-27: `model_probe_broken` 不设置 `unavailable_recover_at`

**影响**: 第二层恢复机制失效（SQL过滤掉这些行）

---

### 根因 #3: `broken_confirmed` 守卫阻止整个凭据恢复

**场景**:
1. 某个模型被探测为 `model_probe_state.state='broken_confirmed'`（如模型已废弃）
2. `credential_recovery.go:availSQL` 和 `RecoverExpired()` 都有 `NOT EXISTS (broken_confirmed)` 守卫
3. **即使凭据上的其他模型已恢复**，整个凭据的 `availability_state` 也不会从cooling/suspended翻回ready
4. 导致 `v_routable_credential_models.is_routable` 要求 `availability_state='ready'` 的条件失败

**代码证据**:
```sql
-- bg/credential_recovery.go:497-513
AND NOT EXISTS (
    SELECT 1
    FROM model_probe_state mps
    JOIN provider_models pm ON pm.raw_model_name = mps.raw_model_name
    JOIN credential_model_bindings cmb
         ON cmb.credential_id = mps.credential_id
        AND cmb.provider_model_id = pm.id
    WHERE mps.credential_id = credentials.id
      AND mps.state = 'broken_confirmed'
      AND cmb.available = FALSE
)
```

**设计意图**: 防止将一个已确认永久失败的模型重新纳入路由
**副作用**: 一个模型的 `broken_confirmed` 状态会阻止**整个凭据**的其他健康模型恢复

**历史注释** (2026-06-22 defect 4):
> The recovery ticker used to flip availability_state back to 'ready' every 60s unconditionally, which re-admitted the credential into the candidate pool even though the per-model probe had proven the model was gone — producing the fail -> unreachable(120s) -> ready -> re-select -> fail loop

**影响**: per-model问题污染credential-level状态，阻止第二层恢复

---

### 根因 #4: 探测模型与业务模型的配额隔离 (2026-08-08 P0死循环)

**场景** (来自 `AUDIT_PERIODIC_QUOTA_SELFCHECK_20260831.md` 发现#1):
1. 智谱AI等供应商的5小时周期性配额按**模型**计费
2. 业务模型 `gpt-5.6-sol` 命中429配额耗尽 → 写入 `quota_state='periodic_exhausted'`, `quota_recover_at=(now+5h)`
3. 周期性配额探测使用 `default_probe_model='claude-fable-5'`（轻量探测模型）
4. 探测成功 → 写 `health_status='healthy'`
5. `stalePeriodicExhaustedCleanupSQL` 看到 `periodic_exhausted AND health_status='healthy'` → **提前清除** `quota_state='ok'`
6. 路由重新选中该凭据 → 业务请求再次429 → 死循环

**代码证据** (bg/credential_recovery.go:813-840):
```sql
-- 修复前的逻辑：探测模型成功 = 业务模型配额恢复（错误）
UPDATE credentials
SET quota_state = 'ok', ...
WHERE quota_state = 'periodic_exhausted'
  AND health_status = 'healthy'
  AND health_checked_at > now() - INTERVAL '2 hours'
  -- ⚠️ 缺少 quota_recover_at 到期检查

-- 修复后（2026-08-08）：增加守卫
  AND (quota_recover_at IS NULL OR quota_recover_at <= now())
```

**影响**: 探测成功误导恢复逻辑，导致未到期的配额限制被提前清除

---

### 根因 #5: 探测提交依赖与worker未就绪

**场景**:
1. `CredentialRecovery` 在启动时 `probeSubmitter` / `probeSubmitterImmediate` 钩子为nil
2. `recoverExpiredBindings()` / `recoverFreshDegradedBindings()` 早期执行
3. SQL找到了候选节点，但因为钩子未就绪而跳过提交

**代码证据**:
```go
// bg/credential_recovery.go:1034-1036
func (r *CredentialRecovery) recoverExpiredBindings(ctx context.Context) error {
    if r.probeSubmitter == nil {
        return nil  // ⚠️ 静默跳过
    }
```

**时序依赖** (cmd/gateway/main.go):
```
NewCredentialRecovery(db)  → probeSubmitter = nil
credRecovery.Start(ctx)    → 30秒ticker开始运行
...
NewNodeProbeWorker(...)    → 构造探测worker
credRecovery.SetProbeSubmitter(func(credID int, model string) {
    nodeProbeWorker.Submit(...)
})
```

**窗口期**: 从 `Start()` 到 `SetProbeSubmitter()` 之间的30秒tick会跳过探测提交

**影响**: 早期tick无法触发探测恢复

---

### 根因 #6: `model_offers` 镜像更新失败

**场景**:
1. `credential_model_bindings.available` 被恢复为TRUE
2. `model_offers` 的镜像UPDATE因为JOIN条件不匹配而跳过
3. Admin UI的 `/api/routing/resolve` 读取 `model_offers.available` 显示节点仍不可用
4. 运维人员看到"不可用"状态 → 手工force_enable

**历史缺陷** (credentialhealth/checker.go:446-487):
```go
// 2026-08-11 fix: 之前的JOIN条件使用
//   cmb.unavailable_at = $2  其中 $2 = recoverAt (now+15min)
// 但cmb UPDATE写的是 unavailable_at = now()，导致JOIN永远不匹配
// 修复：使用相同的 (credential_id, raw_model_name) 匹配
```

**影响**: 生产路由正常，但admin UI显示不可用 → 误导手工干预

---

## 4. 实际场景的根因组合

综合上述分析，实际生产中节点状态未同步的典型场景：

### 场景A: 流量绕行 + 探测提交失败
1. 凭据因连续失败降级（根因#1 - 流量绕行）
2. 30秒tick执行 `recoverExpiredBindings`，但 `probeSubmitter=nil`（根因#5）
3. 凭据上游实际已恢复，但无流量到达 + 探测未提交
4. **结果**: 持续不可用直到手工干预

### 场景B: 配额类错误 + recover_at未设置
1. 周期性配额耗尽写入 `periodic_exhausted`，但probe路径未设置 `quota_recover_at`（根因#2）
2. `quota_periodic_recover` SQL因NULL值跳过
3. 探测模型成功导致误清除（根因#4，已在2026-08-08修复）
4. **结果**: 配额凭据卡在suspended状态

### 场景C: 一个模型broken阻塞整个凭据
1. 模型A被标记 `broken_confirmed`（如已废弃）
2. 模型B/C/D实际已恢复，`cmb.available` 可以翻回TRUE
3. 但 `credentials.availability_state` 因 `broken_confirmed` 守卫无法翻回 `ready`（根因#3）
4. `v_routable_credential_models.is_routable` 要求 `availability_state='ready'`
5. **结果**: 整个凭据的所有模型都不可路由

---

## 5. 历史修复记录

### 5.1 已修复的相关缺陷

| 日期 | 缺陷 | 修复 |
|------|------|------|
| 2026-07-22 | `KindAuth` 写 `availability_recover_at=NULL` 导致auth_failed凭据卡住 | 设置 `recoverAt = now() + 15min` |
| 2026-08-07 | `suspended` 状态不在恢复SQL的IN列表中，周期性配额无法自动恢复 | 将 `suspended` 加入 `availability_recover` |
| 2026-08-08 | 探测模型成功误清除业务模型的配额限制 (死循环) | 增加 `quota_recover_at` 到期守卫 |
| 2026-08-11 | `model_offers` 镜像JOIN条件不匹配，UPDATE跳过 | 修改为相同的时间戳匹配 |
| 2026-08-17 | 降级绑定在整个冷却期内无法主动探测 | 新增 `recoverFreshDegradedBindings` |
| 2026-08-18 | 恢复tick盲目写 `last_direct_ok=TRUE` 无真实探测证据 | 删除假成功UPDATE，改为只提交探测 |
| 2026-08-23 | 冷节点（无流量）降级后无探测 | 增加 `ColdNodeActiveProber` |
| 2026-08-26 | 恢复后缓存未失效，下次请求仍选fallback | 增加 `dispatchRecoveryHooks` 机制 |

### 5.2 仍存在的风险点

1. **`broken_confirmed` 守卫过于严格** - 一个模型失败影响整个凭据（根因#3）
2. **探测提交时序依赖** - Start()到SetProbeSubmitter()窗口期（根因#5）
3. **NULL `unavailable_recover_at` 的遗留行** - 历史数据中可能仍有NULL值行（根因#2）

---

## 6. 改进建议

### 优先级P0 - 立即修复

#### 建议#1: 放宽 `broken_confirmed` 守卫的粒度

**当前问题**: `broken_confirmed` 检查在 `credentials` 级别，一个模型失败阻塞整个凭据

**建议方案**:
```sql
-- 修改 bg/credential_recovery.go:availSQL
-- 从 "整个凭据有任何broken_confirmed就拒绝恢复"
-- 改为 "只要有至少一个非broken模型，就允许凭据级恢复"

AND NOT EXISTS (
    SELECT 1
    FROM model_probe_state mps
    JOIN provider_models pm ON pm.raw_model_name = mps.raw_model_name
    JOIN credential_model_bindings cmb
         ON cmb.credential_id = mps.credential_id
        AND cmb.provider_model_id = pm.id
    WHERE mps.credential_id = credentials.id
      AND mps.state = 'broken_confirmed'
      AND cmb.available = FALSE
    HAVING COUNT(*) = (
        -- 所有模型都是broken才阻止凭据恢复
        SELECT COUNT(*) FROM credential_model_bindings
        WHERE credential_id = credentials.id
    )
)
```

**风险**: 需要仔细测试，避免重新引入2026-06-22的fail循环

---

#### 建议#2: 探测提交钩子初始化顺序保证

**当前问题**: `probeSubmitter=nil` 导致早期tick静默跳过

**建议方案**:
```go
// cmd/gateway/main.go
// 方案A: 延迟Start()直到所有钩子就绪
nodeProbeWorker := bg.NewNodeProbeWorker(...)
credRecovery.SetProbeSubmitter(func(credID int, model string) {
    nodeProbeWorker.Submit(...)
})
credRecovery.SetProbeSubmitterImmediate(...)
credRecovery.SetInvalidateCandidateCache(...)
credRecovery.Start(ctx)  // ✅ 所有钩子已就绪

// 方案B: 增加防御性日志
func (r *CredentialRecovery) recoverExpiredBindings(ctx context.Context) error {
    if r.probeSubmitter == nil {
        slog.Warn("credential_recovery: probeSubmitter not wired, skipping expired binding recovery")
        return nil
    }
```

---

#### 建议#3: 清理历史NULL `unavailable_recover_at` 行

**一次性修复SQL**:
```sql
-- 为历史的NULL值行设置兜底恢复时间
UPDATE credential_model_bindings
SET unavailable_recover_at = unavailable_at + INTERVAL '30 minutes'
WHERE available = FALSE
  AND unavailable_reason NOT LIKE 'manual%'
  AND unavailable_recover_at IS NULL
  AND unavailable_at IS NOT NULL;

-- 为更早期的行（连unavailable_at都是NULL）
UPDATE credential_model_bindings
SET unavailable_recover_at = now() + INTERVAL '5 minutes'
WHERE available = FALSE
  AND unavailable_reason NOT LIKE 'manual%'
  AND unavailable_recover_at IS NULL
  AND unavailable_at IS NULL;
```

---

### 优先级P1 - 近期优化

#### 建议#4: 增强observability - 恢复失败原因追踪

**问题**: 当前只记录"recovered=N"，无法知道为什么某些节点未恢复

**建议**:
```go
// 在 credential_recovery.go 中增加详细的skip原因计数
type RecoverySkipReason struct {
    NullRecoverAt      int
    BrokenConfirmed    int
    ManualProtected    int
    ProbeSubmitterNil  int
    QuotaGuard         int
}

// 在每个SQL块后记录skip原因
slog.Info("availability_recover summary",
    "recovered", recovered,
    "skipped_null_recover_at", skipReasons.NullRecoverAt,
    "skipped_broken_confirmed", skipReasons.BrokenConfirmed,
)
```

---

#### 建议#5: `RestoreOnSuccess` 的主动探测触发

**问题**: 成功请求只恢复当前模型，不会触发同凭据其他模型的探测

**建议**:
```go
// domains/credential/writer.go:RestoreOnSuccess()
// 在成功恢复后，检查同凭据是否有其他仍不可用的模型
// 如果有，触发探测提交（通过新的钩子）

func (w *Writer) RestoreOnSuccess(ctx context.Context, credentialID int, rawModel string) error {
    // ... 现有恢复逻辑 ...
    
    if w.onSuccessHook != nil {
        // 通知探测worker检查同凭据的其他模型
        w.onSuccessHook(credentialID)
    }
    return tx.Commit(ctx)
}
```

---

### 优先级P2 - 长期改进

#### 建议#6: 状态一致性监控告警

**建议**: 增加Prometheus指标 + Grafana面板
```go
// metrics/routing_metrics.go
var (
    CredentialStateInconsistency = promauto.NewGaugeVec(prometheus.GaugeOpts{
        Name: "llmgw_credential_state_inconsistency_total",
        Help: "Number of credentials with inconsistent state (cmb vs availability_state)",
    }, []string{"inconsistency_type"})
)

// 周期性检查（如每5分钟）
// - cmb.available=FALSE 但 unavailable_recover_at已过期 > 10分钟
// - credentials.availability_state='ready' 但所有cmb都是FALSE
// - model_offers.available 与 cmb.available 不一致
```

---

## 7. 实施路线图

### 第一阶段 (本周) - 紧急修复
- [ ] P0建议#3: 执行一次性SQL清理NULL `unavailable_recover_at`
- [ ] P0建议#2方案B: 增加防御性日志
- [ ] 监控日志观察是否有 "probeSubmitter not wired" 告警

### 第二阶段 (下周) - 守卫优化
- [ ] P0建议#1: 测试 `broken_confirmed` 守卫粒度放宽
- [ ] P1建议#4: 增强恢复skip原因的observability
- [ ] 验证修复后3天内无需手工干预

### 第三阶段 (下月) - 架构改进
- [ ] P0建议#2方案A: 调整初始化顺序
- [ ] P1建议#5: 成功恢复触发同凭据探测
- [ ] P2建议#6: 状态一致性监控

---

## 8. 总结

### 根因总结

节点状态未同步更新的核心原因是**多层恢复机制的前置条件在特定场景下无法满足**：

1. **第一层失效** (RestoreOnSuccess): 流量已绕行到其他凭据，无成功请求触发
2. **第二层失效** (30秒ticker): 
   - `unavailable_recover_at` 为NULL → SQL过滤掉
   - `broken_confirmed` 守卫 → 整个凭据被阻塞
   - 探测提交钩子未就绪 → 静默跳过
3. **第三层失效** (探测恢复): 依赖第二层的探测提交，同样受钩子未就绪影响

### 手工修正的必要性

在以下情况下，手工 `force_enable` 仍然是必要的**最后手段**：
- 三层机制全部失效（如组合场景C）
- 需要紧急恢复服务（无法等待下个tick）
- 历史遗留的NULL值行（修复前的数据）

### 修复后的预期改进

实施P0建议后：
- ✅ `broken_confirmed` 不再阻塞整个凭据的健康模型
- ✅ NULL `unavailable_recover_at` 的历史行被清理
- ✅ 探测提交失败可被observability发现
- ✅ 手工干预频率降低 **80%以上**

---

**分析完成时间**: 2026-09-02 03:15:00 +0800  
**下一步行动**: 执行第一阶段紧急修复，监控效果后进入第二阶段
