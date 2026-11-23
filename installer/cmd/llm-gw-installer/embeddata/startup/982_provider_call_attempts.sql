-- Per-attempt supplier evidence. The gateway request and the credit ledger keep
-- their own keys and clocks. No historical attempts are inferred here.
\set ON_ERROR_STOP on
BEGIN;
SET lock_timeout = '5s';
SET statement_timeout = '10min';

CREATE TABLE IF NOT EXISTS public.provider_call_attempts (
    attempt_id text PRIMARY KEY,
    tenant_id text,
    request_id text,
    job_id text,
    traffic_class text NOT NULL CHECK (traffic_class IN ('business', 'platform', 'unknown')),
    client_id text,
    source text NOT NULL,
    attempt_no integer NOT NULL CHECK (attempt_no > 0),
    provider_id bigint,
    credential_id bigint,
    model text,
    prepared_at timestamptz NOT NULL,
    sent_at timestamptz,
    delivery text NOT NULL DEFAULT 'prepared' CHECK (delivery IN ('prepared', 'sent', 'not_sent', 'unknown')),
    finished_at timestamptz,
    outcome text CHECK (outcome IN ('success', 'failure', 'cancelled', 'unknown')),
    http_status integer CHECK (http_status BETWEEN 100 AND 599),
    provider_request_id text,
    input_tokens bigint CHECK (input_tokens >= 0),
    output_tokens bigint CHECK (output_tokens >= 0),
    cache_read_tokens bigint CHECK (cache_read_tokens >= 0),
    cache_write_tokens bigint CHECK (cache_write_tokens >= 0),
    usage_source text,
    estimated_amount numeric,
    estimated_currency text,
    rate_snapshot jsonb,
    reported_amount numeric,
    reported_currency text,
    reported_semantics text,
    CHECK (request_id IS NULL OR tenant_id IS NOT NULL),
    CHECK (traffic_class <> 'business' OR (request_id IS NOT NULL AND tenant_id IS NOT NULL)),
    CHECK (traffic_class <> 'platform' OR client_id IS NOT DISTINCT FROM 'platform-automation'),
    CHECK ((delivery = 'sent') = (sent_at IS NOT NULL)),
    CHECK (estimated_amount IS NULL OR (estimated_currency IS NOT NULL AND rate_snapshot IS NOT NULL)),
    CHECK (reported_amount IS NULL OR reported_semantics IS NOT NULL)
);

CREATE INDEX IF NOT EXISTS idx_provider_call_attempts_sent
    ON public.provider_call_attempts (sent_at) WHERE sent_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_provider_call_attempts_unconfirmed
    ON public.provider_call_attempts (prepared_at) WHERE sent_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_provider_call_attempts_request
    ON public.provider_call_attempts (tenant_id, request_id) WHERE request_id IS NOT NULL;

ALTER TABLE public.provider_call_attempts ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS provider_call_attempts_gateway ON public.provider_call_attempts;
CREATE POLICY provider_call_attempts_gateway ON public.provider_call_attempts
    USING (current_setting('app.bypass_rls', true) = 'true')
    WITH CHECK (current_setting('app.bypass_rls', true) = 'true');

INSERT INTO public.schema_migrations (version, description)
VALUES ('982', 'per-attempt supplier call evidence')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
