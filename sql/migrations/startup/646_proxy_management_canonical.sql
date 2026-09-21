-- Migration 646: canonical proxy management schema
--
-- Canonicalizes the schema originally introduced by the legacy
-- db/migrations/364_proxy_management.sql migration. The legacy file remains in
-- place for historical deployments; this startup migration is the authoritative
-- replay-safe path for current environments.
--
-- This migration intentionally does not seed provider_domains. Reachability and
-- proxy requirements are deployment policy and can become stale; only the
-- schema required by the proxy subscription/node/admin feature is canonicalized.

BEGIN;

-- ── 1. Proxy subscriptions ───────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS public.proxy_subscriptions (
    id SERIAL PRIMARY KEY,
    name VARCHAR(100) NOT NULL,
    subscribe_url TEXT NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'active',
    last_fetch_at TIMESTAMP,
    last_fetch_status VARCHAR(20),
    last_error TEXT,
    node_count INTEGER NOT NULL DEFAULT 0,
    priority INTEGER NOT NULL DEFAULT 0,
    notes TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);

ALTER TABLE public.proxy_subscriptions ADD COLUMN IF NOT EXISTS name VARCHAR(100);
ALTER TABLE public.proxy_subscriptions ADD COLUMN IF NOT EXISTS subscribe_url TEXT;
ALTER TABLE public.proxy_subscriptions ADD COLUMN IF NOT EXISTS status VARCHAR(20) DEFAULT 'active';
ALTER TABLE public.proxy_subscriptions ADD COLUMN IF NOT EXISTS last_fetch_at TIMESTAMP;
ALTER TABLE public.proxy_subscriptions ADD COLUMN IF NOT EXISTS last_fetch_status VARCHAR(20);
ALTER TABLE public.proxy_subscriptions ADD COLUMN IF NOT EXISTS last_error TEXT;
ALTER TABLE public.proxy_subscriptions ADD COLUMN IF NOT EXISTS node_count INTEGER DEFAULT 0;
ALTER TABLE public.proxy_subscriptions ADD COLUMN IF NOT EXISTS priority INTEGER DEFAULT 0;
ALTER TABLE public.proxy_subscriptions ADD COLUMN IF NOT EXISTS notes TEXT;
ALTER TABLE public.proxy_subscriptions ADD COLUMN IF NOT EXISTS created_at TIMESTAMP DEFAULT NOW();
ALTER TABLE public.proxy_subscriptions ADD COLUMN IF NOT EXISTS updated_at TIMESTAMP DEFAULT NOW();

-- Partial legacy installs may have added nullable columns without defaults.
-- Preserve those rows, but make them explicitly disabled until an operator fills
-- in real credentials. This prevents runtime scans of required fields from
-- receiving NULL while avoiding fabricated reachable configuration.
UPDATE public.proxy_subscriptions
SET name = COALESCE(NULLIF(name, ''), 'legacy-subscription-' || id::text),
    status = CASE
        WHEN subscribe_url IS NULL OR btrim(subscribe_url) = '' THEN 'disabled'
        ELSE COALESCE(NULLIF(status, ''), 'disabled')
    END,
    subscribe_url = COALESCE(subscribe_url, ''),
    node_count = COALESCE(node_count, 0),
    priority = COALESCE(priority, 0),
    created_at = COALESCE(created_at, NOW()),
    updated_at = COALESCE(updated_at, NOW());
ALTER TABLE public.proxy_subscriptions ALTER COLUMN name SET NOT NULL;
ALTER TABLE public.proxy_subscriptions ALTER COLUMN subscribe_url SET NOT NULL;
ALTER TABLE public.proxy_subscriptions ALTER COLUMN status SET NOT NULL;
ALTER TABLE public.proxy_subscriptions ALTER COLUMN node_count SET NOT NULL;
ALTER TABLE public.proxy_subscriptions ALTER COLUMN priority SET NOT NULL;
ALTER TABLE public.proxy_subscriptions ALTER COLUMN created_at SET NOT NULL;
ALTER TABLE public.proxy_subscriptions ALTER COLUMN updated_at SET NOT NULL;

CREATE INDEX IF NOT EXISTS idx_proxy_subs_status
    ON public.proxy_subscriptions(status);
