BEGIN;

DROP INDEX IF EXISTS idx_analysis_events_claimable;

ALTER TABLE analysis_events
    DROP COLUMN IF EXISTS claimed_by,
    DROP COLUMN IF EXISTS claimed_at;

ALTER TABLE session_embeddings
    DROP COLUMN IF EXISTS embedding_v2;

COMMIT;
