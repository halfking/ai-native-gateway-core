-- 043_model_capability_fields.sql
-- Phase: 标准模型能力字段扩展（需求 #4）
-- 新增 released_at / strengths / cost_tier / multimodal_caps / version_rank
-- Idempotent: ADD COLUMN IF NOT EXISTS

BEGIN;

-- 发布时间（用于 version_recency 评分维度）
ALTER TABLE models_canonical
  ADD COLUMN IF NOT EXISTS released_at DATE;

-- 优势方向（运营标注，用于 strength_match 评分维度）
-- 可选值示例: ["reasoning","code","long_context","vision","math","multimodal","creative","agent"]
ALTER TABLE models_canonical
  ADD COLUMN IF NOT EXISTS strengths TEXT[] NOT NULL DEFAULT '{}';

-- 成本粗评（快速筛选用）
ALTER TABLE models_canonical
  ADD COLUMN IF NOT EXISTS cost_tier TEXT NOT NULL DEFAULT 'unknown'
    CHECK (cost_tier IN ('free','low','medium','high','premium','unknown'));

-- 多模态能力（细粒度能力标签）
-- 可选值示例: ["vision","audio","image_gen","video","embedding"]
ALTER TABLE models_canonical
  ADD COLUMN IF NOT EXISTS multimodal_caps TEXT[] NOT NULL DEFAULT '{}';

-- 版本级次（1=最新, 2=次新, ...）
-- 运营手填或按 released_at DESC 自动排序生成
-- NULL = 未分级
ALTER TABLE models_canonical
  ADD COLUMN IF NOT EXISTS version_rank INT;

-- 索引：按发布时间降序（version_recency 评分需要）
CREATE INDEX IF NOT EXISTS idx_models_canonical_released 
  ON models_canonical (released_at DESC NULLS LAST);

-- 索引：按 strengths 数组搜索（GIN 索引支持数组包含查询）
CREATE INDEX IF NOT EXISTS idx_models_canonical_strengths 
  ON models_canonical USING GIN (strengths);

-- 索引：按 version_rank 升序（路由优先级需要）
CREATE INDEX IF NOT EXISTS idx_models_canonical_version_rank 
  ON models_canonical (version_rank ASC NULLS LAST);

-- 注释
COMMENT ON COLUMN models_canonical.released_at IS 
  '模型发布日期，用于 version_recency 评分维度（高难度任务偏好最新版，普通任务偏好次新版）';
COMMENT ON COLUMN models_canonical.strengths IS 
  '运营标注的优势方向数组，用于 strength_match 评分维度（比 tags 更精准）';
COMMENT ON COLUMN models_canonical.cost_tier IS 
  '成本粗评：free/low/medium/high/premium，用于快速筛选和展示';
COMMENT ON COLUMN models_canonical.multimodal_caps IS 
  '多模态能力细粒度标签：vision/audio/image_gen/video/embedding 等';
COMMENT ON COLUMN models_canonical.version_rank IS 
  '版本级次：1=最新, 2=次新, 3=稳定版... 用于路由策略（普通任务偏次新，高难度偏最新）';

COMMIT;
