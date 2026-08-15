# 2026-08-15 turns 会话列表线上 500 / 空数据 hotfix（245 + 154 已执行）

## 根因
1. `session_dim`（350/358/407）从未在 245/154 落地 → `/api/admin/turns/sessions` 500
   （relation "session_dim" does not exist）。
2. 431/456/464/474/476 的列/约束实际不在 public 表上：历史原因是 430 曾把 V2 表
   重复建到 gateway schema，513 又整体 DROP 了 gateway schema，早期迁移账本与
   public 表状态脱节 → sessionv2mirror 影子写入持续失败
   （column "attachment_count" ... / ON CONFLICT 42P10 / aggregate_applied_at），
   `public.sessions` / `public.session_turns` 长期为空。
3. `session_bodies_2026_07/08` 分区是 columnar 存取方法，不支持写入端必需的
   ON CONFLICT speculative insert（columnar_tuple_insert_speculative not implemented）。

## 修复内容（全部幂等，可重复执行）
- fix-turns-schema.sql：431+456 列、session_dim 建表（350+358+407）
- fix-turns-constraints.sql：476 tenant 维度唯一约束
- fix-bodies-heap.sql：session_bodies 07/08 分区重建为 heap（执行前确认分区为空）
- fix-turns-batch2.sql：464 aggregate_applied_at、474 附件索引+submit_mode CHECK、
  465 session_titles 主键去重、471 session_summaries 归档列

## 遗留事项
- 迁移账本（schema_migrations）记录与实际 schema 脱节的深层问题未治理，
  建议另行做一次 ledger vs 实际 schema 的全量核对。
- columnar 自动化名单（columnar_insert_only_parents）当前仅含
  routing_decision_log，不会把 session_bodies 转回 columnar。
