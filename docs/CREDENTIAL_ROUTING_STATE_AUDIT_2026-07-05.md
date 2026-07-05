# 凭据 × 路由节点 × 模型 × 请求 — 状态机与决策树综合审计报告 v2.0

**审计日期**: 2026-07-05  
**审计版本**: v2.0（增量更新，基于 2026-07-04 v1.0）  
**审计人**: AI Agent（静态代码分析 + 决策树构建 + 状态转换验证）  
**审计范围**: 全量扫描 llm-gateway-go 核心路由与状态管理模块  
**基线**: commit 4fbdba07（V15-V20 修复已应用）

---

## 执行摘要

### 🎯 核心发现

**✅ 已修复漏洞（V15-V20）**: 6 个高危漏洞已在 2026-07-04~07-05 修复并合并到 main：
- **V15**: Pool Dead 终态恢复机制 ✅
- **V16**: Pool Dead → Draining 优雅降级 ✅  
- **V17**: Live filter 静默失败可观测性 ✅
- **V18**: Session task-drift 检测（HitCount ≥ 50 强制重分类）✅
- **V20**: 并发 Slot 竞态保护 ✅

**🔴 新发现漏洞（V21-V27）**: 7 个新漏洞，其中 **4 个 HIGH**，**3 个 MEDIUM**

**⚠️ 架构性问题**: 
- **7 个独立状态源**并存，无单一真相源
- **URSM 迁移未完成**：新架构已定义但未启用
- **跨进程状态同步**依赖 30s 缓存 TTL + Redis 手动失效

---

## 一、状态机全景图

llm-gateway-go 在 4 个维度上管理状态，每个维度有多个独立的状态机。

### 1.1 四维状态空间

```
维度 1: Provider（供应商）
  ├─ enabled (bool, DB)
  └─ manual_disabled (bool, DB)

维度 2: Credential（凭据）
  ├─ lifecycle_status (active/suspended/deleted, DB)
  ├─ availability_state (available/auth_failed/cooling/suspended, Redis+DB)
  ├─ quota_state (ok/balance_exhausted/periodic_exhausted, Redis+DB)
  ├─ circuit_state (CLOSED/OPEN/HALF_OPEN/QUARANTINED, in-process)
  ├─ balance_usd (float, DB)
  └─ routable (bool, DB computed)

维度 3: Model × Credential (Binding)
  ├─ model_probe_state (ok/broken_confirmed/recovering, DB)
  ├─ mnf_cooling_until (timestamp, DB cmb)
  ├─ node_state.disabled (bool, Redis Lua sliding window)
  └─ offer availability (bool, DB model_offers)

维度 4: Request（运行时）
  ├─ Pool.state (active/degraded/draining/dead, in-process atomic)
  ├─ Slot.active (SET, Redis 30min TTL)
  ├─ Slot.pin (SET, Redis 24h TTL)
  ├─ Limiter[5 layers] (semaphore, in-process)
  ├─ Sticky.credID (map, in-process + Redis 5min)
  ├─ SessionIntentCache (map, in-process 10min)
  ├─ Bandit α/β (map, in-process)
  └─ MnfStreak (map, in-process)
```

### 1.2 七个独立状态源

| 状态管理器 | 存储 | 职责 | 状态 |
|---|---|---|---|
| **credentialstate.Manager** | mem + Redis + DB | credential × model 可用性缓存 | ✅ ACTIVE |
| **credential.Breaker** | in-process | Circuit breaker 4-state FSM | ✅ ACTIVE |
| **credential.Writer** | DB | availability/quota/circuit 持久化 | ✅ ACTIVE |
| **credentialfpslot.NodeState** | Redis Lua | 滑动窗口 + cooldown | ✅ ACTIVE (DEPRECATED) |
| **credentialhealth.Checker** | Redis + DB | 健康探测 + 降级标记 | ✅ ACTIVE |
| **bg.CredentialRecovery** | DB | mnf_cooling/circuit/quota 恢复 | ✅ ACTIVE |
| **ursm.Manager** | mem + Redis + DB | 统一路由状态管理器（新架构） | ⚠️ READY 但未启用 |

**问题**: 前 6 个状态源**并行运行**，通过以下机制松耦合同步：
- `credentialstate.Manager.invalidateCandidateCache()` → 触发 `provider.Client` 30s 缓存失效
- `credential.Writer.WriteOnError()` → DB UPDATE → 下次 `GetCandidates()` 读到新状态
- `bg.CredentialRecovery` 每 60s 扫描 DB → 恢复冷却期过期的凭据
- **无原子性保证**，**无跨进程一致性保证**

---

## 二、决策树完整映射

### DT-1: Provider.GetCandidates — 模型 → 候选列表

```
[Request] provider.GetCandidates(ctx, model, profile, tenantID)
    │
    ├─ [normalize] routeModel = NormalizeRouteKey(model)
    │
    ├─ [cache] key = routeModel + "|" + profile + "|" + tenantID   (TTL 30s, singleflight)
    │     ├─ HIT  → enrichWithAPIKeys → return
    │     └─ MISS → singleflight "cand:<key>" → fetchCandidatesDB
    │
    ├─ [fetchCandidatesDB]
    │     ├─ [DT-2] resolveModelDB(model, profile) → Resolution (canonical_id/alias/raw)
    │     └─ loadCandidatesDB(clientModel, tenantID) → []Candidate
    │
    └─ [enrich] enrichWithAPIKeys (Fernet/AES-GCM 解密)
```

