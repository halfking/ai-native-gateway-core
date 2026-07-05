-- 310_session_summaries.down.sql
-- 回滚会话聚合视图

-- 1. 删除视图
DROP VIEW IF EXISTS session_stats_today;

-- 2. 删除触发器
DROP TRIGGER IF EXISTS trg_update_session_summary ON request_logs;

-- 3. 删除函数
DROP FUNCTION IF EXISTS update_session_summary();
DROP FUNCTION IF EXISTS array_unique_append(TEXT[], TEXT);

-- 4. 删除表（级联删除索引和策略）
DROP TABLE IF EXISTS session_summaries CASCADE;
