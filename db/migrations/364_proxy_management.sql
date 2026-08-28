-- Migration 364: Proxy Management System
-- 代理管理系统：支持海外供应商通过代理访问

-- ============================================================================
-- 1. 代理订阅表 (proxy_subscriptions)
-- ============================================================================
CREATE TABLE IF NOT EXISTS proxy_subscriptions (
    id SERIAL PRIMARY KEY,
    name VARCHAR(100) NOT NULL,                    -- 订阅名称
    subscribe_url TEXT NOT NULL,                   -- 订阅地址
    status VARCHAR(20) DEFAULT 'active',           -- active/disabled/error
    last_fetch_at TIMESTAMP,                       -- 上次拉取时间
    last_fetch_status VARCHAR(20),                 -- success/failed
    last_error TEXT,                               -- 错误信息
    node_count INTEGER DEFAULT 0,                  -- 节点数量
    priority INTEGER DEFAULT 0,                    -- 优先级（数字越大优先级越高）
    notes TEXT,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_proxy_subs_status ON proxy_subscriptions(status);
CREATE INDEX IF NOT EXISTS idx_proxy_subs_priority ON proxy_subscriptions(priority DESC) WHERE status = 'active';

COMMENT ON TABLE proxy_subscriptions IS '代理订阅配置表';
COMMENT ON COLUMN proxy_subscriptions.subscribe_url IS '订阅地址，支持 NPS/V2Ray/Clash 等格式';
COMMENT ON COLUMN proxy_subscriptions.priority IS '优先级，自动选择节点时优先使用高优先级订阅';

-- ============================================================================
-- 2. 代理节点表 (proxy_nodes)
-- ============================================================================
CREATE TABLE IF NOT EXISTS proxy_nodes (
    id SERIAL PRIMARY KEY,
    subscription_id INTEGER REFERENCES proxy_subscriptions(id) ON DELETE CASCADE,
    name VARCHAR(200) NOT NULL,                    -- 节点名称
    protocol VARCHAR(20) NOT NULL,                 -- http/https/socks5/ss/vmess/trojan
    server VARCHAR(255) NOT NULL,                  -- 服务器地址
    port INTEGER NOT NULL,                         -- 端口
    username VARCHAR(100),                         -- 用户名（HTTP/SOCKS5）
    password TEXT,                                 -- 密码（加密存储）
    config JSONB,                                  -- 协议特定配置
    location VARCHAR(50),                          -- 地理位置
    status VARCHAR(20) DEFAULT 'active',           -- active/disabled/unhealthy
    health_check_url TEXT DEFAULT 'https://www.google.com/generate_204',
    last_health_check_at TIMESTAMP,
    last_health_check_status VARCHAR(20),          -- success/failed/timeout
    response_time_ms INTEGER,                      -- 响应时间(ms)
    success_rate FLOAT DEFAULT 1.0,                -- 成功率
    consecutive_failures INTEGER DEFAULT 0,        -- 连续失败次数
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_proxy_nodes_sub_id ON proxy_nodes(subscription_id);
CREATE INDEX IF NOT EXISTS idx_proxy_nodes_status ON proxy_nodes(status);
CREATE INDEX IF NOT EXISTS idx_proxy_nodes_health ON proxy_nodes(status, response_time_ms) WHERE status = 'active';

COMMENT ON TABLE proxy_nodes IS '代理节点表';
COMMENT ON COLUMN proxy_nodes.protocol IS '代理协议：http/https/socks5/ss/vmess/trojan';
COMMENT ON COLUMN proxy_nodes.config IS '协议特定配置，如 vmess 的 uuid/alterId，ss 的 method 等';
COMMENT ON COLUMN proxy_nodes.success_rate IS '成功率（0.0-1.0），用于节点选择';

-- ============================================================================
-- 3. 供应商域名分类表 (provider_domains)
-- ============================================================================
CREATE TABLE IF NOT EXISTS provider_domains (
    id SERIAL PRIMARY KEY,
    domain VARCHAR(255) NOT NULL UNIQUE,           -- 域名（如 api.groq.com）
    catalog_code VARCHAR(100),                     -- 关联 catalog_code
    requires_proxy BOOLEAN DEFAULT FALSE,          -- 是否需要代理
    location VARCHAR(50),                          -- 地理位置标记
    probe_status VARCHAR(20),                      -- reachable/blocked/unknown
    last_probe_at TIMESTAMP,
    last_probe_direct_ms INTEGER,                  -- 直连响应时间
    last_probe_proxy_ms INTEGER,                   -- 代理响应时间
    notes TEXT,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_provider_domains_requires_proxy ON provider_domains(requires_proxy);
CREATE INDEX IF NOT EXISTS idx_provider_domains_catalog ON provider_domains(catalog_code);
CREATE INDEX IF NOT EXISTS idx_provider_domains_probe ON provider_domains(probe_status);

COMMENT ON TABLE provider_domains IS '供应商域名分类表，用于自动判断是否需要代理';
COMMENT ON COLUMN provider_domains.requires_proxy IS '是否需要代理访问（用于自动路由）';
COMMENT ON COLUMN provider_domains.probe_status IS '探测状态：reachable(可直连)/blocked(被墙)/unknown(未知)';

-- ============================================================================
-- 4. 供应商表新增字段
-- ============================================================================
-- egress_profile 已存在，扩展其语义：
-- - 'direct': 直连
-- - 'proxy': 使用代理
-- - 'auto': 自动判断（根据 domestic 字段或 provider_domains 表）

-- 新增：指定特定代理订阅 ID（可选，为空则自动选择最优节点）
ALTER TABLE providers ADD COLUMN IF NOT EXISTS proxy_subscription_id INTEGER 
    REFERENCES proxy_subscriptions(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_providers_egress ON providers(egress_profile) WHERE egress_profile IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_providers_proxy_sub ON providers(proxy_subscription_id) WHERE proxy_subscription_id IS NOT NULL;

COMMENT ON COLUMN providers.egress_profile IS '出口配置：direct(直连)/proxy(代理)/auto(自动判断)';
COMMENT ON COLUMN providers.proxy_subscription_id IS '指定代理订阅ID，为空则自动选择最优节点';

-- ============================================================================
-- 5. 初始化常见域名数据
-- ============================================================================
INSERT INTO provider_domains (domain, catalog_code, requires_proxy, location, probe_status, notes) VALUES
-- 海外需要代理的供应商
('api.groq.com', 'groq-free', true, 'US', 'blocked', 'Groq API'),
('api.openai.com', 'openai', true, 'US', 'blocked', 'OpenAI API'),
('api.anthropic.com', 'anthropic', true, 'US', 'blocked', 'Anthropic API'),
('api.together.xyz', 'together-free', true, 'US', 'blocked', 'Together AI'),
('api.mistral.ai', 'mistral-free', true, 'EU', 'blocked', 'Mistral AI'),
('api.cohere.ai', 'cohere', true, 'US', 'blocked', 'Cohere API'),
('integrate.api.nvidia.com', 'nvidia-nim-free', true, 'US', 'blocked', 'NVIDIA NIM'),
('api.cerebras.ai', 'cerebras-free', true, 'US', 'blocked', 'Cerebras'),
('api.sambanova.ai', 'sambanova-free', true, 'US', 'blocked', 'SambaNova'),
('openrouter.ai', 'openrouter-free', true, 'US', 'blocked', 'OpenRouter'),

-- 国内可直连的供应商
('api.siliconflow.cn', 'siliconflow-free', false, 'CN', 'reachable', 'SiliconFlow'),
('open.bigmodel.cn', 'zhipu-free', false, 'CN', 'reachable', '智谱 AI'),
('dashscope.aliyuncs.com', 'aliyun', false, 'CN', 'reachable', '阿里云百炼'),
('aip.baidubce.com', 'baidu', false, 'CN', 'reachable', '百度文心'),

-- Google Gemini 可直连（使用国内节点）
('generativelanguage.googleapis.com', 'google-gemini-free', false, 'Global', 'reachable', 'Google Gemini')

ON CONFLICT (domain) DO NOTHING;

-- ============================================================================
-- 6. 审计日志扩展
-- ============================================================================
-- audit_logs 表已存在，无需修改，代理相关操作会记录在该表中
-- 操作类型示例：
-- - proxy_subscription_created
-- - proxy_subscription_updated
-- - proxy_subscription_refreshed
-- - proxy_node_health_check
-- - provider_proxy_config_changed