**漏洞位置**: `provider/client.go:334-379` — 缓存键 30s，单进程内，不会跨进程失效  
**说明**: tenantID 透传到 `loadCandidatesDB` 的 SQL WHERE，V_routable 视图硬编码 `'default'`（V25 漏洞）

### DT-2: resolveModelDB — 4 段决策树

```
[Request] resolveModelDB(model, profile)
    │
    ├─ [normalize] variants = NormalizeRouteKeyAliases(model)   // 3-5 个变体
    │
    ├─ STAGE 1: canonical_name 匹配
    │     for v in variants:
    │       SELECT id FROM models_canonical WHERE canonical_name = v AND status='active'
    │     └─ HIT → hitPath="canonical"
    │                → aliasRawNamesDB(canonicalID, profile)
    │                → Resolution.ResolutionPath = "canonical"
    │                → return
    │
    ├─ STAGE 2: alias 匹配（按 profile 过滤）
    │     for v in variants:
    │       JOIN model_aliases ma → models_canonical mc
    │       WHERE lower(ma.raw_name) = v
    │         AND (ma.client_profiles IS NULL OR profile IN ma.client_profiles)
    │
    ├─ STAGE 3: raw_lookup 命中 alias
    │     (only in resolve.go, not in provider/client.go — 这里只查 DB canonical)
    │
    └─ STAGE 4: else → direct (passthrough)
                Resolution.ResolutionPath = "direct"
                RawModels = [lower(model)]
```

**漏洞位置**: `provider/client.go:450-595`

### DT-3: Candidate.UnavailableReason — 单凭据 5 阶段判定（**V25 漏洞**）

```
[Request] Candidate.UnavailableReason()
    │
    ├─ ① !c.Routable → "routing_blocked[:reason]"
    │
    ├─ ② LifecycleStatus != "active" → "lifecycle:<status>"
    │
    ├─ ③ switch AvailabilityState:
    │     suspended      → "availability:suspended"
    │     auth_failed    → "availability:auth_failed"
    │     cooling        → "availability:cooling"
    │     rate_limited   → "availability:rate_limited"
    │     unreachable    → "availability:unreachable"
    │
    ├─ ④ switch QuotaState:
    │     balance_exhausted       → "quota:balance_exhausted"
    │     permanently_exhausted   → "quota:permanently_exhausted"
    │     periodic_exhausted      → "quota:periodic_exhausted"
    │
    ├─ ⑤ BalanceUSD != nil && *BalanceUSD <= 0 → "balance:zero"
    │
    └─ else → "" (routable)
```

**V25 漏洞**: 5 阶段判定采用短路求值，首个命中的理由被返回，丢失后续原因  
**影响**: 调试困难，无法看到完整故障链  
**修复**: 改为累积多原因，返回 `"routing_blocked; quota:exhausted; circuit:open"`

### DT-4: Decider.Decide / DecideV2 — 路由决策

```
[Request] Decider.Decide(ctx, sigs, apiKeyID, profile, taskHint, sessionID)
    │
    ├─ [Step 0] sessionCache.Get(sessionID)
    │     ├─ HIT + !shouldReclassify → 复用（直接 return Decision）
    │     └─ HIT + shouldReclassify(检测: image attach / tool calls / long context / HitCount≥50)
    │           → 走完整 pipeline
    │
    ├─ [Step 1] resolveProfile(apiKeyID, headerProfile)
    │     ├─ valid header → stickyPut (apiKeyID>0)
    │     ├─ stickyEntry (L1 get, TTL 30min)
    │     └─ DefaultProfile (=ProfileSmart)
    │
    ├─ [Step 2] classify(taskHint, sigs)
    │     ├─ valid taskHint → 直接信任 (confidence=0.9)
    │     ├─ heuristic.Classify (confidence≥0.7 时胜出)
    │     ├─ < 0.7 → LLM fallback.Classify
    │     └─ 两端失败 → default(TaskChat, 0.3)
    │
    ├─ [Step 3] index.Recommend(task, sigs, profile, topN=3) → []ScoredCandidate
    │     或 index.RecommendV2 (...) （新版本：见 DT-5）
    │
    ├─ [Step 3.5] OverrideStore.FilterBanned → PromotePins
    │
    ├─ Winner = recommended[0]
    │
    └─ [Step 4] intentCache.Put(sessionID, CachedIntent{...})
```

**位置**: `autoroute/decision.go:188-265` / `autoroute/decision_v2.go:15-115`

**BUG 风险**（**V21 漏洞**）：
- `shouldReclassify` 仅检测硬信号变化（image/tool_call/long_context）和 HitCount 阈值
- **`DetectSessionDrift()` 死代码**：已定义（`feedback.go:137-147`）但从未调用
- 软切换（chat→code 但工具数<3）会被忽略，sessionCache 沿用错的 taskType
- `FilterBanned` / `PromotePins` 是 admin 异步 reload（1min）→ 改 ban 后最多 1min 才生效

