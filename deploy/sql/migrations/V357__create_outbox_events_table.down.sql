-- V357__create_outbox_events_table.down.sql
-- Rollback for V357: Drop outbox_events table

-- Drop trigger first
DROP TRIGGER IF EXISTS trigger_outbox_events_updated_at ON outbox_events;
DROP FUNCTION IF EXISTS update_outbox_events_updated_at();

-- Drop indexes (will be dropped automatically with table, but explicit for clarity)
DROP INDEX IF EXISTS idx_outbox_events_failed_attempts;
DROP INDEX IF EXISTS idx_outbox_events_tenant_occurred;
DROP INDEX IF EXISTS idx_outbox_events_aggregate;
DROP INDEX IF EXISTS idx_outbox_events_dispatch;

-- Drop table
DROP TABLE IF EXISTS outbox_events;
