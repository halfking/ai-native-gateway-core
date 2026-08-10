-- 478_model_reasoning_caps.sql
-- Phase: 模型推理能力元数据（P4 — reasoncap 三层架构的 tier-1 DB 列）
-- Idempotent: ADD COLUMN IF NOT EXISTS

-- 背景（docs/参数全量兼容/02-目标架构.md §3.2）：
-- 此前网关对模型的思考能力完全没有元数据，thinking/reasoning_effort 是无条件
-- 下发的，对不支持的模型必然 400 或被静默忽略。
-- 本迁移新增 reasoning_caps JSONB 列，供运营热改能力参数，优先于代码内置的
-- 名称模式表（modelname/reasoning_defaults.go）。
--
-- JSONB schema（对应 internal/reasoncap.Caps，Source 字段不存储）：
--
--   {
--     "supported": true,
--     "dialect": "anthropic",            -- 见 reasoncap.Dialect 常量
--     "efforts": ["low","medium","high"], -- 支持的 effort 档位
--     "budget_min": 1024,                -- 预算下限（0 = 不支持预算）
--     "budget_max": 32000,               -- 预算上限
--     "can_disable": true,               -- 能否显式关闭
--     "adaptive": true,                  -- 支持 {type:"adaptive"}
--     "history_field": ""                -- 历史保留控制字段名
--   }
--
-- 取值示例：
--   claude-sonnet-4：{"supported":true,"dialect":"anthropic","budget_min":1024,"budget_max":32000,"can_disable":true,"adaptive":true}
--   deepseek-reasoner：{"supported":true,"dialect":"deepseek","efforts":["low","high","max"],"can_disable":true}
--   grok-4.5：{"supported":true,"dialect":"grok","efforts":["low","medium","high"],"can_disable":false}
--   gpt-4o（无思考）：{"supported":false}

BEGIN;

ALTER TABLE models_canonical
  ADD COLUMN IF NOT EXISTS reasoning_caps JSONB;

-- 索引：快速筛选支持推理的模型
CREATE INDEX IF NOT EXISTS idx_models_canonical_reasoning_caps_supported
  ON models_canonical ((reasoning_caps->>'supported'))
  WHERE reasoning_caps IS NOT NULL;

-- 索引：按 dialect 聚合（用于路由策略）
CREATE INDEX IF NOT EXISTS idx_models_canonical_reasoning_caps_dialect
  ON models_canonical ((reasoning_caps->>'dialect'))
  WHERE reasoning_caps IS NOT NULL AND reasoning_caps->>'dialect' != '';

COMMENT ON COLUMN models_canonical.reasoning_caps IS
  '模型推理/思考能力描述（JSONB）。'
  '{"supported":bool, "dialect":"anthropic|deepseek|openai|...", '
  '"efforts":[], "budget_min":int, "budget_max":int, '
  '"can_disable":bool, "adaptive":bool, "history_field":""}。'
  '由 internal/reasoncap.Resolve 的 tier-1 读取（最高优先级）；'
  '空=回退到名称模式表。';

COMMIT;
