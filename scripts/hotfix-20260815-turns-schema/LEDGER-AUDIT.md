# 迁移账本 vs 实际 schema 核对报告（2026-08-15，245 + 154）

## 方法
- 仓库侧：`sql/migrations/startup/*.sql`（排除 .down/.skip/.disabled）提取数字版本号
- 数据库侧：`schema_migrations.version` 数字条目
- 两环境逐一比对，并抽查关键对象实际存在性

## 结论（245 与 154 完全一致）

| 项 | 数量 | 说明 |
|---|---|---|
| 账本数字条目 | 119 | 部署器按「412+ 逐版本核对」的基线制 |
| 仓库迁移文件 | 229 | 含大量基线纪元（<412）文件 |
| 文件不在账本 | ~110 | 全部为 <412 基线纪元 + `478p2` + 日期命名文件 |
| 账本不在文件 | 10 | `025,100,230,231,290,327,328,336,338,999`（上游改名/移除的历史记录） |

### 关键判定
1. **<412 的缺失属预期**：部署器把 412 之前视为 baseline（不重放），账本从 412 起逐版本核对。
   风险在于 baseline 与实际 schema 脱节——已在 2026-08-15 turns hotfix 中实证：
   `431/456/464/474/476` 记账但 DDL 未落到 public 表（历史原因：430 曾把 V2 表重复
   建到 gateway schema，513 又整体 DROP）。该批缺失已由本目录 SQL 修复。
2. **478p2 已应用但未记账**（反向漂移）：`idx_request_logs_hot_gw_session_id` 与
   `idx_request_logs_hot_auto_task_ts` 在两环境实际存在。无需补账（重放幂等，
   部署器核对 412+ 时以账本为准，不会重复执行）。
3. **日期命名迁移** `2026-07-13-multimodal-token-fields-hot.sql` 不在数字账本，
   属另一记账口径，未深入核对。

## 建议（后续治理，非本次范围）
- 为 baseline 纪元的会话域表（sessions/session_turns/session_bodies/session_dim/
  session_summaries/session_titles）建立对象级存在性巡检（列/约束/索引），
  而非依赖账本——本次 500 与影子写失败就是账本失真的直接后果。
- 账本中 10 条无对应文件的记录，建议在下次 schema 大版本时清理并注明来源。

## 核对脚本
见本文件同目录提交记录；核心比对逻辑：
`psql -Atc "select version from schema_migrations"` 与仓库文件版本号集合做差。
