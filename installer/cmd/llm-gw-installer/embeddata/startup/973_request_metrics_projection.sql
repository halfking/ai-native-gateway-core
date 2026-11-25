\set ON_ERROR_STOP on
BEGIN;
SET LOCAL lock_timeout = '5s';
-- The two indexes and the identity constraint can scan existing request_metrics partitions.
-- Keep lock acquisition bounded independently of that scan.
-- 实测 2026-11-25：211 同源隔离 PostgreSQL 17.10，request_metrics 68,140 行，973 完整事务用时 0.697 秒。
SET LOCAL statement_timeout = '10min';

ALTER TABLE public.request_metrics
    ADD COLUMN IF NOT EXISTS raw_model_name text,
    ADD COLUMN IF NOT EXISTS credential_id bigint,
    ADD COLUMN IF NOT EXISTS credential_label text,
    ADD COLUMN IF NOT EXISTS provider_code text,
    ADD COLUMN IF NOT EXISTS provider_name text,
    ADD COLUMN IF NOT EXISTS client_id text,
    ADD COLUMN IF NOT EXISTS agent_name text,
    ADD COLUMN IF NOT EXISTS client_ip inet,
    ADD COLUMN IF NOT EXISTS identity_hash text,
    ADD COLUMN IF NOT EXISTS client_model text,
    ADD COLUMN IF NOT EXISTS task_type text,
    ADD COLUMN IF NOT EXISTS work_type text,
    ADD COLUMN IF NOT EXISTS session_intent text,
    ADD COLUMN IF NOT EXISTS compression_applied boolean,
    ADD COLUMN IF NOT EXISTS owner_user text,
    ADD COLUMN IF NOT EXISTS api_key_owner_user text,
    ADD COLUMN IF NOT EXISTS client_cancelled boolean,
    ADD COLUMN IF NOT EXISTS api_key_prefix text,
    ADD COLUMN IF NOT EXISTS application_code text,
    ADD COLUMN IF NOT EXISTS person_key text,
    ADD COLUMN IF NOT EXISTS cache_write_tokens bigint,
    ADD COLUMN IF NOT EXISTS cache_input_tokens bigint,
    ADD COLUMN IF NOT EXISTS cost_amount numeric(20,8),
    ADD COLUMN IF NOT EXISTS cost_currency text,
    ADD COLUMN IF NOT EXISTS usage_source text,
    ADD COLUMN IF NOT EXISTS source_revision integer,
    ADD COLUMN IF NOT EXISTS source_finalized_at timestamptz,
    ADD COLUMN IF NOT EXISTS source_fact_id bigint,
    ADD COLUMN IF NOT EXISTS fact_request_id text;

-- Keep the original conflict target available throughout blue-green deployment.
-- Projected rows reserve a separate key space while retaining the exact fact ID
-- for reports and reconciliation.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conrelid = 'public.request_metrics'::regclass
          AND conname = 'request_metrics_fact_identity'
    ) THEN
        ALTER TABLE public.request_metrics
          ADD CONSTRAINT request_metrics_fact_identity CHECK (
            (source_fact_id IS NULL AND fact_request_id IS NULL AND left(request_id,1) <> chr(1))
            OR (source_fact_id IS NOT NULL AND fact_request_id IS NOT NULL
                AND request_id = chr(1) || jsonb_build_array(tenant_id,fact_request_id)::text)
          );
    END IF;
END
$$;

COMMENT ON COLUMN public.request_metrics.fact_request_id IS 'Original request ID for canonical projections; the primary key reserves an independent namespace for projections.';

CREATE INDEX IF NOT EXISTS idx_request_metrics_raw_model
    ON public.request_metrics (raw_model_name, occurred_date);

CREATE INDEX IF NOT EXISTS idx_request_metrics_projected_time
    ON public.request_metrics (occurred_at DESC) WHERE source_fact_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_request_metrics_fact_request
    ON public.request_metrics (tenant_id, fact_request_id, occurred_at DESC)
    WHERE source_fact_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS public.request_metrics_pending (
    fact_id bigint NOT NULL,
    occurred_at timestamptz NOT NULL,
    tenant_id text,
    enqueued_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (fact_id, occurred_at)
);

ALTER TABLE public.request_metrics_pending ADD COLUMN IF NOT EXISTS tenant_id text;

CREATE INDEX IF NOT EXISTS idx_request_metrics_pending_enqueued
    ON public.request_metrics_pending (enqueued_at);

CREATE TABLE IF NOT EXISTS public.request_metrics_projection_state (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    cursor_fact_id bigint NOT NULL DEFAULT 0,
    cursor_occurred_at timestamptz NOT NULL DEFAULT '-infinity',
    backfill_complete boolean NOT NULL DEFAULT false,
    updated_at timestamptz
);

INSERT INTO public.request_metrics_projection_state (singleton)
VALUES (true) ON CONFLICT DO NOTHING;

CREATE OR REPLACE FUNCTION public.enqueue_request_metrics_fact()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO public.request_metrics_pending (fact_id, occurred_at, tenant_id)
    VALUES (NEW.fact_id, NEW.occurred_at, NEW.tenant_id) ON CONFLICT DO NOTHING;
    RETURN NEW;
END
$$;

DROP TRIGGER IF EXISTS enqueue_request_metrics_fact ON public.usage_facts;
CREATE TRIGGER enqueue_request_metrics_fact
AFTER INSERT ON public.usage_facts
FOR EACH ROW EXECUTE FUNCTION public.enqueue_request_metrics_fact();

INSERT INTO public.schema_migrations (version, description)
VALUES ('973', 'request metrics projection and lossless usage fields')
ON CONFLICT (version) DO NOTHING;

COMMIT;