CREATE INDEX IF NOT EXISTS idx_proxy_subs_priority
    ON public.proxy_subscriptions(priority DESC)
    WHERE status = 'active';

COMMENT ON TABLE public.proxy_subscriptions IS 'Proxy subscription configuration used by the admin proxy management API.';
COMMENT ON COLUMN public.proxy_subscriptions.subscribe_url IS 'Sensitive subscription URL; API responses must remove userinfo, query, and fragment.';
COMMENT ON COLUMN public.proxy_subscriptions.priority IS 'Higher values are preferred when automatically selecting a subscription.';

-- ── 2. Proxy nodes ───────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS public.proxy_nodes (
    id SERIAL PRIMARY KEY,
    subscription_id INTEGER REFERENCES public.proxy_subscriptions(id) ON DELETE CASCADE,
    name VARCHAR(200) NOT NULL,
    protocol VARCHAR(20) NOT NULL,
    server VARCHAR(255) NOT NULL,
    port INTEGER NOT NULL,
    username VARCHAR(100),
    password TEXT,
    config JSONB,
    location VARCHAR(50),
    status VARCHAR(20) NOT NULL DEFAULT 'active',
    health_check_url TEXT DEFAULT 'https://www.google.com/generate_204',
    last_health_check_at TIMESTAMP,
    last_health_check_status VARCHAR(20),
    response_time_ms INTEGER,
    success_rate FLOAT NOT NULL DEFAULT 1.0,
    consecutive_failures INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);

ALTER TABLE public.proxy_nodes ADD COLUMN IF NOT EXISTS subscription_id INTEGER;
ALTER TABLE public.proxy_nodes ADD COLUMN IF NOT EXISTS name VARCHAR(200);
ALTER TABLE public.proxy_nodes ADD COLUMN IF NOT EXISTS protocol VARCHAR(20);
ALTER TABLE public.proxy_nodes ADD COLUMN IF NOT EXISTS server VARCHAR(255);
ALTER TABLE public.proxy_nodes ADD COLUMN IF NOT EXISTS port INTEGER;
ALTER TABLE public.proxy_nodes ADD COLUMN IF NOT EXISTS username VARCHAR(100);
ALTER TABLE public.proxy_nodes ADD COLUMN IF NOT EXISTS password TEXT;
ALTER TABLE public.proxy_nodes ADD COLUMN IF NOT EXISTS config JSONB;
ALTER TABLE public.proxy_nodes ADD COLUMN IF NOT EXISTS location VARCHAR(50);
ALTER TABLE public.proxy_nodes ADD COLUMN IF NOT EXISTS status VARCHAR(20) DEFAULT 'active';
ALTER TABLE public.proxy_nodes ADD COLUMN IF NOT EXISTS health_check_url TEXT DEFAULT 'https://www.google.com/generate_204';
ALTER TABLE public.proxy_nodes ADD COLUMN IF NOT EXISTS last_health_check_at TIMESTAMP;
ALTER TABLE public.proxy_nodes ADD COLUMN IF NOT EXISTS last_health_check_status VARCHAR(20);
ALTER TABLE public.proxy_nodes ADD COLUMN IF NOT EXISTS response_time_ms INTEGER;
ALTER TABLE public.proxy_nodes ADD COLUMN IF NOT EXISTS success_rate FLOAT DEFAULT 1.0;
ALTER TABLE public.proxy_nodes ADD COLUMN IF NOT EXISTS consecutive_failures INTEGER DEFAULT 0;
ALTER TABLE public.proxy_nodes ADD COLUMN IF NOT EXISTS created_at TIMESTAMP DEFAULT NOW();
ALTER TABLE public.proxy_nodes ADD COLUMN IF NOT EXISTS updated_at TIMESTAMP DEFAULT NOW();

-- Nodes without a valid subscription cannot be repaired or selected safely.
DELETE FROM public.proxy_nodes n
WHERE n.subscription_id IS NULL
   OR NOT EXISTS (
       SELECT 1 FROM public.proxy_subscriptions s WHERE s.id = n.subscription_id
   );

