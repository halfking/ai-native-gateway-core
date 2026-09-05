-- 0002_shadow_user_providers.up.sql
-- (provider, subject, tenant_id) 三元组 → shadow_user_id 映射

CREATE TABLE IF NOT EXISTS shadow_user_providers (
    provider           TEXT NOT NULL,
    subject VARCHAR(255) NOT NULL,
    tenant_id          TEXT NOT NULL DEFAULT 'default',
    shadow_user_id     UUID NOT NULL REFERENCES shadow_users ON DELETE CASCADE,
    external_id        VARCHAR(255),
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    linked_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (provider, subject, tenant_id)
);

CREATE INDEX IF NOT EXISTS idx_shadow_user_providers_shadow ON shadow_user_providers(shadow_user_id);
CREATE INDEX IF NOT EXISTS idx_shadow_user_providers_tenant_subject ON shadow_user_providers(tenant_id, subject);
CREATE INDEX IF NOT EXISTS idx_shadow_user_providers_external ON shadow_user_providers(external_id) WHERE external_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_shadow_user_providers_last_seen ON shadow_user_providers(last_seen_at);