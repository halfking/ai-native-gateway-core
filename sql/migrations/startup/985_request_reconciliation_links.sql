-- Correlate internal orchestration requests with the original client request.
\set ON_ERROR_STOP on
BEGIN;
SET lock_timeout = '5s';
SET statement_timeout = '10min';

CREATE TABLE IF NOT EXISTS public.request_reconciliation_links (
    token_digest bytea PRIMARY KEY CHECK (octet_length(token_digest) = 32),
    tenant_id text NOT NULL CHECK (tenant_id <> ''),
    api_key_id bigint NOT NULL CHECK (api_key_id > 0),
    parent_request_id text NOT NULL CHECK (parent_request_id <> ''),
    body_sha256 text NOT NULL CHECK (body_sha256 ~ '^[a-f0-9]{64}$'),
    registered_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    child_request_id text CHECK (child_request_id <> '' AND child_request_id <> parent_request_id),
    claimed_at timestamptz,
    CHECK ((child_request_id IS NULL) = (claimed_at IS NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_reconciliation_links_child
    ON public.request_reconciliation_links (tenant_id, child_request_id) WHERE child_request_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_reconciliation_links_parent
    ON public.request_reconciliation_links (tenant_id, parent_request_id);
CREATE INDEX IF NOT EXISTS idx_reconciliation_links_registered
    ON public.request_reconciliation_links (registered_at);

ALTER TABLE public.request_reconciliation_links ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS request_reconciliation_links_write ON public.request_reconciliation_links;
CREATE POLICY request_reconciliation_links_write ON public.request_reconciliation_links
    USING (current_setting('app.bypass_rls', true) = 'true')
    WITH CHECK (current_setting('app.bypass_rls', true) = 'true');
DROP POLICY IF EXISTS request_reconciliation_links_read ON public.request_reconciliation_links;
CREATE POLICY request_reconciliation_links_read ON public.request_reconciliation_links FOR SELECT
    USING (tenant_id = NULLIF(current_setting('app.current_tenant', true), '') AND child_request_id IS NOT NULL);

INSERT INTO public.schema_migrations (version, description)
VALUES ('985', 'client and internal request reconciliation links')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;
COMMIT;
