# 06-deployment/02-database — 数据库（schema/migration/backup）

## 职责

维护当前数据库运行、同步、迁移与恢复文档。当前环境仅包括 RDS production、252 test 和 local Docker。

本地刷新数据库的标准入口：`local-pg-sync-from-252.md`。

## 命名约定

- 环境：`{env}.md`（dev.md / staging.md / prod.md）
- 数据库：`schema-design.md` / `migrations.md` / `backup-restore.md`
- 初始化：`init-scripts.md` / `seed-data.md`
- Runbook：`{场景}-runbook.md` 或 `incident-response.md`
