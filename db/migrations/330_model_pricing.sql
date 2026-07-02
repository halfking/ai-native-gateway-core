-- Migration 330: Model Pricing Configuration
-- 创建模型价格配置表，支持动态定价和成本控制
-- 2026-07-02: 初始版本，支持输入/输出/缓存分离定价

-- ══════════════════════════════════════════════════════════════
-- 1. 创建 model_pricing 表
-- ══════════════════════════════════════════════════════════════

CREATE TABLE IF NOT EXISTS model_pricing (
    id SERIAL PRIMARY KEY,
    
    -- 模型标识
    model_canonical VARCHAR(64) NOT NULL UNIQUE,
    display_name VARCHAR(128) NOT NULL,
    
    -- 价格配置（以 credits 为单位，1M tokens）
    input_credits_per_1m BIGINT NOT NULL,
    output_credits_per_1m BIGINT NOT NULL,
    
    -- 缓存价格（Anthropic Prompt Caching 等特性）
    cache_write_credits_per_1m BIGINT,
    cache_read_credits_per_1m BIGINT,
    
    -- 供应商信息
    provider VARCHAR(32) NOT NULL,
    provider_model VARCHAR(64),
    
    -- 模型特性
    context_window INT NOT NULL DEFAULT 128000,
    max_output_tokens INT NOT NULL DEFAULT 4096,
    supports_streaming BOOLEAN NOT NULL DEFAULT true,
    supports_tools BOOLEAN NOT NULL DEFAULT true,
    supports_vision BOOLEAN NOT NULL DEFAULT false,
    supports_caching BOOLEAN NOT NULL DEFAULT false,
    
    -- 定价等级
    tier VARCHAR(16) NOT NULL CHECK (tier IN ('free', 'basic', 'standard', 'premium', 'enterprise')),
    
    -- 成本控制
    daily_free_quota_credits BIGINT DEFAULT 0,
    requires_plan VARCHAR(32)[],
    min_credits_per_request BIGINT DEFAULT 0,
    
    -- 状态
    active BOOLEAN NOT NULL DEFAULT true,
    deprecated BOOLEAN NOT NULL DEFAULT false,
    replacement_model VARCHAR(64),
    
    -- 审计字段
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    notes TEXT,
    
    -- 约束
    CONSTRAINT positive_input_price CHECK (input_credits_per_1m >= 0),
    CONSTRAINT positive_output_price CHECK (output_credits_per_1m >= 0),
    CONSTRAINT cache_write_gte_read CHECK (
        cache_write_credits_per_1m IS NULL 
        OR cache_read_credits_per_1m IS NULL 
        OR cache_write_credits_per_1m >= cache_read_credits_per_1m
    )
);

-- 索引
CREATE INDEX IF NOT EXISTS idx_model_pricing_provider ON model_pricing(provider);
CREATE INDEX IF NOT EXISTS idx_model_pricing_tier ON model_pricing(tier);
CREATE INDEX IF NOT EXISTS idx_model_pricing_active ON model_pricing(active) WHERE active = true;

-- 更新时间触发器
CREATE OR REPLACE FUNCTION update_model_pricing_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER model_pricing_updated_at
    BEFORE UPDATE ON model_pricing
    FOR EACH ROW
    EXECUTE FUNCTION update_model_pricing_updated_at();

-- ══════════════════════════════════════════════════════════════
-- 2. 插入初始价格配置
-- ══════════════════════════════════════════════════════════════

-- Anthropic Models
INSERT INTO model_pricing (
    model_canonical, display_name, provider, provider_model,
    input_credits_per_1m, output_credits_per_1m,
    cache_write_credits_per_1m, cache_read_credits_per_1m,
    tier, context_window, max_output_tokens,
    supports_caching, supports_vision, notes
) VALUES
-- Claude Sonnet 4 (2026-06)
('claude-sonnet-4-6', 'Claude Sonnet 4 (June 2026)', 'anthropic', 'claude-sonnet-4-20240620',
    30000, 150000, 37500, 3000,
    'premium', 200000, 8192,
    true, true, '最新版本，支持 extended thinking 和 prompt caching'),

('claude-sonnet-4-20250514', 'Claude Sonnet 4 (May 2025)', 'anthropic', 'claude-sonnet-4-20250514',
    30000, 150000, 37500, 3000,
    'premium', 200000, 8192,
    true, true, '稳定版本'),

-- Claude Opus 4
('claude-opus-4', 'Claude Opus 4', 'anthropic', 'claude-opus-4-20250514',
    150000, 750000, 187500, 15000,
    'enterprise', 200000, 16384,
    true, true, '最强推理能力'),

('claude-opus-4-8', 'Claude Opus 4 (Aug)', 'anthropic', 'claude-opus-4-20250808',
    150000, 750000, 187500, 15000,
    'enterprise', 200000, 16384,
    true, true, '8月优化版本'),

-- Claude Sonnet 3.5
('claude-sonnet-3-5', 'Claude 3.5 Sonnet', 'anthropic', 'claude-3-5-sonnet-20241022',
    30000, 150000, 37500, 3000,
    'standard', 200000, 8192,
    true, true, '成熟稳定版本'),

