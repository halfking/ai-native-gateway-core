-- 359_canonical_metadata_sync.sql
-- Phase: 标准模型元数据同步（grok-4.6 / kimi-k* / gemini-3.*）
-- Idempotent: 仅在 reasoning_caps IS NULL 时回填；不会覆盖运营热改值。
--
-- 背景：
--   - 352-356 已把 grok-4.6 / kimi-k3/k2.6/k2.7 / gemini-3.* 写入 models_canonical
--   - 357 修复了 model_aliases 的 (canonical_id, raw_name) 唯一约束
--   - 358 把 kimi-k3 modality 对齐为 multimodal（与 Go modality_defaults.go:169 一致）
--   - 478 (startup) 新增了 reasoning_caps JSONB 列，供 reasoncap.Resolve 的最高优先级 tier-1 读取
--   - 但 352-356/478 都没有把"硬编码在 Go 代码里的 reasoning 配置"反向同步到 DB 行
--
-- 本迁移把以下 3 家族的 reasoning 配置下沉到 models_canonical.reasoning_caps，
-- 便于运营热改、不改代码即可调整 effort 档位 / can_disable：
--
--   grok-4.6        → DialectGrok       efforts=[low,medium,high,xhigh]    can_disable=true
--   kimi-k3         → DialectKimiEffort efforts=[low,high,max]             can_disable=true
--   kimi-k2.6       → DialectKimiThink  history_field=keep                can_disable=true
--   kimi-k2.7-code  → DialectKimiThink  history_field=keep                can_disable=true
--   kimi-k2.7-code-highspeed → DialectKimiThink history_field=keep        can_disable=true
--   gemini-3.*      → DialectGemini3    efforts=[minimal,low,medium,high]  can_disable=true
--                    （gemini-3.1-flash-image / 3-pro-image 同样适用）
--
-- 同步 modality（防止 358 之外的 drift；DB 与 Go registry 需一致）：
--   kimi-k3          = multimodal (Go: modelname/modality_defaults.go:169)
--   kimi-k2.6        = vision      (Go: modelname/modality_defaults.go:172)
--   kimi-k2.7-code*  = text        (Go: modelname/modality_defaults.go:170-171)
--   grok-4.6         = vision      (Go: modelname/modality_defaults.go:247)
--   gemini-3.*       = multimodal  (Go: modelname/modality_defaults.go:111-118)
--
-- 设计原则：
--   - 不改动 352-358 既有迁移（迁移不可变约束）
--   - 不改 Go 代码的硬编码（reasoning_defaults.go 仍作为 tier-2 回退）
--   - 不写 credentials / bindings（由 361-362 处理）
--   - 不引入新的 vendor-prefix 别名（由 360 处理）
--
-- 审计修复（feat/standard-models-rollout 审计 2026-08-20）：
--   - 防御性 ALTER TABLE：保证 359 在 478 未应用的环境下不报错
--   - 在 reasoning_caps JSONB 中写入 'source'='migration-359' 键，让 down 迁移能区分
--     运营热改 vs 迁移写入，避免 .down 误清空

BEGIN;

-- 防御性：478 (startup) 新增的 JSONB 列。如果环境只跑 domain 而没跑 startup，
-- 这里确保 359 不至于在 column-does-not-exist 上失败。
ALTER TABLE models_canonical
  ADD COLUMN IF NOT EXISTS reasoning_caps JSONB;

CREATE INDEX IF NOT EXISTS idx_models_canonical_reasoning_caps_supported
  ON models_canonical ((reasoning_caps->>'supported'))
  WHERE reasoning_caps IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_models_canonical_reasoning_caps_dialect
  ON models_canonical ((reasoning_caps->>'dialect'))
  WHERE reasoning_caps IS NOT NULL AND reasoning_caps->>'dialect' != '';

-- ─────────────────────────────────────────────────────────────────────────
-- 1) reasoning_caps 回填（幂等：仅在 NULL 时写入；用 'source' 键标记迁移来源）
-- ─────────────────────────────────────────────────────────────────────────

-- grok-4.6 — Grok reasoning_effort 4 档
UPDATE models_canonical
SET reasoning_caps = jsonb_build_object(
        'source',      'migration-359',
        'supported',   true,
        'dialect',     'grok',
        'efforts',     jsonb_build_array('low', 'medium', 'high', 'xhigh'),
        'can_disable', true
     ),
    updated_at = NOW()
