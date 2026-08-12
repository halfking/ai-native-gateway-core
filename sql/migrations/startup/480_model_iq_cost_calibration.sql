-- 480_model_iq_cost_calibration.sql
-- Phase: Auto 智能路由 — 模型智商（complexity_ceiling）与成本（cost_tier）校准
--
-- 问题：models_canonical 表中所有 text 模型的 cost_tier='unknown'、
-- complexity_ceiling=NULL、min_complexity=NULL。autoroute 评分无法区分
-- 免费模型（glm-4-flash）和旗舰模型（claude-fable-5），导致：
--   1. cost 维度评分全部相同（无价格信号）
--   2. complexity 过滤永远通过（ceiling 为空 → ComplexityMatch 返回 true）
--   3. is_free 派生判定失效（cost_tier='unknown' 不等于 'free'）
--
-- 本 migration 基于原厂公开定价为所有 text modality 模型设置 cost_tier 和
-- complexity_ceiling。原则：
--   - 同模态贵的智商更高（cost_tier 与 complexity_ceiling 正相关）
--   - premium 旗舰模型设 min_complexity='medium'（防止大材小用）
--   - 专精模型（reasoning/code）额外标注 complexity_ceiling 提升到对应档位
--
-- 原厂定价参考（2025-2026 公开定价）：
--   Anthropic:  opus/sonnet $15-75/1M → premium/high
--   OpenAI:     gpt-5.2-pro $10+/1M → premium; gpt-5.2-chat → high
--   智谱 GLM:   flash=免费; 4.5/4.7 ¥0.05/1k; 5.x ¥0.1+/1k → free/medium/high/premium
--   DeepSeek:   v3 ¥0.001/1k; r1 ¥0.004/1k; v4-pro 更高 → low/medium/high
--   火山方舟:   lite ¥0.0003/1k; pro ¥0.001/1k; seed-2-pro ¥0.05+/1k → low/medium/high
--   MiniMax:    m2 便宜; m3 旗舰 → low/medium/high
--   Moonshot:   kimi-k2 旗舰; kimi-k2-thinking 推理 → medium/high
--   阿里 Qwen:  小模型便宜; 72b 中等 → low/medium
--
-- 幂等：纯 UPDATE，可重复执行。
-- Rollback:
--   UPDATE models_canonical SET cost_tier='unknown', complexity_ceiling=NULL, min_complexity=NULL
--   WHERE cost_tier IN ('free','low','medium','high','premium') AND modality='text';
--   DELETE FROM task_default_routing WHERE reason LIKE 'v6.1 default%';

\set ON_ERROR_STOP on
BEGIN;

-- ═══════════════════════════════════════════════════════════════
-- Part 1: cost_tier + complexity_ceiling 校准
-- ═══════════════════════════════════════════════════════════════

-- ── premium（旗舰：$15+/1M 或 ¥0.5+/1k），complexity_ceiling=frontier，min=medium ──
UPDATE models_canonical
SET cost_tier = 'premium',
    complexity_ceiling = 'frontier',
    min_complexity = 'medium',
    updated_at = now()
WHERE modality = 'text'
  AND canonical_name IN (
    'claude-fable-5', 'claude-opus-5',
    'gpt-5.2-pro',
    'glm-5', 'glm-5.2'
  );

-- ── high（高端：¥0.05-0.5/1k），complexity_ceiling=hard ──
UPDATE models_canonical
SET cost_tier = 'high',
    complexity_ceiling = 'hard',
    min_complexity = NULL,
    updated_at = now()
WHERE modality = 'text'
  AND canonical_name IN (
    -- Anthropic / OpenAI 高端
    'claude-sonnet-5',
    'gpt-5.2-chat-latest', 'gpt-5.3-codex-spark',
    -- 智谱高端
    'glm-5.1', 'glm-5-turbo',
    'glm-4.6',
    -- MiniMax 旗舰
    'minimax-m3',
    -- 火山方舟旗舰
    'doubao-seed-2-0-pro', 'doubao-seed-2-1-pro',
    -- DeepSeek 高端
    'deepseek-v4-pro',
    -- Moonshot 推理
    'kimi-k2-thinking',
    -- 火山方舟推理
    'doubao-1-5-thinking-pro'
  );

