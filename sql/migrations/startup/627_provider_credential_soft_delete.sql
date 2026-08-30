-- Migration 627: soft-delete for providers, terminal "deleted" status for credentials.
--
-- Two changes that together support the operator workflow requested
-- 2026-08-31 (删除功能): once a credential or provider is deleted it must
-- never reappear in any list, but the row stays in the table for audit
-- and FK integrity (model_offers, credential_model_bindings, request
-- history).
--
-- 1) credentials.status: extend the CHECK constraint with the new
--    terminal value 'deleted'.  This is the same shape as 'disabled'
--    for routing purposes (every "WHERE status = 'active'" filter
--    naturally excludes it) but the value is distinct so:
--      - the UI can show "已删除" rather than "已停用" on a re-add attempt;
--      - the audit log can record which path was taken (revoke vs delete);
--      - operators can grep for the transition later.
--
-- 2) providers.deleted_at: nullable timestamptz.  NULL = live row.
--    Existing queries that filter on p.enabled / p.manual_disabled stay
--    untouched; we add `p.deleted_at IS NULL` alongside them in the
--    admin handlers in the same commit.  The migration only adds the
--    column + an index so the new filter is index-backed and the
--    migration stays small / safe to run on a populated DB.
--
-- Compatibility:
--   - credentials_status_check recreation rewrites the constraint atomically.
--     Existing 'active'/'disabled'/etc. values remain valid.
--   - providers.deleted_at defaults to NULL, so the migration is a
--     metadata-only change for all existing rows.

BEGIN;

-- ── 1) credentials.status: add 'deleted' to the allowed enum ──────────────
ALTER TABLE public.credentials
    DROP CONSTRAINT IF EXISTS credentials_status_check;

ALTER TABLE public.credentials
    ADD CONSTRAINT credentials_status_check
    CHECK (status = ANY (ARRAY[
        'active'::text,
        'cooling'::text,
        'degraded'::text,
        'quarantine'::text,
        'quota_expired'::text,
        'disabled'::text,
        'deleted'::text
    ]));

-- ── 2) providers.deleted_at: soft-delete column + partial index ───────────
ALTER TABLE public.providers
    ADD COLUMN IF NOT EXISTS deleted_at timestamptz;

-- Partial index: the live-row query path (`... WHERE deleted_at IS NULL`)
-- is the hot one; indexing the rare dead rows is wasted space.
CREATE INDEX IF NOT EXISTS idx_providers_live
    ON public.providers (id)
    WHERE deleted_at IS NULL;

COMMIT;
