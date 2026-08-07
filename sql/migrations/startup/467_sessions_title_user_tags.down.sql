-- Migration 467 down: remove title and user_tags columns

BEGIN;

ALTER TABLE public.sessions DROP COLUMN IF EXISTS title;
ALTER TABLE public.sessions DROP COLUMN IF EXISTS user_tags;

COMMIT;
