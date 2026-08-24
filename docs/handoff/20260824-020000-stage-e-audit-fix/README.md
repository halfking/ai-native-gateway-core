# Handoff — Stage E audit fix（已合 main @ `b996bb0c7`）

**日期**：2026-08-24  
**生产基线**：build **#1693+**（修复已合 main，已随 `8ce1e71e3` 一并推上 `origin/main`）  
**变更提交**：`fix(dispatch+bg): stage-E audit fixes — listener lifecycle, publisher ctx, migration parity`（`b996bb0c7`，14 文件，+618/−138）  
**CHANGELOG 条目**：`Stage E audit 修复（2026-08-24）`（`[Unreleased] / Fixed`）

---

## 1. 目标

Stage E（hot reload credential 治理策略发布）的 `bg.AutoRouteRealtimeListener`（`auto_route_refresh` 通知） `+ domains/dispatch.PolicyPublisher`（`credentials_revision` 通知） 与 migration 566 全链路审计出五类 P0/P1 缺陷。一次性修复后留个“Stage E 后续”文档，把 follow-ups、风险与验证基线记下来。

---

## 2. 修复总览

### A. `bg/auto_route_realtime_listener.go` — 生命周期/防抖/取消语义

| 缺陷 | 修复 |
|---|---|
| `Stop-before-Start` 永久死锁（`<-l.done` 从未关闭） | `started atomic.Bool` + `stopOnce sync.Once`；未启动时 `Stop` 立即返回 |
| 重复 `Start` 双开 goroutine、重复 close `done`、竞态 | `started.CompareAndSwap(false,true)` 幂等屏障 |
| `time.Sleep(5*time.Second)` 取消不感知 | 统一替换为 `sleepCtx(ctx, d)` 工具函数（select on `<-ctx.Done()` 与 `<-timer.C()`） |
| 一次性 `goroutine-per-NOTIFY` 防抖（第一个 timer 赢，`lastPending` 从不读） | 改为单 owned trailing-edge 防抖循环：`notifyCh` buffer(64) + 单一 `debounceLoop`；每条 NOTIFY reset timer，`timer.C` 才跑一次 `RefreshOnce`；最后一个 burst 事件后 `debounceWindow` 才执行 |
| 失败刷新清脏标，丢失待重试 | 失败后保留 `pending=true`、重置 timer、下一窗再试；仅成功后清 `pending` |
| `RefreshOnce` 用 `context.Background()` 跳过 lifecycle，关闭后还能跑 | `RefreshOnce` ctx 改 `rctx, cancel := context.WithTimeout(lCtx, 30*time.Second)`，受 `Stop` 取消 |

测试 `bg/auto_route_realtime_listener_test.go` 8 个用例：
`StopWithoutStartDoesNotDeadlock`、`StopTwiceDoesNotPanic`、`StartWithoutPoolIsNoop`、`StartTwiceThenStopReturnsPromptly`、`DebounceCoalescesBurst`、`DebounceUsesTrailingEdge`、`StopCancelsPendingRefresh`、`FailedRefreshRetries`、`HandleNotificationNeverBlocks`。`go test -race ./bg` 全绿。

### B. `domains/dispatch/policy_publisher.go` — ctx + fail-closed

| 缺陷 | 修复 |
|---|---|
| `run` 用 `context.Background()` 忽略调用方取消，关闭时无法取消 | `Start(parent)` → `WithCancel(parent)` 透传 |
| backend `NotifyRevisions` 失败时仍 `SetActivePolicyRevision`，revision 跳跃 | 改为 fail-closed：失败返回 error，`lastRevision` 与 `Pipeline.ActiveRevision` 都保持原值；`publishCatchUpWithRetry` 含 4 次指数回退重试，同一 delta 不前进多次 |
| 死 `stopCh` plumbing | 移除（ctx 取消承担） |

### C. Migration 566 + db ensure + 对象文件 + schema dump