-- Keep attributable partial nodes for operator repair, but force them out of
-- routing and fill scan-required columns with non-dialable placeholders.
UPDATE public.proxy_nodes
SET name = COALESCE(NULLIF(name, ''), 'legacy-node-' || id::text),
    status = CASE
        WHEN protocol IS NULL OR btrim(protocol) = ''
          OR server IS NULL OR btrim(server) = ''
          OR port IS NULL OR port < 1 OR port > 65535 THEN 'disabled'
        WHEN status IN ('active', 'disabled', 'unhealthy') THEN status
        ELSE 'disabled'
    END,
    protocol = COALESCE(NULLIF(protocol, ''), 'http'),
    server = COALESCE(NULLIF(server, ''), 'invalid.local'),
    port = CASE WHEN port BETWEEN 1 AND 65535 THEN port ELSE 0 END,
    health_check_url = COALESCE(NULLIF(health_check_url, ''), 'https://www.google.com/generate_204'),
    success_rate = COALESCE(success_rate, 0.0),
    consecutive_failures = COALESCE(consecutive_failures, 0),
    created_at = COALESCE(created_at, NOW()),
    updated_at = COALESCE(updated_at, NOW())
WHERE name IS NULL OR name = '' OR protocol IS NULL OR protocol = ''
   OR server IS NULL OR server = '' OR port IS NULL OR port < 1 OR port > 65535
   OR status IS NULL OR status NOT IN ('active', 'disabled', 'unhealthy')
   OR health_check_url IS NULL OR health_check_url = ''
   OR success_rate IS NULL OR consecutive_failures IS NULL
   OR created_at IS NULL OR updated_at IS NULL;
ALTER TABLE public.proxy_nodes ALTER COLUMN subscription_id SET NOT NULL;
ALTER TABLE public.proxy_nodes ALTER COLUMN name SET NOT NULL;
ALTER TABLE public.proxy_nodes ALTER COLUMN protocol SET NOT NULL;
ALTER TABLE public.proxy_nodes ALTER COLUMN server SET NOT NULL;
ALTER TABLE public.proxy_nodes ALTER COLUMN port SET NOT NULL;
ALTER TABLE public.proxy_nodes ALTER COLUMN status SET NOT NULL;
ALTER TABLE public.proxy_nodes ALTER COLUMN health_check_url SET DEFAULT 'https://www.google.com/generate_204';
ALTER TABLE public.proxy_nodes ALTER COLUMN health_check_url SET NOT NULL;
ALTER TABLE public.proxy_nodes ALTER COLUMN success_rate SET NOT NULL;
ALTER TABLE public.proxy_nodes ALTER COLUMN consecutive_failures SET NOT NULL;
ALTER TABLE public.proxy_nodes ALTER COLUMN created_at SET NOT NULL;
ALTER TABLE public.proxy_nodes ALTER COLUMN updated_at SET NOT NULL;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint c
        WHERE c.conrelid = 'public.proxy_nodes'::regclass
          AND c.conname = 'proxy_nodes_subscription_id_fkey'
          AND NOT (
              c.contype = 'f'
              AND c.conkey = ARRAY[(SELECT attnum FROM pg_attribute
                  WHERE attrelid = 'public.proxy_nodes'::regclass
                    AND attname = 'subscription_id' AND NOT attisdropped)]::smallint[]
              AND c.confrelid = 'public.proxy_subscriptions'::regclass
              AND c.confdeltype = 'c'
          )
    ) THEN
        ALTER TABLE public.proxy_nodes DROP CONSTRAINT proxy_nodes_subscription_id_fkey;
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint c
        WHERE c.conrelid = 'public.proxy_nodes'::regclass
          AND c.contype = 'f'
          AND c.conkey = ARRAY[(SELECT attnum FROM pg_attribute
              WHERE attrelid = 'public.proxy_nodes'::regclass
                AND attname = 'subscription_id' AND NOT attisdropped)]::smallint[]
          AND c.confrelid = 'public.proxy_subscriptions'::regclass
          AND c.confdeltype = 'c'
    ) THEN
        ALTER TABLE public.proxy_nodes ADD CONSTRAINT proxy_nodes_subscription_id_fkey
            FOREIGN KEY (subscription_id) REFERENCES public.proxy_subscriptions(id)
            ON DELETE CASCADE NOT VALID;
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_proxy_nodes_sub_id ON public.proxy_nodes(subscription_id);
CREATE INDEX IF NOT EXISTS idx_proxy_nodes_status ON public.proxy_nodes(status);
CREATE INDEX IF NOT EXISTS idx_proxy_nodes_health ON public.proxy_nodes(status, response_time_ms)
    WHERE status = 'active';

