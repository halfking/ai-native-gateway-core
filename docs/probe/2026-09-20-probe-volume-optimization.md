# 自检探测量优化专项（错误门控探测策略 v3）

- 日期：2026-09-20
- 范围：`bg/` 主动探测体系（默认新探测模式：`CredentialSelfcheckWorker` + `NodeProbeWorker` + 统一队列 `credential_probe_queue` + 各周期扫描器）
- 状态：已实施（见文末变更清单）

## 0. 需求（用户原话归纳）

1. **3 天使用范围**：一个凭据下，只对 3 天内使用过的模型（**排除探测任务自身的流量**）进行探测。
2. **两连成功早停**：一个节点（凭据）连续两个模型都探测成功后，该凭据的其它模型不需要再探测。
3. **正常态零探测**：正常情况下不需要连续探测；只有出现**错误 / 余额用尽 / 并发限流等供应商端错误**后，才需要进行持续的跟踪探测。

## 1. 现状盘点：探测量为什么大

默认部署（`LLM_GATEWAY_USE_NEW_PROBE_MODE=true`）下，每个 (credential, model) 对的一次探测 = direct + gateway 两轮真实上游请求。以下引擎在"无任何错误"的常态下持续产生探测：

| # | 引擎 | 节奏 | 常态行为 | 违反 |
|---|------|------|----------|------|
| 1 | `MarkNodeProbeHealthy`（executor 每次业务成功调用，`bg/node_probe.go`） | 每请求 | 写 `node_probe_state.next_retry_at = now()+1h` | ①成功反而武装了下一次探测；需求 3 |
| 2 | `mirrorNodeProbeState` 成功分支（`bg/probe_service.go`）+ legacy `runOne` 成功分支（`bg/node_probe.go`） | 每次探测成功 | `next_retry_at = now()+1h` | 探测成功 → 1 小时后再探 → 永动循环；需求 3 |
| 3 | `reconcileStaleNodeProbeStates`（`bg/credential_recovery.go`，30s tick，LIMIT 50） | 30s | 把 `next_retry_at > now()`（即任何未来时间戳的行，包括健康行）判为"stale"重新 Submit | ①②写入的健康行被它反复重新提交；需求 3 |
| 4 | `pumpDueStatesToQueue`（`bg/node_probe.go`，30s） | 30s | 到期行无健康过滤全部入队 | 健康行被 #1/#2 武装后按小时入队；需求 3 |
| 5 | `TodaySuccessProbe`（`bg/today_success_probe.go`，15min，批 40） | 15min | 24h 内有业务成功的对，`last_attempt_at` 超 60 分钟即重探 | 健康对按小时重探；需求 3 |
| 6 | `DailyProbeAudit`（`bg/daily_probe_audit.go`，24h，**无总量上限**） | 24h | 3 天内用过**或失败过**的全部对一次性提交；request_logs 分支**未排除探测流量** | 探测流量被当"使用"→ 自我延续；无 3 天使用约束内的进一步门控；需求 1/3 |
| 7 | `featuredCycle` 常用模型深探（`bg/model_probe.go`，默认 900s） | 15min | 对**所有**凭据的 featured 静态清单 ∪ 7 天用量 Top-N 模型深探 | 探测该凭据 3 天内从未用过的模型；健康凭据也持续深探；需求 1/3 |

互相增强的闭环（最大体量来源）：**业务成功 → #1 写 +1h → #3 在 30s 内把它重新 Submit（队列 dedup 吸收一部分、必要性门禁跳过一部分，Redis 证据过期时 fail-open 变成真实探测）→ 探测成功 → #2 再写 +1h → 循环**。#4/#5 各自再叠加一层按小时的重探。

**错误触发通道（保留不动，正是需求 3 要的跟踪探测）**：
- `ActiveProbeWorker`：请求连续失败 ≥2 触发，5s→15m 退避链、最多 5 次。
- `NodeProbeWorker.Submit`（request_failure 源）：真实请求失败武装 5s→6h 七级梯子。
- `balance_quota_probe` / `credential_probe_v2`（余额/配额，已有"近期业务成功跳过"）。
- `credential_recovery`：`cmb.available=FALSE` 且 recover_at 到期 → 提交复核（错误态恢复）。
- `recoveringSweeper`、`ProbeSync`（no_candidate 请求路径同步探测）。

## 2. 目标策略（不变量）

> **成功即停放（park），失败才武装（arm）。**
> 健康凭据/节点在没有任何供应商端错误信号时，不产生任何计划性探测。

具体不变量：

