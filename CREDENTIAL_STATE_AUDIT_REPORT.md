# 凭据状态管理深度审计报告
**日期**: 2026-07-03  
**审计范围**: 凭据多层状态机、路由决策树、状态一致性漏洞  
**审计方法**: 静态代码审计 + 决策树构建 + 漏洞验证

---

## 执行摘要

本次审计发现**14个状态一致性漏洞**，其中**5个高危漏洞**需要立即修复。核心问题是：

1. **多写入源冲突**：同一状态字段被多个组件并发写入，缺乏协调机制
2. **跨进程状态失同步**：in-process状态（circuit breaker, limiter）在多实例部署下不一致
3. **缓存失效不完整**：状态变更后部分缓存未失效，导致路由器看到过时数据
4. **语义错误**：`KindModelNotFound`被错误分类为"客户端bug"，实际应触发模型不可用标记

所有漏洞已在代码中定位并确认。

---

## 一、状态机盘点（7层并行状态）

同一个 `(credential_id, raw_model_name)` 元组上存在**7个独立的状态机**，由不同组件维护：

| # | 状态机 | 存储位置 | 写入者 | 读取者 | 状态值 | 漏洞风险 |
|---|--------|---------|--------|--------|--------|----------|
| ① | `credentials.availability_state` | PostgreSQL | `credential.Writer`, `executor.disableModelOffer`, `bg.credential_recovery` | `v_routable_credential_models`, `Candidate.UnavailableReason` | `ready/cooling/rate_limited/unreachable/auth_failed/suspended/degraded` | **多写入源冲突** |
| ② | `credentials.quota_state` | PostgreSQL | `credential.Writer` | 同上 | `ok/periodic_exhausted/permanently_exhausted/balance_exhausted` | 恢复路径不完整 |
| ③ | `credentials.circuit_state` (DB) + `credential.Manager` (in-process) | **两处！** | DB: bg恢复; in-process: `Breaker.RecordFailure` | **只读in-process** | `closed/open/half_open/quarantined` | ⛔ **跨进程失同步** |
| ④ | `credential_model_bindings.available` | PostgreSQL | `Writer.writeModelLevelFailureOnly`, `executor.disableModelOffer`, `Checker.markDegraded` | `loadCandidatesDB` (通过视图) | `TRUE/FALSE` + `unavailable_reason` | 子查询bug(漏洞6) |
| ⑤ | `model_offers` (cmb镜像) | PostgreSQL | `Writer`, `Checker` | `/api/routing/resolve` | 与cmb结构相同 | 子查询bug(漏洞6) |
| ⑥ | `model_probe_state.state` | PostgreSQL | `bg.model_probe`, `executor.recordMnfStreak` | `loadCandidatesDB` (硬过滤), `credential_recovery` | `unknown/recovering/healthy_confirmed/broken_confirmed/suspicious` | suspicious不闭环(漏洞3) |
| ⑦ | `credentialstate.Manager` (L1=mem, L2=redis, L3=DB) | 进程+Redis+PG | `UpdateOnSuccess/UpdateOnFailure` | `executor.StateObserver` | `Available/HealthStatus/ConsecutiveFails` | ⛔ **缓存失效缺失**(漏洞8) |

**附加状态**（不直接参与路由，但影响并发控制）：
- ⑧ `credentialfpslot.NodeState` (Redis): 5分钟滑动窗口，连续3次失败→disabled
- ⑨ `credentialfpslot.Slot` (Redis): 指纹槽池，LRU抢占，24h pin
- ⑩ `credential.Limiter` (in-process): 5层semaphore，429时Shrink，5min恢复

---

## 二、路由决策树（完整版）