| 项 | 修复 |
|---|---|
| `setval('credentials_governor_revision_seq', MAX(revision), ...)` 回卷已下发的 revision | `GREATEST(MAX(revision), pg_sequence_last_value(...))`；`is_called` 也用 `pg_sequence_last_value IS NOT NULL`；rerun 永不回卷，revision 不复用 |
| 非 governor 列变更也 bump revision | bump function 显式 `NEW.revision := OLD.revision`（防手工回写） |
| 注解错（“11 列”） | 改为 7 列基线（`status, availability_state, quota_state, circuit_state, concurrency_limit, lifecycle_status, manual_disabled`，即 307 baseline） |
| `01-schema.sql` 缺 `CREATE SEQUENCE` 与 `CREATE INDEX` | 补齐 `credentials_governor_revision_seq` 与 `credentials_revision_idx`；新增对应 `sql/objects/sequences/credentials_governor_revision_seq.sql` 与 `sql/objects/indices/credentials_revision_idx.sql`；fresh bootstrap 不再缺对象 |
| 测试仅文本契约 | 新增 `TestMigration566EnsureParity`（打开 `db/db.go` 提取 `ensureCredentialGovernorRevision` 函数体，断言 `pg_sequence_last_value`、`NEW.revision := OLD.revision;`、`pg_notify('credentials_revision', ...)` 都在 ensure 内出现，防止 migration/ensure 漂移） |

### D. `admin/provider_credential.go` — `pgx.ErrNoRows` + 空 PATCH 缓存 + 审计

| 缺陷 | 修复 |
|---|---|
| 错误匹配 `err.Error() == "no rows in result set"` 脆且不跨驱动 | 改 `errors.Is(err, pgx.ErrNoRows)`；`import "github.com/jackc/pgx/v5"` + `"errors"` |
| 空 PATCH 不失效 candidate cache | 补 `provider.InvalidateAllCandidateCache()` 走 `currentRevision` 分支（写完 revision=0 之前） |
| `fp_slot_limit` 变更无 `settings_history` 审计 | 补 `INSERT INTO settings_history` 在 `tx.Exec` 内（与原变更同事务，`actor` 用 `X-Admin-User` header，默认 `admin`） |

### E. `cmd/gateway/main.go` — 关闭顺序

`autoRouteListener.Stop()` 不再嵌套在 `autoIndexRefresher != nil` 之下；`Stop` listener 必须先于 `autoIndexRefresher.Stop()`，避免 NOTIFY 触发的 refresh 与已停止的 refresher 抢资源。

---

## 3. 验证

```bash
go build ./...                                                # green
go vet ./bg ./domains/dispatch ./sql/migrations/startup ./admin ./db ./cmd/gateway
go test -race ./bg ./domains/dispatch ./sql/migrations/startup ./admin ./db ./cmd/gateway -count=1
# All packages pass. auto_route_realtime_listener_test.go exercises 8 cases
# (lifecycle / debounce / cancellation / race / notify-not-blocking).
```

`TestPipelineQueueStats_RealPipelineTraffic` 在 full suite 下偶发 miniredis timing flake（与本修复无关，已知问题，需后续独立修整）。

---

## 4. 不在范围内（已记录在 §3，但本次未做）

下列项目原审计摘要列出，但**未**被本次 commit 覆盖；列入 follow-up 而非本 handoff 责任：

| 项 | 原因 |
|---|---|
| `Pipeline` 加 `GovernPolicyApplier()` 并让 `PolicyPublisher` 通过 `ApplyPolicySnapshot` 写活 `Pipeline.activePolicyRevision` | 跨阶段耦合，留给 Stage F 重组；本次仅把 `PolicyPublisher` 的 run-loop / ctx 修好，没动接口 |
| `Pipeline.activePolicyRevision` 走 `ApplyPolicySnapshot`（非 `Pipeline.SetActivePolicyRevision`）的产线语义 | 同上 |
| `credForwarder.gov` 真热切换 | Stage F 范围；本次只做到 publisher 端 `fail-closed` 防误报，不改 live forwarder |
| AutoRouteRealtimeListener 集成测试（真实 PG LISTEN） | 需要 testcontainer PG harness；本仓现仅单元 + fake refresher；后续接入 testcontainer harness 时补 |
| `bg.credential_autoheal` / `provider.InvalidateAllCandidateCache` 集成路径 Stage E 端到端 e2e | 待 Stage F 合并后统一 e2e |
| `bg/auto_route_realtime_listener` 旧 `lastPending` 字段移除 | 注释中已确认 dead；后续跟 dead-code cleanup 任务一起删 |
| `bg.credential_autoheal` 与新 publisher 串接的 context 校验 | 等 Stage F |