-- Claude Haiku
('claude-haiku-3-5', 'Claude 3.5 Haiku', 'anthropic', 'claude-3-5-haiku-20241022',
    8000, 40000, 10000, 800,
    'basic', 200000, 8192,
    true, false, '快速响应，成本优化'),

-- OpenAI Models
('gpt-4o', 'GPT-4o', 'openai', 'gpt-4o-2024-11-20',
    25000, 100000, NULL, NULL,
    'premium', 128000, 16384,
    false, true, 'OpenAI 旗舰多模态模型'),

('gpt-4o-mini', 'GPT-4o Mini', 'openai', 'gpt-4o-mini-2024-07-18',
    1500, 6000, NULL, NULL,
    'basic', 128000, 16384,
    false, true, '轻量级高性价比模型'),

('gpt-4-turbo', 'GPT-4 Turbo', 'openai', 'gpt-4-turbo-2024-04-09',
    100000, 300000, NULL, NULL,
    'premium', 128000, 4096,
    false, true, 'GPT-4 增强版'),

('gpt-3.5-turbo', 'GPT-3.5 Turbo', 'openai', 'gpt-3.5-turbo-0125',
    5000, 15000, NULL, NULL,
    'standard', 16385, 4096,
    false, false, '经典版本'),

-- O1 系列（推理模型）
('o1', 'O1', 'openai', 'o1-2024-12-17',
    150000, 600000, NULL, NULL,
    'enterprise', 200000, 100000,
    false, false, 'OpenAI 推理模型'),

('o1-mini', 'O1 Mini', 'openai', 'o1-mini-2024-09-12',
    30000, 120000, NULL, NULL,
    'premium', 128000, 65536,
    false, false, '轻量级推理模型')

ON CONFLICT (model_canonical) DO UPDATE SET
    display_name = EXCLUDED.display_name,
    input_credits_per_1m = EXCLUDED.input_credits_per_1m,
    output_credits_per_1m = EXCLUDED.output_credits_per_1m,
    cache_write_credits_per_1m = EXCLUDED.cache_write_credits_per_1m,
    cache_read_credits_per_1m = EXCLUDED.cache_read_credits_per_1m,
    context_window = EXCLUDED.context_window,
    max_output_tokens = EXCLUDED.max_output_tokens,
    updated_at = now();

-- ══════════════════════════════════════════════════════════════
-- 3. 创建价格历史表（审计用）
-- ══════════════════════════════════════════════════════════════