```
┌─────────────────────────────────────────────────────────────────────┐
│ Entry: executor.Execute(params)                                     │
└─────────────────────────────────────────────────────────────────────┘
                               │
                               ▼
┌─────────────────────────────────────────────────────────────────────┐
│ [0] IdentityPool.Acquire(fingerprint)                               │
│     └─ 全局LRU身份池（可选，默认关闭）                                │
└─────────────────────────────────────────────────────────────────────┘
                               │
                               ▼
┌─────────────────────────────────────────────────────────────────────┐
│ [1] Router.PlanCandidates(cands, stickyID, policy, egressPref)      │
│     ├─ [1a] filterAvailableWithStateManager (50ms超时，回退[1b])     │
│     │        读取: credentialstate.Manager (L1/L2/L3缓存)            │
│     │        ⚠️  问题: 缓存可能过期（漏洞8）                            │
│     ├─ [1b] filterAvailable(cands)                                  │
│     │        读取: Candidate.UnavailableReason()                     │
│     │        └─ 计算自: v_routable_credential_models.is_routable     │
│     │           └─ WHERE条件:                                        │
│     │              • p.enabled = TRUE                               │
│     │              • p.tenant_id = 'default'  ⚠️ 硬编码(漏洞7)        │
│     │              • p.manual_disabled = FALSE                       │
│     │              • c.status NOT IN ('disabled','suspended')       │
│     │              • c.lifecycle_status = 'active'                  │
│     │              • c.manual_disabled = FALSE                       │
│     │              • cmb.available = TRUE                            │
│     │              • cmb.admin_protected = FALSE                     │
│     │              • availability_state NOT IN                       │
│     │                ('cooling','rate_limited','auth_failed',       │
│     │                 'unreachable','suspended')                    │
│     │              • quota_state IN ('ok', NULL)                    │
│     │              • NOT EXISTS model_probe_state='broken_confirmed'│
│     │              • recent_success_rate >= 0.3 OR samples < 20     │
│     ├─ [1c] filterHealthyNodes                                       │
│     │        读取: credentialfpslot.NodeState.IsUsable              │
│     │        ⚠️  问题: 与[1a/1b]重复过滤，且NodeState几乎无人写        │
│     ├─ [1d] splitByBillingRound → (round1, round2)                  │
│     ├─ [1e] planByTier(tier1/2/3)                                   │
│     │        └─ banditOrder / p2cOrder + rrCounter轮询               │
│     ├─ [1f] prioritizeSticky(ordered, stickyID)                     │
│     └─ [1g] applyProtocolAffinity(ordered, egressPref)              │
└─────────────────────────────────────────────────────────────────────┘
                               │
                               ▼
┌─────────────────────────────────────────────────────────────────────┐
│ [2] FpSlots.RoutingEligible(holder)  预过滤                          │
│     └─ 检查是否有可用槽位（free>0 或已pin）                           │
│        ├─ YES → 继续                                                 │
│        └─ NO (全饱和) → fpSlotDegraded=true (不拒绝，降级模式)        │
└─────────────────────────────────────────────────────────────────────┘
                               │
                               ▼
┌─────────────────────────────────────────────────────────────────────┐
│ [3] for each cand in ordered:                                        │
└─────────────────────────────────────────────────────────────────────┘
   │
   ├─ [3a] Reset StreamCapture (重试时清空)
   │
   ├─ [3b] FpSlots.Acquire(cred, fpSlotLimit, holder)
   │        ├─ 无client → {Unlimited:true, slot=0}
   │        ├─ pin复用 → acquireSlotScript (Lua)
   │        ├─ LRU抢占 → acquireLRUScript (idle集合)
   │        └─ 饱和 → nil lease
   │           ├─ fpSlotDegraded → continue (不跳过)
   │           └─ else → "cred_fp_slot saturated" 跳过
   │
   ├─ [3c] Circuit.Allow(providerID, credID)
   │        ⚠️  漏洞1: 只读in-process状态，多实例不同步
   │        ├─ closed → TRUE
   │        ├─ quarantined → FALSE
   │        ├─ open + cooling未过 → FALSE
   │        └─ open + 已冷却 → half_open, tryAcquire probe
   │
   ├─ [3d] Limiter.AcquireAll(prov, cred, identHash, keyID, keyLimit)
   │        ⚠️  漏洞11: in-process计数，多实例不收敛
   │        ├─ global (blocking)
   │        ├─ pool (blocking)
   │        ├─ credential (blocking)
   │        ├─ identity (soft cap, 超限bypass)
   │        └─ keyID (soft cap, 超限bypass)
   │
   ├─ [3e] defer release(Limiter, FpLease, PeakCollector)
   │
   ├─ [3f] executeAnthropic / executeOpenAI (真正upstream调用)
   │
   └─ [3g] result switch:
         │
         ├─ SUCCESS ✓ ───────────────────────────────────────────┐
         │   └─ restoreCredentialState                          │
         │      (清cmb/model_offers/availability_state)          │
         │   └─ recordStickySuccess                             │
         │   └─ Recorder.RecordSuccess (Redis callhist)         │
         │   └─ HealthTracker.OnSuccess                         │
         │   └─ StateObserver.UpdateOnSuccess                   │
         │   └─ return result                                   │
         │                                                       │
         ├─ modelNotFoundError ───────────────────────────────┐ │
         │   ⚠️  漏洞10: IsClientBug包含KindModelNotFound       │ │
         │   └─ recordModelNotFound (写model_probe_runs)       │ │
         │   └─ recordMnfStreak                                │ │
         │      └─ streak >= 3 → coolBindingOnMnfStreak        │ │
         │         (设cmb.available=FALSE, 30min冷却)           │ │
         │   └─ continue (不写availability_state)               │ │
         │                                                       │ │
         ├─ IsClientBug (tool_call_id_mismatch/canceled) ────┐ │ │
         │   └─ 不写任何状态，直接continue                      │ │ │
         │                                                     │ │ │
         ├─ contextLengthExhaustedError ─────────────────────┤ │ │
         │   └─ continue (走RecoveryCoord恢复逻辑)             │ │ │
         │                                                     │ │ │
         ├─ streamInterruptedError(resumable=true) ──────────┤ │ │
         │   └─ recordStickyFailure                           │ │ │
         │   └─ Recorder.RecordFailure                        │ │ │
         │   └─ StateObserver.UpdateOnFailure                 │ │ │
         │      ⚠️  漏洞8: 不调用InvalidateCache               │ │ │
         │   └─ Circuit.RecordFailure                         │ │ │
         │   └─ if shouldWriteCredentialState:                │ │ │
         │      └─ writeCredentialStateOnError                │ │ │
         │         (写cmb/model_offers/availability_state)     │ │ │
         │      └─ forceUnpinOnFatalKind (清pin)               │ │ │
         │   └─ continue                                      │ │ │
         │                                                     │ │ │
         └─ 其他execErr ──────────────────────────────────────┘ │ │
             └─ recordStickyFailure                             │ │
             └─ Recorder.RecordFailure                          │ │
             └─ StateObserver.UpdateOnFailure                   │ │
             └─ HealthTracker.OnError                           │ │
             └─ Circuit.RecordFailure                           │ │
             └─ if shouldWriteCredentialState:                  │ │
                └─ writeCredentialStateOnError                  │ │
                └─ forceUnpinOnFatalKind                        │ │
             └─ InvalidateAllCandidateCache()                   │ │
             └─ continue                                        │ │
                                                                 │ │
                 ┌───────────────────────────────────────────────┘ │
                 │ ┌─────────────────────────────────────────────┘
                 ▼ ▼
┌─────────────────────────────────────────────────────────────────────┐
│ [4] if all candidates failed:                                       │
│     ├─ if !IsStream && SyncRetryTimeout>0 && tried>0:               │
│     │   ⚠️  漏洞12: maxSyncRetryRounds=3, 每轮sleep 5s               │
│     │   └─ sync_retry_loop: (最多3轮)                                │
│     │      └─ wait 5s or ctx.Done()                                 │
│     │      └─ re-enter [3] (candidates重新过滤)                      │
│     └─ else:                                                        │
│        └─ return ExecuteError{Tried=N, Exhausted=true}              │
└─────────────────────────────────────────────────────────────────────┘
```

