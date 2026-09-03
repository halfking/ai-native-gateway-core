-- 363_featured_models_standard.sql
-- Phase: routing_policy.featured_models 扩展（grok-4.6 / kimi-k3/k2.6 / gemini-3.*）
-- Idempotent: 仅追加每个 canonical_name（per-name guard），已存在的 name 跳过。
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
--   - 不动既有默认项
--   - 仅追加 5 个新 ID（每个家族挑 1-2 个代表）：
--     grok-4.6              ← xAI 旗舰
--     kimi-k3               ← Moonshot 多模态旗舰
--     kimi-k2.6             ← Moonshot 视觉模型
--     gemini-3.5-flash      ← Google Gemini 3 系列代表
--     gemini-3-flash-preview ← Gemini 3 预览版代表
--   - per-name guard（NOT name = ANY(featured_models)）：每个 name 单独判断。
--     即便运维已经手动加过其中 2-3 个，仍会补齐缺失项；operator 主动删除的也会被重新加入，
--     这是 rollout 默认行为（运维可通过设置 featured_models_locked 进一步控制，但本次不引入）。
--
-- 审计修复（2026-08-20）：原 WHERE 改为 per-name 检查，兼容 3 个 seed 文件 line 934 已
-- 含部分 gemini-3 name 的情况；DROP 旧的单一 guard (NOT grok-4.6 = ANY(...))。

BEGIN;

-- 单条 UPDATE 不能在同一 SET 列表中既有标量子查询又有 NOW()。
-- 把更新拆成两步：先用数组表达式合并 featured_models，再用一条独立的 UPDATE
-- 戳 updated_at。
UPDATE routing_policy
SET featured_models = (
    SELECT ARRAY(
        SELECT DISTINCT unnest(
            featured_models ||
            ARRAY['grok-4.6', 'kimi-k3', 'kimi-k2.6',
                  'gemini-3.5-flash', 'gemini-3-flash-preview']
        )
    )
)
WHERE id = 1
  AND NOT (featured_models @> ARRAY['grok-4.6','kimi-k3','kimi-k2.6',
                                    'gemini-3.5-flash','gemini-3-flash-preview']::text[]);
-- @> "contains" 检查：5 个 name 全在才跳过；只要缺任意一个就重新追加。
-- 重复 name 由 SELECT DISTINCT 去重。

UPDATE routing_policy
SET updated_at = NOW()
WHERE id = 1
  AND NOT (featured_models @> ARRAY['grok-4.6','kimi-k3','kimi-k2.6',
                                    'gemini-3.5-flash','gemini-3-flash-preview']::text[]);

COMMIT;