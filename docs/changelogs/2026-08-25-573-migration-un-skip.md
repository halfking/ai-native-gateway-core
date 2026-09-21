# 2026-08-25 — 573 迁移 un-skip + `data_lifecycle` trend LEFT JOIN bodies (LP1 follow-up)

> **LP1 follow-up (2026-08-25)** — 迁移 573 因 PG 的 `CREATE OR REPLACE VIEW`
> 不能移除已有列而阻塞所有部署（原 commit `446638792` `.sql.skip`）。
> 此次把 view 重建改为 `DROP + CREATE`，并在生产（<env:HOST_252_INTERNAL_IP>/llm_gateway）
> 直接验证后 un-skip deploy gate。

## 背景

LP1 把 body 写入拆分到 `request_logs_bodies_hot` 后，热表 `request_logs_hot`
+ 父表 `request_logs` 上的 3 个 body 列（`request_body` / `response_body` /
`outbound_body`）成为冗余副本。迁移 573 原计划 `CREATE OR REPLACE VIEW
request_logs_with_current_month` 直接重建视图，但 PG 的 `CREATE OR REPLACE
VIEW` **只能追加尾部列，不能移除已有列**——所以原 SQL 在生产报 42703
（列 `outbound_body` 已被 drop 但 view 还在引用），被 `.sql.skip` 延后。

`admin/data_lifecycle.go` 的"最近 7 天增长趋势"接口 SELECT 该 view 的
`outbound_body IS NOT NULL` 计数压缩率，是少数 18 个 reader 之一由 LP1
owner 修过 LEFT JOIN bodies、但**还有 1 处**遗漏——这条接口在 573 应用后
会因 42703 报 SQL 错误，被应用层 `slog.Warn` 吞掉，前端"压缩率"图显示
空数组（**非 5xx**，业务可降级为"无数据"）。

## 改动

| 文件 | 变化 |
|------|------|
| `sql/migrations/startup/573_drop_request_logs_body_columns.sql.skip` → `.sql` | un-skip deploy gate；view 重建改为 `DROP VIEW bodies_progress + DROP VIEW request_logs_with_current_month + CREATE VIEW`（同事务）|
| `admin/data_lifecycle.go` | trend SQL 加 `LEFT JOIN request_logs_bodies_with_current_month rb ON rb.request_id = rl.request_id`，把 `compressed` 谓词从 `outbound_body IS NOT NULL` 改为 `rb.outbound_body IS NOT NULL`，列名加 `rl.` 前缀 |

**关键修复**：`DROP` 必须包含 `request_logs_bodies_progress`（migration 328a
2026-07-02 引入的 backfill 进度视图，其 `definition` 引用 `request_logs.body`
列做 `IS NOT NULL` 检查）。573 应用后该 view 的定义变成 42703，所以一并
DROP。pg_depend 不记录 view→基表列级依赖（PG 17 改用 parse tree 检查），
需用 `pg_get_viewdef` 文本搜索才能完整覆盖。

## 影响面

| 维度 | 数值 |
|------|------|
| DB schema 改动 | 1 view 重建（116→113 列）+ 1 view DROP（bodies_progress）+ 6 DROP COLUMN（hot + parent，PG 11+ partition propagation 到 5 monthly + default）|
| 实际磁盘收益 | **2.27 GB**（仅 `request_logs_hot.outbound_body` 的冗余副本；其它 2 列全 0 字节） |
| 业务数据损失 | **0**（body 真值在 `request_logs_bodies_hot` 11 GB / 95,041 行完整保留） |
| 应用接口影响 | 1 处降级（admin 数据生命周期 trend 接口；非 5xx，可降级） |

## 回滚方案

`sql/migrations/startup/573_drop_request_logs_body_columns.down.sql`（已存在）
加回 3 列 jsonb NULLABLE + 重建 view + 重建 `request_logs_bodies_progress` view。
预计 < 5 分钟，rollback 后业务接口立即恢复。

## 验证（生产 <env:HOST_252_INTERNAL_IP>/llm_gateway）

- 迁移应用：2026-08-25 01:09，`psql --single-transaction -v ON_ERROR_STOP=1`
  （失败自动 ROLLBACK，第一次 dry-run 因漏 DROP `bodies_progress` 失败，已
  修补后重试成功，事务 COMMIT）
- view 列数：108（原 113 列；hot 表的 113 列又少了 5 列 — 拆分期间调整过 SELECT
  列表，并非 573 直接删除列）
- `request_logs_hot`：0 个 body 列 ✓
- `request_logs` 父表：0 个 body 列 ✓（propagate 到所有月度分区 + default）
- `request_logs_bodies_hot`：95,041 行 / 11 GB **完整保留** ✓
- `go build ./...` exit 0 ✓
- `go vet ./admin/...` exit 0 ✓

## Refs

- LP1 plan: `docs/04-implementation/plan/2026-08-24-request-body-storage-optimization-plan.md`
- Handoff: `docs/handoff/2026-08-24-LP1-deploy-gate.md`
- Original `.sql.skip` blocker commit: `446638792 docs: changelog for admin 401 polling storm fix + 573 migration deferral`
- Original view column projection: `sql/objects/views/request_logs_with_current_month.sql`
- `request_logs_bodies_progress` (DROP 目标): `sql/objects/views/request_logs_bodies_progress.sql` + migration `328a_request_logs_bodies_table.sql`
- Rule 19 §11 三阶审批：DROP COLUMN 不可逆 + 数据完整性 + 回滚方案 + 老板签字后执行
- Rule 49 §9.1：PG 的 `CREATE OR REPLACE VIEW` 只能追加列，必须 DROP+CREATE 才能移除列
- Rule 20：view 重建由 schema-truth-first 校验（不破坏 view 列投影）