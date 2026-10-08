-- 845 down: 删除 stats 日聚合 JOIN 承重复合索引（恢复 536 原状：仅 pkey(event_id)）。
-- 回滚后 stats 日聚合将回到 30s 击杀边缘（见迁移头注），仅在明确指令下执行。
DROP INDEX CONCURRENTLY IF EXISTS idx_stats_event_dedup_event_id_occurred_at;