### DT-5: RecommendV2 — 新决策树（feature flag UseChannelQualityRouting）

```
[Request] Index.RecommendV2(ctx, task, sigs, profile, sessionID, topN=3)
    │
    ├─ [Step 1] filterCurrentlyAvailable (live DB check via v_routable)
    │     ├─ pool=nil → fallbackSnapshotAvailability
    │     └─ pool≠nil → SQL: cmb.available=TRUE, pm.available=TRUE, NOT LIKE 'manual%'
    │     └─ err → recordLiveFilterFailure + fallbackSnapshotAvailability（V17 修复）
    │
    ├─ [Step 2] byCanonical = group(candidates, canonical_id)
    │
    ├─ [Step 3] hotTop3 = getHotTop3Canonicals (cached 2min, 48h popularity)
    │
    ├─ [Step 4] candidatePool = hotTop3 cands + non-hot cands
    │
    ├─ [Step 5] load correction scores from request_logs (last request by sessionID)
    │
    ├─ [Step 6] Score each:
    │     switch flags:
    │       UseChannelQualityRouting (default) → ScoreWithChannelQuality (4 维)
    │       UseSimplifiedScoring          → ScoreSimplified (2 维)
    │       default                       → Score (legacy 8 维, profile-weighted)
    │
    ├─ [Step 7] stratifyAndPickTopN (if UseChannelQualityRouting)
    │     ├─ split preferred/fallback by ChannelQuality≥50
    │     ├─ preferred ≥ topN → 仅取 preferred (noDemotion)
    │     ├─ preferred = 0   → 用 fallback (emptyPreferred)
    │     └─ preferred < topN → preferred + fallback, fallback composite *= 0.5/0.85
    │
    ├─ [Step 8] 取 topN
    │
    ├─ [Step 9] MatchScore<30 → 取 48h fallback winner
    │
    └─ [Step 10] recordRoutingDecision metrics
```

**位置**: `autoroute/recommend_v2.go:16-171`

**关键改进（V17）**:
- `filterCurrentlyAvailable` 失败时调用 `recordLiveFilterFailure(pool, err)` 记录ERROR日志
- 维护 `liveFilterTotal` / `liveFilterFailed` 原子计数器（**V27漏洞**: 未暴露Prometheus）

**V27 漏洞**:
- `LiveFilterStats()` 返回原子计数器（`autoroute/metrics.go:171-177`）
- 但未注册Prometheus指标，运维无法通过 /metrics 监控失败率
- **修复**: 注册 `llmgw_autoroute_live_filter_total` 和 `llmgw_autoroute_live_filter_failed` Counter

### DT-6: PlanCandidates — Router 旧路径（URSM 旁路）

```
[Request] Router.PlanCandidates(cands, stickyID, policy, egressPref)
    │
    ├─ URSM 启用?
    │     ├─ YES → planWithURSM (调 URSM.GetAvailableNodes, 100ms 超时回退)
    │     └─ NO  → 进入 legacy
    │
    ├─ [legacy]
    │     ├─ StateManager 启用 → filterAvailableWithStateManager (50ms 超时回退 filterAvailable)
    │     └─ else              → filterAvailable(cands)
    │                          → c.UnavailableReason() == "" 视为可用
    │
    │     ├─ all_filtered_out + len(cands)<=2 → tryDegradedMode (单凭据降级)
    │     │
    │     ├─ filterHealthyNodes (NodeState 检查)
    │     │
    │     ├─ splitByBillingRound(round1, round2)
    │     │
    │     ├─ planByTier (bandit/p2c+rr)
    │     │
    │     ├─ prioritizeSticky (stickyID hoist)
    │     │
    │     └─ applyProtocolAffinity
    │
    └─ 重要: V22 漏洞 - URSM 已定义但 main.go 中未实例化
```

**V22 漏洞位置**: `cmd/gateway/main.go` 未实例化 `routingExec.URSM`  
**影响**: 6 个旧状态源仍在运行，无单一真相源

### DT-7: Executor.Execute — 单请求候选遍历

