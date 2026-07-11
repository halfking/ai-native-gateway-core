DROP VIEW IF EXISTS v_sme_failures;
DROP VIEW IF EXISTS v_sme_cache_hit_rate;
DROP VIEW IF EXISTS v_sme_module_stats;
DROP FUNCTION IF EXISTS ensure_session_module_executions_partition(DATE);
DROP FUNCTION IF EXISTS archive_session_module_executions(INT);
DROP TABLE IF EXISTS session_module_executions CASCADE;
DROP TABLE IF EXISTS session_module_executions_hot CASCADE;
