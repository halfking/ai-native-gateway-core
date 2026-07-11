-- Migration 387: session analytics embedding dimensions and event claims.

BEGIN;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_type WHERE typname = 'vector') THEN
        ALTER TABLE session_embeddings
            ADD COLUMN IF NOT EXISTS embedding_v2 vector(1024);
    ELSE
        RAISE NOTICE 'pgvector unavailable; session embeddings remain hash-only';
    END IF;
END $$;

ALTER TABLE analysis_events
    ADD COLUMN IF NOT EXISTS claimed_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS claimed_by TEXT;

CREATE INDEX IF NOT EXISTS idx_analysis_events_claimable
    ON analysis_events (occurred_at)
    WHERE processed_at IS NULL;

COMMIT;
