-- 044_task_route_tiers.sql
-- Phase: 任务模型路由三级（需求 #3 热路径接入）
-- 扩展 work_type_model_route: tier (primary/secondary/fallback) + task_quality_score
-- Idempotent: ADD COLUMN IF NOT EXISTS

BEGIN;

-- 三级偏好（首选/次选/兜底）
ALTER TABLE work_type_model_route
  ADD COLUMN IF NOT EXISTS tier TEXT NOT NULL DEFAULT 'secondary'
    CHECK (tier IN ('primary','secondary','fallback'));

-- 该模型在该任务上的质量评分 override（0 = 使用公式计算，> 0 = 人工覆盖）
ALTER TABLE work_type_model_route
  ADD COLUMN IF NOT EXISTS task_quality_score NUMERIC(5,2) NOT NULL DEFAULT 0
    CHECK (task_quality_score >= 0 AND task_quality_score <= 100);

-- 索引：按任务+tier 查询（Index.Recommend 热路径）
CREATE INDEX IF NOT EXISTS idx_wtmr_tier 
  ON work_type_model_route (work_type_key, tier, weight DESC);

-- 注释
COMMENT ON COLUMN work_type_model_route.tier IS 
  '三级偏好：primary（首选）/ secondary（次选）/ fallback（兜底）。Index.Recommend 先推荐 primary，全挂时用 secondary，最后才 fallback';
COMMENT ON COLUMN work_type_model_route.task_quality_score IS 
  '该模型在该任务上的人工评分覆盖（0-100）。0 表示用公式计算 scoreStrengthMatch；>0 则直接用该分数';
COMMENT ON COLUMN work_type_model_route.weight IS 
  '同 tier 内的排序权重（tier 间优先级：primary > secondary > fallback，tier 内按 weight DESC 排）';

-- 数据迁移：现有行默认 tier='secondary' 保持向后兼容
-- （ALTER TABLE ADD COLUMN DEFAULT 'secondary' 自动处理）

COMMIT;