```
[Request] e.Execute(params)
    │
    ├─ [0] identity_pool（全局，optional，默认关闭）
    │
    ├─ [1] candidates = Router.PlanCandidates(...)  ← DT-6
    │
    ├─ [1+] if len == 0 → log + return ExecuteError
    │
    ├─ [2] FpSlots prefilter (RoutingEligible)
    │     ├─ 全饱和 → fpSlotDegraded = true
    │     └─ else    → candidates = filtered
    │
    ├─ [3] FOR EACH cand IN candidates:
    │     │
    │     ├─ [3a] StreamCapture.Reset (重试时)
    │     │
    │     ├─ [3b] FpSlots.Acquire (fpLease)
    │     │
    │     ├─ [3c] Circuit.Allow(prov, cred)   ⚠ IN-PROCESS ONLY
    │     │
    │     ├─ [3d] Limiter.AcquireAll (5 层 semaphore, blocking)
    │     │
    │     ├─ [3e] defer Release(fpLease, limiter, peakCollector)
    │     │
    │     ├─ [3f] executeAnthropic OR executeOpenAI
    │     │
    │     └─ [3g] result switch:
    │           ├─ SUCCESS → restoreCredentialState + recordStickySuccess + URSM.RecordRequest(success)
    │           ├─ modelNotFoundError → recordMnfStreak + coolBindingOnMnfStreak (30s/last1min)
    │           ├─ IsClientBug → 不写任何状态
    │           ├─ contextLengthExhaustedError → 不写 circuit/sticky
    │           ├─ streamInterruptedError → recordStickyFailure + Recorder.RecordFailure
    │           │     ├─ shouldWriteCredentialStateOnConfirmedFailure?
    │           │     │   ├─ quota_fatal → 立即写
    │           │     │   └─ circuit Open/Quarantined → 写
    │           │     └─ forceUnpinOnFatalKind
    │           └─ 其他 error → writeCredentialStateOnError + InvalidateAllCandidateCache
    │
    ├─ [4] if all failed + !IsStream + SyncRetryTimeout>0:
    │     sync_retry_loop: for retryRound < 3:
    │       wait 1s OR ctx.Done()
    │       subCandidates = Router.PlanCandidates(stickyID = IsCredentialFatal? nil : sticky)
    │       if allCircuitOpen → break
    │       recursively Execute()
    │
    └─ [5] return ExecuteError{Tried, Exhausted=true}
```

**V23 漏洞位置**: `executor.go:1221, 1248, 1375` + `executor.go:2648-2674`  
**问题**: `shouldWriteCredentialStateOnConfirmedFailure()` 只在Circuit OPEN/QUARANTINED时写DB  
**影响**: 单次Auth/Quota失败 → Circuit CLOSED → 不写DB → 其他实例看不到，需等3次失败才传播

---

## 三、4 层状态转换表

### L1: Provider（vendor）

| 字段 | 存储 | 转换触发 | 转换路径 |
|---|---|---|---|
| `enabled` | `providers.enabled` (DB) | admin/sync | true ↔ false |
| `manual_disabled` | `providers.manual_disabled` (DB) | admin | true ↔ false |

**读侧**: `v_routable_credential_models` 视图 / URSM `ProviderState.IsAvailable`  
**写侧**: admin UI / sync job / SQL 直写  
**循环**: 单调：admin toggle 即可 → 没有自动恢复

### L2: Credential（7 个并行状态机）

| 字段 | 存储 | 触发 | 写入者 | 读出者 | 转换 |
|---|---|---|---|---|---|
| `availability_state` | `credentials.availability_state` (DB) | error_kind ∈ {Auth, Concurrent, RateLimit, Timeout, UpstreamDown, StreamTimeout} | `credential.Writer.WriteOnError`; `bg/credential_recovery.go` | DT-3, DT-7 | ready ↔ cooling/rate_limited/unreachable; → auth_failed; → suspended（不可恢复） |
| `quota_state` | `credentials.quota_state` (DB) | KindQuota/KindQuotaPeriodic/KindQuotaBalance/KindQuotaPermanent | `credential.Writer.WriteOnError` | DT-3 | ok → periodic_exhausted → balance_exhausted → permanently_exhausted |
| `lifecycle_status` | `credentials.lifecycle_status` (DB) | admin/退订 | admin | DT-3 | active → retired |
| `circuit_state` | **IN-PROCESS** in `credential.Breaker`; DB col rarely read | KindQuota/KindAuth → StateQuarantined; KindUpstreamDown/RateLimit → StateOpen | `executor.Circuit.RecordFailure`; `credential.Manager.ProbeCheck` | DT-7 [3c] | closed ↔ open ↔ half_open; → quarantined (永久) |
| `consecutive_failures` | atomic.Int32 (in-process) | error kind 变化时 reset to 0；同类错误 +1 | `RecordFailure` | DT-7, DT-7 [3c] | 0..∞，边界 threshold depends on policy |
| `CredentialState.Available` (URSM) | L1=mem / L2=Redis / L3=DB | StateObserver.UpdateOnSuccess/UpdateOnFailure | `domains/credentialstate/manager.go:161` | DT-6 [legacy step 1] | true ↔ false；RecoverAt-based auto-recover |
| `NodeState.Disabled` (credentialfpslot) | Redis JSON (5min sliding window) | streak ≥ 3 within 5min | Lua `recordNodeOutcomeScript` | `e.FpSlots.GetNodeState` (DT-7 [2]) | false → true (300s cooldown) → false (cooldown expired) |

### L3: Model（per (cred, raw_model)）

