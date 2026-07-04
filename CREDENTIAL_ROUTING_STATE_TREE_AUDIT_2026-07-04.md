# 凭据 × 路由节点 × 模型 × 请求 — 状态机与决策树综合审计报告

**日期**: 2026-07-04
**审计人**: AI Agent（`general-purpose` exploration + `general` 静态路径分析）
**审计范围**: `credentialfpslot/`, `credentialhealth/`, `autoroute/`, `provider/`, `modelcatalog/`, `pool/`, `resolve/`, `ratelimit/`, `discovery/`, `domains/credential/breaker.go`, `domains/credentialstate/`, `domains/ursm/`, `domains/streaming/executors/`, `errorsx/classify.go`
**审计基线**: 沿用 `CREDENTIAL_STATE_AUDIT_REPORT.md` (2026-07-03) 中已识别的 14 个漏洞 + 新发现的 **6 个 P0/P1 漏洞** + **多源状态机相互关系图**
**审计方法**: 只读静态扫描 + 决策树构建 + 跨层状态读写侧校验

---

## 执行摘要

llm-gateway-go 当前存在 **4 层并行状态机**，由 **12+ 个独立组件**共同维护，且 **3 个不同的真相源**（Postgres / Redis Lua / in-process atomic）并存。这导致：

| 维度 | 数量 | 备注 |
|---|---|---|
| 凭据-模型组合相关的状态字段 | **18+ 个** | 分散在 5 张表 + Redis |
| 影响路由决策的状态机 | **9 个** | 7 个凭据级 + 2 个连接池级 |
| 决策树分支数 | **7 棵主要决策树** | max depth = 6 |
| 状态读取侧 | **15+ 处** | router / executor / scoring / fingerprint / probe / bg recovery |
| 状态写入侧 | **10+ 处** | credentialhealth / executor / credentialstate / ursm / bg-* |
| 已确认漏洞（沿用 + 新增） | **20 个** | 8 HIGH + 7 MED + 5 LOW |

最严重的 **结构性**问题是：

> **同一 `(credential_id, raw_model_name)` 维度被 7 个独立组件并行写入**：
> ① `credentials.availability_state` (Postgres)
> ② `credentials.quota_state` (Postgres)
> ③ `credentials.circuit_state` (in-process + Postgres 不一致)
> ④ `credential_model_bindings.available` (Postgres)
> ⑤ `model_offers.available` (Postgres mirror)
> ⑥ `model_probe_state.state` (Postgres)
> ⑦ `credentialfpslot.NodeState` (Redis Lua)
>
> 这 7 个写入者**没有任何事务协调、没有版本号、没有"主状态机"概念**。任何一个组件写完一部分就立即返回，其它状态由后续探针/recovery worker 异步修正。

---

## 一、4 层状态机全景