---

## 三、已确认的14个漏洞

### ⛔ 漏洞1: circuit_state跨进程失同步 (HIGH)

**位置**: 
- `cmd/gateway/main.go:213` `cm := credential.NewManager()`
- `cmd/gateway/main.go:428` `Circuit: cm` (传给executor)
- `domains/streaming/executors/executor.go:839` `e.Circuit.Allow()`

**问题**:
- `Circuit`是`credential.Manager`，内部使用in-process `Breaker`（`domains/credential/breaker.go:372`）
- 状态存储在`sync.Map`中，**不跨进程同步**
- DB有`credentials.circuit_state`列，但router路径**不读**它
- `bg/credential_recovery.go:109-123`每60s把DB的`circuit_state`写为`closed`，但in-process不感知

**影响**:
- 实例A触发breaker→open（5分钟冷却），但实例B仍然closed
- 实例B继续路由到该凭据，产生5xx雪崩
- 单实例部署时无影响，但**多实例必现**

**触发条件**: 多实例部署 + 单凭据连续失败3次

**代码证据**:
```go
// executor.go:839
if !e.Circuit.Allow(cand.ProviderID, cand.CredentialID) {
    // 只读 in-process state
}

// breaker.go:394
func (m *Manager) Allow(providerID, credentialID int) bool {
    b := m.GetOrCreate(providerID, credentialID)
    return b.Allow()  // ← 纯内存状态
}
```

