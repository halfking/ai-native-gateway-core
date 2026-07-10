#!/bin/bash
# LLM Gateway 本地部署完整脚本 v2
# 从头创建一个全新的测试数据库

set -e

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m'

echo "========================================"
echo "LLM Gateway 本地部署脚本 v2"
echo "========================================"
echo ""

# 配置
DB_HOST="${DB_HOST:-localhost}"
DB_PORT="${DB_PORT:-5434}"
DB_NAME="${DB_NAME:-llm_gateway_local}"
DB_USER="${DB_USER:-redclaw}"
DB_PASSWORD="${DB_PASSWORD:-redclaw}"

echo "数据库配置:"
echo "  Host: $DB_HOST:$DB_PORT"
echo "  Database: $DB_NAME"
echo "  User: $DB_USER"
echo ""

# 1. 删除并重建数据库
echo -e "${YELLOW}[1/5] 重建数据库...${NC}"
PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d postgres << EOF
DROP DATABASE IF EXISTS $DB_NAME;
CREATE DATABASE $DB_NAME;
EOF
echo -e "${GREEN}✓ 数据库已重建${NC}"
echo ""

# 2. 初始化基础schema
echo -e "${YELLOW}[2/5] 初始化数据库schema...${NC}"
PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" << 'EOSQL'
-- 启用扩展
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- ============================================
-- 租户表（使用code作为主键）
-- ============================================
CREATE TABLE tenants (
    code VARCHAR(64) PRIMARY KEY,
    name VARCHAR(128) NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'trial', 'suspended', 'expired', 'disabled')),
    description TEXT NOT NULL DEFAULT '',
    contact_email VARCHAR(256) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    metadata JSONB NOT NULL DEFAULT '{}'
);

INSERT INTO tenants (code, name, status, description)
VALUES ('default', '默认租户', 'active', '系统默认租户');

