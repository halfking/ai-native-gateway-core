-- Normalized supplier invoice evidence. Invoice scope freezes the account
-- credential mapping and natural month boundaries at import time.
\set ON_ERROR_STOP on
BEGIN;
SET lock_timeout = '5s';
SET statement_timeout = '10min';

CREATE TABLE IF NOT EXISTS public.provider_invoices (
    invoice_id uuid PRIMARY KEY,
    provider_id bigint NOT NULL CHECK (provider_id > 0),
    account_ref text NOT NULL CHECK (account_ref <> ''),
    currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    invoice_ref text NOT NULL CHECK (invoice_ref <> ''),
    month_start date NOT NULL CHECK (extract(day FROM month_start) = 1),
    timezone text NOT NULL,
    window_start timestamptz NOT NULL,
    window_end timestamptz NOT NULL CHECK (window_end > window_start),
    credential_ids bigint[] NOT NULL CHECK (cardinality(credential_ids) > 0),
    stated_total numeric NOT NULL,
    content_sha256 text NOT NULL CHECK (content_sha256 ~ '^[a-f0-9]{64}$'),
    imported_by text NOT NULL,
    imported_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (provider_id, account_ref, currency, invoice_ref)
);

CREATE TABLE IF NOT EXISTS public.provider_invoice_lines (
    invoice_id uuid NOT NULL REFERENCES public.provider_invoices(invoice_id),
    line_ref text NOT NULL CHECK (line_ref <> ''),
    kind text NOT NULL CHECK (kind IN ('request_total','usage_bucket','fixed_fee','tax','discount','credit','refund','adjustment')),
    provider_request_id text CHECK (provider_request_id <> ''),
    amount numeric NOT NULL,
    model text,
    description text,
    PRIMARY KEY (invoice_id, line_ref),
    CHECK (kind <> 'request_total' OR (provider_request_id IS NOT NULL AND amount >= 0)),
    CHECK (kind = 'request_total' OR provider_request_id IS NULL),
    CHECK (kind NOT IN ('usage_bucket','fixed_fee','tax') OR amount >= 0),
    CHECK (kind NOT IN ('discount','credit','refund') OR amount <= 0)
);

CREATE INDEX IF NOT EXISTS idx_provider_invoices_provider_month
    ON public.provider_invoices (provider_id, month_start, invoice_id);
CREATE INDEX IF NOT EXISTS idx_provider_invoice_lines_request
    ON public.provider_invoice_lines (provider_request_id, invoice_id) WHERE kind = 'request_total';
CREATE INDEX IF NOT EXISTS idx_provider_call_attempts_provider_request
    ON public.provider_call_attempts (provider_id, provider_request_id, sent_at)
    WHERE provider_request_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_provider_call_attempts_account_time
    ON public.provider_call_attempts (provider_id, credential_id, COALESCE(sent_at, prepared_at));

ALTER TABLE public.provider_invoices ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.provider_invoice_lines ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS provider_invoices_gateway ON public.provider_invoices;
CREATE POLICY provider_invoices_gateway ON public.provider_invoices
    USING (current_setting('app.bypass_rls', true) = 'true')
    WITH CHECK (current_setting('app.bypass_rls', true) = 'true');
DROP POLICY IF EXISTS provider_invoice_lines_gateway ON public.provider_invoice_lines;
CREATE POLICY provider_invoice_lines_gateway ON public.provider_invoice_lines
    USING (current_setting('app.bypass_rls', true) = 'true')
    WITH CHECK (current_setting('app.bypass_rls', true) = 'true');

INSERT INTO public.schema_migrations (version, description)
VALUES ('986', 'normalized supplier invoice evidence')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
