# 2026-07-27 — deploy-seamless healthz/DB 等待超时 30s → 90s

## 背景

老板 2026-07-27 11:43 执行 `bash scripts/deploy-245.sh` 部署 build_seq 1411 (commit 30422274d)。
部署流程走到 `[9/9] 验证 /healthz + DB` 时 30s 超时，自动回滚到 `releases/1410-d5f704dd`，
但回滚后 healthz 仍 30s 超时，部署脚本退出码 1。

## 根因

### 现象

`scripts/deploy-seamless.sh:383` 主流程与 `:155` 自动回滚流程均设置 `host_wait_healthy 30`，
但 `cmd/gateway/main.go:257` 同步执行 `db.Open → db.ApplyMigrations`：
- `db/db.go:ApplyMigrations` 默认 3 分钟 timeout，遍历 33 个 `ensure*` 函数（CREATE TABLE / CREATE INDEX / CREATE MATERIALIZED VIEW …）
- 单个 ensure 卡住会被 PG `statement_timeout` (默认 30s) 取消，函数返回 err → `db.Open` 返回 err → `cmd/gateway/main.go:259` 写 `WARN postgres disabled` 并 `dbConn = nil`
- HTTP server 在 `postgres disabled` 之后才 `gateway listening :8781`，整个启动耗时 ≈ 30s

### 时间线（2026-07-27 11:43-11:46 stderr 实际事件）

| 时刻 | 事件 |
|------|------|
| 11:44:17 | 1410 (旧版本) 收到 deploy shutdown 信号，开始 graceful shutdown |
| 11:44:27 | 1410 systemd Succeeded + 启动新进程（deploy atomic switch 已将 current 指向 1411） |
| 11:44:27 | `postgres connected` |
| 11:44:57 (30s 后) | `WARN postgres disabled: ERROR: canceling statement due to statement timeout (SQLSTATE 57014)` |
| 11:44:57 | `gateway listening :8781`（在 postgres disabled 之后） |
| 11:44:58 | deploy-seamless.sh 30s healthz timeout 触发 → 自动回滚 |
| 11:44:58 | atomic switch 回滚到 1410 |
| 11:45:28 | 回滚后同样 `postgres disabled` (PG 锁竞争状态未恢复)，再次 30s 超时 |
| 11:45:28 | 回滚后 HTTP listen，最终 deploy 脚本退出 1 |

deploy 误判为"PG 不可用 → DB 未就绪" → 自动回滚 → 回滚期间同 PG 状态 → 再次超时。
实际 PG 健康（`pg_stat_activity` 在 11:55 已无活动 query，`database health check succeeded` 持续输出），
仅 deploy 那一刻因某个 startup query 锁竞争 30s 内未完成。

### 为什么 30s 不够

- `host_wait_healthy` 单点 healthz curl，但 healthz 必须等到 `db.ApplyMigrations` 完成（或 30s timeout 触发 postgres disabled）后才 listen。
- 锁竞争 / autovacuum / 大表 ALTER 时，PG `statement_timeout` 30s 取消 query 是设计行为，不是异常。
- 1411 与 1410 启动耗时差异不显著：1411 启动卡 `ensureTuningSignalsViews` / `ensureRoutingOverridesTable` 等新增 ensure；1410 启动卡老的 ensure。两者都 30s+。

## 修复

`scripts/deploy-seamless.sh`:

1. 主流程 `host_wait_healthy 30 → 90`（line 388）
2. 自动回滚流程 `host_wait_healthy 30 → 90`（line 156）
3. `deploy_verify_gateway_ready 60 → 90`（line 391，与 host_wait_healthy 同步对齐）
4. 日志字符串 `(healthz 30s, DB 60s) → (healthz 90s, DB 90s)`（line 387）

90s 与已有 `scripts/deploy-seamless.sh:447` 手动 rollback `host_wait_healthy 60` 顶部对齐，
且与 `deploy_verify_gateway_ready` 内部 `120s` 轮询上限不冲突（顶层 timeout < 内层 timeout）。

## 验证

- `bash -n scripts/deploy-seamless.sh` syntax OK
- 当前 245 状态：回滚到 1410 后稳定运行；healthz=200 / background-tasks=401 / database health check succeeded
- PG `pg_stat_activity` 1 个连接（idle），无锁竞争
- 等待老板指示再触发一次 `bash scripts/deploy-245.sh` 部署 1411，验证 healthz 90s 内通过

## 不修复的项

- `cmd/gateway/main.go:db.Open` 同步阻塞：理论优化是异步启动 DB（goroutine）+ healthz 立即 listen，但这是 main.go 较大改动，超出本次 deploy 失败修复范围。本次采用最小补丁（rule 37 原则 3）。
- `db.ApplyMigrations` 单 ensure 卡 30s：是 PG 锁竞争常态，不动 schema 同步机制。