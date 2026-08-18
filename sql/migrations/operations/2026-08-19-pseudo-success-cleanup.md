# 现场 PG 伪成功行清理 SQL 使用说明

- 文件：`sql/migrations/operations/2026-08-19-pseudo-success-cleanup.sql`
- 关联修复 commit：`8ebaee0b2 fix(recovery): stop fake-success loop`
- 前置交接：`docs/handoff/2026-08-18-global-routing-audit-handoff.md` §6.1
- 整改报告：`docs/handoff/2026-08-19-global-routing-state-machine-remediation.md` §4.2

## 这是什么

清理脚本，针对修复 commit `8ebaee0b2` 前 `bg/credential_recovery.go:443-485` 30 秒循环直接伪造的 `node_probe_state` 行。

**不会删除任何行**——只把污染字段重置为 NULL/FALSE/now()，让新版本代码路径（`reconcileStaleNodeProbeStates` → `ProbeService.Run` → 真实 direct + gateway 双轮）自然重新探测。

## 执行步骤

### 步骤 1 — 前置检查（必须先做）

执行脚本第一段（前缀 `-- 1) ... -- 4)` 的 SELECT 与备份），确认：

- 命中数（期望与交接文档"82 行"对账，可能有 ±10% 偏差）
- 按 credential 聚合的范围（确认没有意外覆盖生产核心 credential）
- paused 状态分布（避免误清 paused=true 的真实失败状态）
- 备份行数（必须等于命中数，否则不要继续）

### 步骤 2 — 执行清理（单独执行）

执行脚本中间段（`DO $$ ... END $$;`）。脚本会按 1000 行/批循环：

- `last_direct_ok = NULL`
- `last_gateway_ok = NULL`
- `next_retry_at = now()`（让 `reconcileStaleNodeProbeStates` 下一个 tick 命中）
- `next_retry_seconds = 5`（7-step backoff ladder 起点）
- `last_err_code = NULL`
- `last_err_detail = NULL`
- `updated_at = now()`

**保留字段**：`paused` / `consecutive_failures` / `last_attempt_at` / `last_run_id` / `in_flight_until`（这些是 probe worker 真实写入的运维状态）。

### 步骤 3 — 后置验证

执行脚本末尾段（`-- 5) ... -- 7)` 的 SELECT）：

- 命中数应清零
- 备份表行数应保留
- 30 秒后查 `credential_probe_queue` 应有新 enqueue（trigger_kind 为 `credential_recovery`，对应新代码路径）

## 何时不要执行

- 245 probe-canary 还没跑过 → 走完 §5 部署门禁的第一阶段后再清理
- 备份表没建好 → 必须先备份
- paused=true 的命中超过 30% → 不要直接清，先排查为什么 paused 后还能进 30s 循环（应已被旧代码 stop，新代码无此问题）

## 回滚

24 小时内如发现严重问题，按脚本末尾的回滚段把备份表写回。备份表保留 24h，24h 后人工 DROP。

## 245 预发 vs 154 生产

| 环境 | 是否执行 |
|------|---------|
| 245 probe-canary | 不需要（canary 用 tenant=999001 隔离，db15 隔离） |
| 245 全量 | 需要 |
| 154 生产 | 需要 |

## 监控指标

清理完成后 1h 内观察：

- `credential_probe_queue` 新 enqueue 数应 > 0（reconcileStaleNodeProbeStates 在跑）
- `node_probe_runs` 新插入数应 > 0（agent B 修复后 audit INSERT 不再被吞）
- `audit_unknown_source_total` 仍为 0
- `audit_persist_failed_total` 仍为 0
- `lease_lost_total` 仍为 0
