-- Credential- and model-scoped supplier cost terms. Routing and retail pricing are independent.
\set ON_ERROR_STOP on
BEGIN;
SET lock_timeout = '5s';
SET statement_timeout = '10min';

CREATE TABLE IF NOT EXISTS public.supplier_contract_rates (
    rate_id uuid PRIMARY KEY,
    provider_id bigint NOT NULL CHECK (provider_id > 0),
    credential_id bigint NOT NULL CHECK (credential_id > 0),
    model text NOT NULL CHECK (btrim(model) <> ''),
    reference text NOT NULL CHECK (btrim(reference) <> ''),
    currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    effective_from timestamptz NOT NULL,
    effective_to timestamptz CHECK (effective_to IS NULL OR effective_to > effective_from),
    terms jsonb NOT NULL CHECK (jsonb_typeof(terms) = 'object'),
    content_sha256 text NOT NULL CHECK (content_sha256 ~ '^[a-f0-9]{64}$'),
    imported_by text NOT NULL,
    imported_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (provider_id, credential_id, model, effective_from)
);
ALTER TABLE public.supplier_contract_rates ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS supplier_contract_rates_platform ON public.supplier_contract_rates;
CREATE POLICY supplier_contract_rates_platform ON public.supplier_contract_rates
    USING (current_setting('app.bypass_rls', true) = 'true')
    WITH CHECK (current_setting('app.bypass_rls', true) = 'true');

INSERT INTO public.schema_migrations (version, description)
VALUES ('988', 'effective supplier contract rate evidence')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;
COMMIT;