-- ── medium（中端：¥0.002-0.05/1k），complexity_ceiling=medium ──
UPDATE models_canonical
SET cost_tier = 'medium',
    complexity_ceiling = 'medium',
    min_complexity = NULL,
    updated_at = now()
WHERE modality = 'text'
  AND canonical_name IN (
    -- DeepSeek 中端（通用 + 推理）
    'deepseek-v3', 'deepseek-v3-1', 'deepseek-v3-2', 'deepseek-v3-1-terminus',
    'deepseek-v4-flash', 'deepseek-v4-flash-ga',
    'deepseek-r1', 'deepseek-r1-distill-qwen-32b',
    -- 火山方舟中端
    'doubao-1-5-pro-256k', 'doubao-1-5-pro-32k', 'doubao-1-5-pro-32k-character',
    'doubao-pro-128k', 'doubao-pro-256k',
    'doubao-pro-32k', 'doubao-pro-32k-browsing', 'doubao-pro-32k-character',
    'doubao-pro-32k-functioncall', 'doubao-pro-32k-functioncall-preview',
    'doubao-pro-4k', 'doubao-pro-4k-browsing', 'doubao-pro-4k-character',
    'doubao-pro-4k-functioncall',
    'doubao-1-5-lite-32k', 'doubao-1-5-thinking-pro-m',
    'doubao-seed-1-6', 'doubao-seed-1-8',
    'doubao-seed-2-0-mini', 'doubao-seed-2-1-turbo',
    'doubao-seed-character', 'doubao-seed-translation',
    'doubao-seaweed', 'doubao-smart-router',
    'doubao-seed-code-preview', 'doubao-seed-2-0-code-preview',
    -- MiniMax 中端
    'minimax-m2.5', 'minimax-m2.5-highspeed',
    'minimax-m2.7', 'minimax-m2.7-highspeed',
    'minimax-text-01',
    -- Moonshot
    'kimi-k2',
    -- Qwen 中端
    'qwen3-32b', 'qwen2-5-72b',
    -- 智谱中端
    'glm-4.5', 'glm-4.7',
    -- 商汤中端
    'sensechat-5', 'sensechat-5-thinking'
  );

-- ── low（低端：<¥0.002/1k），complexity_ceiling=easy ──
UPDATE models_canonical
SET cost_tier = 'low',
    complexity_ceiling = 'easy',
    min_complexity = NULL,
    updated_at = now()
WHERE modality = 'text'
  AND canonical_name IN (
    -- 火山方舟 lite 系列（最便宜）
    'doubao-lite-128k', 'doubao-lite-32k', 'doubao-lite-32k-character',
    'doubao-lite-4k', 'doubao-lite-4k-character', 'doubao-lite-4k-pretrain-character',
    'doubao-seed-1-6-flash', 'doubao-seed-1-6-lite',
    'doubao-seed-2-0-lite', 'doubao-seed-evolving',
    -- MiniMax 低端
    'minimax-m2', 'minimax-m2.1', 'minimax-m2.1-highspeed',
    -- Qwen 小模型
    'qwen3-0-6b', 'qwen3-8b', 'qwen3-14b',
    -- DeepSeek 蒸馏小模型
    'deepseek-r1-distill-qwen-7b',
    -- 其他小模型
    'mistral-7b-instruct-v0.2',
    'abab5.5-chat', 'abab6.5s-chat',
    'sensechat-turbo',
    'sensenova-6.7-flash-lite', 'sensenova-6.8-flash-lite', 'sensenova-u1-fast'
  );

-- ── free（原厂免费层），complexity_ceiling=easy ──
UPDATE models_canonical
SET cost_tier = 'free',
    complexity_ceiling = 'easy',
    min_complexity = NULL,
    updated_at = now()
