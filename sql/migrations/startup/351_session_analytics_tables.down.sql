-- 351_session_analytics_tables.down.sql
-- 回滚会话全景分析插件数据模型

BEGIN;

DROP TABLE IF EXISTS session_optimization_suggestions CASCADE;
DROP TABLE IF EXISTS session_cluster_members CASCADE;
DROP TABLE IF EXISTS session_clusters CASCADE;
DROP TABLE IF EXISTS session_embeddings CASCADE;
DROP TABLE IF EXISTS session_request_summaries CASCADE;
DROP TABLE IF EXISTS session_tags CASCADE;

COMMIT;