---

## 5. 部署后复测清单

1. burst 通知合并：连续 50 条 `auto_route_refresh` NOTIFY 触发 1 次 `AutoIndexRefresher.RefreshOnce`。
2. 关闭路径：`Ctrl-C` 后 5s 内 `autoRouteListener.Stop()` 与 `pipeline.Stop()` 均返回；miniredis/refresher pool 释放无 leak。
3. migration 566 幂等：154 环境重跑 `.up.sql` 与 `.down.sql` 各一次，credentials 表 `revision` 列归位，无 NOTIFY 残留。
4. admin 端：PATCH `fp_slot_limit=12`、`status='cooling'`（非 governor 列）—— `settings_history` 仅 `fp_slot_limit` 一行；`pg_notify` 触发 1 次（governor 列变更场景）。

---

## 6. 风险与回退

- migration 566 `setval` 在 rerun 不回卷；但 `CREATE OR REPLACE FUNCTION` 重写 `bump_credentials_governor_revision()`，已部署 154 需在低峰窗口执行（写入期 <1s，函数替换瞬时，但 `NOTIFY` 流量会有 <1s 双发）。
- 关闭顺序改为 listener → refresher 优先于原 顺序；新顺序在 5s `stopCtx` budget 内能完成两段清理（refresher Stop 不再被 listener 触发的 refresh 抢）。
- Stage F 前不要启 `auto_route_shadow_log` 一类的另一条 NOTIFY 通道（暂未引入）。

---

## 7. 关联文件

- `bg/auto_route_realtime_listener.go`、`bg/auto_route_realtime_listener_test.go`
- `domains/dispatch/policy_publisher.go`、`domains/dispatch/policy_publisher_test.go`
- `db/db.go`（`ensureCredentialGovernorRevision`）
- `sql/migrations/startup/566_credentials_governor_revision.sql` / `.down.sql`
- `sql/migrations/startup/migration_566_test.go`
- `sql/objects/sequences/credentials_governor_revision_seq.sql`
- `sql/objects/indices/credentials_revision_idx.sql`
- `sql/objects/functions/bump_credentials_governor_revision.sql`
- `sql/schema/01-schema.sql`
- `admin/provider_credential.go`
- `cmd/gateway/main.go`
- `CHANGELOG.md`（`[Unreleased] / Fixed` 段）

---

## 8. 新会话提示词（复制即用）

```
llm-gateway-go Stage E audit 修复后 — 2026-08-24 后续。

已完成合 main（b996bb0c7）：
- AutoRouteRealtimeListener 生命周期（Start 幂等、Stop 不死锁、debounce trailing-edge、ctx-aware refresh）
- PolicyPublisher 尊重 parent ctx、backend NotifyRevisions fail-closed
- migration 566 setval 不回卷 + bump 保留 OLD.revision + schema dump 补 sequence/index
- admin updateCredential: errors.Is(pgx.ErrNoRows)、空 PATCH 缓存、settings_history 审计恢复
- main.go: listener 先于 refresher 关闭

待选：
1. Pipeline GovernPolicyApplier 接口 + ApplyPolicySnapshot 产线语义（活路径）
2. credForwarder.gov 真热切换
3. AutoRouteRealtimeListener testcontainer LISTEN e2e
4. credForwarder HotConfig + publisher 串接 e2e（Stage F 入口）
5. 移除 dead lastPending 字段

参考：docs/handoff/20260824-020000-stage-e-audit-fix/README.md
```