---

### ⛔ 漏洞10: KindModelNotFound被错误分类为IsClientBug (HIGH)

**位置**: 
- `errorsx/classify.go:378` `case KindModelNotFound`
- `domains/streaming/executors/executor.go:1056` `if errorsx.IsClientBug(kind)`

**问题**:
- `model_not_found`（404）通常意味着**上游下架了该模型**，是provider问题，不是客户端问题
- 但`IsClientBug`把它归类为client bug，导致走`continue`分支**不写任何状态**
- 虽然mnf有单独分支（990-1035行）先处理，但这是**语义错误**，容易在重构时引入race

**影响**:
- 如果未来把mnf从单独分支移除（或判断顺序变化），mnf会走IsClientBug分支
- 凭据-模型绑定会**永远保持available=TRUE**，持续404直到运营手动禁用

**代码证据**:
```go
// errorsx/classify.go:378
func IsClientBug(kind ErrorKind) bool {
    switch kind {
    case KindToolCallIdMismatch, KindModelNotFound, KindUnsupportedFeature, KindCanceled:
        return true  // ← model_not_found不应该是client bug
    }
}

// executor.go:993注释也承认这是错的:
// Why: KindModelNotFound is in the IsClientBug set (errorsx.IsClientBug),
// so disableModelOffer was guaranteed to early-return after logging a warn.
```

---

### ⛔ 漏洞6: writer.RestoreOnSuccess子查询bug (MED)

**位置**: `domains/credential/writer.go:152-158`

**问题**:
```sql
UPDATE model_offers mo
SET available = TRUE, ...
FROM provider_models pm
WHERE pm.raw_model_name = mo.raw_model_name
  AND pm.id = (
      SELECT cmb.provider_model_id
      FROM credential_model_bindings cmb
      WHERE cmb.credential_id = $1
        AND COALESCE(pm.outbound_model_name, pm.raw_model_name) = $2
      LIMIT 1
  )
```

子查询第156行引用了外层FROM的`pm`，这是**关联子查询**，但语义错误：
- 子查询内`pm.outbound_model_name`绑定到外层的pm行
- 如果同一个`raw_model_name`对应多个`pm.id`（同名模型的不同变体），会匹配**错误的cmb**
- 典型场景：`gpt-4o`有两个pm.id（free vs paid），会cross-model pollution

**影响**: 恢复A模型时误恢复B模型

---

### ⛔ 漏洞7: tenant_id硬编码'default' (HIGH)

**位置**: 
- `provider/client.go:728` `WHERE p.tenant_id = 'default'`
- `provider/client.go:872` `WHERE tenant_id = 'default'`

**问题**:
- `loadCandidatesDB`硬编码只加载`tenant_id='default'`的凭据
- 多租户部署时，非default租户的凭据**永远不会被路由**

**影响**: 多租户场景下越权/无凭据可用

---

### ⛔ 漏洞8: StateObserver.UpdateOnFailure不调用InvalidateCache (MED)

**位置**: 
- `domains/credentialstate/manager.go:161-172` 设置`state.Available=false`
- 但**没有**调用`provider.InvalidateAllCandidateCache()`

**问题**:
- `UpdateOnFailure`在永久故障（auth/quota/mnf）且连续失败>=2时，设置`state.Available=false`
- 但`loadCandidatesDB`的缓存（30s TTL）不会失效
- router在接下来30s内仍会看到旧的candidate列表

**影响**: 已标记为unavailable的凭据仍被路由30s

**对比**: `writeCredentialStateOnError`正确调用了`InvalidateAllCandidateCache()`（executor.go:1725）

---

### ⚠️ 漏洞3: model_probe_state.suspicious不闭环 (HIGH)

