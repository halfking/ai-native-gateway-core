-- ============================================================================
-- llm-gateway-go 基础数据初始化
-- ============================================================================
-- 说明: 仅保留 default 租户和 admin 用户，其他租户和用户不初始化
-- ============================================================================

-- ----------------------------------------------------------------------------
-- 租户数据（仅保留 default）
-- ----------------------------------------------------------------------------
INSERT INTO public.tenants (code, name, status, description)
VALUES ('default', '默认租户', 'active', '系统默认租户')
ON CONFLICT (code) DO NOTHING;

-- ----------------------------------------------------------------------------
-- 用户数据（仅保留 default 租户的 admin 用户）
-- ----------------------------------------------------------------------------
-- 注意：password_hash 是 bcrypt 加密后的默认值，生产环境需重新设置
INSERT INTO public.users (id, tenant_id, username, password_hash, display_name, email, role, enabled)
VALUES (1, 'default', 'admin', '$2a$10$CDoD6XNSX95kax/obEo6r.cfgdoctcX/N51cH2DOcASUuGefo7QFK', '系统管理员', '', 'super_admin', true)
ON CONFLICT (username) DO NOTHING;

-- 重置序列，确保新插入的用户ID不会冲突
SELECT setval('public.users_id_seq', COALESCE((SELECT MAX(id) FROM users), 1), true);

-- ----------------------------------------------------------------------------
-- 应用数据
-- ----------------------------------------------------------------------------
INSERT INTO public.applications (id, tenant_id, code, display_name, owner_user, data_sensitivity, enabled)
VALUES
    (1, 'default', 'admin', 'Admin Console', 'admin', 'confidential', true),
    (5, 'default', 'applicant', 'API Key Applicant', 'public', 'public', true)
ON CONFLICT (tenant_id, code) DO NOTHING;

-- 重置序列
SELECT setval('public.applications_id_seq', COALESCE((SELECT MAX(id) FROM applications), 1), true);

-- ----------------------------------------------------------------------------
-- 系统身份池
-- ----------------------------------------------------------------------------
INSERT INTO public.system_identity_pool (id, max_identities)
VALUES (1, 10000)
ON CONFLICT (id) DO NOTHING;