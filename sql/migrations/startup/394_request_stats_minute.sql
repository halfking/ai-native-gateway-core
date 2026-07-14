-- 394_request_stats_minute.sql
-- Minute-level dashboard stats rollup tables (decoupled from request_logs retention).

BEGIN;

CREATE TABLE IF NOT EXISTS public.request_stats_minute (
    bucket              timestamptz NOT NULL,
    tenant_id           text NOT NULL DEFAULT 'default',
    provider_id         bigint NOT NULL DEFAULT 0,
    canonical_id        bigint NOT NULL DEFAULT 0,
    requests            bigint NOT NULL DEFAULT 0,
    success_count       bigint NOT NULL DEFAULT 0,
    failure_count       bigint NOT NULL DEFAULT 0,
    prompt_tokens       bigint NOT NULL DEFAULT 0,
    completion_tokens   bigint NOT NULL DEFAULT 0,
    total_tokens        bigint NOT NULL DEFAULT 0,
    credits_charged     bigint NOT NULL DEFAULT 0,
    cost_usd            numeric(18,8) NOT NULL DEFAULT 0,
    latency_ms_sum      bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (bucket, tenant_id, provider_id, canonical_id)
);

COMMENT ON TABLE public.request_stats_minute IS
  'Per-minute usage aggregates for dashboard KPIs and trend charts. provider_id=0 and canonical_id=0 denote tenant-wide totals.';

CREATE INDEX IF NOT EXISTS idx_rsm_bucket
  ON public.request_stats_minute (bucket DESC);

CREATE INDEX IF NOT EXISTS idx_rsm_tenant_bucket
  ON public.request_stats_minute (tenant_id, bucket DESC);

CREATE TABLE IF NOT EXISTS public.request_stats_dim_minute (
    bucket          timestamptz NOT NULL,
    tenant_id       text NOT NULL DEFAULT 'default',
    dim_type        text NOT NULL,
    dim_key         text NOT NULL,
    requests        bigint NOT NULL DEFAULT 0,
    success_count   bigint NOT NULL DEFAULT 0,
    failure_count   bigint NOT NULL DEFAULT 0,
    total_tokens    bigint NOT NULL DEFAULT 0,
    credits_charged bigint NOT NULL DEFAULT 0,
    cost_usd        numeric(18,8) NOT NULL DEFAULT 0,
    PRIMARY KEY (bucket, tenant_id, dim_type, dim_key)
);

COMMENT ON TABLE public.request_stats_dim_minute IS
  'Per-minute dimension breakdowns: client_profile, virtual_ip, identity_hash, model, error_kind, tenant, provider.';

CREATE INDEX IF NOT EXISTS idx_rsdm_bucket
  ON public.request_stats_dim_minute (bucket DESC);

CREATE INDEX IF NOT EXISTS idx_rsdm_type_bucket
  ON public.request_stats_dim_minute (dim_type, bucket DESC);

CREATE TABLE IF NOT EXISTS public.request_stats_error_drill_minute (
    bucket          timestamptz NOT NULL,
    tenant_id       text NOT NULL DEFAULT 'default',
    error_kind      text NOT NULL,
    model_name      text NOT NULL DEFAULT '',
    provider_id     bigint NOT NULL DEFAULT 0,
    client_profile  text NOT NULL DEFAULT '',
    requests        bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (bucket, tenant_id, error_kind, model_name, provider_id, client_profile)
);

COMMENT ON TABLE public.request_stats_error_drill_minute IS
  'Error drill-down aggregates for dashboard pie chart second level.';

CREATE INDEX IF NOT EXISTS idx_rsedm_error_bucket
  ON public.request_stats_error_drill_minute (error_kind, bucket DESC);

CREATE TABLE IF NOT EXISTS public.request_stats_rollup_cursor (
    id              smallint PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    last_ts         timestamptz,
    last_request_id text,
    updated_at      timestamptz NOT NULL DEFAULT now()
);

INSERT INTO public.request_stats_rollup_cursor (id)
VALUES (1)
ON CONFLICT (id) DO NOTHING;

COMMIT;