WHERE modality = 'text'
  AND canonical_name IN (
    -- 智谱免费层（bigmodel.cn 官方免费）
    'glm-4-flash', 'glm-4.5-flash', 'glm-4.7-flash',
    'glm-z1-flash',
    'glm-4', 'glm-4-air', 'glm-4-9b-chat',
    'glm-4.5-air',
    'glm-4-7', 'glm-4-5-air'
  );

-- ═══════════════════════════════════════════════════════════════
-- Part 1b: 推理/代码专精模型的 complexity_ceiling 上调
-- 这些模型虽然 cost_tier 是 medium，但推理/代码能力强，
-- complexity_ceiling 应该比通用模型更高。
-- ═══════════════════════════════════════════════════════════════

-- deepseek-r1 是推理模型，complexity_ceiling 上调到 hard（但不改 cost_tier）
UPDATE models_canonical
SET complexity_ceiling = 'hard', updated_at = now()
WHERE canonical_name = 'deepseek-r1' AND modality = 'text';

-- deepseek-r1-distill-qwen-32b 蒸馏推理模型，上调到 hard
UPDATE models_canonical
SET complexity_ceiling = 'hard', updated_at = now()
WHERE canonical_name = 'deepseek-r1-distill-qwen-32b' AND modality = 'text';

-- doubao-1-5-thinking-pro-m 是推理模型，上调到 hard
UPDATE models_canonical
SET complexity_ceiling = 'hard', updated_at = now()
WHERE canonical_name = 'doubao-1-5-thinking-pro-m' AND modality = 'text';

-- glm-z1-flash 是推理模型（虽然是 free），complexity_ceiling 上调到 medium
UPDATE models_canonical
SET complexity_ceiling = 'medium', updated_at = now()
WHERE canonical_name = 'glm-z1-flash' AND modality = 'text';

-- gpt-5.3-codex-spark 是代码专精，complexity_ceiling 确认 hard
-- （已在 high 组设置）

-- ═══════════════════════════════════════════════════════════════
-- Part 2: task_default_routing 清理与重建
-- 477 migration 引用了大量幽灵模型（gemini-2.0-flash-exp, deepseek-chat,
-- o5-preview, codestral 等），这些在 154 的 models_canonical 中不存在。
-- 本节删除幽灵行并重新写入使用实际存在模型的路由。
-- ═══════════════════════════════════════════════════════════════

-- 删除 477 写入的 v6 default 行（含幽灵模型）
DELETE FROM task_default_routing WHERE reason LIKE 'v6 default%';

