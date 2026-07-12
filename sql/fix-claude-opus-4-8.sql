-- fix-claude-opus-4-8.sql
-- 修复 claude-opus-4-8 模型缺失的问题
--
-- 问题描述：
--   - model_aliases 表中有 claude-opus-4.8 的别名记录 (canonical_id=354)
--   - 但 models_canonical 表中没有 claude-opus-4-8 模型记录
--   - 导致路由时找不到可用的候选节点
--
-- 修复方案：
--   1. 在 models_canonical 表中添加 claude-opus-4-8 模型
--   2. 更新 model_aliases 表中的 canonical_id
--
-- 执行方式：
--   psql -h localhost -p 5432 -U xutaohuang -d llm_gateway -f sql/fix-claude-opus-4-8.sql

BEGIN;

-- 1. 在 models_canonical 表中添加 claude-opus-4-8 模型
INSERT INTO models_canonical (
    canonical_name, 
    family, 
    source, 
    status, 
    notes,
    display_name,
    context_window,
    input_price_cny,
    output_price_cny
)
VALUES (
    'claude-opus-4-8',
    'anthropic-claude',
    'seed',
    'active',
    'Anthropic Claude 4.8 Opus - 修复缺失的模型记录',
    'Claude Opus 4.8',
    200000,  -- 200K context window
    15.00,   -- 输入价格 CNY/1M tokens
    75.00    -- 输出价格 CNY/1M tokens
)
ON CONFLICT (canonical_name) DO UPDATE SET
    family = EXCLUDED.family,
    notes = EXCLUDED.notes,
    display_name = EXCLUDED.display_name,
    updated_at = NOW()
RETURNING id;

-- 获取新插入的 canonical_id
DO $$
DECLARE
    v_canonical_id BIGINT;
BEGIN
    SELECT id INTO v_canonical_id
    FROM models_canonical
    WHERE canonical_name = 'claude-opus-4-8';
    
    -- 2. 更新 model_aliases 表中的 canonical_id
    UPDATE model_aliases
    SET canonical_id = v_canonical_id,
        updated_at = NOW()
    WHERE raw_name IN ('claude-opus-4.8', 'claude-opus-4-8')
       OR (canonical_id = 354 AND raw_name LIKE '%opus-4.8%');
    
    RAISE NOTICE 'Updated model_aliases with canonical_id=%', v_canonical_id;
END $$;

-- 3. 验证修复结果
SELECT 
    mc.id as canonical_id,
    mc.canonical_name,
    mc.family,
    mc.status,
    COUNT(ma.id) as alias_count
FROM models_canonical mc
LEFT JOIN model_aliases ma ON mc.id = ma.canonical_id
WHERE mc.canonical_name = 'claude-opus-4-8'
GROUP BY mc.id, mc.canonical_name, mc.family, mc.status;

COMMIT;