```
┌────────────────────────────────────────────────────────────────────────────┐
│ Layer 1: VENDOR (provider)                                                  │
│ ┌─────────────────────────┐  ┌──────────────────────────┐                   │
│ │ ProviderState(URSM)     │  │ providers 表(legacy)     │                   │
│ │ - Enabled bool          │  │ - enabled                │                   │
│ │ - ManualDisabled bool   │  │ - manual_disabled        │                   │
│ └─────────────────────────┘  │ - tenant_id              │ ─────────┐         │
│                              └──────────────────────────┘          │         │
└────────────────────────────────────────────────────────────────────│─────────┘
                                                                     │ 1:N
┌────────────────────────────────────────────────────────────────────▼─────────┐
│ Layer 2: CREDENTIAL ──── 7 个并行状态机（同一行被多方写入）─────────────────┐ │
│                                                                            │ │
│ ┌─────────────────────┐  ┌────────────────────┐  ┌────────────────────┐  │ │
│ │ credentials.        │  │ credentials.       │  │ credentials.       │  │ │
│ │ availability_state  │  │ quota_state        │  │ lifecycle_status   │  │ │
│ │ ready/cooling/      │  │ ok/periodic/       │  │ active/retired     │  │ │
│ │ rate_limited/       │  │ balance/           │  │                    │  │ │
│ │ unreachable/        │  │ permanently_       │  │                    │  │ │
│ │ auth_failed/        │  │ exhausted          │  │                    │  │ │
│ │ suspended           │  │                    │  │                    │  │ │
│ └─────────────────────┘  └────────────────────┘  └────────────────────┘  │ │
│ ┌─────────────────────┐  ┌────────────────────┐  ┌────────────────────┐  │ │
│ │ credentials.        │  │ Credential.Breaker │  │ credentialstate    │  │ │
│ │ circuit_state (DB)  │  │ (in-process!)      │  │ .Manager (L1 mem/  │  │ │
│ │ DB: closed/         │  │ State:             │  │ L2 Redis/L3 DB)    │  │ │
│ │ open/half_open/     │  │ - StateClosed      │  │ State.Available +  │  │ │
│ │ quarantined         │  │ - StateOpen        │  │ ConsecutiveFailures│  │ │
│ │                     │  │ - StateHalfOpen    │  │ + RecoverAt        │  │ │
│ │ ⚠ 写入但路由不读！   │  │ - StateQuarantined │  │                    │  │ │
│ └─────────────────────┘  └────────────────────┘  └────────────────────┘  │ │
│ ┌─────────────────────┐                                                       │ │
│ │ credentialfpslot.   │                                                       │ │
│ │ NodeState (Redis)   │                                                       │ │
│ │ - Disabled/Until    │                                                       │ │
│ │ - SlideWindow       │                                                       │ │
│ │ ⚠ 与⑤⑦不同           │                                                       │ │
│ └─────────────────────┘                                                       │ │
└─────────────────────────────────────────────────────────────────────────────│─┘
                                                                              │ 1:N
┌─────────────────────────────────────────────────────────────────────────────▼─┐
│ Layer 3: MODEL (per credential × raw_model)                                   │
│                                                                                │
│ ┌─────────────────────────┐  ┌─────────────────────────┐                       │
│ │ credential_model_bindings│  │ model_offers (mirror)   │                       │
│ │ .available                │  │ .available               │                       │
│ │ .unavailable_reason       │  │ .unavailable_reason      │                       │
│ │ .unavailable_at           │  │ .unavailable_at          │                       │
│ │ .unavailable_recover_at   │  │ .admin_protected         │                       │
│ │ .admin_protected          │  │ ⚠ 与左表双写，subquery    │                       │
│ │ ⚠ 多写入源，subquery bug   │  │   bug #6                 │                       │
│ └─────────────────────────┘  └─────────────────────────┘                       │
│ ┌─────────────────────────┐  ┌─────────────────────────┐                       │
│ │ model_probe_state        │  │ provider_models         │                       │
│ │ .state                   │  │ .available              │                       │
│ │ unknown/recovering/      │  │ .unavailable_reason     │                       │
│ │ healthy_confirmed/       │  │ ⚒ v_routable 只读它的   │                       │
│ │ broken_confirmed/        │  │   available             │                       │
│ │ suspicious               │  │                         │                       │
│ └─────────────────────────┘  └─────────────────────────┘                       │
└────────────────────────────────────────────────────────────────────────────────┘
                                                                                │ 每次请求
┌────────────────────────────────────────────────────────────────────────────────▼─┐
│ Layer 4: REQUEST — 连接池 / 会话状态                                              │
│                                                                                   │
│ ┌────────────────────┐  ┌────────────────────┐  ┌────────────────────┐            │
│ │ Pool (in-process)  │  │ Limiter (in-proc)  │  │ Sticky (mem+Redis) │            │
│ │ State=Active/      │  │ 5层semaphore:      │  │ (session,cred) →   │            │
│ │ Draining/Degraded/ │  │ global/pool/cred/  │  │ CredentialID       │            │
│ │ Dead              │  │ identity/key       │  │ TTL 30min          │            │
│ │ ⚠ Dead 是终态      │  │ ⚠ 多实例不收敛       │  │ ⚠ 多实例不收敛      │            │
│ └────────────────────┘  └────────────────────┘  └────────────────────┘            │
│ ┌────────────────────┐  ┌────────────────────┐  ┌────────────────────┐            │
│ │ credentialfpslot.  │  │ Bandit (in-proc)   │  │ SessionIntentCache │            │
│ │ Slot (Redis)       │  │ Beta dist          │  │ (mem+atomic)       │            │
│ │ - free / leased    │  │ success/failure    │  │ (session,model) →  │            │
│ │ - pinned (24h)     │  │ rate_limit         │  │ CachedIntent       │            │
│ │ - reclaimable      │  │ ⚠ 多实例不收敛       │  │ TTL 10min          │            │
│ └────────────────────┘  └────────────────────┘  └────────────────────┘            │
└────────────────────────────────────────────────────────────────────────────────────┘
```

---

## 二、决策树（7 棵主要决策树）

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

