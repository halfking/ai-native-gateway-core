# 2026-08-06 — Promote 争用导致启动 EnsureSchema 超时 → 部署自动回滚

## 背景

154 生产连续两次部署（1464-fc606a69）验证失败并自动回滚到 1461-fc395aac：
新进程启动时 `db.Open()` 的 `ensureRequestLogSchema` 被 PG 取消，
日志出现 `postgres disabled ... ERROR: canceling statement due to statement
timeout (SQLSTATE 57014)`，网关进入永久 no-DB 模式，deploy verify 视为失败。

## 根因

- 共享库（252 PG，245 + 154 双网关共用）`statement_timeout = 30s` 全局生效；
  `ensureRequestLogSchema` 是单个巨型 Exec（19 列 ALTER + 12 个 CREATE INDEX），
  空闲仅 0.24s，但在锁争用窗口下阻塞 >30s 即被 PG 取消。
- 慢性争用源：`request_logs_bodies_hot` 已积累 13 GB / 37k 行（平均 ~350 KB/行，
  TOAST 大字段）。promote 批量硬编码 5000 行 → 每批移动 ~1.7 GB → 稳定超过
  30s 语句超时 → 每小时 tick 反复失败、锁 + I/O 空转；且两个网关每小时对同一
  共享表并发 promote，互相阻塞（无 advisory lock）。
- 结论：启动失败是**锁争用下的持续阻塞**（非语句本身慢），拆分语句无法解决，
  需要**有界重试** + **根治 promote 争用**。

## 修复（三个独立但协同的改动）

1. `db/db.go` — `ApplyMigrations` 有界重试（2 次，backoff 5s）。
   瞬态 57014 不再永久 brick 进程；最坏 ~65s < systemd TimeoutStartSec=90s。
   原函数体改名 `applyMigrationsOnce`，逻辑不变。
2. `bg/partition_manager.go` — `request_logs_bodies` promote 批量改为可配置
   `lifecycle.request_logs_bodies_promote_batch_size`，默认 500（~175 MB/批），
   替代硬编码 5000。每批稳定低于 30s 上限，promote 才能真正推进。
3. `bg/partition_manager.go` — promote 每批包事务级 advisory lock
   （`pg_try_advisory_xact_lock(promoteLockKey(label))`）。两个网关对同一
   热表序列化，未抢到锁的实例本 tick 跳过该表，不再阻塞进语句超时。

## 验证

- `go build ./...`、`go vet ./db/... ./bg/...` 全绿
- `go test ./bg/ ./db/` 全绿；新增单测：bodies 批量默认值 < 5000 且 ≥ 100、
  lock key 跨实例确定性（无 DB 依赖）

## 部署

- 生产部署为 human_only：先部署 245 预发布验证，再 154 生产
- 历史遗留：13 GB `request_logs_bodies_hot` 积压需要数个 promote tick 逐步
  消化（每 tick ~75 批 × 500 行），期间共享库锁窗口仍在，但单批已低于超时上限

## 后续建议（本次未做）

- 对积压 bodies 手工触发一次小批量回放，或评估直接归档/清理 > 24h 旧 bodies
- 可选：把 `SET statement_timeout` 改为按连接差异化（迁移连接用更大超时），
  进一步降低启动对争用的敏感度