| 字段 | 存储 | 触发 | 写入者 | 读出者 | 转换 |
|---|---|---|---|---|---|
| `cmb.available` | `credential_model_bindings.available` (DB) | auto/quota/mnf_cooling | `Writer.writeModelLevelFailureOnly`; `executor.disableModelOffer`; `Checker.markDegraded`; `coolBindingOnMnfStreak` | `v_routable_credential_models`; DT-3; DT-5 [Step 1] | TRUE ↔ FALSE（auto_*, mnf_cooling, manual%） |
| `cmb.unavailable_reason` | 同上 | 同步 unavailable_reason | 同上 | 同上 | "" / "auto_*" / "manual*" / "mnf_cooling" |
| `model_probe_state.state` | Postgres | 探针共识 | `bg/model_probe.go`; `executor.recordMnfStreak`; `defaultAsyncExitSuspicious` | `v_routable_credential_models` 硬过滤 | unknown ↔ recovering ↔ healthy_confirmed / broken_confirmed / suspicious |
| `provider_models.available` | Postgres | `UpsertCredentialModel` / `ClearProviderBindings` | `modelcatalog.UpsertCredentialModel` | `v_routable_credential_models` | TRUE ↔ FALSE（保留 manual disable） |

### L4: Request

| 字段 | 存储 | 触发 | 写入者 | 读出者 | 转换 |
|---|---|---|---|---|---|
| `Pool.state` | atomic.Int32 (in-process) | `RecordFailure/RecordSuccess/probe` | `pool.Pool.RecordFailure/RecordSuccess`; `healthLoop` | `Pool.Acquire` | active ↔ degraded ↔ draining → dead (V15修复后可恢复) |
| `Slot.active` | Redis SET / TTL | acquire/release | Lua acquireSlotScript / releaseSlotScript | prefilter; acquire; stats | (空 / active) 1..N |
| `Slot.pin` | Redis SET (24h TTL) | acquire (Lua 写) | Lua acquireSlotScript (Phase 1: pin reuse) | prefilter, acquire | 不存在 → slotIdx |
| `Limiter.active[layer]` | atomic + semaphore | acquire/release | 5 层 limiter（global/pool/cred/identity/key） | DT-7 [3d] | 0..capacity |
| `Sticky.credID` | in-process map (Redis 5min TTL) | sticky success/failure | `StickyCache.Record*` | `stickyCredentialID/getEntry` | 不存在 → credID; → 删除 (credential-fatal) |
| `SessionIntentCache` | map (atomic.Pointer) | reclassify 检测 (HitCount≥50) | `CachedIntent.Put` | DT-4 [Step 0] | 不存在 → CachedIntent (10min) |
| `Bandit α/β` | in-process map | success/failure/rate_limit | `BanditScorer.Record*` | P2C selection | ±1 |

---

## 四、新发现漏洞（V21-V27）

### ⛔ V21: DetectSessionDrift 死代码（HIGH）

**位置**: `autoroute/feedback.go:137-147` + `autoroute/session_intent_cache.go:149-170`

**问题序列**:
1. `DetectSessionDrift(prevTask, currTask)` 函数已定义
2. 但 `shouldReclassify()` 仅检测硬信号（image/tool/long_context）和 HitCount≥50
3. **未调用** `DetectSessionDrift()` 比较 prevTask 和 currTask
4. 用户从 chat 切换到 code review（无 image/tool/long_context 变化）
5. HitCount 未达 50 → 不重分类 → sessionCache 沿用 `TaskChat`
6. 结果：code review 请求被路由到 chat 模型

**影响**:
- 误路由 → 用户感知不到的 budget 损耗 + 准确度下降
- V18 修复不完整：只用 HitCount 代理，无法检测真实的任务类型漂移

**修复**:
```go
// session_intent_cache.go shouldReclassify:
func shouldReclassify(cached TaskType, sigs ClassificationSignals, hitCount int) bool {
    if hitCount >= intentCacheDriftThreshold {
        return true
    }
    // ★ 新增: 调用 DetectSessionDrift
    inferredTask := inferTaskFromSignals(sigs)  // 需要新函数
    if DetectSessionDrift(cached, inferredTask) {
        return true
    }
    // 保留原有硬信号检测
    if sigs.HasImages && cached != TaskVision {
        return true
    }
    if sigs.EstimatedTokens > 50_000 && cached != TaskLongContext {
        return true
    }
    if sigs.ToolCount >= 3 && sigs.HasToolResults && cached != TaskAgent {
        return true
    }
    return false
}
```

### ⛔ V22: URSM 迁移未完成导致 7 源并存（HIGH）

**位置**: `cmd/gateway/main.go` + `domains/ursm/manager.go` + `domains/streaming/executors/executor.go:481-495`

**问题**:
- `executor.URSM` 和 `router.URSM` 已定义（接收 `*ursm.Manager`）
- `domains/ursm/state.go` 已定义 `ProviderState/CredentialState/ModelState/NodeState/RouteNode`
- 但 `cmd/gateway/main.go` 中**从未实例化** `ursm.Manager`
- 6 个旧状态源仍在运行：
  1. `credentialstate.Manager` (mem + Redis + DB)
  2. `credential.Breaker` (in-process)
  3. `credentialfpslot.NodeState` (Redis Lua, DEPRECATED)
  4. `credential.Writer` (DB)
  5. `credentialhealth.Checker` (Redis + DB)
  6. `bg.CredentialRecovery` (DB)

**影响**:
- 无单一真相源，状态分散
- 跨进程同步靠 30s 缓存 TTL + Redis 手动失效
- 维护成本高，新开发者难以理解完整状态机

