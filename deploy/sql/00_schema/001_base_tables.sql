-- ============================================================================
-- llm-gateway-go 基础表结构
-- 包含: tenants, users, applications, system_identity_pool
-- ============================================================================

-- ----------------------------------------------------------------------------
-- Tenants 表 - 租户管理
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.tenants (
    code VARCHAR(64) PRIMARY KEY,
    name VARCHAR(128) NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'trial', 'suspended', 'expired', 'disabled')),
    description TEXT NOT NULL DEFAULT '',
    contact_email VARCHAR(256) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_tenants_status ON tenants(status);
CREATE INDEX IF NOT EXISTS idx_tenants_name ON tenants(name);

-- ----------------------------------------------------------------------------
-- Users 表 - 用户管理（多租户）
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.users (
    id SERIAL PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL DEFAULT 'default',
    username VARCHAR(128) NOT NULL UNIQUE,
    password_hash VARCHAR(256) NOT NULL,
    display_name VARCHAR(128) NOT NULL DEFAULT '',
    email VARCHAR(256) NOT NULL DEFAULT '',
    role VARCHAR(32) NOT NULL DEFAULT 'tenant_admin',
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    last_login_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_users_tenant ON users(tenant_id);
CREATE INDEX IF NOT EXISTS idx_users_username ON users(username);

-- RLS: 行级安全策略
ALTER TABLE public.users ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation_users ON public.users;
CREATE POLICY tenant_isolation_users ON public.users
    USING ((tenant_id)::text = (public.get_current_tenant())::text);

-- ----------------------------------------------------------------------------
-- Applications 表 - 应用管理
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.applications (
    id BIGSERIAL PRIMARY KEY,
    tenant_id TEXT NOT NULL DEFAULT 'default',
    code TEXT NOT NULL,
    display_name TEXT NOT NULL,
    owner_user TEXT,
    data_sensitivity TEXT NOT NULL DEFAULT 'internal',
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    notes TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    default_client_profile TEXT,
    allowed_models_json JSONB,
    CONSTRAINT applications_tenant_id_code_key UNIQUE (tenant_id, code),
    CONSTRAINT applications_data_sensitivity_check
        CHECK (data_sensitivity = ANY (ARRAY['public'::text, 'internal'::text, 'confidential'::text]))
);
CREATE INDEX IF NOT EXISTS idx_applications_tenant_code
    ON applications (tenant_id, code)
    WHERE enabled = TRUE;

-- ----------------------------------------------------------------------------
-- System Identity Pool 表 - 全局身份池（终端用户数量上限）
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.system_identity_pool (
    id INT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    max_identities INT NOT NULL DEFAULT 10000,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by TEXT
);