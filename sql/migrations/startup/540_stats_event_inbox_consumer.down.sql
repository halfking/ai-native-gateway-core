-- 540_stats_event_inbox_consumer.down.sql

BEGIN;

ALTER TABLE IF EXISTS stats_event_inbox
    DROP CONSTRAINT IF EXISTS stats_event_inbox_processing_status_check;
DROP INDEX IF EXISTS idx_stats_event_inbox_claimable;
DROP INDEX IF EXISTS idx_stats_event_inbox_dead_letter;
ALTER TABLE IF EXISTS stats_event_inbox
    DROP COLUMN IF EXISTS dead_letter_reason,
    DROP COLUMN IF EXISTS dead_lettered_at,
    DROP COLUMN IF EXISTS fencing_token,
    DROP COLUMN IF EXISTS next_attempt_at,
    DROP COLUMN IF EXISTS processing_status;

COMMIT;
