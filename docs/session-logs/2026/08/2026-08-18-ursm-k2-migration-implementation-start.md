# 2026-08-18 — URSM k2 key 迁移实施启动（Slice 0）

## 需求与范围

按批准的 L1→L3 实施计划启动 URSM delimiter-safe Redis key 迁移。本条目记录 Slice 0：Migration owner 具名、ledger ID 与目标 Redis 拓扑冻结、显式移交文件清单落档。仅修改 Markdown 文档；不修改生产 Go、Lua、SQL，不触碰生产 Redis/PostgreSQL。

## 基线与所有权

- 实施基线：worktree 分支 `feat/ursm-k2-migration`，基点 `fafdc60a5ec34416a72a0df654943ec81d001c34`（origin/main tip，2026-08-18）。
- 设计冻结基线：`7a219f265`（本地 HEAD，与 doc 13/14 冻结时点一致）；`7a219f265..fafdc60a5` 之间 7 个提交（credentials Fernet 修复、streaming keepalive、web NodeDetailDrawer）均不触及 `domains/ursm/**`。
- 主工作树 `main` 的 11 个未归属 Web 导航文件改动原样保留：未 stash、未 reset、未 add、未 commit，与本 workstream 隔离。
- 本轮 owner：Migration owner（`halfking` 具名，ZCode 执行）。实施计划已经用户批准（四项决策：owner 具名、standalone-only 拓扑、fafdc60a5 基点、Redis meta + PG 080 双轨 ledger）。

## 冻结记录（doc 14 §10）

1. Migration owner = `halfking`；`ledger_id = ursm-v2-k2-20260818-001`（不可复用）。
2. 目标 Redis 拓扑 = standalone 单实例；k2 grammar 不含 hash-tag；**Redis Cluster 为 NO-GO blocker**（legacy 无 tag，dual 原子写不可能在 Cluster 成立）；G 阶段前须运维书面确认 154/245/acc 为 standalone。
3. ledger 落点 = Redis `<prefix>meta:migration:*`（doc 14 §3 字段）+ PG `sql/migrations/080_ursm_key_migration_ledger.sql` + `db/db.go` ensure 函数；断点续跑复用 durable lane owner+fencing_token 模式。
4. §10.1 显式移交文件清单（store keys/record_request/pipeline/persist writer/recovery coverage 段/bootstrap/migration 包/preflight CLI/SQL 080/doc 13-14）；§10.2 Slice 0b 三项字节等价前置 + deprecated 标注。

## 文档产物

- [14-URSM Redis delimiter-safe key兼容迁移冻结决策](../../../03-design/02-feature-design/会话优化v4/14-URSM Redis delimiter-safe key兼容迁移冻结决策.md)：新增 §10 冻结记录、§10.1 移交清单、§10.2 移交前置；头部状态与 §9 变更记录更新。
- [13-T0契约冻结与所有权](../../../03-design/02-feature-design/会话优化v4/13-T0契约冻结与所有权.md)：§1 增加具名与移交清单指针。

## 审查与验证

本条目为 Slice 0 文档记录；验证在 Slice 0b/L1 提交时按 doc 14 §7 门槛执行（相关包测试、全仓 `go test -count=1 ./...`、`-race`、`go vet`、`go build`、pre-commit、scoped secret scan、双轴 review）。未执行真实 Redis/PG/provider 验证。

## 裁决不变

`M5-0/T0 = BLOCKED / NO-GO`。owner 具名解锁 L1-L3 实施，但不构成 G1/G4 证据；G1 判定只能在 L3 完成后由独立审计按 doc 14 §7 全门槛作出。miniredis/sqlmock/SKIP 一律标注替身。
