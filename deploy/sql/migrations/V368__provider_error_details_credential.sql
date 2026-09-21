-- V368: credential-attributed provider error aggregates (mirror of startup
-- migration 639). The aggregation grain gains credential_id so per-credential
-- error sets stay separable and same-fingerprint failures from different
-- credentials in one bucket are no longer silently merged.
BEGIN;

ALTER TABLE public.provider_error_details
    ADD COLUMN IF NOT EXISTS credential_id TEXT;

COMMENT ON COLUMN public.provider_error_details.credential_id IS
    '凭据归因：聚合自 candidate_failure_logs_hot.credential_id；NULL 表示历史行（V368 之前）或无凭据的拒绝';

-- Fail closed when the credential-scoped identity would collide (same
-- convention as V364). Resolve production rows explicitly and rerun.
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
        RAISE EXCEPTION 'V368 blocked: duplicate provider_error_details identities under the credential-scoped fingerprint require explicit operator resolution';
    END IF;
END
$$;

DROP INDEX IF EXISTS public.idx_provider_error_details_tenant_fingerprint;
CREATE UNIQUE INDEX IF NOT EXISTS idx_provider_error_details_tenant_cred_fingerprint
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
