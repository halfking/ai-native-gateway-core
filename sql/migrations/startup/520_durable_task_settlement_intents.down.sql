-- Down migration for 520_durable_task_settlement_intents.sql
BEGIN;
DROP TABLE IF EXISTS durable_task_settlement_intents;
COMMIT;
