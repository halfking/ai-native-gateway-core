-- Migration 470: session_summaries archival — decay old inactive summaries (M5)
--
-- docs/omni-ref3 M5: add archived_at + last_accessed_at columns to enable
-- archival of old inactive summaries. Summaries not accessed for N days
-- (and session ended) can be marked archived, reducing active table size
-- while preserving data for compliance/audit.
--
-- Columns:
--   last_accessed_at  — updated when summary is read (session load, analytics, etc.)
--   archived_at       — set when summary is archived (nullable)
--
-- Archival policy (external job):
--   - session ended (last_request_at > 30 days ago)
--   - not accessed recently (last_accessed_at > 30 days ago OR NULL)
--   - archived_at IS NULL
--   → SET archived_at = NOW()
--
-- Queries can filter WHERE archived_at IS NULL to exclude archived records.
--
-- Idempotent: IF NOT EXISTS guards prevent duplicate column errors on re-run.

BEGIN;

DO $$
BEGIN
  -- Add last_accessed_at column
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public'
      AND table_name = 'session_summaries'
      AND column_name = 'last_accessed_at'
  ) THEN
    ALTER TABLE public.session_summaries ADD COLUMN last_accessed_at TIMESTAMP WITH TIME ZONE;
    COMMENT ON COLUMN public.session_summaries.last_accessed_at IS 'M5: last time this summary was read (session load, analytics, etc.)';
    
    -- Backfill: set to last_request_at for existing rows
    UPDATE public.session_summaries SET last_accessed_at = last_request_at WHERE last_accessed_at IS NULL;
  END IF;

  -- Add archived_at column
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public'
      AND table_name = 'session_summaries'
      AND column_name = 'archived_at'
  ) THEN
    ALTER TABLE public.session_summaries ADD COLUMN archived_at TIMESTAMP WITH TIME ZONE;
    COMMENT ON COLUMN public.session_summaries.archived_at IS 'M5: archival timestamp for old inactive summaries';
  END IF;

  -- Create index for archival queries
  IF NOT EXISTS (
    SELECT 1 FROM pg_indexes
    WHERE schemaname = 'public'
      AND tablename = 'session_summaries'
      AND indexname = 'idx_session_summaries_archival'
  ) THEN
    CREATE INDEX idx_session_summaries_archival
      ON public.session_summaries (archived_at, last_accessed_at, last_request_at)
      WHERE archived_at IS NULL;
    COMMENT ON INDEX public.idx_session_summaries_archival IS 'M5: support archival job finding old inactive summaries';
  END IF;
END $$;

COMMIT;
