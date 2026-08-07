-- Migration 467: sessions_title_user_tags — add title and user_tags to gateway.sessions
--
-- docs/omni-ref3 M2/M3: unify metadata fact source. Move title from Redis/session_titles
-- and user tags from Redis to V2 gateway.sessions table, making it the single source
-- of truth for session metadata.
--
-- Columns:
--   title       — session title (auto-generated or user-provided)
--   user_tags   — user-supplied tags (client X-Gw-Tags header), stored as text array
--
-- session_tags table (auto-generated structured tags with tag_source='auto') remains
-- separate and is merged on read via GetSessionMetadata (M3).
--
-- Idempotent: IF NOT EXISTS guards prevent duplicate column errors on re-run.

BEGIN;

DO $$
BEGIN
  -- Add title column
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public'
      AND table_name = 'sessions'
      AND column_name = 'title'
  ) THEN
    ALTER TABLE public.sessions ADD COLUMN title text;
    COMMENT ON COLUMN public.sessions.title IS 'Session title (auto-generated or user-provided). M2: unified fact source.';
  END IF;

  -- Add user_tags column
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public'
      AND table_name = 'sessions'
      AND column_name = 'user_tags'
  ) THEN
    ALTER TABLE public.sessions ADD COLUMN user_tags text[];
    COMMENT ON COLUMN public.sessions.user_tags IS 'User-supplied tags (X-Gw-Tags header). M3: distinct from session_tags (auto).';
  END IF;
END $$;

COMMIT;
