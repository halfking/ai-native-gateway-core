-- W4.2: 会话能力绑 Key —— api_keys 增加 capabilities JSONB 列。
-- 能力 ID 词表见 internal/capability（单一事实源：session.audit 等 6 项）。
-- 空 '[]' 表示未购买任何会话能力。
--
-- ★★ 2026-11-26 12h 审计轮订正本文件头注释的**失实表述** ★★：
--   原注释写「请求面鉴权 fail-closed（缺能力即 403）」⇒ **该行为此刻不存在**：
--   `internal/capabilitygate`（裁决门）与 `KeyInfo.HasCapability` 的
--   **非测试调用方均为 0** ⇒ 本列目前是**记录字段**，尚未参与任何裁决。
--   ⇒ 事实登记：★ W4.3（把裁决门接进请求面鉴权）尚未落地 ★；
--     接线后才是 fail-closed（缺能力即 403）★★
--   （接线前说成已接线＝把「文件在仓里」说成「闸在生效」——同类失实已在
--     AGENTS.md L115 ③ 登记过一轮。）
--
-- GIN 索引支持 W4.3 渠道商视角按能力过滤 Key。

ALTER TABLE public.api_keys
    ADD COLUMN IF NOT EXISTS capabilities jsonb NOT NULL DEFAULT '[]'::jsonb;

CREATE INDEX IF NOT EXISTS idx_api_keys_capabilities
    ON public.api_keys USING GIN (capabilities);

COMMENT ON COLUMN public.api_keys.capabilities IS
    '会话能力清单（internal/capability 词表）：api_keys 级可售卖功能授权，[] 表示未授权；★ W4.3 裁决门接线前为记录字段，接线后缺能力即 403';
