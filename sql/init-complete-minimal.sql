-- 完整的最小化数据库初始化脚本
-- 创建LLM Gateway所需的所有基础表

BEGIN;

-- ============================================
-- 1. 用户和租户表
-- ============================================

-- 用户表
CREATE TABLE IF NOT EXISTS users (
    id SERIAL PRIMARY KEY,
    username TEXT UNIQUE NOT NULL,
    password_hash TEXT NOT NULL,
    role TEXT NOT NULL DEFAULT 'user',
    tenant_id TEXT NOT NULL DEFAULT 'default',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 租户表
CREATE TABLE IF NOT EXISTS tenants (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ============================================
-- 2. 供应商和凭证表
-- ============================================

-- 供应商表
CREATE TABLE IF NOT EXISTS providers (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    type TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 凭证表
CREATE TABLE IF NOT EXISTS credentials (
    id SERIAL PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    provider_id TEXT NOT NULL,
    api_key TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ============================================
-- 3. 请求日志表
-- ============================================

-- request_logs_hot 表
CREATE TABLE IF NOT EXISTS request_logs_hot (
    id BIGSERIAL PRIMARY KEY,
    request_id TEXT NOT NULL,
    ts TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    tenant_id TEXT,
    session_id TEXT,
    client_model TEXT,
    provider TEXT,
    provider_id TEXT,
    upstream_model TEXT,
    status_code INTEGER,
    error_message TEXT,
    latency_ms INTEGER,
    prompt_tokens INTEGER,
    completion_tokens INTEGER,
    total_tokens INTEGER,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- request_logs 主表（简化版）
CREATE TABLE IF NOT EXISTS request_logs (LIKE request_logs_hot INCLUDING ALL);

-- ============================================
-- 4. 模型和配置表
-- ============================================

-- 模型表
CREATE TABLE IF NOT EXISTS models (
    id SERIAL PRIMARY KEY,
    name TEXT UNIQUE NOT NULL,
    provider_id TEXT,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 路由表
CREATE TABLE IF NOT EXISTS routes (
    id SERIAL PRIMARY KEY,
    client_model TEXT NOT NULL,
    upstream_model TEXT NOT NULL,
    provider_id TEXT NOT NULL,
    priority INTEGER NOT NULL DEFAULT 0,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ============================================
-- 5. Session 相关表
-- ============================================

-- sessions 表
CREATE TABLE IF NOT EXISTS sessions (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    user_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_access_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- session_summaries 表
CREATE TABLE IF NOT EXISTS session_summaries (
    session_key TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    request_count INTEGER NOT NULL DEFAULT 0,
    error_count INTEGER NOT NULL DEFAULT 0,
    avg_latency_ms INTEGER,
    health_score REAL,
    health_grade TEXT,
    outcome TEXT,
    last_health_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ============================================
-- 6. Dashboard 表（已创建的保留）
-- ============================================

-- session_module_executions_hot (已存在)
-- dashboard_access_events_hot (已存在)

-- ============================================
-- 7. 基础索引
-- ============================================

CREATE INDEX IF NOT EXISTS idx_users_username ON users(username);
CREATE INDEX IF NOT EXISTS idx_users_tenant ON users(tenant_id);
CREATE INDEX IF NOT EXISTS idx_credentials_tenant ON credentials(tenant_id);
CREATE INDEX IF NOT EXISTS idx_request_logs_hot_ts ON request_logs_hot(ts DESC);
CREATE INDEX IF NOT EXISTS idx_request_logs_hot_tenant ON request_logs_hot(tenant_id, ts DESC);
CREATE INDEX IF NOT EXISTS idx_sessions_tenant ON sessions(tenant_id);
CREATE INDEX IF NOT EXISTS idx_session_summaries_tenant ON session_summaries(tenant_id);

-- ============================================
-- 8. 初始数据
-- ============================================

-- 插入默认租户
INSERT INTO tenants (id, name) VALUES ('default', 'Default Tenant')
ON CONFLICT (id) DO NOTHING;

-- 插入默认管理员用户 (密码: admin123)
-- bcrypt hash for "admin123"
INSERT INTO users (username, password_hash, role, tenant_id)
VALUES (
    'admin',
    '$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy',  -- admin123
    'super_admin',
    'default'
) ON CONFLICT (username) DO NOTHING;

-- 插入基础供应商
INSERT INTO providers (id, name, type) VALUES
    ('openai', 'OpenAI', 'llm'),
    ('anthropic', 'Anthropic', 'llm'),
    ('test', 'Test Provider', 'llm')
ON CONFLICT (id) DO NOTHING;

COMMIT;

-- 验证
SELECT 'Tables created:' as status;
SELECT tablename FROM pg_tables 
WHERE schemaname='public' 
  AND tablename IN ('users', 'tenants', 'providers', 'credentials', 'request_logs', 'request_logs_hot', 'sessions', 'session_summaries')
ORDER BY tablename;
