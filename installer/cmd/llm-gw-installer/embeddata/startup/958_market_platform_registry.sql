-- 958_market_platform_registry.sql
-- 市场平台注册表：区域/币种裁定 + A-5「我方平台排除」
-- 双币制（A-17）的唯一权威；同步器未登记平台→拒绝族 platform_unmapped_region

CREATE TABLE IF NOT EXISTS public.market_platform_registry (
    platform_id     text PRIMARY KEY,             -- 与 provider_catalog.id 一致
    platform_name   text NOT NULL DEFAULT '',     -- 显示名
    region          text NOT NULL,                -- 'overseas' | 'domestic'
    native_currency text NOT NULL,                -- 'USD' | 'CNY'（与 region 联动）
    is_ours         boolean NOT NULL DEFAULT false, -- A-5: C2 地板计算时排除我方平台
    enabled         boolean NOT NULL DEFAULT true,
    notes           text NOT NULL DEFAULT '',     -- 边缘案例登记（如 deepseek 双币页）
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT market_platform_registry_region_chk 
        CHECK (region IN ('overseas','domestic')),
    CONSTRAINT market_platform_registry_currency_chk 
        CHECK (native_currency IN ('USD','CNY')),
    CONSTRAINT market_platform_registry_pair_chk
        CHECK ((region='overseas' AND native_currency='USD')
            OR (region='domestic' AND native_currency='CNY'))
);

COMMENT ON TABLE public.market_platform_registry IS 
    '市场平台注册表：双币制区域/币种裁定权威（A-17）+ A-5 我方平台排除清单';
COMMENT ON COLUMN public.market_platform_registry.platform_id IS 
    '平台标识，与 provider_catalog.id 一致（不含 kind=local 的12个本地provider）';
COMMENT ON COLUMN public.market_platform_registry.region IS 
    '区域分类：overseas（海外）| domestic（国内），判定依据=标价/结算币种';
COMMENT ON COLUMN public.market_platform_registry.native_currency IS 
    '原生币种：USD（海外）| CNY（国内），与 region 联动约束';
COMMENT ON COLUMN public.market_platform_registry.is_ours IS 
    'A-5: 我方平台标记，C2 市场地板计算时排除（避免自己的价格污染市场参考）';
COMMENT ON COLUMN public.market_platform_registry.notes IS 
    '边缘案例登记：deepseek（国内厂商但有USD页）、evol/scnet/vapeur（待确认标价币种）等';

-- 种子数据（34个云provider，基于 provider_catalog 实际存在且 kind != 'local'）
-- 海外(USD, 14) + 国内(CNY, 20)
-- is_ours 全默认 false，待运营确认后手工 UPDATE

-- === 海外 USD (14) ===
INSERT INTO public.market_platform_registry 
    (platform_id, platform_name, region, native_currency, is_ours, enabled, notes)
VALUES
    ('openai', 'OpenAI', 'overseas', 'USD', false, true, ''),
    ('anthropic', 'Anthropic', 'overseas', 'USD', false, true, ''),
    ('google-gemini', 'Google Gemini', 'overseas', 'USD', false, true, ''),
    ('openrouter', 'OpenRouter', 'overseas', 'USD', false, true, '第三方聚合商'),
    ('together', 'Together AI', 'overseas', 'USD', false, true, ''),
    ('fireworks', 'Fireworks AI', 'overseas', 'USD', false, true, ''),
    ('xai', 'xAI', 'overseas', 'USD', false, true, ''),
    ('groq', 'Groq', 'overseas', 'USD', false, true, ''),
    ('perplexity', 'Perplexity', 'overseas', 'USD', false, true, ''),
    ('mistral', 'Mistral AI', 'overseas', 'USD', false, true, ''),
    ('cohere', 'Cohere', 'overseas', 'USD', false, true, ''),
    ('nvidia', 'NVIDIA NIM', 'overseas', 'USD', false, true, ''),
    ('azure-openai', 'Azure OpenAI', 'overseas', 'USD', false, true, ''),
    ('github-copilot', 'GitHub Copilot', 'overseas', 'USD', false, true, '')
ON CONFLICT (platform_id) DO NOTHING;

-- === 国内 CNY (20) ===
INSERT INTO public.market_platform_registry 
    (platform_id, platform_name, region, native_currency, is_ours, enabled, notes)
VALUES
    ('zhipu', '智谱AI', 'domestic', 'CNY', false, true, ''),
    ('minimax', 'MiniMax', 'domestic', 'CNY', false, true, ''),
    ('stepfun', '阶跃星辰', 'domestic', 'CNY', false, true, ''),
    ('moonshot', 'Moonshot / Kimi', 'domestic', 'CNY', false, true, ''),
    ('deepseek', 'DeepSeek', 'domestic', 'CNY', false, true, '边缘案例：国内厂商、官网另有USD页，以catalog为准记domestic/CNY'),
    ('sensenova', '商汤 SenseNova', 'domestic', 'CNY', false, true, ''),
    ('siliconflow', '硅基流动', 'domestic', 'CNY', false, true, '聚合平台'),
    ('doubao', '豆包（字节跳动）', 'domestic', 'CNY', false, true, ''),
    ('qwen', '阿里百炼（通义千问）', 'domestic', 'CNY', false, true, ''),
    ('baichuan', '百川 AI', 'domestic', 'CNY', false, true, ''),
    ('yi', '零一万物', 'domestic', 'CNY', false, true, ''),
    ('xiaomi', '小米 AI', 'domestic', 'CNY', false, true, ''),
    ('volcengine-coding', '火山方舟 Coding', 'domestic', 'CNY', false, true, ''),
    ('volcano-normal', 'Volcano Ark (OpenAI)', 'domestic', 'CNY', false, true, ''),
    ('volcano-tokenplan', 'Volcano Ark (TokenPlan)', 'domestic', 'CNY', false, true, ''),
    ('scnet', 'SCNet', 'domestic', 'CNY', false, true, '边缘案例：需核实标价币种'),
    ('vapeur', 'Vapeur AI', 'domestic', 'CNY', false, true, '边缘案例：需核实标价币种'),
    ('evol', 'Evol', 'domestic', 'CNY', false, true, '边缘案例：需核实标价币种')
ON CONFLICT (platform_id) DO NOTHING;

-- 索引（查询优化）
CREATE INDEX IF NOT EXISTS idx_market_platform_registry_region 
    ON public.market_platform_registry(region) WHERE enabled;
CREATE INDEX IF NOT EXISTS idx_market_platform_registry_is_ours 
    ON public.market_platform_registry(is_ours) WHERE enabled;
