-- Rollback Migration 355: Drop Session Analytics Indexes

DROP INDEX IF EXISTS idx_request_logs_gw_session_id;
DROP INDEX IF EXISTS idx_request_logs_tenant_ts;
DROP INDEX IF EXISTS idx_request_logs_ts_day;
DROP INDEX IF EXISTS idx_session_summaries_health_grade;
DROP INDEX IF EXISTS idx_session_summaries_outcome;
DROP INDEX IF EXISTS idx_session_summaries_health_score;
