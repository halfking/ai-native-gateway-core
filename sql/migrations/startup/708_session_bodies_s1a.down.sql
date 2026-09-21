-- Rollback Migration 708: session_bodies kind pivot prep + sessions dead columns.
--
-- 1. Drops the partial unique final_full indexes and the kind column from BOTH
--    parent and hot (restoring the 638 promote contract). Any final_full rows
--    written between up and down are destroyed; turn_delta backfill is lost
--    (moot: the column disappears with it).
-- 2. RESTORES the three sessions.last_full_* columns. The DROP is an
--    irreversible point (storage plan §7): data dropped by the up-migration
--    CANNOT be recovered — the columns come back empty. Migration 456 set them
--    dead (zero writers) and the 2026-09-14 local audit confirmed 100% NULL,
--    so no data loss was incurred by the up-migration.
-- 3. The promote function rewrite is intentionally kept (same rationale as the
--    707 down note: the SELECT * body is shape-agnostic and correct for the
--    narrow pre-708 shape).
--
-- The schema_migrations row is kept (append-only ledger convention).

DROP INDEX IF EXISTS public.uq_session_bodies_hot_final_full;
DROP INDEX IF EXISTS public.uq_session_bodies_final_full;

ALTER TABLE public.session_bodies_hot
    DROP COLUMN IF EXISTS kind;
ALTER TABLE public.session_bodies
    DROP COLUMN IF EXISTS kind;

ALTER TABLE public.sessions
    ADD COLUMN IF NOT EXISTS last_full_request JSONB,
    ADD COLUMN IF NOT EXISTS last_full_response JSONB,
    ADD COLUMN IF NOT EXISTS last_full_payload_at TIMESTAMPTZ;
