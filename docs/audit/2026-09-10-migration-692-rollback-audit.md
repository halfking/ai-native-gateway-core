# 2026-09-10 24h 审计（第八轮 — Migration 692 rollback closure）

## 范围

审阅 `e3738133a` 引入的 startup migration 692：
`session_summaries.user_intent` 从 `varchar(50)` 扩展到 `varchar(200)`，以及
canonical migration 与 installer embeddata 副本的 up/down 一致性、事务闭环和
回滚兼容性。

## 发现与修复

### P1：down migration 未处理 `v_session_flow` 视图依赖

692 up migration 已先删除并重建 `public.v_session_flow`，但 down migration 原先
直接执行 `ALTER COLUMN user_intent TYPE varchar(50)`。由于该视图直接选择
`user_intent`，PostgreSQL 会以 SQLSTATE `42P16` 拒绝回滚，导致 schema ledger
无法完成删除，回滚流程不闭环。

已在以下两份 down 文件同步修复：

- `sql/migrations/startup/692_session_summaries_user_intent_widen.down.sql`
- `installer/cmd/llm-gw-installer/embeddata/startup/692_session_summaries_user_intent_widen.down.sql`

修复顺序为：

1. `DROP VIEW IF EXISTS public.v_session_flow`
2. `ALTER COLUMN user_intent TYPE varchar(50)`
3. 按 V354 原始定义重建视图并恢复注释
4. 删除 migration 692 ledger 行并提交事务

回滚仍会在存在超过 50 字符数据时失败；这是 down 文件原有的显式安全告警，
避免静默截断语义数据，执行前需按告警先处理超长值。

## 数据流与兼容性核验

- up/down 均在单事务中执行，失败时 PostgreSQL 回滚视图、列类型和 ledger 变更。
- 重建视图保留 V354 的列清单、任务/项目窗口函数和 `WHERE` 条件；项目维度
  使用 `PARTITION BY tenant_id, gw_project_id`。
- canonical 与 installer embeddata down 副本逐字一致。
- migration 692 的 up 文件仍与当前 installer embeddata 副本一致。
- startup migration 数字版本无重复。

## 验证

- `go test ./...` PASS
- `cd installer && go test ./...` PASS
- `go test -race ./proxy -count=1` PASS
- `go test ./sql/migrations/startup -run 'TestMigration692DownRecreatesDependentView|TestNumericUpMigrationVersionsAreUnique' -count=1` PASS
- `cd installer && go test ./cmd/llm-gw-installer -run TestStartupFilesAreAllEmbedded -count=1` PASS
- `go vet ./sql/migrations/startup ./proxy` PASS
- `cd installer && go vet ./cmd/llm-gw-installer` PASS
- `git diff --check` PASS

## 结论

本轮发现的 migration 692 rollback 缺陷已修复，并补充契约测试防止 down 文件
再次退化。当前审计范围无未关闭的 P0/P1/P2 缺陷；低优先级后续候选仍以
R7 报告为准，不属于本轮 migration 修复范围。
