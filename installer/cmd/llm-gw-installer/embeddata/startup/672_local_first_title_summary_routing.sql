-- Migration 672: 会话标题/总结任务本地模型优先路由
--
-- 目标（2026-09-07 运营决策）：session_title（标题生成）与
-- session_summary（会话总结）两项网关内部 LLM 任务默认指向本机
-- Qwen3.8-27B（mlx, canonical '4-bit'，provider kind='local'）；
-- minimax-m3 作为第二优先，当定时自检探针发现本地模型不可用时自动回退。
--
-- 机制（无需新增代码）：admin.resolveAdminLLMFallbackModel 按
-- work_type_model_route.weight DESC 遍历候选池，且只选中
-- v_routable_credential_models.is_routable = TRUE 的模型 ——
-- 本地模型探针失败/进程不存在时该视图自动排除它，标题/总结请求
-- 落到 minimax-m3；探针恢复后自动回到本地。实测：
--   1. 本地在线        → 选中 4-bit
--   2. 本地超时/停机   → cooling → 自动选中 minimax-m3
--   3. 本地恢复（≤15s）→ 自动回到 4-bit
--   4. auto-title 实测 request_logs: model='4-bit' 生成成功。
--
-- Rollback: DELETE FROM work_type_model_route
--   WHERE work_type_key IN ('session_title','session_summary')
--     AND canonical_name = '4-bit';

-- ── 1. 本地 Qwen3.8-27B：两任务的最高优先 ──
INSERT INTO work_type_model_route
  (work_type_key, canonical_name, weight, min_score, enabled, tier, task_quality_score)
VALUES
  ('session_title',   '4-bit', 10.0, 0.5, TRUE, 'primary', 0.85),
  ('session_summary', '4-bit', 10.0, 0.5, TRUE, 'primary', 0.85)
ON CONFLICT (work_type_key, canonical_name) DO UPDATE SET
  weight = EXCLUDED.weight,
  tier = EXCLUDED.tier,
  enabled = TRUE,
  task_quality_score = EXCLUDED.task_quality_score;

-- ── 2. minimax-m3：第二优先（本地不可用时的回退）──
UPDATE work_type_model_route
SET weight = 8.0, tier = 'primary', enabled = TRUE
WHERE work_type_key IN ('session_title', 'session_summary')
  AND canonical_name = 'minimax-m3';

-- ── 3. 验证 ──
-- 期望：每个 work_type 前两行依次为 4-bit(10, primary)、minimax-m3(8, primary)
SELECT work_type_key, canonical_name, tier, weight
FROM work_type_model_route
WHERE work_type_key IN ('session_title', 'session_summary')
  AND enabled = TRUE
ORDER BY work_type_key, weight DESC;
