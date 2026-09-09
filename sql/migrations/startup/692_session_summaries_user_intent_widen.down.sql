-- Rollback migration 692: shrink session_summaries.user_intent back to varchar(50).
--
-- WARNING: this rollback will FAIL if any existing row has user_intent longer
-- than 50 chars (which is now the common case after migration 692). To roll
-- back safely, first run:
--   UPDATE public.session_summaries SET user_intent = LEFT(user_intent, 50)
--   WHERE LENGTH(user_intent) > 50;
-- then apply this down migration.

BEGIN;

ALTER TABLE public.session_summaries
    ALTER COLUMN user_intent TYPE varchar(50);

DELETE FROM public.schema_migrations WHERE version = '692';

COMMIT;
