-- =============================================================================
-- 800_provider_endpoint_protocols.sql
-- (moved 2026-09-24, r0924 fix-a task 7, from
-- deploy/sql/migrations/V800__provider_endpoint_protocols.sql into the
-- sql/migrations/startup/ delivery channel; DDL body unchanged.)
-- 2026-09-24 (r0924 supplier-protocol-optimization §3.2): bootstrap the
-- per-provider endpoint table + backfill from the legacy
-- (providers.base_url, providers.protocol) pair.
--
-- Delivery-channel notes (r0924 fix-a task 7):
--
--   1. IDEMPOTENCY: this file is safe to re-run. The DDL uses
--      CREATE TABLE IF NOT EXISTS / CREATE [UNIQUE] INDEX IF NOT EXISTS,
--      and the backfill INSERT ... SELECT ends in
--      ON CONFLICT (provider_id, protocol) DO NOTHING. No statement here
--      needs a down migration (nothing destructive).
--
--   2. "NO PRIMARY ROW" EDGE: the ON CONFLICT DO NOTHING backfill skips a
--      provider whose (provider_id, protocol) row ALREADY exists as a
--      non-primary row (e.g. an operator-created endpoint inserted before
--      this migration ran). Because the partial unique index allows only
--      one is_primary=true row and the backfilled row was skipped, such a
--      provider ends up with NO primary endpoint at all. Any consumer of
--      this table MUST validate "every provider has ≥1 primary row"
--      (and repair it) BEFORE relying on Stage 4 fallback wiring.
--
--   3. ZERO GO CONSUMERS TODAY: as of r0924 fix-a, no Go code reads or
--      writes public.provider_endpoint_protocols (grep-verified). The P4
--      wiring work MUST add a boot-time ensure (create + backfill + the
--      no-primary check above) before switching the dispatcher onto this
--      table.
--
-- Backfill contract:
--   - One row per existing provider, mirroring (base_url, protocol).
--   - is_primary=true for every backfilled row (the legacy row IS the
--     primary by definition; new endpoints the operator adds later
--     default to is_primary=false).
--   - vendor_native is inferred from the catalog family mapping where
--     possible; self-hosted / unknown families get NULL and operators
--     fill them in via the admin UI in P1 follow-up.
--   - Backfill is ON CONFLICT DO NOTHING so this migration is idempotent
--     (re-running it after a partial backfill is safe).
--   - Backfill excludes soft-deleted providers (deleted_at IS NULL).
-- =============================================================================

-- 1. DDL: create the table if it doesn't already exist (the new SQL
-- objects/tables file is the SSOT; this DDL is duplicated here for the
-- runtime migration path because boot migrations don't load the
-- objects/ directory).
CREATE TABLE IF NOT EXISTS public.provider_endpoint_protocols (
    id                    bigserial    PRIMARY KEY,
    provider_id           bigint       NOT NULL,
    tenant_id             text         NOT NULL DEFAULT 'default',
    protocol              text         NOT NULL,
    base_url              text         NOT NULL,
    is_primary            boolean      NOT NULL DEFAULT false,
    vendor_native         text,
    enabled               boolean      NOT NULL DEFAULT true,
    weight                smallint     NOT NULL DEFAULT 100,
    notes                 text,
    health_status         text         NOT NULL DEFAULT 'unknown',
    health_checked_at     timestamptz,
    health_latency_ms     integer,
    health_error          text,
    consecutive_failures  integer      NOT NULL DEFAULT 0,
    last_failure_kind     text,
    created_at            timestamptz  NOT NULL DEFAULT now(),
    updated_at            timestamptz  NOT NULL DEFAULT now(),
    CONSTRAINT provider_endpoint_protocols_unique
        UNIQUE (provider_id, protocol),
    CONSTRAINT provider_endpoint_protocols_provider_fk
        FOREIGN KEY (provider_id) REFERENCES public.providers(id) ON DELETE CASCADE,
    CONSTRAINT provider_endpoint_protocols_protocol_check
        CHECK (protocol = ANY (ARRAY[
            'openai-completions',
            'openai-responses',
            'anthropic-messages',
            'gemini-generate',
            'ollama-native'
        ])),
    CONSTRAINT provider_endpoint_protocols_base_url_nonempty
        CHECK (length(base_url) > 0),
    CONSTRAINT provider_endpoint_protocols_weight_positive
        CHECK (weight BETWEEN 1 AND 999)
);

CREATE UNIQUE INDEX IF NOT EXISTS provider_endpoint_protocols_one_primary
    ON public.provider_endpoint_protocols (provider_id)
    WHERE is_primary = true;

CREATE INDEX IF NOT EXISTS provider_endpoint_protocols_provider_idx
    ON public.provider_endpoint_protocols (provider_id, protocol);

CREATE INDEX IF NOT EXISTS provider_endpoint_protocols_family_idx
    ON public.provider_endpoint_protocols (provider_id, vendor_native)
    WHERE vendor_native IS NOT NULL AND enabled = true;

-- 2. Backfill from the legacy providers row. Wrapped in a single
-- INSERT ... SELECT so the migration runs in one statement and the
-- ON CONFLICT clause protects idempotency. We intentionally DO NOT
-- batch (the parent table is small and the partial unique index is
-- cheap to build); runtime cost is dominated by the index build, not
-- the insert.
INSERT INTO public.provider_endpoint_protocols (
    provider_id, tenant_id, protocol, base_url, is_primary, vendor_native
)
SELECT
    p.id, p.tenant_id, p.protocol, p.base_url, TRUE,
    CASE
        WHEN pc.code = 'anthropic'                  THEN 'anthropic-claude'
        WHEN pc.code IN ('openai', 'azure')          THEN 'openai-gpt'
        WHEN pc.code IN ('google-gemini', 'vertex-ai') THEN 'google-gemini'
        WHEN pc.code = 'ollama'                      THEN NULL
        ELSE NULL
    END
FROM public.providers p
LEFT JOIN public.provider_catalog pc ON pc.code = p.catalog_code
WHERE p.deleted_at IS NULL
ON CONFLICT (provider_id, protocol) DO NOTHING;