-- Migration 645 rollback: drop the repaired session_bodies_hot unique
-- constraint and remove the migration tracking row (same convention as the
-- 642 down).
--
-- WARNING: rolling this back re-introduces the production failure this
-- migration fixes — every domains/session/v2/bodies_writer.go upsert fails
-- with "no unique or exclusion constraint matching the ON CONFLICT
-- specification" again. On fresh installs the constraint of the same name
-- originally comes from migration 614, so this down returns every
-- environment to the pre-645 state, broken write path included. Apply only
-- as a deliberate debugging measure.
BEGIN;

DO $$
BEGIN
    IF to_regclass('public.session_bodies_hot') IS NOT NULL THEN
        ALTER TABLE public.session_bodies_hot
            DROP CONSTRAINT IF EXISTS session_bodies_with_current_month;
        RAISE NOTICE 'migration 645 rolled back: session_bodies_with_current_month dropped';
    ELSE
        RAISE NOTICE 'migration 645 rolled back: public.session_bodies_hot does not exist; nothing to drop';
    END IF;
END
$$;

DELETE FROM public.schema_migrations WHERE version = '645';

COMMIT;
