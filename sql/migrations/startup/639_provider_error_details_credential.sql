-- Migration 639: attribute provider error aggregates to credentials.
--
-- 2026-09-01 24h-audit round2 (P0-1): the aggregator's CTE read
-- candidate_failure_logs_unified but never selected credential_id, and the
-- target table (migration 435) has no such column. Every aggregate row was
-- therefore provider-scoped only: the admin credential-detail view could not
-- show "the error set belonging to THIS credential", and multiple credentials
-- failing with the same fingerprint in one 10-minute bucket were silently
-- merged into one row.
--
-- This migration:
--   1. adds provider_error_details.credential_id (TEXT NULL — the source
--      column candidate_failure_logs_hot.credential_id is int, but legacy
--      rows and non-credential rejections may be NULL; TEXT keeps the
--      sentinel handling identical to tenant_id/model_name endpoints),
--   2. rebuilds the 620 tenant-fingerprint unique index WITH credential_id
--      so the upsert conflict target groups per credential. Old rows keep
--      NULL credential_id and remain one bucket per (tenant, fingerprint):
--      COALESCE(credential_id, '') makes them conflict among themselves
--      exactly as before, so the upsert never creates duplicates.
--
-- Idempotent. Fails closed if the new identity would collide (same 620
-- convention) so operators resolve production data explicitly.
-- Down: restore the 620 index shape and drop the column.
BEGIN;

ALTER TABLE public.provider_error_details
    ADD COLUMN IF NOT EXISTS credential_id TEXT;

COMMENT ON COLUMN public.provider_error_details.credential_id IS
    '凭据归因：聚合自 candidate_failure_logs_hot.credential_id；NULL 表示历史行（639 之前）或无凭据的拒绝';

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM public.provider_error_details
        GROUP BY COALESCE(tenant_id, ''), provider_id, COALESCE(credential_id, ''),
                 COALESCE(model_name, ''), COALESCE(endpoint, ''), error_type,
                 COALESCE(error_code, ''), COALESCE(LEFT(error_message, 200), ''),
                 COALESCE(aggregation_bucket, TIMESTAMPTZ 'epoch')
        HAVING count(*) > 1
    ) THEN
        RAISE EXCEPTION 'migration 639 blocked: duplicate provider_error_details identities under the credential-scoped fingerprint require explicit operator resolution';
    END IF;
END
$$;

DROP INDEX IF EXISTS public.idx_provider_error_details_tenant_fingerprint;
CREATE UNIQUE INDEX idx_provider_error_details_tenant_cred_fingerprint
ON public.provider_error_details (
    COALESCE(tenant_id, ''), provider_id, COALESCE(credential_id, ''),
    COALESCE(model_name, ''), COALESCE(endpoint, ''), error_type,
    COALESCE(error_code, ''), COALESCE(LEFT(error_message, 200), ''),
    COALESCE(aggregation_bucket, TIMESTAMPTZ 'epoch')
);

CREATE INDEX IF NOT EXISTS idx_ped_tenant_credential
    ON public.provider_error_details (tenant_id, credential_id, last_seen_at DESC)
    WHERE credential_id IS NOT NULL;

COMMIT;