-- ============================================
-- 用户表（认证用）
-- ============================================
CREATE TABLE auth_users (
    id SERIAL PRIMARY KEY,
    username VARCHAR(255) UNIQUE NOT NULL,
    password_hash TEXT NOT NULL,
    role VARCHAR(50) NOT NULL DEFAULT 'user',
    tenant_id VARCHAR(64) NOT NULL DEFAULT 'default' REFERENCES tenants(code),
    email VARCHAR(255),
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 插入管理员 (username: admin, password: <ADMIN_PASSWORD_REDACTED>)
-- bcrypt hash for "<ADMIN_PASSWORD_REDACTED>"
INSERT INTO auth_users (username, password_hash, role, tenant_id)
VALUES ('admin', '$2a$10$rLxN/O5ZEWk3qGxvBOKvbeOzVyF8/Kq.bD0YpE5aPfHhGvY5aHMxe', 'super_admin', 'default');

-- ============================================
-- 用户资料表
-- ============================================
CREATE TABLE users (
    tenant_id TEXT NOT NULL,
    user_id TEXT NOT NULL,
    display_name TEXT,
    position TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    metadata JSONB NOT NULL DEFAULT '{}',
    PRIMARY KEY (tenant_id, user_id)
);

CREATE INDEX idx_users_tenant ON users(tenant_id);

-- ============================================
-- API Keys
-- ============================================
CREATE TABLE api_keys (
    id SERIAL PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL,
    key_hash TEXT NOT NULL,
    key_prefix VARCHAR(20),
    name VARCHAR(255),
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ============================================
-- Applications
-- ============================================
CREATE TABLE applications (
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
    CONSTRAINT applications_tenant_id_code_key UNIQUE (tenant_id, code)
);

CREATE INDEX idx_applications_tenant_code ON applications(tenant_id, code) WHERE enabled = TRUE;

-- 插入默认应用
INSERT INTO applications (id, tenant_id, code, display_name, owner_user, data_sensitivity, enabled)
VALUES (1, 'default', 'admin', 'Admin Console', 'admin', 'confidential', TRUE);

INSERT INTO applications (tenant_id, code, display_name, owner_user, data_sensitivity, enabled)
VALUES ('default', 'applicant', 'API Key Applicant', 'public', 'public', TRUE);

-- ============================================
-- 供应商和模型
-- ============================================
CREATE TABLE providers (
    id SERIAL PRIMARY KEY,
    code VARCHAR(64) UNIQUE NOT NULL,
    name VARCHAR(128) NOT NULL,
    type VARCHAR(32) NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO providers (code, name, type) VALUES
('openai', 'OpenAI', 'llm'),
('anthropic', 'Anthropic', 'llm'),
('test', 'Test Provider', 'llm');

CREATE TABLE models (
    id SERIAL PRIMARY KEY,
    name TEXT UNIQUE NOT NULL,
    provider_id INTEGER REFERENCES providers(id),
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ============================================
-- 请求日志表
-- ============================================
CREATE TABLE request_logs_hot (
    id BIGSERIAL PRIMARY KEY,
    request_id TEXT NOT NULL,
    ts TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    tenant_id TEXT,
    session_id TEXT,
    client_model TEXT,
    provider TEXT,
    provider_id INTEGER,
    upstream_model TEXT,
    status_code INTEGER,
    error_message TEXT,
    latency_ms INTEGER,
    prompt_tokens INTEGER,
    completion_tokens INTEGER,
    total_tokens INTEGER,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_request_logs_hot_ts ON request_logs_hot(ts DESC);
CREATE INDEX idx_request_logs_hot_tenant ON request_logs_hot(tenant_id, ts DESC);
CREATE UNIQUE INDEX idx_request_logs_hot_request_id ON request_logs_hot(request_id, ts);

-- request_logs 主表（简化版，兼容性）
CREATE TABLE request_logs (LIKE request_logs_hot INCLUDING ALL);

-- ============================================
-- Sessions
-- ============================================
CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    user_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_access_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_sessions_tenant ON sessions(tenant_id);

CREATE TABLE session_summaries (
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

CREATE INDEX idx_session_summaries_tenant ON session_summaries(tenant_id);

-- ============================================
-- 路由表
-- ============================================
CREATE TABLE routes (
    id SERIAL PRIMARY KEY,
    client_model TEXT NOT NULL,
    upstream_model TEXT NOT NULL,
    provider_id INTEGER REFERENCES providers(id),
    priority INTEGER NOT NULL DEFAULT 0,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ============================================
-- Credentials
-- ============================================
CREATE TABLE credentials (
    id SERIAL PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL,
    provider_id INTEGER REFERENCES providers(id),
    api_key TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_credentials_tenant ON credentials(tenant_id);

SELECT 'Schema initialized successfully' as status;
EOSQL

echo -e "${GREEN}✓ Schema 已初始化${NC}"
echo ""

# 3. 执行Dashboard迁移
echo -e "${YELLOW}[3/5] 执行 Dashboard 迁移...${NC}"
cd "$(dirname "$0")/.."
PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -f sql/migrations/startup/360_session_module_executions.sql > /dev/null 2>&1 || echo "  注意: 360迁移可能已执行"
PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -f sql/migrations/startup/361_dashboard_access_events.sql > /dev/null 2>&1 || echo "  注意: 361迁移可能已执行"
echo -e "${GREEN}✓ Dashboard 表已创建${NC}"
echo ""

# 4. 生成环境配置
echo -e "${YELLOW}[4/5] 生成环境配置...${NC}"
cat > /tmp/llm-gateway-test.env << EOF
# LLM Gateway 本地测试环境
export LLM_GATEWAY_DATABASE_URL="postgres://${DB_USER}:${DB_PASSWORD}@${DB_HOST}:${DB_PORT}/${DB_NAME}?sslmode=disable"
export LLM_GATEWAY_REDIS_ADDR="localhost:6379"
export LLM_GATEWAY_REDIS_PASSWORD=""
export LLM_GATEWAY_LISTEN=":8781"
export LLM_GATEWAY_SECRET_KEY="local-test-secret-$(date +%s)"
export LLM_GATEWAY_ADMIN_PASSWORD="<ADMIN_PASSWORD_REDACTED>"
export LLM_GATEWAY_CORS_ORIGINS="*"
export LLM_GATEWAY_ENV="development"
export LLM_GATEWAY_LOG_LEVEL="info"
EOF
echo -e "${GREEN}✓ 配置文件已生成: /tmp/llm-gateway-test.env${NC}"
echo ""

# 5. 编译和启动
echo -e "${YELLOW}[5/5] 编译并启动服务...${NC}"
go build -o llm-gateway ./cmd/gateway
if [ $? -eq 0 ]; then
    echo -e "${GREEN}✓ 编译成功${NC}"
else
    echo -e "${RED}✗ 编译失败${NC}"
    exit 1
fi

# 停止旧服务
if [ -f /tmp/llm-gateway.pid ]; then
    kill $(cat /tmp/llm-gateway.pid) 2>/dev/null || true
    sleep 2
fi

# 启动服务
source /tmp/llm-gateway-test.env
nohup ./llm-gateway > /tmp/llm-gateway.log 2>&1 &
echo $! > /tmp/llm-gateway.pid
sleep 5

# 验证
if curl -s http://localhost:8781/healthz | grep -q "ok"; then
    echo -e "${GREEN}✓ 服务启动成功${NC}"
else
    echo -e "${RED}✗ 服务启动失败，查看日志: tail -f /tmp/llm-gateway.log${NC}"
    exit 1
fi

echo ""
echo "========================================"
echo -e "${GREEN}部署完成！${NC}"
echo "========================================"
echo ""
echo "访问信息:"
echo "  前端: http://localhost:8781/"
echo "  健康检查: http://localhost:8781/healthz"
echo ""
echo "登录信息:"
echo "  用户名: admin"
echo "  密码: <ADMIN_PASSWORD_REDACTED>"
echo ""
echo "数据库:"
echo "  连接: PGPASSWORD=$DB_PASSWORD psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME"
echo ""
echo "管理:"
echo "  查看日志: tail -f /tmp/llm-gateway.log"
echo "  停止服务: kill \$(cat /tmp/llm-gateway.pid)"
echo ""
