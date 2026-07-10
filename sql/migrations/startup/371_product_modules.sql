-- 371_product_modules.sql
-- 产品模块定义、订阅层级与功能门控基础数据
--
-- License = 设备绑定 + 功能授权 + 订阅有效期
-- 本迁移定义"功能授权"部分的元数据（26个产品模块 + 4个订阅层级）

CREATE TABLE IF NOT EXISTS product_modules (
    id              SERIAL PRIMARY KEY,
    key             TEXT NOT NULL UNIQUE,
    name            TEXT NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    category        TEXT NOT NULL,
    icon            TEXT,
    setting_key     TEXT,
    is_base         BOOLEAN NOT NULL DEFAULT FALSE,
    sort_order      INT NOT NULL DEFAULT 0,
    enabled         BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_pm_category ON product_modules (category);
CREATE INDEX IF NOT EXISTS idx_pm_setting ON product_modules (setting_key);

CREATE TABLE IF NOT EXISTS product_module_features (
    id              SERIAL PRIMARY KEY,
    module_key      TEXT NOT NULL REFERENCES product_modules(key) ON DELETE CASCADE,
    feature_key     TEXT NOT NULL,
    feature_name    TEXT NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    setting_key     TEXT,
    enabled         BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (module_key, feature_key)
);
CREATE INDEX IF NOT EXISTS idx_pmf_module ON product_module_features (module_key);

CREATE TABLE IF NOT EXISTS subscription_tiers (
    id              SERIAL PRIMARY KEY,
    code            TEXT NOT NULL UNIQUE,
    name            TEXT NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    price_cents     INT NOT NULL DEFAULT 0,
    sort_order      INT NOT NULL DEFAULT 0,
    enabled         BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_st_code ON subscription_tiers (code);

CREATE TABLE IF NOT EXISTS tier_module_map (
    tier_code       TEXT NOT NULL REFERENCES subscription_tiers(code) ON DELETE CASCADE,
    module_key      TEXT NOT NULL REFERENCES product_modules(key) ON DELETE CASCADE,
    max_features    TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tier_code, module_key)
);

INSERT INTO product_modules (key, name, description, category, is_base, sort_order) VALUES
('routing',          '智能路由',       '自动选择最优模型和凭据',           'basic',        TRUE, 1),
('authentication',   '身份认证',       'API Key和JWT认证',                'basic',        TRUE, 2),
('audit',            '基础审计',       '请求日志和基础审计',               'basic',        TRUE, 3),
('prompt_injection', '提示词注入防护',  '检测和阻止提示词注入攻击',        'security',     FALSE, 10),
('output_compliance','输出合规检查',     '检测PII、毒性内容、偏见',         'security',     FALSE, 11),
('sensitive_data_dlp','敏感数据脱敏',   '输入输出敏感数据识别与脱敏',      'security',     FALSE, 12),
('session_audit',    '会话审计',        '深度会话行为审计与合规',          'security',     FALSE, 13),
('model_armor',      '模型防护',        '模型层面的安全防护',              'security',     FALSE, 14),
('session_management','会话生命周期管理','会话创建、保存、恢复、清理',     'session',      FALSE, 20),
('session_analytics', '会话分析仪表盘',  '会话数据统计与可视化',           'session',      FALSE, 21),
('session_inspector', '会话检查器',      '实时会话健康监控',               'session',      FALSE, 22),
('session_context',   '会话上下文',      '会话级上下文管理与注入',         'session',      FALSE, 23),
('handoff',           '会话交接',        '会话在设备/客户端间交接',        'session',      FALSE, 24),
('vibe_coding_core',  'VibeCoding核心',  'AI辅助编程核心引擎',             'vibe',         FALSE, 30),
('code_review',       '代码审查',        'AI驱动的代码审查',               'vibe',         FALSE, 31),
('code_explain',      '代码解释',        '代码逻辑解释与文档生成',         'vibe',         FALSE, 32),
('code_refactor',     '代码重构',        '智能代码重构建议',               'vibe',         FALSE, 33),
('vibe_coding_agent', '编程Agent',       '多步骤编程任务Agent',            'vibe',         FALSE, 34),
('compression',       '提示词压缩',      '自动压缩长提示词降低成本',       'advanced',     FALSE, 40),
('cache',             '语义缓存',        '相似请求复用缓存响应',           'advanced',     FALSE, 41),
('disguise',          '请求伪装',        '请求特征伪装绕过风控',           'advanced',     FALSE, 42),
('auto_routing',      '自动路由调优',    '基于反馈的自动路由优化',         'advanced',     FALSE, 43),
('goal',              '目标导向模式',     '目标驱动的任务执行',             'advanced',     FALSE, 44),
('feishu_bot',        '飞书机器人',      '飞书消息通知与审批',             'integration',  FALSE, 50),
('dingtalk_bot',      '钉钉机器人',      '钉钉消息通知与审批',             'integration',  FALSE, 51),
('wechat_bot',        '企业微信机器人',  '企业微信消息通知',               'integration',  FALSE, 52),
('mcp_gateway',       'MCP工具网关',     'Model Context Protocol工具集成', 'integration',  FALSE, 53)
ON CONFLICT (key) DO NOTHING;

INSERT INTO subscription_tiers (code, name, description, price_cents, sort_order) VALUES
('starter',    'Starter版',    '适合个人开发者',                     0,    1),
('pro',        'Pro版',        '适合小团队',                         29900, 2),
('enterprise', 'Enterprise版', '适合企业客户',                       99900, 3),
('custom',     '定制版',       '按需定价',                           0,    4)
ON CONFLICT (code) DO NOTHING;

INSERT INTO tier_module_map (tier_code, module_key) VALUES
('starter', 'routing'),
('starter', 'authentication'),
('starter', 'audit'),
('starter', 'session_management'),
('starter', 'session_analytics')
ON CONFLICT DO NOTHING;

INSERT INTO tier_module_map (tier_code, module_key) VALUES
('pro', 'prompt_injection'),
('pro', 'output_compliance'),
('pro', 'sensitive_data_dlp'),
('pro', 'session_audit'),
('pro', 'session_inspector'),
('pro', 'session_context'),
('pro', 'handoff'),
('pro', 'compression'),
('pro', 'cache'),
('pro', 'feishu_bot'),
('pro', 'dingtalk_bot'),
('pro', 'wechat_bot')
ON CONFLICT DO NOTHING;

INSERT INTO tier_module_map (tier_code, module_key) VALUES
('enterprise', 'model_armor'),
('enterprise', 'vibe_coding_core'),
('enterprise', 'code_review'),
('enterprise', 'code_explain'),
('enterprise', 'code_refactor'),
('enterprise', 'vibe_coding_agent'),
('enterprise', 'disguise'),
('enterprise', 'auto_routing'),
('enterprise', 'goal'),
('enterprise', 'mcp_gateway')
ON CONFLICT DO NOTHING;
