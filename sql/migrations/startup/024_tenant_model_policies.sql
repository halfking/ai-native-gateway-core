-- Round 48 (2026-06-21) — Tenant Model Policy
-- Denylist that lets super_admin restrict which canonical model names a
-- specific tenant may use.  Default behaviour (table empty) is
-- "all models allowed for all tenants" — matching the product
-- requirement that the gate is opt-in.
--
-- See docs/llm-gateway-go/2026-06-21-tenant-model-policy.md §1.
--
-- ── Design notes ────────────────────────────────────────────────
--   - Soft delete via deleted_at; Checker queries the
--     tenant_model_policies_active VIEW (deleted_at IS NULL) so
--     restored policies resume enforcement immediately.
--   - canonical_name is NOT a FK to models_canonical because we
--     intentionally want to allow denylisting a model that has not
--     yet been synced into the catalog (defence in depth — block
--     before a misconfigured upstream can start serving it).
--   - RLS (Pattern A, multi-tenant-standards.md §1): any SELECT
--     must filter by tenant_id via app.current_tenant GUC.
--   - The trigger on INSERT/UPDATE/DELETE writes an audit row
--     capturing the actor from the app.current_admin GUC (matches
--     routing_overrides_audit pattern from P7.9).

CREATE TABLE IF NOT EXISTS tenant_model_policies (
    id              BIGSERIAL PRIMARY KEY,
    tenant_id       VARCHAR(64) NOT NULL REFERENCES tenants(code) ON DELETE CASCADE,
    canonical_name  TEXT NOT NULL,
    reason          TEXT NOT NULL DEFAULT '',
    created_by      VARCHAR(128) NOT NULL DEFAULT '',
    deleted_at      TIMESTAMPTZ,
    deleted_by      VARCHAR(128),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, canonical_name),
    CHECK (canonical_name <> '')
);

-- Active-only lookup index for the Checker cache reload.
CREATE INDEX IF NOT EXISTS idx_tmp_tenant_active
    ON tenant_model_policies (tenant_id) WHERE deleted_at IS NULL;

-- Cross-tenant analytics index (which models get denied most).
CREATE INDEX IF NOT EXISTS idx_tmp_canonical
    ON tenant_model_policies (canonical_name);

-- Pattern A RLS: tenant-scoped reads require app.current_tenant GUC.
-- Checker bypass is handled at the application layer (SET LOCAL
-- row_security = off) — see internal/modelpolicy/checker.go.
ALTER TABLE tenant_model_policies ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation_tmp ON public.tenant_model_policies;
CREATE POLICY tenant_isolation_tmp ON public.tenant_model_policies
    USING ((tenant_id)::text = (public.get_current_tenant())::text);

-- ── Active view (Checker queries this) ───────────────────────────
CREATE OR REPLACE VIEW tenant_model_policies_active AS
    SELECT id, tenant_id, canonical_name, reason, created_by,
           created_at, updated_at
    FROM tenant_model_policies
    WHERE deleted_at IS NULL;

-- ── Audit table + trigger ────────────────────────────────────────
CREATE TABLE IF NOT EXISTS tenant_model_policies_audit (
    id              BIGSERIAL PRIMARY KEY,
    ts              TIMESTAMPTZ NOT NULL DEFAULT now(),
    action          TEXT NOT NULL CHECK (action IN ('insert','update','delete','undelete')),
    policy_id       BIGINT,
    tenant_id       TEXT,
    canonical_name  TEXT,
    reason          TEXT,
    actor           TEXT
);
CREATE INDEX IF NOT EXISTS idx_tmp_audit_ts ON tenant_model_policies_audit (ts DESC);
CREATE INDEX IF NOT EXISTS idx_tmp_audit_tenant_ts ON tenant_model_policies_audit (tenant_id, ts DESC);

-- Pattern A RLS for the audit table.  tenant_id is NULL when an
-- actor with no tenant context (e.g., system super_admin acting
-- without an app.current_tenant GUC) writes; that NULL row is
-- only visible to superusers / DBAs (the application sets
-- app.current_tenant = 'default' for super_admin reads, so all
-- rows — including NULL ones — are returned; in production this
-- table is admin-only via the /api/admin/* surface and tenant
-- admins cannot read it).
ALTER TABLE tenant_model_policies_audit ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation_tmp_audit ON public.tenant_model_policies_audit;
CREATE POLICY tenant_isolation_tmp_audit ON public.tenant_model_policies_audit
    USING ((tenant_id)::text = (public.get_current_tenant())::text
           OR (tenant_id) IS NULL);

CREATE OR REPLACE FUNCTION tenant_model_policies_audit_fn()
RETURNS TRIGGER AS $$
DECLARE
    v_actor TEXT := COALESCE(
        NULLIF(current_setting('app.current_admin', true), ''),
        'system'
    );
BEGIN
    IF (TG_OP = 'INSERT') THEN
        INSERT INTO tenant_model_policies_audit
            (action, policy_id, tenant_id, canonical_name, reason, actor)
        VALUES
            ('insert', NEW.id, NEW.tenant_id, NEW.canonical_name, NEW.reason, v_actor);
        RETURN NEW;
    ELSIF (TG_OP = 'UPDATE') THEN
        IF NEW.deleted_at IS DISTINCT FROM OLD.deleted_at THEN
            IF NEW.deleted_at IS NULL THEN
                INSERT INTO tenant_model_policies_audit
                    (action, policy_id, tenant_id, canonical_name, reason, actor)
                VALUES
                    ('undelete', NEW.id, NEW.tenant_id, NEW.canonical_name, NEW.reason, v_actor);
            ELSE
                INSERT INTO tenant_model_policies_audit
                    (action, policy_id, tenant_id, canonical_name, reason, actor)
                VALUES
                    ('delete', NEW.id, NEW.tenant_id, NEW.canonical_name, OLD.reason, v_actor);
            END IF;
        ELSIF NEW.reason IS DISTINCT FROM OLD.reason
              OR NEW.canonical_name IS DISTINCT FROM OLD.canonical_name
        THEN
            INSERT INTO tenant_model_policies_audit
                (action, policy_id, tenant_id, canonical_name, reason, actor)
            VALUES
                ('update', NEW.id, NEW.tenant_id, NEW.canonical_name, NEW.reason, v_actor);
        END IF;
        RETURN NEW;
    ELSIF (TG_OP = 'DELETE') THEN
        INSERT INTO tenant_model_policies_audit
            (action, policy_id, tenant_id, canonical_name, reason, actor)
        VALUES
            ('delete', OLD.id, OLD.tenant_id, OLD.canonical_name, OLD.reason, v_actor);
        RETURN OLD;
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS tenant_model_policies_audit_trg ON tenant_model_policies;
CREATE TRIGGER tenant_model_policies_audit_trg
    AFTER INSERT OR UPDATE OR DELETE ON tenant_model_policies
    FOR EACH ROW EXECUTE FUNCTION tenant_model_policies_audit_fn();