COMMENT ON TABLE public.proxy_nodes IS 'Proxy node inventory and health state.';
COMMENT ON COLUMN public.proxy_nodes.protocol IS 'Inventory protocol. Only http, https, and socks5 are directly dialable by the Go gateway.';
COMMENT ON COLUMN public.proxy_nodes.password IS 'Proxy credential secret; encrypt at rest and never return plaintext from admin APIs.';
COMMENT ON COLUMN public.proxy_nodes.config IS 'Protocol-specific configuration for imported inventory nodes.';
COMMENT ON COLUMN public.proxy_nodes.success_rate IS 'Health success ratio from 0.0 to 1.0 used during node selection.';

-- ── 3. Provider domain policy ────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS public.provider_domains (
    id SERIAL PRIMARY KEY,
    domain VARCHAR(255) NOT NULL UNIQUE,
    catalog_code VARCHAR(100),
    requires_proxy BOOLEAN NOT NULL DEFAULT FALSE,
    location VARCHAR(50),
    probe_status VARCHAR(20),
    last_probe_at TIMESTAMP,
    last_probe_direct_ms INTEGER,
    last_probe_proxy_ms INTEGER,
    notes TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);

ALTER TABLE public.provider_domains ADD COLUMN IF NOT EXISTS domain VARCHAR(255);
ALTER TABLE public.provider_domains ADD COLUMN IF NOT EXISTS catalog_code VARCHAR(100);
ALTER TABLE public.provider_domains ADD COLUMN IF NOT EXISTS requires_proxy BOOLEAN DEFAULT FALSE;
ALTER TABLE public.provider_domains ADD COLUMN IF NOT EXISTS location VARCHAR(50);
ALTER TABLE public.provider_domains ADD COLUMN IF NOT EXISTS probe_status VARCHAR(20);
ALTER TABLE public.provider_domains ADD COLUMN IF NOT EXISTS last_probe_at TIMESTAMP;
ALTER TABLE public.provider_domains ADD COLUMN IF NOT EXISTS last_probe_direct_ms INTEGER;
ALTER TABLE public.provider_domains ADD COLUMN IF NOT EXISTS last_probe_proxy_ms INTEGER;
ALTER TABLE public.provider_domains ADD COLUMN IF NOT EXISTS notes TEXT;
ALTER TABLE public.provider_domains ADD COLUMN IF NOT EXISTS created_at TIMESTAMP DEFAULT NOW();
ALTER TABLE public.provider_domains ADD COLUMN IF NOT EXISTS updated_at TIMESTAMP DEFAULT NOW();

UPDATE public.provider_domains
SET domain = COALESCE(NULLIF(domain, ''), 'legacy-domain-' || id::text),
    requires_proxy = COALESCE(requires_proxy, FALSE),
    created_at = COALESCE(created_at, NOW()),
    updated_at = COALESCE(updated_at, NOW());
-- Preserve duplicate policy rows rather than deleting them. The renamed rows are
-- visible for operator repair and cannot shadow the canonical domain lookup.
UPDATE public.provider_domains d
SET domain = left(d.domain, 255 - length('#legacy-' || d.id::text)) || '#legacy-' || d.id::text
WHERE d.domain IS NOT NULL
  AND EXISTS (SELECT 1 FROM public.provider_domains older
              WHERE older.domain = d.domain AND older.id < d.id);