CREATE TABLE IF NOT EXISTS model_pricing_history (
    id SERIAL PRIMARY KEY,
    model_canonical VARCHAR(64) NOT NULL,
    
    -- 变更前的价格
    old_input_credits_per_1m BIGINT,
    old_output_credits_per_1m BIGINT,
    
    -- 变更后的价格
    new_input_credits_per_1m BIGINT,
    new_output_credits_per_1m BIGINT,
    
    -- 变更信息
    changed_by VARCHAR(128),
    change_reason TEXT,
    effective_date TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_pricing_history_model ON model_pricing_history(model_canonical);
CREATE INDEX IF NOT EXISTS idx_pricing_history_date ON model_pricing_history(effective_date DESC);

-- ══════════════════════════════════════════════════════════════
-- 4. 创建价格变更审计触发器
-- ══════════════════════════════════════════════════════════════

CREATE OR REPLACE FUNCTION log_model_pricing_change()
RETURNS TRIGGER AS $$
BEGIN
    -- 仅在价格变更时记录
    IF OLD.input_credits_per_1m != NEW.input_credits_per_1m 
        OR OLD.output_credits_per_1m != NEW.output_credits_per_1m THEN
        
        INSERT INTO model_pricing_history (
            model_canonical,
            old_input_credits_per_1m,
            old_output_credits_per_1m,
            new_input_credits_per_1m,
            new_output_credits_per_1m,
            change_reason
        ) VALUES (
            NEW.model_canonical,
            OLD.input_credits_per_1m,
            OLD.output_credits_per_1m,
            NEW.input_credits_per_1m,
            NEW.output_credits_per_1m,
            'Price update via migration or admin API'
        );
    END IF;
    
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER model_pricing_change_log
    AFTER UPDATE ON model_pricing
    FOR EACH ROW
    EXECUTE FUNCTION log_model_pricing_change();

-- ══════════════════════════════════════════════════════════════
-- 5. 创建价格查询辅助函数
-- ══════════════════════════════════════════════════════════════

-- 计算请求成本
CREATE OR REPLACE FUNCTION calculate_request_cost(
    p_model_canonical VARCHAR,
    p_input_tokens INT,
    p_output_tokens INT,
    p_cache_write_tokens INT DEFAULT 0,
    p_cache_read_tokens INT DEFAULT 0
) RETURNS BIGINT AS $$
DECLARE
    v_input_price BIGINT;
    v_output_price BIGINT;
    v_cache_write_price BIGINT;
    v_cache_read_price BIGINT;
    v_total_credits BIGINT;
BEGIN
    -- 获取价格配置
    SELECT 
        input_credits_per_1m,
        output_credits_per_1m,
        COALESCE(cache_write_credits_per_1m, 0),
        COALESCE(cache_read_credits_per_1m, 0)
    INTO 
        v_input_price,
        v_output_price,
        v_cache_write_price,
        v_cache_read_price
    FROM model_pricing
    WHERE model_canonical = p_model_canonical
        AND active = true;
    
    -- 如果未找到价格配置，返回 NULL
    IF NOT FOUND THEN
        RETURN NULL;
    END IF;
    
    -- 计算总成本（credits）
    v_total_credits := 
        (p_input_tokens * v_input_price / 1000000) +
        (p_output_tokens * v_output_price / 1000000) +
        (p_cache_write_tokens * v_cache_write_price / 1000000) +
        (p_cache_read_tokens * v_cache_read_price / 1000000);
    
    RETURN v_total_credits;
END;
$$ LANGUAGE plpgsql STABLE;

-- 获取模型价格摘要
CREATE OR REPLACE FUNCTION get_model_pricing_summary(p_model_canonical VARCHAR)
RETURNS TABLE (
    model VARCHAR,
    display_name VARCHAR,
    input_price_cny NUMERIC,
    output_price_cny NUMERIC,
    tier VARCHAR,
    active BOOLEAN
) AS $$
BEGIN
    RETURN QUERY
    SELECT 
        mp.model_canonical,
        mp.display_name,
        ROUND(mp.input_credits_per_1m * ms.cents_per_credit / 100.0, 2) as input_price_cny,
        ROUND(mp.output_credits_per_1m * ms.cents_per_credit / 100.0, 2) as output_price_cny,
        mp.tier,
        mp.active
    FROM model_pricing mp
    CROSS JOIN maas_settings ms
    WHERE mp.model_canonical = p_model_canonical;
END;
$$ LANGUAGE plpgsql STABLE;

-- ══════════════════════════════════════════════════════════════
-- 6. 创建价格对比视图
-- ══════════════════════════════════════════════════════════════

CREATE OR REPLACE VIEW v_model_pricing_comparison AS
SELECT 
    mp.model_canonical,
    mp.display_name,
    mp.provider,
    mp.tier,
    mp.input_credits_per_1m,
    mp.output_credits_per_1m,
    -- 转换为人民币价格（per 1M tokens）
    ROUND(mp.input_credits_per_1m * ms.cents_per_credit / 100.0, 2) as input_price_cny,
    ROUND(mp.output_credits_per_1m * ms.cents_per_credit / 100.0, 2) as output_price_cny,
    -- 缓存价格
    CASE 
        WHEN mp.supports_caching THEN 
            ROUND(mp.cache_read_credits_per_1m * ms.cents_per_credit / 100.0, 2)
        ELSE NULL 
    END as cache_read_price_cny,
    -- 性价比指标（output credits per CNY）
    CASE 
        WHEN mp.output_credits_per_1m > 0 THEN
            ROUND(1000000.0 / (mp.output_credits_per_1m * ms.cents_per_credit / 100.0), 0)
        ELSE NULL
    END as output_tokens_per_cny,
    mp.context_window,
    mp.supports_tools,
    mp.supports_vision,
    mp.supports_caching,
    mp.active
FROM model_pricing mp
CROSS JOIN maas_settings ms
WHERE mp.active = true
ORDER BY mp.provider, mp.tier DESC, mp.output_credits_per_1m;

-- ══════════════════════════════════════════════════════════════
-- 7. 注释
-- ══════════════════════════════════════════════════════════════

COMMENT ON TABLE model_pricing IS '模型价格配置表，支持输入/输出/缓存分离定价';
COMMENT ON COLUMN model_pricing.model_canonical IS '模型规范化名称，与 models 表关联';
COMMENT ON COLUMN model_pricing.input_credits_per_1m IS '输入价格：每100万token消耗的credits';
COMMENT ON COLUMN model_pricing.output_credits_per_1m IS '输出价格：每100万token消耗的credits';
COMMENT ON COLUMN model_pricing.cache_write_credits_per_1m IS '缓存写入价格（Anthropic Prompt Caching）';
COMMENT ON COLUMN model_pricing.cache_read_credits_per_1m IS '缓存读取价格（通常为写入价格的1/10）';
COMMENT ON COLUMN model_pricing.tier IS '定价等级：free, basic, standard, premium, enterprise';
COMMENT ON COLUMN model_pricing.requires_plan IS '需要的订阅计划代码数组';
COMMENT ON COLUMN model_pricing.daily_free_quota_credits IS '每日免费额度（credits）';

COMMENT ON TABLE model_pricing_history IS '模型价格变更历史，用于审计和趋势分析';
COMMENT ON FUNCTION calculate_request_cost IS '计算单次请求的 credits 成本';
COMMENT ON FUNCTION get_model_pricing_summary IS '获取模型价格摘要（含人民币价格）';
COMMENT ON VIEW v_model_pricing_comparison IS '模型价格对比视图，含人民币价格和性价比指标';
