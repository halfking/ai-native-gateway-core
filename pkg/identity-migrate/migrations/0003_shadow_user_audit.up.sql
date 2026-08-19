-- 0003_shadow_user_audit.up.sql
-- 跨项目身份变更审计（不分区，append-only）

CREATE TABLE IF NOT EXISTS shadow_audit (
    id BIGSERIAL PRIMARY KEY,
    actor_project TEXT NOT NULL,
    action TEXT NOT NULL,
    target_provider TEXT,
    target_subject TEXT,
    target_shadow_id UUID,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_shadow_audit_shadow ON shadow_audit(target_shadow_id) WHERE target_shadow_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_shadow_audit_actor ON shadow_audit(actor_project, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_shadow_audit_action ON shadow_audit(action, created_at DESC);