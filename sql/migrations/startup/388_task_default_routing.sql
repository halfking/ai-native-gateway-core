-- 388_task_default_routing.sql
-- Explicit default routing (task_type × profile × tier × tenant) for smart auto-route.
-- Platform scope = tenant_id IS NULL; tenant scope = tenants.code.
-- Unique key: (task_type, profile, tier, COALESCE(tenant_id, '')).

CREATE TABLE IF NOT EXISTS task_default_routing (
    id              bigserial PRIMARY KEY,
    task_type       text NOT NULL,
    profile         text NOT NULL DEFAULT '',
    tier            text NOT NULL DEFAULT 'primary',
    canonical_model text NOT NULL,
    tenant_id       varchar(64) NULL,
    priority        int NOT NULL DEFAULT 100,
    reason          text NOT NULL DEFAULT '',
    created_by      text,
    created_at      timestamptz NOT NULL DEFAULT NOW(),
    updated_at      timestamptz NOT NULL DEFAULT NOW(),
    expires_at      timestamptz NULL,
    CONSTRAINT task_default_routing_tier_check
        CHECK (tier = ANY (ARRAY['primary'::text, 'secondary'::text, 'fallback'::text])),
    CONSTRAINT task_default_routing_profile_check
        CHECK (profile = ANY (ARRAY[''::text, 'smart'::text, 'speed_first'::text, 'cost_first'::text]))
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_task_default_routing_scope
    ON task_default_routing (task_type, profile, tier, (COALESCE(tenant_id, '')));

CREATE INDEX IF NOT EXISTS idx_task_default_routing_task
    ON task_default_routing (task_type);

CREATE INDEX IF NOT EXISTS idx_task_default_routing_active
    ON task_default_routing (task_type, profile)
    WHERE expires_at IS NULL OR expires_at > NOW();

CREATE TABLE IF NOT EXISTS task_default_routing_audit (
    id              bigserial PRIMARY KEY,
    ts              timestamptz NOT NULL DEFAULT NOW(),
    action          text NOT NULL,
    routing_id      bigint,
    task_type       text,
    profile         text,
    tier            text,
    canonical_model text,
    tenant_id       varchar(64),
    priority        int,
    reason          text,
    expires_at      timestamptz,
    actor           text
);

CREATE INDEX IF NOT EXISTS idx_task_default_routing_audit_ts
    ON task_default_routing_audit (ts DESC);
