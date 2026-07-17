-- 422_models_canonical_complexity.sql
-- Phase: Auto 智能路由 — 复杂度/难度路由维度（M2）
-- 详见 docs/拆分/22-Auto智能路由与任务识别.md §22.9 M2
-- 给 models_canonical 加 complexity_ceiling（模型能胜任的最高难度）
-- 和 min_complexity（低于此难度的请求不应路由到该模型，避免大模型接简单任务）。
-- Idempotent: ADD COLUMN IF NOT EXISTS

BEGIN;

-- complexity_ceiling：模型能稳定胜任的最高任务难度。
-- 取值：easy / medium / hard / frontier。NULL = 不参与复杂度过滤（向后兼容）。
--   easy      : 简单对话、翻译、摘要（小模型足够）
--   medium    : 常规代码、多轮对话、function call
--   hard      : 复杂推理、长上下文、agent、code_audit
--   frontier  : 最难推理、前沿数学、超长上下文
-- 过滤规则：任务难度 > complexity_ceiling 的候选被剔除。
ALTER TABLE models_canonical
  ADD COLUMN IF NOT EXISTS complexity_ceiling text
    CHECK (complexity_ceiling IS NULL OR complexity_ceiling IN ('easy','medium','hard','frontier'));

-- min_complexity：模型不应承接低于此难度的请求（成本/能力浪费）。
-- 取值同上，NULL = 不过滤。例如 frontier 模型可设 min_complexity='hard'，
-- 避免简单 chat 请求占用昂贵资源。
ALTER TABLE models_canonical
  ADD COLUMN IF NOT EXISTS min_complexity text
    CHECK (min_complexity IS NULL OR min_complexity IN ('easy','medium','hard','frontier'));

COMMENT ON COLUMN models_canonical.complexity_ceiling IS
  '模型能稳定胜任的最高任务难度（easy/medium/hard/frontier）；NULL=不参与复杂度过滤。auto 路由在 auto_complexity_score=true 时剔除任务难度>ceiling 的候选。';
COMMENT ON COLUMN models_canonical.min_complexity IS
  '模型不应承接的难度下限；NULL=不过滤。避免 frontier 模型承接简单 chat 请求。';

-- 辅助索引：复杂度过滤时按 ceiling 快速筛
CREATE INDEX IF NOT EXISTS idx_models_canonical_complexity_ceiling
  ON models_canonical (complexity_ceiling)
  WHERE complexity_ceiling IS NOT NULL;

COMMIT;
