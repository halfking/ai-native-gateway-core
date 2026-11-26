\set ON_ERROR_STOP on
BEGIN;
SET LOCAL lock_timeout = '5s';
DROP TRIGGER IF EXISTS enqueue_request_metrics_fact ON public.usage_facts;
DROP FUNCTION IF EXISTS public.enqueue_request_metrics_fact();
DROP TABLE IF EXISTS public.request_metrics_pending;
DROP TABLE IF EXISTS public.request_metrics_projection_state;
DROP INDEX IF EXISTS public.idx_request_metrics_projected_time;
DROP INDEX IF EXISTS public.idx_request_metrics_fact_request;
-- Projection rows are rebuildable from durable usage facts. Preserve rows
-- written under the original storage key when removing the projection.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema='public' AND table_name='request_metrics' AND column_name='fact_request_id'
    ) THEN
        DELETE FROM public.request_metrics WHERE fact_request_id IS NOT NULL;
    END IF;
END
$$;
ALTER TABLE public.request_metrics
    DROP CONSTRAINT IF EXISTS request_metrics_fact_identity,
    DROP COLUMN IF EXISTS fact_request_id,
    DROP COLUMN IF EXISTS credential_id,
    DROP COLUMN IF EXISTS credential_label,
    DROP COLUMN IF EXISTS provider_code,
    DROP COLUMN IF EXISTS provider_name,
    DROP COLUMN IF EXISTS client_id,
    DROP COLUMN IF EXISTS agent_name,
    DROP COLUMN IF EXISTS client_ip,
    DROP COLUMN IF EXISTS identity_hash,
    DROP COLUMN IF EXISTS client_model,
    DROP COLUMN IF EXISTS task_type,
    DROP COLUMN IF EXISTS work_type,
    DROP COLUMN IF EXISTS session_intent,
    DROP COLUMN IF EXISTS compression_applied,
    DROP COLUMN IF EXISTS owner_user,
    DROP COLUMN IF EXISTS api_key_owner_user,
    DROP COLUMN IF EXISTS client_cancelled,
    DROP COLUMN IF EXISTS api_key_prefix,
    DROP COLUMN IF EXISTS application_code,
    DROP COLUMN IF EXISTS person_key,
    DROP COLUMN IF EXISTS cache_write_tokens,
    DROP COLUMN IF EXISTS cache_input_tokens,
    DROP COLUMN IF EXISTS cost_amount,
    DROP COLUMN IF EXISTS cost_currency,
    DROP COLUMN IF EXISTS usage_source,
    DROP COLUMN IF EXISTS source_revision,
    DROP COLUMN IF EXISTS source_finalized_at,
    DROP COLUMN IF EXISTS source_fact_id;

DELETE FROM public.schema_migrations WHERE version = '973';
COMMIT;