WHERE canonical_name = 'grok-4.6'
  AND reasoning_caps IS NULL;

-- kimi-k3 — Kimi reasoning_effort (low/high/max)
UPDATE models_canonical
SET reasoning_caps = jsonb_build_object(
        'source',      'migration-359',
        'supported',   true,
        'dialect',     'kimi_effort',
        'efforts',     jsonb_build_array('low', 'high', 'max'),
        'can_disable', true
     ),
    updated_at = NOW()
WHERE canonical_name = 'kimi-k3'
  AND reasoning_caps IS NULL;

-- kimi-k2.6 — Kimi thinking{keep}
UPDATE models_canonical
SET reasoning_caps = jsonb_build_object(
        'source',        'migration-359',
        'supported',     true,
        'dialect',       'kimi_think',
        'can_disable',   true,
        'history_field', 'keep'
     ),
    updated_at = NOW()
WHERE canonical_name = 'kimi-k2.6'
  AND reasoning_caps IS NULL;

-- kimi-k2.7-code — Kimi thinking{keep}
UPDATE models_canonical
SET reasoning_caps = jsonb_build_object(
        'source',        'migration-359',
        'supported',     true,
        'dialect',       'kimi_think',
        'can_disable',   true,
        'history_field', 'keep'
     ),
    updated_at = NOW()
WHERE canonical_name = 'kimi-k2.7-code'
  AND reasoning_caps IS NULL;

-- kimi-k2.7-code-highspeed — Kimi thinking{keep}
UPDATE models_canonical
SET reasoning_caps = jsonb_build_object(
        'source',        'migration-359',
        'supported',     true,
        'dialect',       'kimi_think',
        'can_disable',   true,
        'history_field', 'keep'
     ),
    updated_at = NOW()
WHERE canonical_name = 'kimi-k2.7-code-highspeed'
  AND reasoning_caps IS NULL;

-- gemini-3.* — Gemini thinkingLevel（minimal/low/medium/high）
-- 包括 355 写入的全部 10 个 ID（含 -image 变体，reasoning 与 base 一致）
UPDATE models_canonical
SET reasoning_caps = jsonb_build_object(
        'source',      'migration-359',
        'supported',   true,
        'dialect',     'gemini3',
        'efforts',     jsonb_build_array('minimal', 'low', 'medium', 'high'),
        'can_disable', true
     ),
    updated_at = NOW()
WHERE family = 'google-gemini'
  AND canonical_name LIKE 'gemini-3%'
  AND reasoning_caps IS NULL;

-- ─────────────────────────────────────────────────────────────────────────
-- 2) modality 对齐（防御性：万一 352-358 未覆盖到该行）
-- ─────────────────────────────────────────────────────────────────────────

-- grok-4.6 → vision
UPDATE models_canonical
SET modality = 'vision', updated_at = NOW()
WHERE canonical_name = 'grok-4.6'
  AND modality IS DISTINCT FROM 'vision';

-- kimi-k3 → multimodal（与 358 重复执行是 no-op）
UPDATE models_canonical
SET modality = 'multimodal', updated_at = NOW()
WHERE canonical_name = 'kimi-k3'
  AND modality IS DISTINCT FROM 'multimodal';

-- kimi-k2.6 → vision
UPDATE models_canonical
SET modality = 'vision', updated_at = NOW()
WHERE canonical_name = 'kimi-k2.6'
  AND modality IS DISTINCT FROM 'vision';

-- kimi-k2.7-code(-highspeed) → text
UPDATE models_canonical
SET modality = 'text', updated_at = NOW()
WHERE canonical_name IN ('kimi-k2.7-code', 'kimi-k2.7-code-highspeed')
  AND modality IS DISTINCT FROM 'text';

-- gemini-3.* → multimodal（除非已是 embedding）
UPDATE models_canonical
SET modality = 'multimodal', updated_at = NOW()
WHERE family = 'google-gemini'
  AND canonical_name LIKE 'gemini-3%'
  AND modality IS DISTINCT FROM 'multimodal'
  AND modality <> 'embedding';

COMMIT;