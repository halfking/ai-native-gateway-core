-- Migration: 2026-07-13 Add multimodal & reasoning token fields to ALL related tables
--
-- Background:
--   commit 5447bf6b1 (audit-ir-multimodal, 2026-07-13 05:03) extended
--   RequestLogEntry with 5 new fields (reasoning_tokens, image_tokens,
--   audio_tokens, video_tokens, provider_tokens) and the INSERT into
--   request_logs_hot was updated to write them.
--
--   The original migration 350_multimodal_token_fields.sql only added
--   the columns to the request_logs parent table. The 2026-07 data-
--   lifecycle architecture however writes exclusively to request_logs_hot
--   (the independent heap table, see migration 341_hot_table_independence.sql),
--   so production INSERTs against request_logs_hot immediately failed with
--   SQLSTATE 42703 "column reasoning_tokens does not exist", the entire
--   transaction was rolled back (which also dropped usage_ledger_hot
--   rows), and rows silently fell back to data/backups/*.jsonl.gz.
--
--   This P0 fix:
--     1. Adds the 5 columns to request_logs_hot (the actual write target)
--     2. Adds the 5 columns to usage_ledger_hot so future INSERTs
--        can carry multimodal token counts through to billing.
--     3. Adds the 5 columns to request_logs (parent) — PostgreSQL 11+
--        automatically propagates ADD COLUMN to existing partitions of
--        a partitioned table, so the monthly partitions (request_logs_default,
--        request_logs_YYYY_MM) inherit them. This was verified in
--        production on PG 17.10.
--
-- This migration is idempotent (ADD COLUMN IF NOT EXISTS) and safe to
-- re-run.

BEGIN;

-- 1. request_logs_hot (the production write target — MUST come first
--    because INSERTs targeting this table are blocking in-flight traffic).
ALTER TABLE request_logs_hot
  ADD COLUMN IF NOT EXISTS reasoning_tokens INT DEFAULT NULL,
  ADD COLUMN IF NOT EXISTS image_tokens    INT DEFAULT NULL,
  ADD COLUMN IF NOT EXISTS audio_tokens    INT DEFAULT NULL,
  ADD COLUMN IF NOT EXISTS video_tokens    INT DEFAULT NULL,
  ADD COLUMN IF NOT EXISTS provider_tokens INT DEFAULT NULL;

COMMENT ON COLUMN request_logs_hot.reasoning_tokens IS 'audit-ir-multimodal (2026-07-13): reasoning/thinking tokens (DeepSeek R1, OpenAI o-series, Claude extended thinking)';
COMMENT ON COLUMN request_logs_hot.image_tokens    IS 'audit-ir-multimodal (2026-07-13): vision/image input tokens';
COMMENT ON COLUMN request_logs_hot.audio_tokens    IS 'audit-ir-multimodal (2026-07-13): audio input/output tokens';
COMMENT ON COLUMN request_logs_hot.video_tokens    IS 'audit-ir-multimodal (2026-07-13): video input tokens';
COMMENT ON COLUMN request_logs_hot.provider_tokens IS 'audit-ir-multimodal (2026-07-13): provider-specific tokens (e.g., Doubao seed_token_usage)';

-- 2. usage_ledger_hot — same 5 columns so billing aggregations can
--    reason about per-modality token usage from the write-time path.
ALTER TABLE usage_ledger_hot
  ADD COLUMN IF NOT EXISTS reasoning_tokens INT DEFAULT NULL,
  ADD COLUMN IF NOT EXISTS image_tokens    INT DEFAULT NULL,
  ADD COLUMN IF NOT EXISTS audio_tokens    INT DEFAULT NULL,
  ADD COLUMN IF NOT EXISTS video_tokens    INT DEFAULT NULL,
  ADD COLUMN IF NOT EXISTS provider_tokens INT DEFAULT NULL;

COMMENT ON COLUMN usage_ledger_hot.reasoning_tokens IS 'audit-ir-multimodal (2026-07-13): reasoning/thinking tokens';
COMMENT ON COLUMN usage_ledger_hot.image_tokens    IS 'audit-ir-multimodal (2026-07-13): vision/image input tokens';
COMMENT ON COLUMN usage_ledger_hot.audio_tokens    IS 'audit-ir-multimodal (2026-07-13): audio input/output tokens';
COMMENT ON COLUMN usage_ledger_hot.video_tokens    IS 'audit-ir-multimodal (2026-07-13): video input tokens';
COMMENT ON COLUMN usage_ledger_hot.provider_tokens IS 'audit-ir-multimodal (2026-07-13): provider-specific tokens';

-- 3. request_logs (parent) — PG 11+ automatically propagates ADD COLUMN
--    to all existing partitions of a partitioned table. The monthly
--    partitions (request_logs_default, request_logs_YYYY_MM) inherit
--    the columns from this single ALTER.
ALTER TABLE request_logs
  ADD COLUMN IF NOT EXISTS reasoning_tokens INT DEFAULT NULL,
  ADD COLUMN IF NOT EXISTS image_tokens    INT DEFAULT NULL,
  ADD COLUMN IF NOT EXISTS audio_tokens    INT DEFAULT NULL,
  ADD COLUMN IF NOT EXISTS video_tokens    INT DEFAULT NULL,
  ADD COLUMN IF NOT EXISTS provider_tokens INT DEFAULT NULL;

-- 4. Index for fast multimodal usage queries (matches migration 350's
--    intent but against the hot table, which is where the live traffic
--    actually lives).
CREATE INDEX IF NOT EXISTS idx_request_logs_hot_multimodal_usage
  ON request_logs_hot (tenant_id, ts DESC)
  WHERE image_tokens > 0 OR audio_tokens > 0 OR video_tokens > 0;

-- 5. Schema migration ledger.
INSERT INTO schema_migrations (version, description)
VALUES (
  '2026-07-13-multimodal-token-fields-hot',
  'P0 fix: add reasoning/image/audio/video/provider_tokens to request_logs_hot, usage_ledger_hot, request_logs (parent+partitions). Restores audit-ir-multimodal INSERT path.'
)
ON CONFLICT DO NOTHING;

COMMIT;

-- ── Post-migration validation ────────────────────────────────────────────
-- Should return 5 columns for each of the 3 tables after the migration.
DO $$
DECLARE
  expected_cols TEXT[] := ARRAY[
    'reasoning_tokens', 'image_tokens', 'audio_tokens',
    'video_tokens', 'provider_tokens'
  ];
  tbl TEXT;
  found_count INT;
  partition_count INT;
BEGIN
  FOR tbl IN SELECT unnest(ARRAY['request_logs_hot', 'usage_ledger_hot', 'request_logs']) LOOP
    SELECT COUNT(*) INTO found_count
      FROM information_schema.columns
     WHERE table_name = tbl
       AND column_name = ANY(expected_cols);
    IF found_count < 5 THEN
      RAISE EXCEPTION 'migration 2026-07-13-multimodal-hot failed: % has only % / 5 multimodal columns',
        tbl, found_count;
    END IF;
    RAISE NOTICE 'OK: % has all 5 multimodal columns', tbl;
  END LOOP;

  -- Verify partitions inherited columns
  SELECT COUNT(*) INTO partition_count
    FROM pg_inherits i
    JOIN pg_class c ON c.oid = i.inhrelid
    JOIN information_schema.columns ic ON ic.table_name = c.relname
   WHERE inhparent = 'request_logs'::regclass
     AND ic.column_name = 'reasoning_tokens';
  RAISE NOTICE 'OK: % partitions inherited multimodal columns', partition_count;
END $$;