-- Migration 665: repair provider_error_details unique fingerprint index
--
-- Background (2026-09-06 PG log audit):
--   The aggregation upsert (bg/provider_error_aggregator.go, introduced by
--   7f1604fb9) uses an ON CONFLICT target of 9 key parts INCLUDING
--   COALESCE(LEFT(error_message, 200), ''). The canonical index lives in
--   sql/migrations/startup/639_provider_error_details_credential.sql and
--   deploy/sql/migrations/V368__provider_error_details_credential.sql with
--   the same 9 parts. On this database the index was rebuilt by an
--   out-of-repo 662 repair (parallel session) WITHOUT the error_message
--   component, so every aggregator tick failed with
--   SQLSTATE 42P10 "there is no unique or exclusion constraint matching the
--   ON CONFLICT specification" and the aggregation batch was lost.
--
-- Contract: the 9-part fingerprint (with LEFT(error_message, 200)) is the
-- authoritative shape per 639/V368 + the Go upsert target.
--
-- Idempotent: if the named index already contains the error_message
-- fingerprint part, nothing is dropped or recreated.

\set ON_ERROR_STOP on
BEGIN;

DO $$
DECLARE
  idx_def text;
BEGIN
  SELECT pg_get_indexdef(c.oid)
    INTO idx_def
    FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace
   WHERE c.relname = 'idx_provider_error_details_tenant_cred_fingerprint'
     AND n.nspname = 'public'
     AND c.relkind = 'i'
   LIMIT 1;

  IF idx_def IS NULL OR position('error_message' in idx_def) = 0 THEN
    DROP INDEX IF EXISTS public.idx_provider_error_details_tenant_cred_fingerprint;
    CREATE UNIQUE INDEX idx_provider_error_details_tenant_cred_fingerprint
      ON public.provider_error_details (
        COALESCE(tenant_id, ''), provider_id, COALESCE(credential_id, ''),
        COALESCE(model_name, ''), COALESCE(endpoint, ''), error_type,
        COALESCE(error_code, ''), COALESCE(LEFT(error_message, 200), ''),
        COALESCE(aggregation_bucket, TIMESTAMPTZ 'epoch')
      );
  END IF;
END
$$;

INSERT INTO public.schema_migrations (version, description)
VALUES ('663', 'provider_error_details fingerprint index repair (restore LEFT(error_message,200) part)')
ON CONFLICT (version) DO NOTHING;

COMMIT;