-- V6.1 默认路由矩阵：11 任务 × 3 profile × 3 tier
-- 所有 canonical_model 均为 154 实际 routable 模型
INSERT INTO task_default_routing (task_type, profile, tier, canonical_model, priority, reason)
VALUES
    -- ── chat（日常对话，速度优先） ──────────────────────────────
    ('chat', 'smart',       'primary',   'deepseek-v4-flash',     100, 'v6.1 default'),
    ('chat', 'smart',       'secondary', 'glm-4.5-flash',         200, 'v6.1 default'),
    ('chat', 'smart',       'fallback',  'kimi-k2',               300, 'v6.1 default'),
    ('chat', 'speed_first', 'primary',   'glm-4.5-flash',         100, 'v6.1 default'),
    ('chat', 'speed_first', 'secondary', 'deepseek-v4-flash',     200, 'v6.1 default'),
    ('chat', 'speed_first', 'fallback',  'minimax-m2.7',          300, 'v6.1 default'),
    ('chat', 'cost_first',  'primary',   'glm-4.5-flash',         100, 'v6.1 default'),
    ('chat', 'cost_first',  'secondary', 'deepseek-v4-flash',     200, 'v6.1 default'),
    ('chat', 'cost_first',  'fallback',  'kimi-k2',               300, 'v6.1 default'),

    -- ── reasoning（逻辑推理，需要高智商） ──────────────────────
    ('reasoning', 'smart',       'primary',   'deepseek-r1',           100, 'v6.1 default'),
    ('reasoning', 'smart',       'secondary', 'kimi-k2-thinking',      200, 'v6.1 default'),
    ('reasoning', 'smart',       'fallback',  'deepseek-v4-pro',       300, 'v6.1 default'),
    ('reasoning', 'speed_first', 'primary',   'deepseek-r1-distill-qwen-32b', 100, 'v6.1 default'),
    ('reasoning', 'speed_first', 'secondary', 'deepseek-r1',           200, 'v6.1 default'),
    ('reasoning', 'speed_first', 'fallback',  'deepseek-v4-flash',     300, 'v6.1 default'),
    ('reasoning', 'cost_first',  'primary',   'deepseek-r1-distill-qwen-32b', 100, 'v6.1 default'),
    ('reasoning', 'cost_first',  'secondary', 'deepseek-v4-flash',     200, 'v6.1 default'),
    ('reasoning', 'cost_first',  'fallback',  'kimi-k2',               300, 'v6.1 default'),

    -- ── code（代码生成与审查） ─────────────────────────────────
    ('code', 'smart',       'primary',   'deepseek-v4-pro',       100, 'v6.1 default'),
    ('code', 'smart',       'secondary', 'deepseek-v4-flash',     200, 'v6.1 default'),
    ('code', 'smart',       'fallback',  'kimi-k2',               300, 'v6.1 default'),
    ('code', 'speed_first', 'primary',   'deepseek-v4-flash',     100, 'v6.1 default'),
    ('code', 'speed_first', 'secondary', 'qwen3-32b',             200, 'v6.1 default'),
    ('code', 'speed_first', 'fallback',  'glm-4.5-flash',         300, 'v6.1 default'),
    ('code', 'cost_first',  'primary',   'qwen3-32b',             100, 'v6.1 default'),
    ('code', 'cost_first',  'secondary', 'deepseek-v4-flash',     200, 'v6.1 default'),
    ('code', 'cost_first',  'fallback',  'glm-4.5-flash',         300, 'v6.1 default'),

    -- ── agent（多步骤任务规划） ────────────────────────────────
    ('agent', 'smart',       'primary',   'deepseek-v4-pro',       100, 'v6.1 default'),
    ('agent', 'smart',       'secondary', 'kimi-k2',               200, 'v6.1 default'),
    ('agent', 'smart',       'fallback',  'deepseek-r1',           300, 'v6.1 default'),
    ('agent', 'speed_first', 'primary',   'deepseek-v4-flash',     100, 'v6.1 default'),
    ('agent', 'speed_first', 'secondary', 'kimi-k2',               200, 'v6.1 default'),
    ('agent', 'speed_first', 'fallback',  'minimax-m2.7',          300, 'v6.1 default'),
    ('agent', 'cost_first',  'primary',   'deepseek-v4-flash',     100, 'v6.1 default'),
    ('agent', 'cost_first',  'secondary', 'kimi-k2',               200, 'v6.1 default'),
    ('agent', 'cost_first',  'fallback',  'glm-4.5-flash',         300, 'v6.1 default'),

    -- ── creative（创意写作） ───────────────────────────────────
    ('creative', 'smart',       'primary',   'glm-5.2',           100, 'v6.1 default'),
    ('creative', 'smart',       'secondary', 'minimax-m3',         200, 'v6.1 default'),
    ('creative', 'smart',       'fallback',  'kimi-k2',           300, 'v6.1 default'),
    ('creative', 'speed_first', 'primary',   'minimax-m3',         100, 'v6.1 default'),
    ('creative', 'speed_first', 'secondary', 'glm-4.5-flash',     200, 'v6.1 default'),
    ('creative', 'speed_first', 'fallback',  'deepseek-v4-flash', 300, 'v6.1 default'),
    ('creative', 'cost_first',  'primary',   'glm-4.5-flash',     100, 'v6.1 default'),
    ('creative', 'cost_first',  'secondary', 'minimax-m2.7',      200, 'v6.1 default'),
    ('creative', 'cost_first',  'fallback',  'deepseek-v4-flash', 300, 'v6.1 default'),

    -- ── long_context（长上下文处理） ───────────────────────────
    ('long_context', 'smart',       'primary',   'kimi-k2',           100, 'v6.1 default'),
    ('long_context', 'smart',       'secondary', 'deepseek-v4-pro',   200, 'v6.1 default'),
    ('long_context', 'smart',       'fallback',  'minimax-text-01',   300, 'v6.1 default'),
    ('long_context', 'speed_first', 'primary',   'minimax-text-01',   100, 'v6.1 default'),
    ('long_context', 'speed_first', 'secondary', 'kimi-k2',           200, 'v6.1 default'),
    ('long_context', 'speed_first', 'fallback',  'deepseek-v4-flash', 300, 'v6.1 default'),
    ('long_context', 'cost_first',  'primary',   'deepseek-v4-flash', 100, 'v6.1 default'),
    ('long_context', 'cost_first',  'secondary', 'kimi-k2',           200, 'v6.1 default'),
    ('long_context', 'cost_first',  'fallback',  'minimax-text-01',   300, 'v6.1 default'),

    -- ── vision（视觉理解，需要多模态模型） ─────────────────────
    ('vision', 'smart',       'primary',   'kimi-k2',           100, 'v6.1 default'),
    ('vision', 'smart',       'secondary', 'deepseek-v4-flash', 200, 'v6.1 default'),
    ('vision', 'smart',       'fallback',  'glm-4.5-flash',     300, 'v6.1 default'),
    ('vision', 'speed_first', 'primary',   'glm-4.5-flash',     100, 'v6.1 default'),
    ('vision', 'speed_first', 'secondary', 'deepseek-v4-flash', 200, 'v6.1 default'),
    ('vision', 'speed_first', 'fallback',  'kimi-k2',           300, 'v6.1 default'),
    ('vision', 'cost_first',  'primary',   'glm-4.5-flash',     100, 'v6.1 default'),
    ('vision', 'cost_first',  'secondary', 'deepseek-v4-flash', 200, 'v6.1 default'),
    ('vision', 'cost_first',  'fallback',  'kimi-k2',           300, 'v6.1 default'),

    -- ── function_call（函数调用/工具使用） ─────────────────────
    ('function_call', 'smart',       'primary',   'deepseek-v4-pro',   100, 'v6.1 default'),
    ('function_call', 'smart',       'secondary', 'kimi-k2',           200, 'v6.1 default'),
    ('function_call', 'smart',       'fallback',  'deepseek-v4-flash', 300, 'v6.1 default'),
    ('function_call', 'speed_first', 'primary',   'deepseek-v4-flash', 100, 'v6.1 default'),
    ('function_call', 'speed_first', 'secondary', 'qwen3-32b',         200, 'v6.1 default'),
    ('function_call', 'speed_first', 'fallback',  'glm-4.5-flash',     300, 'v6.1 default'),
    ('function_call', 'cost_first',  'primary',   'qwen3-32b',         100, 'v6.1 default'),
    ('function_call', 'cost_first',  'secondary', 'deepseek-v4-flash', 200, 'v6.1 default'),
    ('function_call', 'cost_first',  'fallback',  'glm-4.5-flash',     300, 'v6.1 default'),

    -- ── code_audit（代码审计，需要高智商） ─────────────────────
    ('code_audit', 'smart',       'primary',   'deepseek-v4-pro',   100, 'v6.1 default'),
    ('code_audit', 'smart',       'secondary', 'deepseek-r1',       200, 'v6.1 default'),
    ('code_audit', 'smart',       'fallback',  'kimi-k2-thinking',  300, 'v6.1 default'),
    ('code_audit', 'speed_first', 'primary',   'deepseek-v4-flash', 100, 'v6.1 default'),
    ('code_audit', 'speed_first', 'secondary', 'deepseek-r1-distill-qwen-32b', 200, 'v6.1 default'),
    ('code_audit', 'speed_first', 'fallback',  'qwen3-32b',         300, 'v6.1 default'),
    ('code_audit', 'cost_first',  'primary',   'qwen3-32b',         100, 'v6.1 default'),
    ('code_audit', 'cost_first',  'secondary', 'deepseek-v4-flash', 200, 'v6.1 default'),
    ('code_audit', 'cost_first',  'fallback',  'glm-4.5-flash',     300, 'v6.1 default'),

    -- ── intent_classification（意图分类，轻量任务） ────────────
    ('intent_classification', 'smart',       'primary',   'deepseek-v4-flash', 100, 'v6.1 default'),
    ('intent_classification', 'smart',       'secondary', 'glm-4.5-flash',     200, 'v6.1 default'),
    ('intent_classification', 'smart',       'fallback',  'minimax-m2.7',      300, 'v6.1 default'),
    ('intent_classification', 'speed_first', 'primary',   'glm-4.5-flash',     100, 'v6.1 default'),
    ('intent_classification', 'speed_first', 'secondary', 'deepseek-v4-flash', 200, 'v6.1 default'),
    ('intent_classification', 'speed_first', 'fallback',  'minimax-m2.5',      300, 'v6.1 default'),
    ('intent_classification', 'cost_first',  'primary',   'glm-4.5-flash',     100, 'v6.1 default'),
    ('intent_classification', 'cost_first',  'secondary', 'deepseek-v4-flash', 200, 'v6.1 default'),
    ('intent_classification', 'cost_first',  'fallback',  'qwen3-14b',         300, 'v6.1 default'),

    -- ── planning（任务规划/方案设计，需要高智商） ──────────────
    ('planning', 'smart',       'primary',   'deepseek-r1',           100, 'v6.1 default'),
    ('planning', 'smart',       'secondary', 'deepseek-v4-pro',       200, 'v6.1 default'),
    ('planning', 'smart',       'fallback',  'kimi-k2',               300, 'v6.1 default'),
    ('planning', 'speed_first', 'primary',   'deepseek-v4-flash',     100, 'v6.1 default'),
    ('planning', 'speed_first', 'secondary', 'kimi-k2',               200, 'v6.1 default'),
    ('planning', 'speed_first', 'fallback',  'deepseek-r1-distill-qwen-32b', 300, 'v6.1 default'),
    ('planning', 'cost_first',  'primary',   'deepseek-v4-flash',     100, 'v6.1 default'),
    ('planning', 'cost_first',  'secondary', 'kimi-k2',               200, 'v6.1 default'),
    ('planning', 'cost_first',  'fallback',  'qwen3-32b',             300, 'v6.1 default')