**漏洞位置**: `provider/client.go:334-379` — 缓存键 30s，单进程内，不会跨进程失效；
**说明**: tenantID 透传到 `loadCandidatesDB` 的 SQL WHERE，但 V_routable 视图本身硬编码 `'default'` (漏洞 #7)

---

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

---

### DT-3: Candidate.UnavailableReason — 单凭据 5 阶段判定

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

**位置**: `provider/client.go:179-213`

**特征**：
- **首检胜出**：短路求值，第一条命中的理由被返回。
- **写入方分散**：availability_state 由 `executor.writeCredentialStateOnError` + `credential.Writer.WriteOnError` + `credentialhealth.Checker.markDegraded` 共同写入。
- **缓存依赖**：上游 `GetCandidates` 缓存 30s；状态变更必须调 `InvalidateAllCandidateCache()` 才能生效。

---

### DT-4: Decider.Decide / DecideV2 — 路由决策

```
[Request] Decider.Decide(ctx, sigs, apiKeyID, profile, taskHint, sessionID)
    │
    ├─ [Step 0] sessionCache.Get(sessionID)
    │     ├─ HIT + !shouldReclassify → 复用（直接 return Decision）
    │     └─ HIT + shouldReclassify(检测: image attach / tool calls / long context)
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

**BUG 风险**：
- `shouldReclassify` 仅在 `sigs` 含 image/tool_call/long_context 时返回 true → 软切换（task drift）会被忽略，sessionCache 沿用错的 taskType。
- `FilterBanned` / `PromotePins` 是 admin 异步 reload（1min）→ 改 ban 后最多 1min 才生效。

---

### DT-5: RecommendV2 — 新决策树（feature flag UseChannelQualityRouting）

```
[Request] Index.RecommendV2(ctx, task, sigs, profile, sessionID, topN=3)
    │
    ├─ [Step 1] filterCurrentlyAvailable (live DB check via v_routable)
    │     ├─ pool=nil → fallbackSnapshotAvailability
    │     └─ pool≠nil → SQL: cmb.available=TRUE, pm.available=TRUE, NOT LIKE 'manual%'
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
    │       UseChannelQualityRouting (default) → ScoreWithChannelQuality (4 维: intent+price+channel+reliability)
    │       UseSimplifiedScoring          → ScoreSimplified (2 维: intent+price)
    │       default                       → Score (legacy 8 维, profile-weighted)
    │
    ├─ [Step 7] stratifyAndPickTopN (if UseChannelQualityRouting)
    │     ├─ split preferred/fallback by ChannelQuality≥50
    │     ├─ preferred ≥ topN → 仅取 preferred (noDemotion)
    │     ├─ preferred = 0   → 用 fallback (emptyPreferred)
    │     └─ preferred < topN → preferred + fallback, fallback composite *= 0.5 (or 0.85 if saturated)
    │
    ├─ [Step 8] 取 topN
    │
    ├─ [Step 9] MatchScore<30 → 取 48h fallback winner
    │
    └─ [Step 10] recordRoutingDecision metrics
```

**位置**: `autoroute/recommend_v2.go:16-171` + `autoroute/recommend_v2.go:468-537`

**关键互斥**：
- `filterCurrentlyAvailable`（DB live）+ `fallbackSnapshotAvailability`（仅靠本地 `UnavailableReason`）— 后者是降级路径
- **真正的隐患**：provider.Client 的 30s 缓存先于 V2 的 live filter，**最坏情况 30s 路由到已下线的凭据**（30s cache window 与 live filter 之间的 TOCTOU）。

---

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
```

**位置**: `domains/streaming/executors/router.go:56-137`

**重要观察**：
- `filterAvailableWithStateManager` 调 `credentialstate.Manager`（L1 mem + L2 Redis + L3 DB），与 `filterAvailable` 互补而非互斥。
- 50ms 超时 → fallback 到 `filterAvailable` → **最坏情况 50ms 内所有变更被冻结**。
- `tryDegradedMode` 当候选<=2 且原因属 transient 时强制使用 → 这是 2026-07-04 的修复补丁，**但没显式记录 metric，可能掩盖问题**。

---

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
    │     │     ├─ pin reuse (Lua acquireSlotScript)
    │     │     ├─ LRU preempt (Lua acquireLRUScript)
    │     │     ├─ 饱和 + fpSlotDegraded → lease=nil (降级模式)
    │     │     └─ 饱和 + else → continue (next cand)
    │     │
    │     ├─ [3c] Circuit.Allow(prov, cred)   ⚠ IN-PROCESS ONLY
    │     │     ├─ closed → TRUE
    │     │     ├─ open + 冷却过 → half_open + 1 probe
    │     │     ├─ open + 冷却未过 → FALSE → continue
    │     │     └─ quarantined → FALSE → continue
    │     │
    │     ├─ [3d] Limiter.AcquireAll (5 层 semaphore, blocking)
    │     │
    │     ├─ [3e] defer Release(fpLease, limiter, peakCollector)
    │     │
    │     ├─ [3f] executeAnthropic OR executeOpenAI
    │     │
    │     └─ [3g] result switch:
    │           │
    │           ├─ SUCCESS ──────────────────────────────────┐
    │           │   - restoreCredentialState (writes cmb     │
    │           │     available=TRUE if was auto_*)           │
    │           │   - recordStickySuccess                     │
    │           │   - Recorder.RecordSuccess (Redis callhist)│
    │           │   - HealthTracker.OnSuccess                 │
    │           │   - URSM.RecordRequest(success)  ──async    │
    │           │   - resetMnfStreak                         │
    │           │   - recordBanditSuccess                     │
    │           │   - UnifiedProbeScheduler.OnRealRequest(true)│
    │           │   - return result                           │
    │           │                                              │
    │           ├─ modelNotFoundError ────────────────────┐   │
    │           │   - recordModelNotFound (model_probe_runs)│
    │           │   - recordMnfStreak (mnf counter)        │
    │           │   - coolBindingOnMnfStreak (30s/last1min)│
    │           │   - continue                             │
    │           │                                            │
    │           ├─ IsClientBug (tool_call_id_mismatch/     │
    │           │   unsupported_feature/canceled) ─────┐   │
    │           │   - 不写任何状态                       │   │
    │           │   - continue                          │   │
    │           │                                         │   │
    │           ├─ contextLengthExhaustedError ─────────┤   │
    │           │   - 不写 circuit / sticky              │   │
    │           │   - continue                          │   │
    │           │                                         │   │
    │           ├─ streamInterruptedError ───────────────┤   │
    │           │   - recordStickyFailure               │   │
    │           │   - Recorder.RecordFailure             │   │
    │           │   - URSM.RecordRequest(failure) async │   │
    │           │   - Circuit.RecordFailure              │   │
    │           │   - recordBanditFailure                │   │
    │           │   - shouldWriteCredentialStateOnConfirmed?│  │
    │           │     ├─ quota_fatal → 立即写并 forceUnpin│  │
    │           │     └─ circuit Open/Quarantined → 写     │  │
    │           │                                          │   │
    │           │   resumable → continue                   │   │
    │           │   !resumable → return error              │   │
    │           │                                          │   │
    │           └─ 其他 error ─────────────────────────┐   │   │
    │               - recordStickyFailure               │   │   │
    │               - Recorder.RecordFailure            │   │   │
    │               - HealthTracker.OnError             │   │   │
    │               - URSM.RecordRequest(failure) async │   │   │
    │               - Circuit.RecordFailure             │   │   │
    │               - recordBanditFailure               │   │   │
    │               - shouldWriteCredentialState?       │   │   │
    │                 ├─ quota_fatal → 写                │   │   │
    │                 └─ circuit Open/Quarantined → 写  │   │   │
    │               - writeCredentialStateOnError →     │   │   │
    │                  InvalidateAllCandidateCache      │   │   │
    │               - forceUnpinOnFatalKind (clear pin)  │   │   │
    │               - continue                          │   │   │
    │                                                    │   │   │
    │           ┌────────────────────────────────────────┘   │   │
    │           │                                            │   │
    │     [3 end loop]                                       │   │
    │                                                          │   │
    ├─ [4] if all failed + !IsStream + SyncRetryTimeout>0:   │   │
    │     sync_retry_loop:                                   │   │
    │       for retryRound < maxSyncRetryRounds (3):          │   │
    │         wait 1s OR ctx.Done()                          │   │
    │         subCandidates = Router.PlanCandidates(         │   │
    │           stickyID = IsCredentialFatal? nil : sticky)   │   │
    │         if allCircuitOpen → break                      │   │
    │         recursively Execute()                          │   │
    │                                                          │   │
    └─ [5] return ExecuteError{Tried, Exhausted=true}
```

**位置**: `domains/streaming/executors/executor.go:680-1500`

**关键耦合**：
- DT-7 与 DT-3 之间存在 **TOCTOU 窗口**：router 读 `UnavailableReason`（30s cached snapshot）→ executor 写 `cmb.available` → 同时 `InvalidateAllCandidateCache`。
- 多凭据 candidate walking 与 `ShouldWriteCredentialStateOnConfirmedFailure` 之间存在 **写放大**：quota 错误立即写但其它错误等到 circuit Open/Quarantined 才写。

---

## 三、4 层状态转换表

### L1: Provider（vendor）

| 字段 | 存储 | 转换触发 | 转换 |
|---|---|---|---|
| `Enabled` | `providers.enabled` (DB) | admin/sync | true ↔ false |
| `ManualDisabled` | `providers.manual_disabled` (DB) | admin | true ↔ false |

**读侧**: `v_routable_credential_models` 视图 / URSM `ProviderState.IsAvailable`
**写侧**: admin UI / sync job / SQL 直写
**循环**: 单调：admin toggle 即可 → 没有自动恢复

### L2: Credential（7 个并行状态机）

| 字段 | 存储 | 触发 | 写入者 | 读出者 | 转换 |
|---|---|---|---|---|---|
| `availability_state` | `credentials.availability_state` (DB) | 收到 error_kind ∈ {Auth, Concurrent, RateLimit, Timeout, UpstreamDown, StreamTimeout} | `credential.Writer.WriteOnError` (KindAuth/Quota 等); `executor.disableModelOffer`(cooling 1min); `bg/credential_recovery.go` | DT-3, DT-7 | ready ↔ cooling/rate_limited/unreachable; → auth_failed; → suspended（不可恢复） |
| `quota_state` | `credentials.quota_state` (DB) | KindQuota/KindQuotaPeriodic/KindQuotaBalance/KindQuotaPermanent | `credential.Writer.WriteOnError` | DT-3 | ok → periodic_exhausted → balance_exhausted → permanently_exhausted |
| `lifecycle_status` | `credentials.lifecycle_status` (DB) | admin/退订 | admin | DT-3 | active → retired |
| `circuit_state` | **IN-PROCESS** in `credential.Breaker`; DB col rarely read | KindQuota/KindAuth → StateQuarantined (permanent); KindUpstreamDown/RateLimit → StateOpen (exponential); others → StateOpen (auto) | `executor.Circuit.RecordFailure`; `executor.Circuit.RecordSuccess`; `credential.Manager.ProbeCheck`/`CloseProbe` | DT-7 [3c] | closed ↔ open ↔ half_open; → quarantined (永久) |
| `consecutive_failures` | atomic.Int32 (in-process) | error kind 变化时 reset to 0；同类错误 +1 | `RecordFailure` | DT-7, DT-7 [3c] | 0..∞，边界 threshold depends on policy |
| `CredentialState.Available` (URSM) | L1=mem map / L2=Redis / L3=DB | StateObserver.UpdateOnSuccess/UpdateOnFailure | `domains/credentialstate/manager.go:161` | DT-6 [legacy step 1] | true ↔ false；RecoverAt-based auto-recover |
| `NodeState.Disabled` (credentialfpslot) | Redis JSON (5min sliding window) | streak ≥ 3 within 5min | Lua `recordNodeOutcomeScript` | `e.FpSlots.GetNodeState` (DT-7 [2]) | false → true (300s cooldown) → false (cooldown expired) |

### L3: Model（per (cred, raw_model)）

| 字段 | 存储 | 触发 | 写入者 | 读出者 | 转换 |
|---|---|---|---|---|---|
| `cmb.available` | `credential_model_bindings.available` (DB) | auto/quota/mnf_cooling | `Writer.writeModelLevelFailureOnly`; `executor.disableModelOffer`; `Checker.markDegraded`; `coolBindingOnMnfStreak`; `bg/credential_recovery.go`; admin | `v_routable_credential_models`; DT-3; DT-5 [Step 1] | TRUE ↔ FALSE（auto_*, mnf_cooling, manual%） |
| `cmb.unavailable_reason` | 同上 | 同步 unavailable_reason | 同上 | 同上 | "" / "auto_*" / "manual*" / "mnf_cooling" |
| `model_offers.available` | Postgres | 双写或测试路径 | `Writer`，`executor.disableModelOffer`，admin | `/api/routing/resolve` test path | TRUE ↔ FALSE |
| `model_probe_state.state` | Postgres | 探针共识 | `bg/model_probe.go`; `executor.recordMnfStreak`; `defaultAsyncExitSuspicious` | `v_routable_credential_models` 硬过滤 | unknown ↔ recovering ↔ healthy_confirmed / broken_confirmed / suspicious（不闭环 V3） |
| `provider_models.available` | Postgres | `UpsertCredentialModel` / `ClearProviderBindings` | `modelcatalog.UpsertCredentialModel` | `v_routable_credential_models` | TRUE ↔ FALSE（保留 manual disable） |

### L4: Request

| 字段 | 存储 | 触发 | 写入者 | 读出者 | 转换 |
|---|---|---|---|---|---|
| `Pool.state` | atomic.Int32 (in-process) | `RecordFailure/RecordSuccess/probe` | `pool.Pool.RecordFailure/RecordSuccess`; `healthLoop` | `Pool.Acquire` | active ↔ degraded ↔ draining → **dead (terminal!)** |
| `Slot.active` | Redis SET / TTL | acquire/release | Lua acquireSlotScript / releaseSlotScript / forceUnpinScript | prefilter; acquire; stats | (空 / active) 1..N |
| `Slot.pin` | Redis SET (24h TTL) | acquire (Lua 写) | Lua acquireSlotScript (Phase 1: pin reuse) | prefilter, acquire | 不存在 → slotIdx |
| `Limiter.active[layer]` | atomic + semaphore | acquire/release | 5 层 limiter（global/pool/cred/identity/key） | DT-7 [3d] | 0..capacity |
| `Sticky.credID` | in-process map (Redis 5min TTL) | sticky success/failure | `StickyCache.Record*` (threshold=5) | `stickyCredentialID/getEntry` | 不存在 → credID; → 删除 (credential-fatal) |
| `SessionIntentCache` | map (atomic.Pointer) | reclassify 检测 | `CachedIntent.Put` | DT-4 [Step 0] | 不存在 → CachedIntent (10min) |
| `Bandit α/β` | in-process map | success/failure/rate_limit | `BanditScorer.Record*` | P2C selection | ±1 |
| `Policy.CircuitOpenSeconds` etc | DB | admin | admin | DT-7 | static (5min) |
| `MnfStreak` | in-process map | mnf events | `mnfStreak.Inc/Reset` | DT-7 [3g] | 0..3 → coolBinding 2min → reset |

---

## 四、跨层读写侧不变量（应该成立但未验证的）

| 期望不变量 | 当前状态 | 影响 |
|---|---|---|
| cmb 上 `available` 与 circuit 上 `state=closed` 同向 | 不一致：cmb=TRUE 时 circuit 可能已 Open | 路由浪费（cmb=TRUE 但 circuit Open 跳过） |
| cmb 上 `available` 与 availability_state 同步 | 写侧多源，没事务保证 | 30s 缓存窗口内可能错配 |
| NodeState.Disabled 与 availability_state 同步 | 完全独立 | 一个 Disable 一个 ready 互相打脸 |
| quota_state='permanently_exhausted' ⇒ circuit 必须 Quarantined | 只有 `Writer.WriteOnError` 写其中一边 | 单边失效 |
| Pool.state=Active ⇒ cmb 上任意 binding 可发请求 | Pool 按 identity×provider×cred 分桶，绑定可达 | OK |
| Pool.state=Dead ⇒ 没有任何请求可通过此身份-凭据组合 | Pool.Acquire 返回 ErrPoolClosed | OK（除了 V15 永久死锁） |
| Sticky.credID 必须 nobe candidate list | sticky 与 router 都通过 DT-6；sticky miss 时降级 | OK |
| Session cache.changedTask ⇒ reclassify | `shouldReclassify` 仅检测 image/tools/long_context | 漏：纯文本 task drift 不被检测 |

---

## 五、新发现漏洞（V15-V20，结合静态路径分析）

### ⛔ V15: Pool "Dead" 是终态 — 无自动恢复（HIGH）

**位置**: `pool/pool.go:42-341`

**状态机重述**:
```
   RecordFailure (count>=3)              RecordSuccess (consecutive>=3)
Active ─────────────────► Degraded ──────────────────────────────► Active
  │
  │ RecordFailure (count>=10)
  ▼
Draining ──[grace_period 3min]──► Dead   ← 终态！
  │
  └──► Probe success (consecutive>=3) ──► Active (但只在 Degraded/Draining 时触发)
```

**问题**: 进入 `Dead` 后:
- `RecordFailure` (line 202-228): 所有 transition 检查 `currentState != PoolDead`，不进 Dead
- `RecordSuccess` (line 231-247): 同样不进 Dead
- `Acquire` (line 140): 直接返回 `ErrPoolClosed`
- **唯一恢复路径**: 进程重启 (`CloseAll`)

**触发场景**:
1. upstream 闪断 10+ 秒（同时 10 个请求同时失败）→ failCount≥10 → Draining
2. 3 分钟内所有 probe 都超时 → 不会冷却（probe() 在 resp.StatusCode>=500 才失败，network error → failure）
3. 3 分钟 grace 过期 → Dead
4. **网络恢复后**：probe() 仍然每 30s 跑（healthLoop 在跑），但 RecordSuccess 看 `currentState == PoolDead`，**永远不会**转回 Active

**影响**:
- 一次 10+ 秒的 blink → 这个 (identity, provider, credential) 组合永久死锁
- 多身份池场景下受害的是当前 holder × 凭据
- 唯一解药是 process restart 或 admin 调用 `pool.Close()` 重建

**修复**:
```go
// pool.go RecordSuccess 中:
if currentState == PoolDead && n >= successThreshold {
    p.state.Store(int32(PoolActive))
    p.successCount.Store(0)
    p.drainingSince.Store(0)
    p.failCount.Store(0)  // ★ 关键
    slog.Info("pool recovered from dead",
        "key", p.key.String(),
        "from_state", currentState.String(),
    )
    return
}
```

**额外加固**:
```go
// pool.go RecordFailure 中：当前实现保留 failCount 不重置
// Dead → Draining 时也应允许转回 Draining：
if currentState == PoolDead && count >= deadThreshold {
    p.state.Store(int32(PoolDraining))
    p.drainingSince.Store(time.Now().UnixMilli())
    slog.Warn("pool revived from dead → draining", ...)
}
```

---

### ⛔ V16: Acquire → Use → Release 在 Pool.Close 时的 TOCTOU（HIGH）

**位置**: `pool/pool.go:140-164` + `pool/pool.go:251-258`

**问题序列**:
1. Goroutine A: `Pool.Acquire(ctx)` → 通过 `closed.Load() == false` 检查
2. Goroutine B (admin / evictLoop): `PoolManager.evictIdle` → `p.Close()` → `CompareAndSwap(false, true)` → `StopHealthCheck` → `wg.Wait` → `transport.CloseIdleConnections`
3. Goroutine A: 此时 `activeConns <- struct{}{}` 已被合并（buffered 32），实际成功，但**该请求使用的 transport 可能已关闭**
4. HTTP 请求 hang → transport 关闭异常 → 静默失败

**触发场景**:
- pool 配置为 `idleTTL=3min` + `poolEvictInterval=30s`
- 某个长 hold 的连接刚好在 evictLoop tick 时被 close
- 下一时刻新请求命中该 closed transport → hang → hang 超时（取决于 http.Client.Timeout=120s）

**影响**:
- 偶发请求 hang 120s
- 返回 504/502 错误 client

**修复**:
```go
func (p *Pool) Acquire(ctx context.Context) error {
    if p.closed.Load() {
        return ErrPoolClosed
    }
    select {
    case p.activeConns <- struct{}{}:
        // ★ 双重检查
        if p.closed.Load() {
            <-p.activeConns
            return ErrPoolClosed
        }
        p.touch()
        return nil
    case <-ctx.Done():
        return ctx.Err()
    case <-p.stopCh:
        return ErrPoolClosed
    }
}
```

---

### ⛔ V17: recommend_v2.go 的 `fallbackSnapshotAvailability` 在 live filter 失败时被吞噬（HIGH）

**位置**: `autoroute/recommend_v2.go:38-41`

```go
filtered, err := idx.filterCurrentlyAvailable(ctx, pool, availabilityFilter, all)
if err != nil {
    filtered = fallbackSnapshotAvailability(all)  // ⚠ 静默降级
}
```

**问题**:
- DB 错误（如连接失败）→ fallback 到 snapshot
- 但 snapshot 来自 5 分钟前 `Refresh()` 的索引，**该 snapshot 与 cmb.available 真相已脱节**
- 用户看到路由到一个"已知 mnf_cooling"的候选，没有日志告警

**影响**:
- DB 故障期间无法路由 → 用户感知到延迟 + 命中率暴跌
- DB 恢复后需要等下一次 Refresh（5min）才能看到状态

**修复**:
```go
filtered, err := idx.filterCurrentlyAvailable(ctx, pool, availabilityFilter, all)
if err != nil {
    slog.Error("recommend_v2: live filter failed, using snapshot",
        "error", err,
        "snapshot_age_s", time.Since(idx.LastRefresh()).Seconds(),
    )
    metricObserve("recommend_v2.live_filter_failed", 1)
    filtered = fallbackSnapshotAvailability(all)
} else {
    metricObserve("recommend_v2.live_filter_ok", 1)
}
```

**额外加固**: 提供 metric 让运维通过 dashboard 看到 `recommend_v2_live_filter_failed_total` 上升时立即介入。

---

### ⛔ V18: Decider.Decide 中 `shouldReclassify` 检测不充分，导致 SessionCache task-drift 残留（HIGH）

**位置**: `autoroute/session_intent_cache.go:139`

```go
func shouldReclassify(cached TaskType, sigs ClassificationSignals) bool {
    // 只检测硬信号变化（images/tools/long_context）
    // 不检测 task 语义漂移（如：同会话内 chat→code）
    return sigs.HasImageAttachment || sigs.HasToolCalls || sigs.EstimatedTokens > LongContextThreshold
}
```

**问题**:
- 用户发了一段 Python 代码 (`TaskCode` 命中并缓存)
- 后续同一会话发问 "这段代码是什么" → `TaskChat` 但**没有 image/tool/long_context 变化**
- 缓存命中 → 复用 `TaskCode` + 选定的 coding 模型
- 用户体验：chat 请求硬塞到代码模型，浪费 budget、响应慢

**影响**:
- 误路由 → 用户感知不到的 budget 损耗 + 准确度下降

**修复**: 加入 task drift 检测。已存在 `feedback.go:137 DetectSessionDrift` 但未在 `shouldReclassify` 中调用

```go
func shouldReclassify(cached TaskType, sigs ClassificationSignals) bool {
    if sigs.HasImageAttachment || sigs.HasToolCalls || sigs.EstimatedTokens > LongContextThreshold {
        return true
    }
    // ★ 新增: 任务类型语义漂移检测（基于最近 N 个请求的 task_type 分布）
    return DetectSessionDrift(sigs.RecentTaskTypes)
}
```

---

### ⚠ V19: Provider.GetCandidates 缓存与 RecommendV2 live filter 之间存在 30s TOCTOU 窗口（MED）

**位置**: `provider/client.go:331-379` ↔ `autoroute/recommend_v2.go:38`

**触发**:
1. t=0: `provider.GetCandidates` 缓存返回 cand with `Routable=true, LifecycleStatus=active`
2. t=15s: upstream 永久下线凭据 → DB UPDATE → `availability_state='suspended'`
3. t=15s+ε: `executor.writeCredentialStateOnError` 调用 `InvalidateAllCandidateCache()`
4. t=15~30s: 另一个请求进来 → `provider.GetCandidates` 仍然返回缓存的 cand（如果 Invalidate 漏掉这次缓存填充）
5. t=15~30s: `recommend_v2.filterCurrentlyAvailable` 拒绝这个 cand
6. **结论**：cache 与 live filter 是冗余保护，**但只在 fillter 启用时生效**。

**当 recommend_v2 的 filterCurrentlyAvailable 因 V17 fallback 失效时，cache 30s 是最后防线；若两条都失效，路由会"成功"路由到已下线的凭据，最多一个请求失败 5xx**。

**触发**: V17 + V19 同时发生，DB 长时间故障 + Refresh 5min 没跑

**修复**:
- `provider.GetCandidates` 的 cache TTL 从 30s 降到 5s（与 Refresh 周期同步）
- 或者：在 `recommend_v2` 的 `filterCurrentlyAvailable` 失败时返回 nil 而非 fallback

---

### ⚠ V20: sessionCache 与 sticky 凭据冲突（MED）

**位置**: `autoroute/decision.go:188-265` + `executor.go:1944-1988`

**问题**:
- `sessionCache` 给定了 `ChosenCredentialID=X`
- sticky cache 同时绑定了同一个 `apiKeyID → Y`（Y ≠ X）
- 实际执行时 `stickyCredentialID=Y` 被 `prioritizeSticky` 提到最前
- 而 `SessionIntentCache.Put` 时记下 `ChosenCredentialID=X`（来自 Decision）

**结果**: 下次同 session 请求 → sessionCache hit 用 X → 但 sticky 提了 Y → 实际走的 Y → 与 sessionCache 不一致

**影响**:
- audit log 显示 A，请求实际打到 B
- 监控指标混乱

**修复**:
```go
// decision.go:Step 3 之后：
if stickyCredentialID != nil {
    decision.ChosenCredentialID = *stickyCredentialID  // ★ 写入最终胜者
}
// 然后再 sessionCache.Put
```

---

## 六、修复优先级（按 ROI 排序）

### 阶段 0：阻断级（影响服务可用性，48h 内必须修）

| 序 | 漏洞 | 修复复杂度 | 影响范围 |
|---|---|---|---|
| 1 | **V15** Pool Dead 终态 | L（小，~30 行代码 + 测试） | 永久不可用 |
| 2 | **V16** Pool Acquire/Close TOCTOU | M（~50 行 + 并发测试） | 偶发 504/502 |
| 3 | **V17** recommend_v2 live filter 静默降级 | S（加 metric + 日志） | DB 故障时命中率坍塌 |
| 4 | **已有 #1** circuit cross-process 失同步 | L（Redis 状态同步 + breaker 重构） | 多实例必现 |
| 5 | **已有 #10** KindModelNotFound 移出 IsClientBug | S | 永久 404 |
| 6 | **已有 #8** UpdateOnFailure 调 InvalidateCache | S | 30s 路由陈旧 |
| 7 | **已有 #7** tenant_id 透传 | M | 多租户失能 |

### 阶段 1：稳定级（降低故障率，提升可观测性，1 周内修）

| 序 | 漏洞 | 修复复杂度 |
|---|---|---|
| 8 | **V18** shouldReclassify 加 task drift | M（接入 DetectSessionDrift） |
| 9 | **V20** sessionCache vs sticky 一致性 | M |
| 10 | **V19** Cache TTL 调低或双保险 | S |
| 11 | **已有 #3** suspicious 不闭环 | L（加 bg worker） |
| 12 | **已有 #12** sync_retry 间隔 | S |

### 阶段 2：架构级（彻底解，1 季度）

| 序 | 项目 | 内容 |
|---|---|---|
| 13 | **URSM 全量切流** | 当前 `executor.RecordRequest` 已经异步调 URSM，目标是让 router 读 URSM 单一源（已在执行 `planWithURSM` 旁路，但 `PlanCandidates` 主入口仍走 legacy） |
| 14 | **Circuit 状态迁 Redis** | 已有 `redis_pool.go`，可类比移到 `credential.Breaker` 加 Redis layer |
| 15 | **State machine 单源化** | 引入 `state_event` 表，记录所有状态变更，通过 logical replication 同步给 router 读侧 |
| 16 | **policy 决策树收敛** | `Candidate.UnavailableReason` 应改为显式表驱动，而非 if-else 链（容易漏检） |

---

## 七、状态机可观测性改造建议

| 现有 | 改造为 |
|---|---|
| 7 个独立 DB 列 + 多组件写入 | 1 张 `cred_health_events` 表（append-only），所有写入者只 INSERT；reader 端用 materialized view 聚合最新状态 |
| 决策树 7 棵分散在 4 个目录 | 1 个 `decision-trace` 子系统，统一 emit `Decision{input, path, winner, reasons}` JSON |
| Prometheus 指标散落 10+ 文件 | 1 组 metrics：`llmgw_state_*`（变迁计数）+ `llmgw_decision_*`（决策路径分布） |
| 状态变更无审计 | 每次 state 变更 emit 1 个 span 到 OpenTelemetry，`cred_id` `model` `kind` `from_state` `to_state` `reason` `actor` |

---

## 八、决策树决策依据与决策可靠度评估

| 决策点 | 决策依据 | 可靠度 | 风险 |
|---|---|---|---|
| Profile 解析 | header > sticky > default | 高 | sticky 写入幂等 ✓ |
| Task 分类 | hint > heuristic > LLM | 中 | hint 信任度 0.9 但没有白名单 |
| Candidate 选择 | 8/4/2 维评分 + 池分层 | 中 | feature flag 多，行为分叉多 |
| Circuit 允许 | in-process 内存状态 | **低** | 多实例失同步 |
| FpSlot 准入 | Redis Lua 原子 | 高 | pin 24h 持久化，可能跨身份混淆 |
| Sticky credential | 内存 L1 + 5min TTL | 中 | 多实例不收敛 |
| 限流 | in-process 5 层 semaphore | 低 | 多实例不收敛 |

---

## 九、决策路径组合测试矩阵（建议生成回归测试）

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

## 十、总结

| 维度 | 评估 |
|---|---|
| **状态机数量** | **4 层 × 7+ 并行** = 28 个独立转换路径 |
| **真相源数量** | 3 个（DB / Redis Lua / in-process） |
| **状态读侧数量** | 15+ 处分散 |
| **状态写侧数量** | 10+ 处分散 |
| **状态机不变量** | 8 个，大部分**未被自动验证** |
| **已确认漏洞** | **20 个**（14 旧 + 6 新），其中 **8 HIGH** |
| **修复优先级最高 5 项** | V15 (Pool Dead 终态) → V17 (live filter 静默) → V18 (drift 检测) → 已有 #10 → 已有 #1 |
| **架构性根本问题** | **无单一真相源，决策树分支依赖不完整的状态视图** |

> **简短结论**: 当前系统在单实例小流量下能跑（凭据状态写少、缓存窗口短）。**多实例 + 真实故障** 场景下，V15 (Pool Dead 终态)、已有 #1 (Circuit 跨进程失同步)、V18 (task drift 残留) 三个问题会**导致服务不可恢复性降级**，建议在阶段 0 优先解决。

---

**审计人**: AI Agent
**审计版本**: llm-gateway-go @ 2026-07-04
**审计基线**: CREDENTIAL_STATE_AUDIT_REPORT.md (2026-07-03) + 6 新发现（V15-V20）
