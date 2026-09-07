# FR — 自检模块及时恢复

日期：2026-09-08 · 状态：已实现（`main`）· 变更记录：`docs/changelogs/2026-09-08-selfcheck-timely-recovery.md`

## 背景

自检模块对错误状态的恢复、额度恢复后的状态更新过迟或缺失。节点实际已可用，但路由面（`credential_model_bindings` / `credentials` / 候选缓存）仍把它排除。

## 目标

对**当天请求成功过的模型节点**做有效但不过量的探测；节点状态变化后**及时同步**到路由可见面；错误与偶发网络问题**加快探测频度**以尽快恢复。

## 功能需求

| ID | 需求 | 验收 | 实现 |
|---|---|---|---|
| FR-1 | 网络/超时/上游 5xx 类错误使用短退避链，封顶 ≤ 60s | attempt 1..8 → 5s/15s/30s/60s/60s… | `bg/probe_recovery_policy.go` `NetworkProbeBackoffChain` |
| FR-2 | 额度类错误按策略固定节拍探测（余额 2m、周期 5m） | `ProbeBackoffForKind(KindQuota*)` 固定值 | 同上 |
| FR-3 | 限流/并发类错误不得快于策略间隔重试（429 不能 5s 再打） | rate_limit attempt 1 = 3m | 同上（策略间隔作下限） |
| FR-4 | 队列 pump holdoff 不得埋掉短链重试 | holdoff ∈ [15s, 60s] | `bg/node_probe.go` `nodeProbeQueuePumpHoldoff = 45s` |
| FR-5 | 直连探测成功即恢复 binding，网关轮失败只影响“完全成功”计数 | `applyOutcome(direct.ok)` 恢复 | `bg/probe_service.go` |
| FR-6 | 业务成功 / 直连成功写回 cmb + credentials（health/availability/quota 清零），且已健康时为 0 行无副作用 | SQL 含 `IS DISTINCT FROM` 守卫 | `bg/node_probe_write_through.go` |
| FR-7 | 额度恢复探测成功后立即通知路由缓存 | `ProbeNow` 成功 → `onQuotaRecovered("probe_now")` | `bg/credential_probe_v2.go` |
| FR-8 | 没有 `default_probe_model` 的凭据仍可被 ProbeNow 探测，优先选可路由 binding | `fallbackProbeModel` 先 available 后 all | 同上 |
| FR-9 | 余额额度 tick 优先走 `ProbeNowAsync` 立即探测 | `probeBalanceExhausted` | `bg/balance_quota_probe.go` |
| FR-10 | 当天成功过的 (credential, model) 定期扫描：失败/不可用/从未探测的必扫；健康的 ≥ 1h 才复扫；每轮 ≤ 40 对，15 分钟一轮 | SQL 谓词 + 常量 | `bg/today_success_probe.go` |
| FR-11 | 出错凭据自检窗口 24h → 15m，且按“最久未检”轮转防饿死 | `ORDER BY l.last_at ASC` | `bg/credential_selfcheck.go` |

## 非功能约束

- 探测量有界：单轮 ≤ 40 对、健康节点 1h 复扫、pump 45s、ProbeNowAsync 按凭据去重。
- 热路径零放大：业务成功写回的两条 UPDATE 在健康态必须 0 行（FR-6）。
- 与现有守卫一致：手工/`admin_protected` binding、`manual_disabled` 凭据不被自动恢复。

## 不在范围

- 前端自检页面改动；生产库/UI 实测（见 `docs/测试/01-selfcheck-timely-recovery/results.md`）。