- **INV-1（健康停放）**：`node_probe_state` 行满足 `last_direct_ok=TRUE AND COALESCE(last_err_code,'')='' AND COALESCE(consecutive_failures,0)=0`（下称 **healthy-parked**）时，任何调度器（pump / reconcile / 扫描器）都不得为其生成探测。成功写入时 `next_retry_at = now()+30d`（显式停放，语义化远离调度窗口）。
- **INV-2（失败立即重武装）**：healthy-parked 行收到新的真实失败（`Submit`）时，立即重置为 `now()+5s` 开启跟踪梯子；已在梯子中的行保持梯子不塌缩（沿用 2026-07-16 修复语义，仅扩展 re-arm 条件）。
- **INV-3（3 天使用范围）**：周期扫描（DailyProbeAudit / featuredCycle / selfcheck 回退）只允许探测该凭据 **3 天内有过非探测流量** 的模型（`NOT 'probe' = ANY(request_logs.quality_flags)`）。
- **INV-4（两连成功早停）**：凭据在 24h 内已有 **≥2 个不同模型** 的最近一次探测结果为成功时，该凭据的其它模型不再需要计划性探测（错误梯子中的行除外——单对错误跟踪继续，见 §3.6）。
- **INV-5（错误门控扫描）**：周期扫描器（DailyProbeAudit 用量分支 / TodaySuccessProbe / featuredCycle / credential_probe_v2）只面向**有错误证据**（candidate_failure_logs 近期记录 / 当前不可用 / 最近探测失败）的凭据。

## 3. 变更设计

### 3.1 共享策略谓词 `bg/probe_policy.go`（新增）

集中定义三段 SQL 谓词，供各扫描器/调度器复用（避免口径漂移）：

- `nodeProbeHealthyParkedSQL(alias)`：healthy-parked 判定（INV-1）。
- `nodeProbeErrorEvidenceSQL(alias)`：行级错误证据（`last_err_code<>'' OR consecutive_failures>0 OR last_direct_ok IS DISTINCT FROM TRUE`）——pump 只放行这类行。
- `credentialFailureEvidenceSQL(alias, interval)`：凭据级错误证据（EXISTS candidate_failure_logs 近窗口记录）。
- `credentialTwoProbeSuccessGateSQL(alias)`：INV-4 两连成功门（≥2 个不同模型的最新完成探测 run 为成功，24h 窗口）。
- `probeTrafficExclusionSQL(col)`：`NOT COALESCE('probe'=ANY(quality_flags),FALSE)` 常量。

### 3.2 成功即停放（#1/#2 根修）

- `MarkNodeProbeHealthy`：INSERT 与 UPDATE 分支均写 `next_retry_at=now()+30d`（`next_retry_seconds=2592000`）；`paused` 在 INSERT 保持 FALSE、在 UPDATE 保留原值（操作员暂停不被业务成功覆盖）。绑定/凭据面恢复（`syncHealthyNodeSurfaces`）不变。
- `mirrorNodeProbeState` 成功分支、legacy `runOne` 成功分支：`next_retry_at = now()+30d`（原 +1h）。
- 选择 `+30d` 时间戳而非 `paused=TRUE` 作为主停放机制：`paused` 在 `credential_recovery`/`credential_autoheal` 里被读作"操作员暂停"，混用会改变那些查询的语义；未来时间戳对全部现有读者（`next_retry_at > now()` 判"未到期"）天然无害。

### 3.3 Submit re-arm 条件扩展（INV-2）

`Submit` 的 `ON CONFLICT` re-arm 条件由 `paused OR next_retry_at <= now()` 扩展为 `paused OR next_retry_at <= now() OR healthy-parked`。梯子中行（`last_err_code` 非空或 `consecutive_failures>0`）仍保持原梯子（不塌缩），只有健康停放行被新失败立即拉回 `now()+5s`。

### 3.4 pump 与 stale-reconcile 收窄（#3/#4 根修）

- `pumpDueStatesSQL`：追加行级错误证据过滤（§3.1 第 2 条）。健康停放行永不上泵。
- `reconcileStaleNodeProbeStateSQL`：删除 `next_retry_at IS NULL OR next_retry_at > now()` 两个"健康行制造机"条件，改为仅 `NOT healthy-parked`（真正失败过/未证实健康的行才需要复核）。该函数的本职（把"表面已恢复但探测行仍失败"的对交回探测）完整保留。

### 3.5 扫描器收窄

- **TodaySuccessProbe → 恢复复核专用**：WHERE 收紧为 `COALESCE(cmb.available,FALSE)=FALSE OR COALESCE(nps.last_direct_ok,FALSE)=FALSE`（当前判定不健康的对）；删除 60 分钟健康重探臂与 `nps.credential_id IS NULL` 臂；加 INV-4 凭据门 + 每凭据每 tick 最多提交 2 对（按最近使用排序）。
- **DailyProbeAudit → 错误凭据核查**：request_logs 分支加探测流量排除（INV-3）+ 凭据错误证据门（EXITS candidate_failure_logs 3 天窗，INV-5）+ 每凭据≤2（最近使用优先）+ 凭据两连成功门 + 运行总量上限（`dailyProbeAuditBatch=100`）；提交改走 `SubmitWithSource(...,"selfcheck")`（修审计归因失真）。candidate_failure_logs 分支（错误对本体）保留。
- **featuredCycle → 错误凭据的常用模型深探**：SQL 加 3 天非探测使用 EXISTS（INV-3）+ 凭据 24h 错误证据门（INV-5）+ 两连成功门（INV-4）；Go 侧 `globalIsFeaturedModel` 过滤不变。`probe.featured_usage_window_hours` 默认 168→72（`settings/spec_probe.go`、`bg/model_probe.go`、`bg/model_tier.go` 三处同步；Top-N 口径与 3 天对齐），且 ModelTier 用量 Top-N 查询排除探测流量（否则探测把模型"常驻常用"，深探自我延续）。
- **CredentialSelfcheckWorker**：recent 回退窗口 7d→3d（INV-3 对齐）。选 primary + 到期失败绑定的既有逻辑不变（本来就有 24h 错误凭据门 + 每 5min 一个凭据的节流）。
- **credential_probe_v2**：`skipProbeOnRecentTrafficSuccess` 扩展——除"30 分钟内有业务成功"外，"健康 + 24h 无失败证据"也跳过（INV-5；env 开关 `LLM_GATEWAY_PROBE_SKIP_RECENT_SUCCESS` 语义不变）。