ALTER TABLE public.provider_domains ALTER COLUMN domain SET NOT NULL;
ALTER TABLE public.provider_domains ALTER COLUMN requires_proxy SET NOT NULL;
ALTER TABLE public.provider_domains ALTER COLUMN created_at SET NOT NULL;
ALTER TABLE public.provider_domains ALTER COLUMN updated_at SET NOT NULL;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint c
        JOIN pg_class t ON t.oid = c.conrelid
        JOIN pg_namespace n ON n.oid = t.relnamespace
        WHERE n.nspname = 'public' AND t.relname = 'provider_domains'
          AND c.contype IN ('p', 'u')
          AND c.conkey = ARRAY[(SELECT attnum FROM pg_attribute
              WHERE attrelid = 'public.provider_domains'::regclass
                AND attname = 'domain' AND NOT attisdropped)]::smallint[]
    ) THEN
        ALTER TABLE public.provider_domains ADD CONSTRAINT provider_domains_domain_key UNIQUE (domain);
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_provider_domains_requires_proxy ON public.provider_domains(requires_proxy);
CREATE INDEX IF NOT EXISTS idx_provider_domains_catalog ON public.provider_domains(catalog_code);
CREATE INDEX IF NOT EXISTS idx_provider_domains_probe ON public.provider_domains(probe_status);

COMMENT ON TABLE public.provider_domains IS 'Provider-domain reachability and proxy-routing policy.';
COMMENT ON COLUMN public.provider_domains.requires_proxy IS 'Whether requests to this domain should use proxy routing.';
COMMENT ON COLUMN public.provider_domains.probe_status IS 'Latest direct-connect probe state: reachable, blocked, or unknown.';

-- ── 4. Provider-to-subscription binding ──────────────────────────────────────
ALTER TABLE public.providers ADD COLUMN IF NOT EXISTS egress_profile TEXT NOT NULL DEFAULT 'direct';
ALTER TABLE public.providers ADD COLUMN IF NOT EXISTS proxy_subscription_id INTEGER;
UPDATE public.providers SET egress_profile = 'direct'
WHERE egress_profile IS NULL OR egress_profile = '';
ALTER TABLE public.providers ALTER COLUMN egress_profile SET DEFAULT 'direct';
ALTER TABLE public.providers ALTER COLUMN egress_profile SET NOT NULL;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint c
        WHERE c.conrelid = 'public.providers'::regclass
          AND c.conname = 'providers_proxy_subscription_id_fkey'
          AND NOT (
              c.contype = 'f'
              AND c.conkey = ARRAY[(SELECT attnum FROM pg_attribute
                  WHERE attrelid = 'public.providers'::regclass
                    AND attname = 'proxy_subscription_id' AND NOT attisdropped)]::smallint[]
              AND c.confrelid = 'public.proxy_subscriptions'::regclass
              AND c.confdeltype = 'n'
          )
    ) THEN
        ALTER TABLE public.providers DROP CONSTRAINT providers_proxy_subscription_id_fkey;
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint c
        WHERE c.conrelid = 'public.providers'::regclass
          AND c.contype = 'f'
          AND c.conkey = ARRAY[(SELECT attnum FROM pg_attribute
              WHERE attrelid = 'public.providers'::regclass
                AND attname = 'proxy_subscription_id' AND NOT attisdropped)]::smallint[]
          AND c.confrelid = 'public.proxy_subscriptions'::regclass
          AND c.confdeltype = 'n'
    ) THEN
        ALTER TABLE public.providers ADD CONSTRAINT providers_proxy_subscription_id_fkey
            FOREIGN KEY (proxy_subscription_id) REFERENCES public.proxy_subscriptions(id)
            ON DELETE SET NULL NOT VALID;
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_providers_egress ON public.providers(egress_profile)
    WHERE egress_profile IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_providers_proxy_sub ON public.providers(proxy_subscription_id)
    WHERE proxy_subscription_id IS NOT NULL;

COMMENT ON COLUMN public.providers.egress_profile IS 'Egress mode: direct, proxy, or auto.';
COMMENT ON COLUMN public.providers.proxy_subscription_id IS 'Optional fixed proxy subscription; NULL allows automatic node selection.';

-- ── 5. Migration ledger ──────────────────────────────────────────────────────
INSERT INTO public.schema_migrations (version, description)
VALUES (
    '646',
    'canonical proxy management schema: subscriptions, nodes, provider-domain policy, and provider subscription binding; replay-safe reconciliation of legacy migration 364'
)
ON CONFLICT (version) DO NOTHING;

COMMIT;
