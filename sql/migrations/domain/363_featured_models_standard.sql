-- 363_featured_models_standard.sql
-- Phase: routing_policy.featured_models 扩展（grok-4.6 / kimi-k3/k2.6 / gemini-3.*）
-- Idempotent: 仅在数组中尚无该 canonical_name 时追加；不删除既有项。
--
-- 背景：
--   routing_policy.featured_models (DEFAULT ARRAY['gpt-4o','gpt-4o-mini',
--   'claude-3-5-sonnet-20241022','claude-3-7-sonnet-20250219','gemini-2.0-flash',
--   'gemini-1.5-pro','deepseek-chat','qwen-plus']) 用于：
--     - 模型发现 (modelquality/discovery) 优先轮询
--     - dashboard 头条展示
--     - 自动路由的 warm cache 候选
--   加入 grok-4.6 / kimi-k3 / kimi-k2.6 / gemini-3.5-flash / gemini-3-flash-preview
--   让前台在凭据未接入时也能展示"计划接入"的模型。
--
-- 设计原则：
--   - 不动既有 8 个默认项
--   - 仅追加 5 个新 ID（每个家族挑 1-2 个代表）
--     grok-4.6              ← xAI 旗舰
--     kimi-k3               ← Moonshot 多模态旗舰
--     kimi-k2.6             ← Moonshot 视觉模型
--     gemini-3.5-flash      ← Google Gemini 3 系列代表
--     gemini-3-flash-preview ← Gemini 3 预览版代表

BEGIN;

UPDATE routing_policy
SET featured_models = (
    SELECT ARRAY(
        SELECT DISTINCT unnest(
            featured_models ||
            ARRAY['grok-4.6', 'kimi-k3', 'kimi-k2.6',
                  'gemini-3.5-flash', 'gemini-3-flash-preview']
        )
    ),
    updated_at = NOW()
)
WHERE id = 1
  AND NOT ('grok-4.6' = ANY(featured_models));

COMMIT;