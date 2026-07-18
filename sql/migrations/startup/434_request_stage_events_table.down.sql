-- 434_request_stage_events_table.down.sql
-- 回滚 request_stage_events 表和视图

BEGIN;

DROP VIEW IF EXISTS upstream_5xx_distribution;
DROP VIEW IF EXISTS stage_performance_recent;
DROP TABLE IF EXISTS request_stage_events;

COMMIT;