### 3.6 必要性门禁条件③（INV-4 的执行侧兜底）

`bg/probe_necessity.go` 新增 `conditionTwoSiblingProbeSuccesses`：任务对应的 pair 本身**不在错误梯子**（node_probe_state 无行或 healthy-parked）且同凭据 24h 内已有 ≥2 个不同模型探测成功 → 跳过（`not_necessary_two_model_successes`）。证据查询失败 fail-open（探测照跑）。错误梯子行（`last_err_code` 非空 / `consecutive_failures>0`）豁免——单对的失败跟踪不被兄弟模型的成功短路。

## 4. 预期体量效果（定性）

- 健康在用 pair：**每小时 2 轮 × 多引擎重复探测 → 0**（业务成功即证据；停放后 pump/reconcile/扫描器全部不再触达）。
- 错误 pair：维持既有 5s→6h 跟踪梯子（不变），但每日 DailyProbeAudit 不再把 3 天内所有用量对全量倒进队列，只核查错误凭据的 ≤2 个最近使用模型。
- featured 深探从"所有凭据 × featured 清单"收缩为"错误凭据 × 3 天内实际用过的 featured 模型"。
- 队列/审计/SSE 瓷砖噪音同步下降（必要性跳过与重复入队消失）。

## 5. 回滚与开关

- 代码级回滚：revert 本次提交即可，无 schema 变更。
- 行为开关：`LLM_GATEWAY_PROBE_SKIP_RECENT_SUCCESS=0` 恢复 v2 每小时全量探测；`probe.featured_cycle_seconds`/`probe.featured_usage_window_hours` 平台设置可调。
- `next_retry_at=+30d` 停放行对新失败立即反应（INV-2），无需手工清理；存量 `next_retry_at<=now()` 的健康行会在下一次被 pump 拾取时因新过滤直接跳过，不产生探测。

## 6. 变更清单（实施记录）

| 文件 | 变更 |
|------|------|
| `bg/probe_policy.go` | 新增：共享 SQL 谓词（healthy-parked / 错误证据 / 两连成功门 / 探测流量排除） |
| `bg/node_probe.go` | MarkNodeProbeHealthy 停放；Submit re-arm 扩展；pumpDueStatesSQL 错误过滤；runOne 成功分支停放 |
| `bg/probe_service.go` | mirrorNodeProbeState 成功分支停放 |
| `bg/credential_recovery.go` | reconcileStaleNodeProbeStateSQL 收窄为 NOT healthy-parked |
| `bg/today_success_probe.go` | 恢复复核专用化 + 两成功门 + 每凭据≤2 |
| `bg/daily_probe_audit.go` | 探测流量排除 + 错误门 + 每凭据≤2 + 总量上限 + selfcheck source |
| `bg/model_probe.go` | featuredCycle 三门控；usage 窗口默认 168→72 |
| `bg/probe_necessity.go` | 条件③ two-sibling-probe-successes |
| `bg/credential_selfcheck.go` | recent 回退 7d→3d |
| `bg/credential_probe_v2.go` | 健康+无失败证据跳过扩展（24h 窗口常量化 `probeFailureEvidenceWindowSQL`，不触碰 ManualBalanceProtectionPredicate 防漂移守卫） |
| `bg/model_tier.go` | 用量 Top-N 默认窗口 72h + 探测流量排除 |
| `settings/spec_probe.go` | `probe.featured_usage_window_hours` 默认 168→72 |
| `bg/probe_policy_test.go`、`bg/today_success_probe_test.go`、`bg/daily_probe_audit_test.go`、`bg/node_probe_test.go`、`bg/credential_recovery_test.go`、`bg/credential_selfcheck_pick_test.go` | 新策略钉桩：谓词形状、pump 错误证据门、featured 三门控、必要性门禁③（跳过/豁免/独立 fail-open）、成功停放 30d、扫描器 SQL 新门 |

### 实施验证

- `go build`：`bg`、`settings` 及全仓编译通过（Windows 本机既有 `syscall.Statfs`/`Flock` 不兼容项与本次无关，Linux 目标不受影响）。
- `go test ./bg/ ./settings/`：全绿；仅 `TestWalkDirSafe_ToleratesIsolatedErrors` 因 Windows symlink 特权在基线同样失败（环境项）。
