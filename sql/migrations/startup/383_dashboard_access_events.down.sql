DROP VIEW IF EXISTS v_dashboard_user_activity;
DROP VIEW IF EXISTS v_dashboard_errors;
DROP VIEW IF EXISTS v_dashboard_slow_queries;
DROP VIEW IF EXISTS v_dashboard_access_stats;
DROP FUNCTION IF EXISTS ensure_dashboard_events_partition(DATE);
DROP FUNCTION IF EXISTS archive_dashboard_events(INT);
DROP TABLE IF EXISTS dashboard_access_events CASCADE;
DROP TABLE IF EXISTS dashboard_access_events_hot CASCADE;
