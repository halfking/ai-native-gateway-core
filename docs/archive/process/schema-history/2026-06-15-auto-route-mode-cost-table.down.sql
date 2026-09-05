-- 2026-06-15-auto-route-mode-cost-table.down.sql
-- 回滚 customer cost dashboard
BEGIN;

DROP VIEW IF EXISTS model_cost_per_task_view;
DROP VIEW IF EXISTS customer_cost_view;

DROP TRIGGER IF EXISTS trg_update_api_key_model_cost ON request_logs;
DROP FUNCTION IF EXISTS update_api_key_model_cost();

DROP TABLE IF EXISTS api_key_model_cost;

COMMIT;