# 2026-08-23 — 自检分层与三类供应商差异化策略

## TL;DR

为「凭据 / 节点自检」三类触发场景（周期性额度、充值型额度、连续错误降级）
落地差异化自检策略，及时性与成本同步收紧：

- **Agent A**（`bg/periodic_quota_probe.go`）：新增「窗口末尾 60s 提前探活」+「`quota_recover_at` 偏离下一个 5h 边界时自动夹紧」两道护栏。
- **Agent B**（`bg/balance_quota_probe.go` + `admin/credential_state_handlers.go`）：新增 `POST /api/admin/probe/force/{id}` 立即触发探活（绕过 2 分钟等待）；SELECT 谓词去掉 `default_probe_model` 强制要求，改为「配置了探测模型 OR 至少一个可用 binding」；每次探活后写入 `credentials.balance_last_checked_at` 用于可观测性。
- **Agent C**（`credentialhealth/checker.go` + `bg/credential_recovery.go`）：引入按错误类型梯度的阈值 / 冷却表（`timeout/rate_limit/upstream_context_loss/...`）；冷节点不再被「无历史样本 → 直接放过」假阴性掩盖，配置 `ColdProber` 后走真实探活；`recoverFreshDegradedBindings` 的冷却期自探活 guard 由 60s 收紧到 30s。

## 触发的背景

`/tmp/handoff-20260823-2255-credential-selfcheck-audit.md` 标注的 245 现场证据 + 154 production
canary `node_probe_runs=0` 表明三类场景当前存在以下缺口：

| 场景 | 缺口 | 根因 |
| --- | --- | --- |
| 周期配额 | 5min 间隔 × 5h 窗口 → 60 次空探；`inferQuotaRecoverAt` 漏命中时 `quota_recover_at` 偏离 | writers.go:558 fallback `next UTC midnight` 对 5h/周/月窗口失真 |
| 充值型 | 2min 间隔合理但**无事件触发路径**；admin 端只能批量触发 | `default_probe_model` 缺省时整条链被 ON CONFLICT DO NOTHING 跳过 |
| 连续错误 | 80%/15min 单一阈值压扁 5xx / 429 / auth 三种本质不同的失败 | `minSampleSize=5` 在低 QPS 模型上第一波抖动即触发降级；`recoverFreshDegradedBindings` 60s 守卫与 credential_recovery 30s tick 不对齐 |

## 实现细节

### Agent A — PeriodicQuotaProbe 分层 + 偏离护栏

`bg/periodic_quota_probe.go`：

- `tick()` 串联三步：`recoverAtDeviationGuard` → `probePreExhausted` → `probePeriodicExhausted`。
- `probePreExhausted` 新增 SELECT：挑 `quota_recover_at > now() AND quota_recover_at <= now() + 60s` 的行，立即入 fastReprobeQueue。`fastReprobeQueue` 的 `ON CONFLICT DO NOTHING dedup_key` 保证多次预探活折叠成单次执行。
- `recoverAtDeviationGuard` 用 `UPDATE … WHERE quota_recover_at > now() + max(5h, linger)` 把偏离值夹到 `now() + 5h`。linger 默认 30 分钟，可由 `LLM_GATEWAY_PERIODIC_QUOTA_RECOVER_AT_MAX_LINGER` 覆盖。
- 新增 env：`LLM_GATEWAY_PERIODIC_QUOTA_PRE_PROBE_WINDOW`（默认 60s）。

### Agent B — BalanceQuotaProbe ForceProbe + 可观测性

`bg/balance_quota_probe.go`：

- 新增 `ForceProbe(credID) bool`：优先走 `ProbeNowAsync`（`CredentialProbeV2:238` 已存在），未注入时退到 `SubmitFastProbe`。30 秒冷却（`LLM_GATEWAY_BALANCE_QUOTA_FORCE_COOLDOWN`）防止操作员连点导致队列堆积。`credentialEligibleForForceProbe` 仅对 `quota_state IN ('balance_exhausted', 'permanently_exhausted')` 放行。
- `SetProbeNowAsync(fn)` 在 `cmd/gateway/main.go:3089` 注入 `credProbeV2.ProbeNowAsync`。
- SELECT 谓词去掉 `COALESCE(c.default_probe_model, '') <> ''` 单条件，改为「配置了 OR 存在 routable binding」双条件。仍为空 → `recordBalanceCheck` 仍然写时间戳但不探活，给 dashboard 一个「为什么没探」的信号。
- `recordBalanceCheck` 写 `credentials.balance_last_checked_at`，observability 字段已存在于 schema（`deploy/sql/schemas/baseline/01-schema.sql:5883`）但之前未被读写。

`admin/credential_state_handlers.go`：

- 新增 `handleForceBalanceProbe`，路由 `POST /api/admin/probe/force/{id}`（superAdmin 包裹）。返回：
  - `202 Accepted` — 探活已下发。
  - `429 Too Many Requests` + `Retry-After: 30` — 操作员点击过快被冷却抑制。
  - `503 Service Unavailable` — `balanceQuotaProbe` 未注入（boot 不完整）。

`admin/handler.go`：

- 新增 `balanceQuotaProbe` 字段 + `SetBalanceQuotaProbe` setter。

### Agent C — 按错误类型梯度 + 冷节点主动探活

`credentialhealth/checker.go`：