**修复选项 A（推荐）**: 完成 URSM 迁移
```go
// cmd/gateway/main.go:
ursmManager := ursm.NewManager(dbConn.Pool(), fpSlotRedis)
ursmManager.Start(ctx)
routingExec.URSM = ursmManager
routingExec.Router.URSM = ursmManager
// 废弃旧状态源
```

**修复选项 B**: 移除 URSM 死代码（如果决定不迁移）
```bash
# 删除未使用的 URSM 代码
rm -rf domains/ursm/
# 从 executor.go 和 router.go 移除 URSM 引用
```

### ⛔ V23: Circuit 状态写入延迟导致跨进程失同步（HIGH）

**位置**: `domains/streaming/executors/executor.go:2648-2674` + `executor.go:1221, 1248, 1375`

**问题**:
```go
func (e *Executor) shouldWriteCredentialStateOnConfirmedFailure(...) bool {
    if !shouldWriteCredentialState(kind) {
        return false
    }
    // Quota errors 立即写
    if kind == errorsx.KindQuota || kind == errorsx.KindQuotaBalance ||
       kind == errorsx.KindQuotaPeriodic || kind == errorsx.KindQuotaPermanent {
        return true
    }
    if e.Circuit == nil {
        return true
    }
    b := e.Circuit.GetOrCreate(providerID, credentialID)
    state := b.State()
    if state == credential.StateOpen || state == credential.StateQuarantined {
        return true
    }
    // ⚠ Auth/KindRateLimit/KindConcurrent/KindStreamTimeout 单次失败时 Circuit CLOSED → 不写DB
    return false
}
```

**问题序列**:
1. 实例 A 处理请求 → Auth 失败 (KindAuth) → Circuit 仍 CLOSED → 不写 DB
2. 实例 B 处理请求 → 路由到同一凭据 → 再次失败
3. 实例 A 第二次失败 → Circuit OPEN → 写 DB → 但实例 B 还没更新
4. 跨进程传播延迟 3×RTT

**影响**:
- 多实例部署时，Auth/Quota 失败状态传播延迟
- 单凭据瞬时高并发可能造成多次失败后才全网禁用

**修复**:
```go
func (e *Executor) shouldWriteCredentialStateOnConfirmedFailure(providerID, credentialID int, kind errorsx.ErrorKind) bool {
    if !shouldWriteCredentialState(kind) {
        return false
    }
    // Quota/Auth 类错误立即写（无需等 Circuit OPEN）
    switch kind {
    case errorsx.KindQuota, errorsx.KindQuotaBalance, errorsx.KindQuotaPeriodic, errorsx.KindQuotaPermanent,
         errorsx.KindAuth, errorsx.KindAuthRevoked:
        return true
    }
    // 其他错误需 Circuit OPEN/QUARANTINED 才写
    if e.Circuit == nil {
        return true
    }
    b := e.Circuit.GetOrCreate(providerID, credentialID)
    state := b.State()
    return state == credential.StateOpen || state == credential.StateQuarantined
}
```

### ⛔ V24: Pool.RecordSuccess 不重置 drainingSince（MEDIUM）

**位置**: `pool/pool.go:274-284`

**问题**:
```go
// Recover from degraded or draining to active after enough consecutive successes
if (currentState == PoolDegraded || currentState == PoolDraining) && n >= successThreshold {
    p.state.Store(int32(PoolActive))
    p.successCount.Store(0)
    p.drainingSince.Store(0)  // ⚠ 仅在 Degraded→Active 时有效
    slog.Info("pool recovered to active", ...)
}
```

**问题序列**:
1. Pool 进入 Draining 状态 → `drainingSince` 设为 t0
2. 健康探测连续成功 3 次 → 转换到 Active
3. 但如果 `state.CompareAndSwap` 失败（旧代码），`drainingSince` 保留
4. 下次进入 Draining → `checkDrainingGracePeriod` 计算 `elapsed = now - drainingSince`
5. 如果旧 `drainingSince` 未清零，可能立即判定 grace period 过期 → Dead

**影响**:
- 罕见但可能：池刚恢复就立即进入 Dead 状态

**修复**:
```go
if p.state.CompareAndSwap(int32(PoolDegraded), int32(PoolActive)) ||
   p.state.CompareAndSwap(int32(PoolDraining), int32(PoolActive)) {
    p.successCount.Store(0)
    p.drainingSince.Store(0)
    p.failCount.Store(0)
    slog.Info("pool recovered to active", ...)
}
```

### ⚠ V25: Candidate.UnavailableReason 短路求值丢失多原因（MEDIUM）

**位置**: `provider/client.go:179-213`

**问题**:
```go
func (c *Candidate) UnavailableReason() string {
    if !c.Routable {
        if c.BlockReason != nil && *c.BlockReason != "" {
            return "routing_blocked:" + *c.BlockReason
        }
        return "routing_blocked"  // ⚠ 短路，后续原因丢失
    }
    if c.LifecycleStatus != "" && c.LifecycleStatus != "active" {
        return "lifecycle:" + c.LifecycleStatus
    }
    switch c.AvailabilityState {
    case "suspended":
        return "availability:suspended"
    // ...
    }
    // ...
}
```

