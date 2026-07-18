-- 435_provider_quality_tables.down.sql
-- 回滚 provider_quality_tables 迁移

BEGIN;

DROP VIEW IF EXISTS provider_quality_summary;
DROP TABLE IF EXISTS provider_quality_events;

COMMIT;
