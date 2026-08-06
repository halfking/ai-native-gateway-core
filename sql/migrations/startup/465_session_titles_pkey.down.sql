-- Down migration for 465_session_titles_pkey.
-- Reverses the backfilled PK. Note: this will only succeed if no
-- duplicates have been inserted since the up migration ran.
BEGIN;

ALTER TABLE public.session_titles
    DROP CONSTRAINT IF EXISTS session_titles_pkey;

COMMIT;