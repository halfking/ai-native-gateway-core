-- 389_maas_credit_consumption_buckets.sql
-- Hourly credit consumption counter for O(1) dashboard KPI reads.
-- See deploy/sql/docs/pricing/2026_07_13_maas_credit_consumption_buckets.sql

BEGIN;

CREATE TABLE IF NOT EXISTS public.maas_credit_consumption_buckets (
    tenant_id     text             NOT NULL,
    bucket_start  timestamptz      NOT NULL,
    credits       bigint           NOT NULL DEFAULT 0,
    request_count integer          NOT NULL DEFAULT 0,
    updated_at    timestamptz      NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, bucket_start)
);

COMMENT ON TABLE public.maas_credit_consumption_buckets IS
  'Hourly credit consumption counter. Incremented atomically by ChargeRequest; read by admin /api/usage/summary.';

CREATE INDEX IF NOT EXISTS idx_mccb_recent
  ON public.maas_credit_consumption_buckets (bucket_start DESC);

COMMIT;