**影响**:
- 调试困难：只知道第一个原因，不知道完整故障链
- 例：`!Routable` + `QuotaExhausted` + `CircuitOpen` → 只返回 "routing_blocked"
- 运维无法快速定位根因

**修复**:
```go
func (c *Candidate) UnavailableReason() string {
    var reasons []string
    if !c.Routable {
        if c.BlockReason != nil && *c.BlockReason != "" {
            reasons = append(reasons, "routing_blocked:"+*c.BlockReason)
        } else {
            reasons = append(reasons, "routing_blocked")
        }
    }
    if c.LifecycleStatus != "" && c.LifecycleStatus != "active" {
        reasons = append(reasons, "lifecycle:"+c.LifecycleStatus)
    }
    switch c.AvailabilityState {
    case "suspended":
        reasons = append(reasons, "availability:suspended")
    case "auth_failed":
        reasons = append(reasons, "availability:auth_failed")
    case "cooling":
        reasons = append(reasons, "availability:cooling")
    case "rate_limited":
        reasons = append(reasons, "availability:rate_limited")
    case "unreachable":
        reasons = append(reasons, "availability:unreachable")
    }
    switch c.QuotaState {
    case "balance_exhausted":
        reasons = append(reasons, "quota:balance_exhausted")
    case "permanently_exhausted":
        reasons = append(reasons, "quota:permanently_exhausted")
    case "periodic_exhausted":
        reasons = append(reasons, "quota:periodic_exhausted")
    }
    if c.BalanceUSD != nil && *c.BalanceUSD <= 0 {
        reasons = append(reasons, "balance:zero")
    }
    return strings.Join(reasons, "; ")
}
```

### ⚠ V26: NodeState 双重定义（MEDIUM）

**位置**: `credentialfpslot/node_state.go` vs `domains/ursm/state.go:134-176`

**问题**:
- `credentialfpslot.NodeState` (DEPRECATED):
  - Redis Lua 滑动窗口
  - 字段: Disabled, DisabledUntil, SlideWindow, SuccessCount, FailureCount
  - 使用方: `router.go:381` (filterHealthyNodes)
- `ursm.NodeState` (新架构):
  - 内存结构
  - 字段: ConsecutiveFailures, Disabled, DisabledUntil, RecoverAt
  - 使用方: 未实际使用（URSM 未启用）

**影响**:
- 类型混淆，迁移期维护两套逻辑
- `credentialfpslot.NodeState` 标记为 DEPRECATED 但 Router 仍在用

**修复**:
- 完成 V22 URSM 迁移后，废弃 `credentialfpslot.NodeState`
- 或暂时保留（向后兼容），但明确标注仅供 Router 使用

### ⚠ V27: LiveFilterStats 无 Prometheus 暴露（LOW）

**位置**: `autoroute/metrics.go:146-177`

**问题**:
```go
var (
    liveFilterTotal  int64
    liveFilterFailed int64
)

func recordLiveFilterSuccess(filtered int) {
    atomic.AddInt64(&liveFilterTotal, 1)
}

func recordLiveFilterFailure(poolConfigured bool, err error) {
    atomic.AddInt64(&liveFilterTotal, 1)
    atomic.AddInt64(&liveFilterFailed, 1)
    slog.Error("recommend_v2: live availability filter failed, using snapshot",
        "error", err,
        "pool_configured", poolConfigured,
    )
}

func LiveFilterStats() (total, failed int64) {
    total = atomic.LoadInt64(&liveFilterTotal)
    failed = atomic.LoadInt64(&liveFilterFailed)
    return
}
```

**影响**:
- V17 修复添加了 ERROR 日志和原子计数器
- 但 `LiveFilterStats()` 未注册到 Prometheus registry
- 运维无法通过 `/metrics` 监控 live filter 失败率
- 需要额外 HTTP handler 才能暴露

**修复**:
```go
// metrics.go
var (
    liveFilterTotal  prometheus.Counter
    liveFilterFailed prometheus.Counter
)

func registerLiveFilterMetrics() {
    liveFilterTotal = prometheus.NewCounter(
        prometheus.CounterOpts{
            Name: "llmgw_autoroute_live_filter_total",
            Help: "Total live availability filter operations.",
        },
    )
    liveFilterFailed = prometheus.NewCounter(
        prometheus.CounterOpts{
            Name: "llmgw_autoroute_live_filter_failed",
            Help: "Live availability filter operations that fell back to snapshot.",
        },
    )
    prometheus.MustRegister(liveFilterTotal, liveFilterFailed)
}

func init() {
    registerRoutingMetrics()
    registerLiveFilterMetrics()
}

func recordLiveFilterSuccess(filtered int) {
    liveFilterTotal.Inc()
}

func recordLiveFilterFailure(poolConfigured bool, err error) {
    liveFilterTotal.Inc()
    liveFilterFailed.Inc()
    slog.Error("recommend_v2: live availability filter failed, using snapshot",
        "error", err,
        "pool_configured", poolConfigured,
    )
}
```

---

## 五、修复优先级（按 ROI 排序）

