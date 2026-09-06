-- Migration 675: 补建 qwen3.8 family 行 + 回填空 vendor 的 family
--
-- 背景：models_canonical 里存在 family='qwen3.8' 的模型（qwen3.8-27b、
-- qwen3.8-max、qwen3.8-max-preview、qwen3.8-2.4t-a95b），但 model_families
-- 没有对应行（seed 只到 qwen3.6）。Go 侧也不会自动建 family 行，导致
-- /api/routing/available-models 按 mf.vendor 分组时 JOIN 不上、vendor 落空，
-- /models 特色模型选择器里 Alibaba 分组看不到 qwen3.8-27b（落到「其他」）。
--
-- 同类隐患：xai、xiaomi-mimo 两族 vendor 为空（seed 即 NULL），grok-* /
-- mimo-* 模型同样会分错组。
--
-- 本迁移：
--   A. 补建 qwen3.8 family 行（vendor='Alibaba'，与 qwen3.5/qwen3.6 一致）。
--   B. 回填 xai→xAI、xiaomi-mimo→小米 的空 vendor。
--   C. 代码侧兜底：admin/routing.go available-models 对 family vendor 为空
--      的行改用 catalog.InferVendor 按模型名前缀推断（本次同提交）。
--
-- 幂等：可重复执行。注意 model_families.id 无唯一约束（历史数据存在重复
-- id 行），不能 ON CONFLICT (id)，用 WHERE NOT EXISTS 防重。
--
-- Rollback:
--   DELETE FROM model_families WHERE id='qwen3.8' AND source='migration-675';
--   UPDATE model_families SET vendor=NULL WHERE id IN ('xai','xiaomi-mimo')
--     AND source='migration-675';

-- ── A. 补建 qwen3.8 ─────────────────────────────────────────────────
INSERT INTO model_families (id, display_name, vendor, status, source, notes)
SELECT 'qwen3.8', 'Qwen 3.8', 'Alibaba', 'active', 'migration-675',
       '补建：qwen3.8-27b/max 等 canonical 的 family 此前无对应行（675）'
WHERE NOT EXISTS (SELECT 1 FROM model_families WHERE id = 'qwen3.8');

-- ── B. 回填空 vendor ────────────────────────────────────────────────
UPDATE model_families
SET vendor = 'xAI', source = 'migration-675', updated_at = now()
WHERE id = 'xai' AND (vendor IS NULL OR btrim(vendor) = '');

UPDATE model_families
SET vendor = '小米', source = 'migration-675', updated_at = now()
WHERE id = 'xiaomi-mimo' AND (vendor IS NULL OR btrim(vendor) = '');