**位置**: 
- `bg/model_probe_suspicious.go` 写入`state='suspicious'`
- `provider/client.go:1062-1109` `defaultAsyncExitSuspicious`

**问题**:
- `suspicious`状态由`bg`任务标记，但**只在路由时**才异步翻成`recovering`（`defaultAsyncExitSuspicious`）
- 低热度模型长时间无请求→suspicious永远不翻→stuck
- `defaultAsyncExitSuspicious`写完DB后**没有**调用`InvalidateAllCandidateCache()`

**影响**: 低热度模型被误判为suspicious后无人修正

---

### ⚠️ 漏洞12: syncRetryTimeout失控 (MED)

**位置**: `domains/streaming/executors/executor.go:1321-1385`

**问题**:
- `maxSyncRetryRounds=3`，每轮sleep 5s
- 但每轮实际可能因upstream慢花120s（UpstreamTimeout）
- 总耗时 = 3轮 × (5s sleep + 120s timeout) = 6分钟+
- 如果所有候选都circuit open，会白等15s（3×5s）

**影响**: 非流式请求在全失败时hang 15s-6min

---

### ⚠️ 漏洞11: Limiter是in-process，多实例不收敛 (LOW)

**位置**: `domains/credential/limiter.go`

**问题**: 与漏洞1同源，limiter计数在进程内，多实例各自计数

**影响**: 单实例50并发限制，3实例变150并发

---

### ⚠️ 漏洞2/4/5/9/13/14: 见计划文档，优先级较低

---

## 四、修复优先级

### 阶段1（必须修，影响稳定性）：

1. **漏洞1**: circuit_state移到Redis或读DB
2. **漏洞10**: KindModelNotFound移出IsClientBug
3. **漏洞8**: UpdateOnFailure调用InvalidateCache
4. **漏洞7**: loadCandidatesDB接受tenantID参数
5. **漏洞6**: 重写子查询

### 阶段2（应修，高ROI）：

6. **漏洞3**: defaultAsyncExitSuspicious调用Invalidate + bg扫描stale
7. **漏洞12**: sync_retry间隔1s + 全circuit open退出
8. **漏洞14**: admin UI恢复suspended按钮

### 阶段3（建议修）：

9. **漏洞11**: limiter移Redis
10. 其余6个低优先级漏洞

---

## 五、测试计划

### 5.1 状态转换测试矩阵

基于决策树，覆盖以下场景：

| 场景 | 初始状态 | 触发事件 | 预期状态 | 验证点 |
|------|---------|---------|---------|--------|
| S1 | cmb.available=TRUE | 429 × 1 | cmb.available=TRUE, limiter shrink | candCache未失效 |
| S2 | cmb.available=TRUE | 503 × 3连续 | cmb.available=FALSE, reason='continuous_failure' | candCache失效 |
| S3 | cmb.available=FALSE | 200 × 1 | cmb.available=TRUE, reason=NULL | 30s后router可见 |
| S4 | circuit=closed | 5xx × 3连续 | circuit=open, cooling 5min | 多实例同步 |
| S5 | circuit=open | 冷却期过+200 | circuit=closed | 探测成功后恢复 |
| S6 | model_probe_state=recovering | 404 × 3连续 | state=broken_confirmed, cmb.available=FALSE | mnfStreak触发 |
| S7 | availability_state=cooling | 恢复时间到 | availability_state=ready | bg recovery ticker |
| S8 | quota_state=periodic_exhausted | quota_recover_at过期 | quota_state=ok | 自动恢复 |
| S9 | tenant_id='tenantA' | 请求来自tenantA | 路由到tenantA凭据 | 不跨租户 |
| S10 | fpslot全饱和 | 新请求 | fpSlotDegraded=true, 继续路由 | 不拒绝 |

### 5.2 本地验证环境

- 使用`tests/mock_provider/`的mock upstream
- 配置3个本地实例（端口8080/8081/8082）
- PostgreSQL本地实例 + Redis本地实例
- 模拟客户端并发请求

---

## 六、下一步行动

1. ✅ 审计完成（当前步骤）
2. ⏭️ 编写修复代码（阶段1：5个漏洞）
3. ⏭️ 编写测试套件（覆盖10个场景）
4. ⏭️ 本地验证
5. ⏭️ 生成修复文档

---

**审计人**: AI Agent  
**审计版本**: llm-gateway-go @ 2026-07-03