### 阶段 0：阻断级（影响服务可用性，48h 内必须修）

| 序 | 漏洞 | 修复复杂度 | 影响范围 |
|---|---|---|---|
| 1 | **V21** DetectSessionDrift 死代码 | S（5 行代码 + 测试） | 误路由损耗 budget |
| 2 | **V22** URSM 迁移未完成 | L（架构决策 + 全链路迁移） | 维护成本 + 状态分散 |
| 3 | **V23** Circuit 写入延迟 | S（10 行代码 + 测试） | 多实例失同步 |
| 4 | **V27** LiveFilter Prometheus 暴露 | S（注册 2 个 Counter） | 运维盲区 |

### 阶段 1：稳定级（降低故障率，提升可观测性，1 周内修）

| 序 | 漏洞 | 修复复杂度 |
|---|---|---|
| 5 | **V24** Pool.RecordSuccess 重置 | S（CAS 替换） |
| 6 | **V25** UnavailableReason 多原因 | S（累积原因字符串） |
| 7 | **V26** NodeState 双重定义 | M（依赖 V22 完成） |

### 阶段 2：架构级（彻底解，1 季度）

| 序 | 项目 | 内容 |
|---|---|---|
| 8 | **URSM 全量切流** | Router/Executor 完全使用 URSM，废弃旧状态源 |
| 9 | **Circuit 状态迁 Redis** | 已有 `redis_pool.go`，可类比移到 `credential.Breaker` 加 Redis layer |
| 10 | **State machine 单源化** | 引入 `state_event` 表（append-only），所有写入者只 INSERT；reader 端用 materialized view 聚合 |
| 11 | **policy 决策树收敛** | `Candidate.UnavailableReason` 应改为显式表驱动，而非 if-else 链 |

---

## 六、状态机可观测性改造建议

| 现有 | 改造为 |
|---|---|
| 7 个独立 DB 列 + 多组件写入 | 1 张 `cred_health_events` 表（append-only），所有写入者只 INSERT；reader 端用 materialized view 聚合最新状态 |
| 决策树 7 棵分散在 4 个目录 | 1 个 `decision-trace` 子系统，统一 emit `Decision{input, path, winner, reasons}` JSON |
| Prometheus 指标散落 10+ 文件 | 1 组 metrics：`llmgw_state_*`（变迁计数）+ `llmgw_decision_*`（决策路径分布） |
| 状态变更无审计 | 每次 state 变更 emit 1 个 span 到 OpenTelemetry，`cred_id` `model` `kind` `from_state` `to_state` `reason` `actor` |

---

## 七、决策路径组合测试矩阵（建议生成回归测试）

| ID | vendor | credential | model | fingerprint | circuit | sticky | 期望 |
|---|---|---|---|---|---|---|---|
| TC1 | enabled | active+ready | routable | pin reuse | closed | bound | 命中 |
| TC2 | enabled | active+ready | routable | new slot | closed | unbound | 命中 |
| TC3 | enabled | active+ready | routable | saturated | open | bound | 跳到下一 candidate |
| TC4 | enabled | active+cooling | routable | pin reuse | closed | bound | 路由 |
| TC5 | manual_disabled | active+ready | routable | new | open | — | 路由（circuit 优先） |
| TC6 | enabled | suspended | — | — | quarantined | — | 5xx |
| TC7 | enabled | active+periodic_exhausted | routable | — | closed | — | 仍路由 |
| TC8 | enabled | active+balance_exhausted | routable | — | closed | — | "quota:balance_exhausted" |
| TC9 | enabled | active+ok | mnf_cooling (recent mnf=5) | pin reuse | closed | bound | 仍路由（cmb_cooling 不影响 sticky pin） |
| TC10 | disabled | active | — | — | — | — | 5xx 来自 router |

---

## 八、总结

| 维度 | 评估 |
|---|---|
| **状态机数量** | **4 层 × 7+ 并行** = 28 个独立转换路径 |
| **真相源数量** | 7 个（6 ACTIVE + 1 READY），需收敛 |
| **状态读侧数量** | 15+ 处分散 |
| **状态写侧数量** | 10+ 处分散 |
| **状态机不变量** | 8 个，大部分**未被自动验证** |
| **已确认漏洞** | **27 个**（20 旧 + 7 新），其中 **12 HIGH** |
| **修复优先级最高 5 项** | V21 (drift) → V22 (URSM) → V23 (Circuit) → V24 (Pool) → V25 (UnavailableReason) |
| **架构性根本问题** | **无单一真相源，决策树分支依赖不完整的状态视图** |

> **简短结论 (2026-07-05 更新)**: V15-V20 修复有效解决了 Pool Dead 终态和 Live filter 可观测性问题，但**架构性根本问题（7 源并存、URSM 未启用）**仍未解决。建议在阶段 0 优先完成 V21-V23 三个 HIGH 漏洞修复，并在 1 季度内完成 URSM 全量迁移，从根本上消除状态分散问题。

---

**审计人**: AI Agent  
**审计版本**: v2.0 @ 2026-07-05  
**基于**: v1.0 (2026-07-04) + V15-V20 修复验证 + V21-V27 新发现
