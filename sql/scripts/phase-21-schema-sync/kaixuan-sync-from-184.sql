-- ============================================================
-- Sync SQL for database: kaixuan
-- Generated: 2026-06-26 (Phase 21 schema reconciliation)
-- Source: 184 (production reference)
-- Tables missing in local: 4
-- ============================================================

\connect kaixuan

CREATE TABLE public.permission_audit_log (
    id integer NOT NULL,
    tenant_id text NOT NULL,
    user_id text NOT NULL,
    action text NOT NULL,
    target_type text NOT NULL,
    target_key text NOT NULL,
    operator_id text NOT NULL,
    ip_address text,
    user_agent text,
    notes text,
    created_at timestamp without time zone DEFAULT now()
);

CREATE TABLE public.roles (
    id integer NOT NULL,
    tenant_id text,
    role_key text NOT NULL,
    role_name text NOT NULL,
    role_name_en text,
    description text,
    is_admin boolean DEFAULT false,
    permissions text[],
    created_at timestamp without time zone DEFAULT now(),
    updated_at timestamp without time zone DEFAULT now()
);

COMMENT ON COLUMN public.roles.tenant_id IS '租户ID，NULL表示跨租户的平台级角色';

COMMENT ON COLUMN public.roles.role_key IS '角色键，如：admin, employee, reviewer';

COMMENT ON COLUMN public.roles.is_admin IS '是否管理员角色（拥有所有权限）';

COMMENT ON COLUMN public.roles.permissions IS '角色默认权限数组';

CREATE TABLE public.user_permissions (
    user_id text NOT NULL,
    tenant_id text NOT NULL,
    permission_key text NOT NULL,
    granted boolean DEFAULT true,
    granted_by text,
    granted_at timestamp without time zone DEFAULT now(),
    expires_at timestamp without time zone,
    notes text
);

COMMENT ON COLUMN public.user_permissions.user_id IS '用户ID格式：{owner}/{name}';

COMMENT ON COLUMN public.user_permissions.tenant_id IS '租户ID（对应Casdoor的owner）';

COMMENT ON COLUMN public.user_permissions.permission_key IS '权限键，如：customers.read, workspace.access';

COMMENT ON COLUMN public.user_permissions.expires_at IS '过期时间，NULL表示永久';

CREATE TABLE public.user_roles (
    user_id text NOT NULL,
    tenant_id text NOT NULL,
    role_id integer NOT NULL,
    assigned_by text,
    assigned_at timestamp without time zone DEFAULT now(),
    notes text
);
