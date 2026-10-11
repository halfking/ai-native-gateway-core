-- ===========================================================================
-- File:          sql/migrations/startup/852_models_canonical_context_window_columns.sql
-- Migration:     852
-- Database:      llm_gateway
-- Purpose:       models_canonical 列集收编——469 的 context_window 三列 +
--                478 的 reasoning_caps（列/部分索引/列注释），fresh-install
--                链从未拿到过的 canonical 形状
--
-- Status:        active
-- Idempotent:    YES (ADD COLUMN IF NOT EXISTS / CREATE INDEX IF NOT EXISTS，
--                存量库零行为变化)
-- Dependencies:  469_context_window_override.sql、478_model_reasoning_caps.sql
--                （本迁移收编的原始创建者，均未注册进 installer fresh 序列）
--
-- Background:
--   01-schema baseline dump 早于这两条迁移，而它们又从未注册进 installer 的
--   fresh-install 序列——全新安装的库上这些列从未被创建，schema_migrations
--   也不记录（<471 视为已烤入 baseline），静默缺列。生产/部署链（V359 血统）
--   的库按历史顺序跑过 469/478 不受影响；fresh 链的库一旦被任何引用这些列的
--   查询扫到即 42703。
--
--   实测（2026-10-12，本机 host-first fresh install，llm.itestu.cn）：
--   - GET /api/providers/{id}/models → 500 "query failed"
--     （admin/provider_models.go 三处 + credential_models_dto.go 共 4 个调用
--     点的三级覆盖链 COALESCE(mo.override, mc.override, mc.context_window)）
--   - GET /api/providers/{id}/credentials/{cid}/models → 同样 500
--     （mc.reasoning_caps，478 的列；同一 DTO 的能力列段）
--
--   这是 804 同类缺口的后半段：804（2026-10-01）收编了 523 在
--   credential_model_bindings 上的列集（682 视图重建的 42703 前置），
--   其头部 Dependencies 就标注了「469 models_canonical 同款三列」，
--   但 models_canonical 半边当时未收编。本迁移补齐，并把逐文件对账
--   （所有 ALTER models_canonical 的未登记迁移 vs baseline 列集）得到的
--   另一缺口 478 一并收编。045/422/451 虽同样未登记，但列/约束已被
--   baseline 覆盖（2026-10-12 本地 fresh install 实测在），无需收编。
--
--   列类型/默认值/索引与 469/478 逐项一致；存量库零行为变化。
-- ===========================================================================

BEGIN;

-- ── 469_context_window_override.sql（A4 Phase 1，2026-07）────────────────
ALTER TABLE IF EXISTS public.models_canonical
    ADD COLUMN IF NOT EXISTS context_window_override integer,
    ADD COLUMN IF NOT EXISTS context_window_source text DEFAULT 'catalog',
    ADD COLUMN IF NOT EXISTS context_window_updated_at timestamp with time zone;

-- ── 478_model_reasoning_caps.sql（P4 reasoncap tier-1，2026-08）──────────
ALTER TABLE IF EXISTS public.models_canonical
    ADD COLUMN IF NOT EXISTS reasoning_caps jsonb;

CREATE INDEX IF NOT EXISTS idx_models_canonical_reasoning_caps_supported
    ON public.models_canonical ((reasoning_caps->>'supported'))
    WHERE reasoning_caps IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_models_canonical_reasoning_caps_dialect
    ON public.models_canonical ((reasoning_caps->>'dialect'))
    WHERE reasoning_caps IS NOT NULL AND reasoning_caps->>'dialect' != '';

COMMENT ON COLUMN public.models_canonical.reasoning_caps IS
  '模型推理/思考能力描述（JSONB）。'
  '{"supported":bool, "dialect":"anthropic|deepseek|openai|...", '
  '"efforts":[], "budget_min":int, "budget_max":int, '
  '"can_disable":bool, "adaptive":bool, "history_field":""}。'
  '由 internal/reasoncap.Resolve 的 tier-1 读取（最高优先级）；'
  '空=回退到名称模式表。';

COMMIT;