ON CONFLICT DO NOTHING;

-- ═══════════════════════════════════════════════════════════════
-- Part 3: 修正 model_offers 中明显不合理的 0 价 flagship
-- glm-5.2 (flagship) 价格为 0 显然不合理——原厂定价 ¥0.1+/1k。
-- 设为合理估值（供应商价格可能与原厂不同，但不应为 0）。
-- ═══════════════════════════════════════════════════════════════

UPDATE model_offers
SET unit_price_in_per_1m = 0.1,
    unit_price_out_per_1m = 0.1,
    updated_at = now()
WHERE unit_price_in_per_1m = 0
  AND unit_price_out_per_1m = 0
  AND EXISTS (
    SELECT 1 FROM provider_models pm
    JOIN models_canonical mc ON mc.id = pm.canonical_id
    WHERE (model_offers.raw_model_name = pm.raw_model_name
           OR model_offers.outbound_model_name = pm.raw_model_name)
      AND mc.cost_tier IN ('premium', 'high')
      AND mc.canonical_name IN ('glm-5.2', 'glm-5', 'glm-5.1', 'glm-4.7', 'glm-4.5')
  );

-- ═══════════════════════════════════════════════════════════════
-- 验证：确认校准后的分布
-- ═══════════════════════════════════════════════════════════════
DO $$
DECLARE
    total_calibrated int;
    still_unknown int;
BEGIN
    SELECT COUNT(*) INTO total_calibrated
    FROM models_canonical
    WHERE modality = 'text' AND cost_tier != 'unknown';

    SELECT COUNT(*) INTO still_unknown
    FROM models_canonical
    WHERE modality = 'text' AND cost_tier = 'unknown';

    RAISE NOTICE 'model_iq_cost_calibration: % text models calibrated, % still unknown',
        total_calibrated, still_unknown;
END $$;

COMMIT;
