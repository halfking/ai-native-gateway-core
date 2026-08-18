-- 537_usage_facts.down.sql
-- Destructive rollback for canonical usage facts. Export facts before use.

BEGIN;
DROP TABLE IF EXISTS usage_facts;
COMMIT;
