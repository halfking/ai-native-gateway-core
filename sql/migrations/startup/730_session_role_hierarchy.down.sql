-- Migration 730 rollback: Session Role Hierarchy (R48)
BEGIN;

-- 1. 删 RLS policy + 表
DROP TABLE IF EXISTS public.role_task_llm_mapping CASCADE;

-- 2. 删索引
DROP INDEX IF EXISTS public.idx_sessions_parent_session;
DROP INDEX IF EXISTS public.idx_sessions_agent_role;

-- 3. 删列（自动级联到所有分区）
ALTER TABLE public.sessions
    DROP COLUMN IF EXISTS parent_task_id,
    DROP COLUMN IF EXISTS parent_session_id,
    DROP COLUMN IF EXISTS agent_role;

-- 4. 回滚 provider_models 轻量池 tier 修正（R48）。
--    尽力恢复到 730 之前的分类：minimax-m3 在 202609_03 中显式为 tier-b；
--    glm-5.3-flash / kimi-k3 当时未显式分类、由"未分类默认 tier-b"兜底，
--    因此三者回 tier-b。deepseek-v4-flash 在 202609_03 本就是 tier-c，
--    不回滚（回滚成 tier-b 反而是错误状态）。
UPDATE public.provider_models
SET tier = 'tier-b'
WHERE canonical_name IN ('minimax-m3', 'glm-5.3-flash', 'kimi-k3')
  AND tier = 'tier-c';

COMMIT;