- 新增 `KindThreshold{FailureThreshold, MinSampleSize, DegradedCooldown}` 结构。
- `Checker.kindThresholds map[string]KindThreshold`：默认表覆盖 9 个生产关键 kind：

  | kind | 阈值 | minSample | 冷却 |
  | --- | --- | --- | --- |
  | `timeout`, `stream_timeout` | 0.70 | 5 | 20m |
  | `rate_limit` | 0.95 | 8 | 1m |
  | `upstream_context_loss` | 0.50 | 3 | 30m |
  | `upstream_down`, `upstream_overloaded` | 0.90 | 8 | 15m |
  | `model_not_found`, `model_deprecated`, `unsupported_feature` | 1.00 | 1 | 24h |

- `CheckAndUpdate` 在阈值比较前调用 `resolveKindThreshold(errorKinds)`：取样本里出现最多的 kind 作为 dominant，未在表里的回退全局字段。`markDegraded` 现在接收 dominantKind + threshold，写入 cmb 时使用 threshold 的 cooldown（rate_limit 1 分钟 vs auth 24 小时）。
- 新增 `ColdNodeActiveProber` interface + `CheckerConfig.ColdProber`：当 entries < minSampleSize 且 `ColdProber != nil` 时，主动走真实上游探活。成功 → 直接放过；失败 → 以 1.0 failureRate + 单一错误 kind 触发 `markDegraded`。`ColdProber == nil` 保留旧的「no data → no action」语义，老测试 / 老调用方零影响。
- `markDegraded` 日志新增 `dominant_kind` + `cooldown` 字段，dashboard 可按根因分组。

`bg/credential_recovery.go`：

- `freshDegradedCmbSQL` 的 `unavailable_at <= now() - INTERVAL '60 seconds'` 收紧为 `'30 seconds'`，与 `defaultCredentialRecoveryInterval = 30s` 对齐。最坏检测延迟从「60s 冷却 + 60s tick = 120s」降到「30s 冷却 + 30s tick = 60s」。

## 验证

```bash
go test ./bg ./credentialhealth ./domains/credential ./admin ./cmd/gateway -count=1
```

新增测试：

- `bg/periodic_quota_probe_test.go::TestPeriodicQuotaProbeLayeredStrategy`：锁定分层 + 偏离护栏存在。
- `bg/balance_quota_probe_test.go::TestBalanceQuotaProbeIncludesRoutableFallback` + `TestBalanceQuotaProbeForceEnvDefaults`：锁定 fallback SELECT + ForceProbe env。
- `bg/credential_recovery_test.go::TestFreshDegradedCmbSQLGuards`：更新期望值到 30s。
- `credentialhealth/checker_kind_threshold_test.go::TestCheckerKindGradientDefaults` + `TestCheckerGradientTableContainsCriticalKinds`：锁定 9 个关键 kind 不被 refactor 误删。
- `credentialhealth/checker_cold_probe_test.go::TestChecker_ColdProbeSuccessSkipsDegradation` + `TestChecker_ColdProbeFailureTriggersDegradation` + `TestChecker_NoColdProbeWiredReturnsEarlyOnEmptySamples` + `TestChecker_DominantKindGradient`：覆盖冷节点成功 / 失败 / 未注入 / 梯度阈值四个分支。

实测结果：`ok ./bg 1.706s` / `ok ./credentialhealth 0.839s` / `ok ./domains/credential 17.009s` / `ok ./admin 6.225s` / `ok ./cmd/gateway 0.688s`。

## Env 总览（新增）

| Env | 默认 | 用途 |
| --- | --- | --- |
| `LLM_GATEWAY_PERIODIC_QUOTA_PRE_PROBE_WINDOW` | `60s` | Agent A：窗口末尾前多少秒开始预探活 |
| `LLM_GATEWAY_PERIODIC_QUOTA_RECOVER_AT_MAX_LINGER` | `30m` | Agent A：`quota_recover_at` 偏离下一个 5h 边界的最大容忍 |
| `LLM_GATEWAY_BALANCE_QUOTA_FORCE_COOLDOWN` | `30s` | Agent B：admin force-probe 同 credential 最短间隔 |

## 兼容性 / 风险

- **BalanceQuotaProbe** 谓词放宽可能让以前被静默跳过的「`default_probe_model` 为空」凭据开始被探活；这是预期行为，对应 handoff §2.2(B) 的核心痛点。但 `default_probe_model` 仍为空且无可用 binding 的凭据会被 SELECT 过滤掉，仍不会下发探活。
- **`ColdProber` 未注入时**：冷节点继续走 legacy「no data → no action」语义，不引入额外探活流量。
- **`KindThreshold` 表**：仅作用于 `continuous_failure` 路径；不会改变 `quota_*` / `auth_*` 的 writer 行为。
- **freshDegradedCmbSQL 30s guard**：与 `defaultCredentialRecoveryInterval = 30s` 对齐，最坏情况每 30s 触发一次，不会增加上游压力（受 fastReprobeQueue dedup_key 抑制）。

## 后续 Phase 3 待办

- 把 `admin/handleForceBalanceProbe` 的 429 提示纳入前端「凭据详情」自检面板（dashboard 任务，非本会话范围）。
- `ColdProber` 在 `cmd/gateway/main.go` 中接入 `nodeProbeWorker.Submit` —— 本会话保留 nil 默认，避免冷启动时 boot 顺序耦合。
- `KindThreshold` 表上线后按 incident log 重新校准（Q4 计划）。
