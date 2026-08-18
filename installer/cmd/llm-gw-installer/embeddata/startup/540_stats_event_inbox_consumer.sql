-- 540_stats_event_inbox_consumer.sql
-- Durable claim, retry, fencing and dead-letter state for the stats inbox.

BEGIN;

SET LOCAL lock_timeout = '5s';

DO $$
BEGIN
    IF to_regclass('stats_event_inbox') IS NULL
       OR to_regclass('usage_facts') IS NULL THEN
        RAISE EXCEPTION '540_stats_event_inbox_consumer requires migrations 536_stats_analytics_foundation and 537_usage_facts';
    END IF;
END $$;

ALTER TABLE IF EXISTS stats_event_inbox
    ADD COLUMN IF NOT EXISTS processing_status text NOT NULL DEFAULT 'pending';
ALTER TABLE IF EXISTS stats_event_inbox
    ADD COLUMN IF NOT EXISTS next_attempt_at timestamptz NOT NULL DEFAULT now();
ALTER TABLE IF EXISTS stats_event_inbox
    ADD COLUMN IF NOT EXISTS fencing_token bigint NOT NULL DEFAULT 0;
ALTER TABLE IF EXISTS stats_event_inbox
    ADD COLUMN IF NOT EXISTS dead_lettered_at timestamptz;
ALTER TABLE IF EXISTS stats_event_inbox
    ADD COLUMN IF NOT EXISTS dead_letter_reason text;

DO $$
BEGIN
    IF to_regclass('stats_event_inbox') IS NOT NULL
       AND NOT EXISTS (
           SELECT 1 FROM pg_constraint
           WHERE conname = 'stats_event_inbox_processing_status_check'
             AND conrelid = 'stats_event_inbox'::regclass
       ) THEN
        ALTER TABLE stats_event_inbox
            ADD CONSTRAINT stats_event_inbox_processing_status_check
            CHECK (processing_status IN ('pending', 'processing', 'processed', 'retryable', 'dead_letter'));
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_stats_event_inbox_claimable
    ON stats_event_inbox (next_attempt_at, occurred_at, created_at)
    WHERE processing_status IN ('pending', 'retryable', 'processing');
CREATE INDEX IF NOT EXISTS idx_stats_event_inbox_dead_letter
    ON stats_event_inbox (tenant_id, occurred_at DESC)
    WHERE processing_status = 'dead_letter';

COMMIT;
