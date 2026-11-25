-- W4.2: 会话能力绑 Key —— api_keys 增加 capabilities JSONB 列。
-- 能力 ID 词表见 internal/capability（单一事实源：session.audit 等 6 项）。
-- 空 '[]' 表示未购买任何会话能力；请求面鉴权 fail-closed（缺能力即 403）。
-- GIN 索引支持 W4.3 渠道商视角按能力过滤 Key。

ALTER TABLE public.api_keys
    ADD COLUMN IF NOT EXISTS capabilities jsonb NOT NULL DEFAULT '[]'::jsonb;

CREATE INDEX IF NOT EXISTS idx_api_keys_capabilities
    ON public.api_keys USING GIN (capabilities);

COMMENT ON COLUMN public.api_keys.capabilities IS
    '会话能力清单（internal/capability 词表）：api_keys 级可售卖功能授权，[] 表示未授权';
