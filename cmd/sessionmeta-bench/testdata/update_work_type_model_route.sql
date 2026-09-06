-- 基于真实指定模型会话的任务分布，更新work_type_model_route
-- 生成时间: 2026-09-06


-- claude-opus-5 的任务分布
INSERT INTO work_type_model_route (work_type_key, canonical_name, weight, tier, enabled, min_score, task_quality_score)
VALUES ('general_chat', 'claude-opus-5', 10.0, 'primary', true, 0.5, 0.8)
ON CONFLICT (work_type_key, canonical_name) 
DO UPDATE SET 
  weight = EXCLUDED.weight,
  tier = EXCLUDED.tier,
  enabled = true;


-- 验证更新结果

SELECT 
  work_type_key,
  canonical_name,
  tier,
  weight,
  enabled
FROM work_type_model_route
WHERE canonical_name IN ('claude-opus-5')
ORDER BY work_type_key, tier, weight DESC;
