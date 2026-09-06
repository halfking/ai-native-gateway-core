-- 基于高频指定模型使用情况，补充work_type_model_route映射
-- 目标：确保auto模式能路由到这些常用模型
-- 生成时间: 2026-09-06

-- ==================================================
-- deepseek-v4-pro (1,392次使用，当前0覆盖)
-- 定位：快速推理、代码生成、长文档
-- ==================================================

INSERT INTO work_type_model_route (work_type_key, canonical_name, weight, tier, enabled, min_score, task_quality_score)
VALUES 
  ('code_gen', 'deepseek-v4-pro', 2.5, 'secondary', true, 0.5, 0.8),
  ('reasoning', 'deepseek-v4-pro', 3.0, 'secondary', true, 0.5, 0.8),
  ('long_doc', 'deepseek-v4-pro', 2.0, 'secondary', true, 0.5, 0.8),
  ('general_chat', 'deepseek-v4-pro', 2.0, 'secondary', true, 0.5, 0.8)
ON CONFLICT (work_type_key, canonical_name) 
DO UPDATE SET 
  weight = EXCLUDED.weight,
  tier = EXCLUDED.tier,
  enabled = true;

-- ==================================================
-- claude-sonnet-5 (3,862次使用，当前仅1覆盖)
-- 定位：高质量代码、推理、智能体
-- ==================================================

INSERT INTO work_type_model_route (work_type_key, canonical_name, weight, tier, enabled, min_score, task_quality_score)
VALUES 
  ('reasoning', 'claude-sonnet-5', 2.0, 'secondary', true, 0.5, 0.9),
  ('agent_workflow', 'claude-sonnet-5', 2.5, 'secondary', true, 0.5, 0.9),
  ('long_doc', 'claude-sonnet-5', 1.5, 'secondary', true, 0.5, 0.9),
  ('general_chat', 'claude-sonnet-5', 1.0, 'fallback', true, 0.5, 0.9)
ON CONFLICT (work_type_key, canonical_name) 
DO UPDATE SET 
  weight = EXCLUDED.weight,
  tier = EXCLUDED.tier,
  enabled = true;

-- ==================================================
-- claude-opus-5 (964次使用，当前仅2覆盖)
-- 定位：顶级推理、复杂分析
-- ==================================================

INSERT INTO work_type_model_route (work_type_key, canonical_name, weight, tier, enabled, min_score, task_quality_score)
VALUES 
  ('agent_workflow', 'claude-opus-5', 1.5, 'secondary', true, 0.5, 0.95),
  ('long_doc', 'claude-opus-5', 2.0, 'secondary', true, 0.5, 0.95),
  ('code_review', 'claude-opus-5', 1.5, 'secondary', true, 0.5, 0.95),
  ('general_chat', 'claude-opus-5', 1.0, 'fallback', true, 0.5, 0.95)
ON CONFLICT (work_type_key, canonical_name) 
DO UPDATE SET 
  weight = EXCLUDED.weight,
  tier = EXCLUDED.tier,
  enabled = true;

-- ==================================================
-- 验证更新结果
-- ==================================================

SELECT 
  canonical_name,
  COUNT(*) as total_tasks,
  SUM(CASE WHEN tier = 'primary' THEN 1 ELSE 0 END) as primary_tasks,
  SUM(CASE WHEN tier = 'secondary' THEN 1 ELSE 0 END) as secondary_tasks,
  STRING_AGG(work_type_key, ', ' ORDER BY weight DESC) as tasks
FROM work_type_model_route
WHERE canonical_name IN ('deepseek-v4-pro', 'claude-sonnet-5', 'claude-opus-5')
  AND enabled = true
GROUP BY canonical_name
ORDER BY total_tasks DESC;

-- ==================================================
-- Auto模式命中率测试查询
-- ==================================================

-- 测试：如果用户请求reasoning任务，哪些模型会被候选
SELECT 
  work_type_key,
  canonical_name,
  tier,
  weight,
  RANK() OVER (PARTITION BY work_type_key, tier ORDER BY weight DESC) as rank_in_tier
FROM work_type_model_route
WHERE work_type_key = 'reasoning'
  AND enabled = true
ORDER BY tier, weight DESC;
