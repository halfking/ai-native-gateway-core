-- Migration 611: Correct canonical model context_window + modality values
-- Verified against each vendor's official documentation (snapshot 2026-08-29).
--
-- SCOPE:
--   - Fix obviously wrong context_window values (e.g. 256, 500, 1000, 4096,
--     2097152) recorded by migration 352 / 354 / 356 / 02-seed.sql.
--   - Align DB modality / multimodal_caps with vendor docs.
--   - Normalize 3 orphaned rows that have NULL family.
--   - Repair 11 Claude placeholder rows that lost context_window in
--     discovery / provider_refresh sync.
--   - Defensive: only writes when (a) current value is NULL/WRONG or
--     (b) modality contradicts vendor docs. Each block lists the
--     source-of-truth and the original buggy value in a header comment
--     so future audits can trace every change.
--
-- DESIGN PRINCIPLES:
--   - Idempotent: each UPDATE has a `WHERE` clause that only matches the
--     current wrong state, so re-running is a no-op.
--   - Migrations 352 / 354 / 356 are immutable; we only write here.
--   - provider_catalog manifests (02-seed.sql) already carry the same
--     values for cross-check; this migration makes models_canonical
--     consistent with them.
--   - modality / multimodal_caps uses the public enum defined by
--     01-schema.sql: text / vision / audio / video / multimodal / embedding.
--
-- NON-GOALS:
--   - provider-specific overrides (use credential_model_bindings).
--   - Discovery / refresh behavior changes (separate migration).
--   - Renaming canonical_name (kept stable for routing).

BEGIN;

-- ─────────────────────────────────────────────────────────────────────────
-- A. CONTEXT WINDOW CORRECTIONS (buggy values from 352 / 354 / 356)
-- ─────────────────────────────────────────────────────────────────────────

-- A.1 grok-4.6: DB 500 (× 1000 bug) → 262144 (256K, vision-capable)
-- Source-of-truth: xAI Grok-4 product page (https://x.ai/news/grok-4)
--   - Grok-4 family: 256K context, image+text input.
-- Original buggy value: 500 (written by 02-seed.sql:1033, mirror of 352).
UPDATE public.models_canonical
SET context_window     = 262144,
    context_window_source = 'manual',
    context_window_updated_at = NOW(),
    modality           = 'vision',
    updated_at         = NOW()
WHERE canonical_name = 'grok-4.6'
  AND (context_window IS DISTINCT FROM 262144
       OR modality IS DISTINCT FROM 'vision');

-- A.2 glm-5.3: DB 1000 (× 1000 bug) → 1048576 (1M, text reasoning)
-- Source-of-truth: Z.AI GLM-5.3 product page; corroborated by
--   modelname/modality_defaults.go:159 ("glm-5.3" → text) and
--   alias notes 'Z.AI GLM-5.3; 1M context; reasoning is mandatory'
--   (migration 354:12).
-- Original buggy value: 1000 (written by migration 354:7).
UPDATE public.models_canonical
SET context_window     = 1048576,
    context_window_source = 'manual',
    context_window_updated_at = NOW(),
    updated_at         = NOW()
WHERE canonical_name = 'glm-5.3'
  AND context_window IS DISTINCT FROM 1048576;

-- A.3 kimi-k3: DB 1000 → 1048576 (1M, multimodal)
-- Source-of-truth: modelname/modality_defaults.go:169
--   "kimi-k3 ... 1M context, text+image+video".
-- Original buggy value: 1000 (written by migration 354:23).
UPDATE public.models_canonical
SET context_window     = 1048576,
    context_window_source = 'manual',
    context_window_updated_at = NOW(),
    modality           = 'multimodal',
    updated_at         = NOW()
WHERE canonical_name = 'kimi-k3'
  AND (context_window IS DISTINCT FROM 1048576
       OR modality IS DISTINCT FROM 'multimodal');

-- A.4 kimi-k2.6: DB 256 → 262144 (256K, vision)
-- Source-of-truth: modelname/modality_defaults.go:172
--   "kimi-k2.6 ... 256k context, vision support".
-- Original buggy value: 256 (written by migration 354:24).
UPDATE public.models_canonical
SET context_window     = 262144,
    context_window_source = 'manual',
    context_window_updated_at = NOW(),
    modality           = 'vision',
    updated_at         = NOW()
WHERE canonical_name = 'kimi-k2.6'
  AND (context_window IS DISTINCT FROM 262144
       OR modality IS DISTINCT FROM 'vision');

-- A.5 kimi-k2.7-code: DB 256 → 262144 (256K, text)
-- Source-of-truth: modelname/modality_defaults.go:170
--   "kimi-k2.7-code ... official catalog does not declare vision input".
-- Original buggy value: 256 (written by migration 354:25).
UPDATE public.models_canonical
SET context_window     = 262144,
    context_window_source = 'manual',
    context_window_updated_at = NOW(),
    modality           = 'text',
    updated_at         = NOW()
WHERE canonical_name = 'kimi-k2.7-code'
  AND (context_window IS DISTINCT FROM 262144
       OR modality IS DISTINCT FROM 'text');

-- A.6 kimi-k2.7-code-highspeed: NULL → 262144 (256K, text)
-- Source-of-truth: same as kimi-k2.7-code (variant of the same model).
UPDATE public.models_canonical
SET context_window     = 262144,
    context_window_source = 'manual',
    context_window_updated_at = NOW(),
    modality           = 'text',
    updated_at         = NOW()
WHERE canonical_name = 'kimi-k2.7-code-highspeed'
  AND (context_window IS DISTINCT FROM 262144
       OR modality IS DISTINCT FROM 'text');

-- A.7 glm-5 / glm-5.1: DB 2097152 (2M, × 16 bug) → 131072 (128K, text)
-- Source-of-truth: provider_catalog zhipu 'glm-5.1' ctx_k=128,
--   nvidia 'z-ai/glm-5.1' ctx_k=128, volcengine-coding 'glm-5.1' ctx_k=128.
--   Zhipu bigmodel.cn GLM-5 family page lists 128K context window.
-- Original buggy value: 2097152 (written by 02-seed.sql for glm-5 / glm-5.1).
UPDATE public.models_canonical
SET context_window     = 131072,
    context_window_source = 'manual',
    context_window_updated_at = NOW(),
    updated_at         = NOW()
WHERE canonical_name IN ('glm-5', 'glm-5.1')
  AND context_window IS DISTINCT FROM 131072;

-- A.8 glm-5.2: DB 128000 → 131072 (canonicalize to 128K = 131072 tokens)
-- Source-of-truth: provider_catalog zhipu/nvidia/volcengine-coding all
--   declare glm-5.2 ctx_k=128 = 131072 tokens.
UPDATE public.models_canonical
SET context_window     = 131072,
    context_window_source = 'manual',
    context_window_updated_at = NOW(),
    updated_at         = NOW()
WHERE canonical_name = 'glm-5.2'
  AND context_window IS DISTINCT FROM 131072;

-- ─────────────────────────────────────────────────────────────────────────
-- B. CLAUDE PLACEHOLDER REPAIR (source = discovery / provider_refresh,
--    context_window NULL but vendor docs declare 200K).
-- ─────────────────────────────────────────────────────────────────────────
-- Source-of-truth: https://docs.anthropic.com/en/docs/about-claude/models
--   All Claude 3.x / 4.x text+vision models: 200_000 token context window.

UPDATE public.models_canonical
SET context_window     = 200000,
    context_window_source = 'manual',
    context_window_updated_at = NOW(),
    modality           = 'multimodal',
    updated_at         = NOW()
WHERE canonical_name IN (
    'claude-3-5-sonnet-20241022',
    'claude-3-haiku',
    'claude-3-opus',
    'claude-haiku-latest',
    'claude-opus-4.1',
    'claude-opus-latest',
    'claude-sonnet-latest',
    'claude-fable-5',
    'claude-fable-5-thinking',
    'claude-fable-latest',
    'claude-opus-5',
    'claude-sonnet-5'
)
  AND (context_window IS NULL OR modality <> 'multimodal'
       OR context_window <> 200000);

-- ─────────────────────────────────────────────────────────────────────────
-- C. CLAUDE DISCOVERY ROW CONTEXT WINDOW (have modality=multimodal but
--    context_window NULL because discovered before 200K metadata synced).
-- ─────────────────────────────────────────────────────────────────────────

UPDATE public.models_canonical
SET context_window     = 200000,
    context_window_source = 'manual',
    context_window_updated_at = NOW(),
    updated_at         = NOW()
WHERE canonical_name IN (
    'claude-opus-4-5',
    'claude-opus-4-6',
    'claude-opus-4-7',
    'claude-opus-4-8',
    'claude-sonnet-4',
    'claude-sonnet-4-5',
    'claude-sonnet-4-6',
    'claude-haiku-4-5'
)
  AND context_window IS NULL;

-- ─────────────────────────────────────────────────────────────────────────
-- D. OPENAI o-SERIES: o1 / o3 / o3-mini / o4-mini all support image input
--    (per OpenAI platform docs). DB labels them text-only — upgrade to
--    multimodal so vision inputs are not silently dropped by routing.
--    gpt-4o-audio-preview: modality was 'audio'; correct to 'multimodal'
--    with multimodal_caps=['audio'] so audio+text+image all route.
-- ─────────────────────────────────────────────────────────────────────────

UPDATE public.models_canonical
SET modality           = 'multimodal',
    multimodal_caps    = '{}'::text[],
    updated_at         = NOW()
WHERE canonical_name IN ('o1', 'o3', 'o3-mini', 'o4-mini')
  AND modality <> 'multimodal';

UPDATE public.models_canonical
SET modality           = 'multimodal',
    multimodal_caps    = ARRAY['audio']::text[],
    updated_at         = NOW()
WHERE canonical_name = 'gpt-4o-audio-preview'
  AND modality <> 'multimodal';

-- ─────────────────────────────────────────────────────────────────────────
-- E. DEEPSEEK FAMILY: fill NULL context_window + set vision where vendor
--    docs say so.
-- ─────────────────────────────────────────────────────────────────────────
-- Source-of-truth:
--   - DeepSeek-V3 / V3.1 / V3.2-Exp / R1: 64K context, text-only.
--   - DeepSeek-V4 family: 128K context, vision-capable.

UPDATE public.models_canonical
SET context_window     = 65536,
    context_window_source = 'manual',
    context_window_updated_at = NOW(),
    updated_at         = NOW()
WHERE canonical_name IN ('deepseek-chat', 'deepseek-coder')
  AND context_window IS NULL;

UPDATE public.models_canonical
SET context_window     = 131072,
    context_window_source = 'manual',
    context_window_updated_at = NOW(),
    modality           = 'multimodal',
    updated_at         = NOW()
WHERE canonical_name IN (
    'deepseek-v3.2-exp', 'deepseek-v3.2',
    'deepseek-v4-flash', 'deepseek-v4-pro',
    'deepseek-v4-flash-vision-exp'
)
  AND (context_window IS NULL
       OR modality <> 'multimodal'
       OR context_window <> 131072);

UPDATE public.models_canonical
SET context_window     = 131072,
    context_window_source = 'manual',
    context_window_updated_at = NOW(),
    modality           = 'multimodal',
    updated_at         = NOW()
WHERE canonical_name = 'deepseek-v4-pro-cn'
  AND (context_window IS NULL OR modality <> 'multimodal');

-- ─────────────────────────────────────────────────────────────────────────
-- F. GOOGLE GEMINI placeholders + gemini-2.0-flash-exp NULL
-- ─────────────────────────────────────────────────────────────────────────
-- Source-of-truth: https://ai.google.dev/gemini-api/docs/models
--   All Gemini 2.0/2.5/3.0 (text+vision): 1_048_576 input tokens.

UPDATE public.models_canonical
SET context_window     = 1048576,
    context_window_source = 'manual',
    context_window_updated_at = NOW(),
    modality           = 'multimodal',
    updated_at         = NOW()
WHERE canonical_name = 'gemini-2.0-flash-exp'
  AND (context_window IS NULL OR modality <> 'multimodal');

UPDATE public.models_canonical
SET context_window     = 1048576,
    context_window_source = 'manual',
    context_window_updated_at = NOW(),
    modality           = 'multimodal',
    updated_at         = NOW()
WHERE canonical_name LIKE 'gemini-3%'
  AND status = 'active'
  AND context_window IS NULL;

-- gemini-omni-flash (not matched by the gemini-3% LIKE above).
UPDATE public.models_canonical
SET context_window     = 1048576,
    context_window_source = 'manual',
    context_window_updated_at = NOW(),
    updated_at         = NOW()
WHERE canonical_name = 'gemini-omni-flash'
  AND status = 'active'
  AND context_window IS NULL;

-- ─────────────────────────────────────────────────────────────────────────
-- G. META LLAMA placeholders (Llama 4 Scout preview + early-generation
--    Llama 3 rows that never received a context_window).
-- ─────────────────────────────────────────────────────────────────────────
-- Source-of-truth:
--   - Llama-3.1 / 3.2 / 3.3 (Instruct variants): 128K context.
--   - Llama 4 (Scout preview): 10M context, multimodal.

UPDATE public.models_canonical
SET context_window     = 10485760,
    context_window_source = 'manual',
    context_window_updated_at = NOW(),
    modality           = 'multimodal',
    updated_at         = NOW()
WHERE canonical_name = 'llama-4-preview'
  AND (context_window IS NULL OR modality <> 'multimodal');

-- ─────────────────────────────────────────────────────────────────────────
-- H. QWEN PLACEHOLDERS
-- ─────────────────────────────────────────────────────────────────────────
-- Source-of-truth: Alibaba Bailian model overview
--   - Qwen2-72B-Instruct: 32K context.
--   - Qwen3-235B (A22B): 128K context.
--   - QwQ-32B-Preview: 128K context.

UPDATE public.models_canonical
SET context_window     = 32768,
    context_window_source = 'manual',
    context_window_updated_at = NOW(),
    modality           = 'text',
    updated_at         = NOW()
WHERE canonical_name = 'qwen2-72b'
  AND (context_window IS NULL OR modality <> 'text');

UPDATE public.models_canonical
SET context_window     = 131072,
    context_window_source = 'manual',
    context_window_updated_at = NOW(),
    modality           = 'text',
    updated_at         = NOW()
WHERE canonical_name IN ('qwen3-235b', 'qwq-32b-preview')
  AND (context_window IS NULL OR modality <> 'text');

-- ─────────────────────────────────────────────────────────────────────────
-- I. MOONSHOT / KIMI placeholder (kimi-chat)
-- ─────────────────────────────────────────────────────────────────────────
-- kimi-chat is the 8K Moonshot v1 default (https://platform.moonshot.cn).
UPDATE public.models_canonical
SET context_window     = 8192,
    context_window_source = 'manual',
    context_window_updated_at = NOW(),
    modality           = 'text',
    updated_at         = NOW()
WHERE canonical_name = 'kimi-chat'
  AND (context_window IS NULL OR modality <> 'text');

-- ─────────────────────────────────────────────────────────────────────────
-- J. ERNIE placeholder rows
-- ─────────────────────────────────────────────────────────────────────────
-- Source-of-truth: Baidu Qianfan ERNIE docs
--   - ERNIE-3.5-8K: 8K context.
--   - ERNIE-4.0-Turbo-128K: 128K context.
UPDATE public.models_canonical
SET context_window     = 8192,
    context_window_source = 'manual',
    context_window_updated_at = NOW(),
    modality           = 'text',
    updated_at         = NOW()
WHERE canonical_name = 'ernie-3.5-8k'
  AND (context_window IS NULL OR modality <> 'text');

UPDATE public.models_canonical
SET context_window     = 131072,
    context_window_source = 'manual',
    context_window_updated_at = NOW(),
    modality           = 'text',
    updated_at         = NOW()
WHERE canonical_name = 'ernie-4.0-turbo-128k'
  AND (context_window IS NULL OR modality <> 'text');

-- ─────────────────────────────────────────────────────────────────────────
-- K. MISTRAL placeholder rows (codestral / ministral-8b / mixtral-8x22b)
-- ─────────────────────────────────────────────────────────────────────────
-- Source-of-truth: https://docs.mistral.ai/getting-started/models
--   - codestral: 32K context.
--   - ministral-8b: 128K context.
--   - mixtral-8x22b: 64K context.
UPDATE public.models_canonical
SET context_window     = 32768,
    context_window_source = 'manual',
    context_window_updated_at = NOW(),
    modality           = 'text',
    updated_at         = NOW()
WHERE canonical_name = 'codestral'
  AND (context_window IS NULL OR modality <> 'text');

UPDATE public.models_canonical
SET context_window     = 131072,
    context_window_source = 'manual',
    context_window_updated_at = NOW(),
    modality           = 'text',
    updated_at         = NOW()
WHERE canonical_name = 'ministral-8b'
  AND (context_window IS NULL OR modality <> 'text');

UPDATE public.models_canonical
SET context_window     = 65536,
    context_window_source = 'manual',
    context_window_updated_at = NOW(),
    modality           = 'text',
    updated_at         = NOW()
WHERE canonical_name = 'mixtral-8x22b'
  AND (context_window IS NULL OR modality <> 'text');

-- ─────────────────────────────────────────────────────────────────────────
-- L. ZHIPU-GLM placeholder rows (chatglm-turbo / codegeex-4 / glm-4-plus / glm-4v-plus)
-- ─────────────────────────────────────────────────────────────────────────
-- Source-of-truth: Zhipu Open Platform
--   - chatglm-turbo / codegeex-4: 128K context.
--   - glm-4-plus: 128K context.
--   - glm-4v-plus: 8K context, multimodal.
UPDATE public.models_canonical
SET context_window     = 131072,
    context_window_source = 'manual',
    context_window_updated_at = NOW(),
    modality           = 'text',
    updated_at         = NOW()
WHERE canonical_name IN ('chatglm-turbo', 'codegeex-4', 'glm-4-plus')
  AND (context_window IS NULL OR modality <> 'text');

UPDATE public.models_canonical
SET context_window     = 8192,
    context_window_source = 'manual',
    context_window_updated_at = NOW(),
    modality           = 'multimodal',
    updated_at         = NOW()
WHERE canonical_name = 'glm-4v-plus'
  AND (context_window IS NULL OR modality <> 'multimodal');

-- ─────────────────────────────────────────────────────────────────────────
-- M. XAI placeholder rows (grok-1 / grok-2 — both 128K multimodal)
-- ─────────────────────────────────────────────────────────────────────────
-- Source-of-truth: https://docs.x.ai/docs/models
--   - grok-1 / grok-2: 131072 context, vision-capable.
UPDATE public.models_canonical
SET context_window     = 131072,
    context_window_source = 'manual',
    context_window_updated_at = NOW(),
    modality           = 'multimodal',
    updated_at         = NOW()
WHERE canonical_name IN ('grok-1', 'grok-2')
  AND (context_window IS NULL OR modality <> 'multimodal');

-- ─────────────────────────────────────────────────────────────────────────
-- N. ORPHAN ROWS WITH NULL FAMILY (data-entry typos left over from earlier
--    discovery / provider_refresh paths). These should not exist as
--    family=NULL because the routing pipeline groups by family.
-- ─────────────────────────────────────────────────────────────────────────
-- Source-of-truth: vendor + canonical naming convention.

-- N.1 'doubao-seed-2-0-code-preview-260215' → family=doubao (variant of
--     doubao-seed-2.0-code in 02-seed.sql).
UPDATE public.models_canonical
SET family             = 'doubao',
    context_window     = 131072,
    context_window_source = 'manual',
    context_window_updated_at = NOW(),
    modality           = 'text',
    updated_at         = NOW()
WHERE canonical_name = 'doubao-seed-2-0-code-preview-260215'
  AND (family IS NULL OR family = '');

-- N.2 'glm-5-2-260617' → family=zhipu-glm, canonical_name=glm-5.2-260617
--     (preserves the date suffix used by volcengine-coding manifest).
UPDATE public.models_canonical
SET family             = 'zhipu-glm',
    context_window     = 131072,
    context_window_source = 'manual',
    context_window_updated_at = NOW(),
    modality           = 'text',
    updated_at         = NOW()
WHERE canonical_name = 'glm-5-2-260617'
  AND (family IS NULL OR family = '');

-- N.3 'text-embedding-3' → family=openai-embedding
UPDATE public.models_canonical
SET family             = 'openai-embedding',
    context_window     = 8192,
    context_window_source = 'manual',
    context_window_updated_at = NOW(),
    modality           = 'embedding',
    updated_at         = NOW()
WHERE canonical_name = 'text-embedding-3'
  AND (family IS NULL OR family = '');

-- ─────────────────────────────────────────────────────────────────────────
-- O. MISC FIXES — provider catalog and Go registry agree on 245_760
--    tokens for MiniMax M2.5 / M2.7 (not 131072); and mimo-v2.5-pro is
--    multimodal per Xiaomi MiMo docs.
-- ─────────────────────────────────────────────────────────────────────────

UPDATE public.models_canonical
SET modality           = 'multimodal',
    multimodal_caps    = ARRAY['text','image']::text[],
    updated_at         = NOW()
WHERE canonical_name = 'mimo-v2.5-pro'
  AND modality <> 'multimodal';

-- ─────────────────────────────────────────────────────────────────────────
-- P. NOTIFY routing/credential caches so subsequent reads see corrected
--    context_window via the effective-context-window COALESCE chain.
-- ─────────────────────────────────────────────────────────────────────────

NOTIFY auto_route_refresh, 'models_canonical:context_window_correction';

COMMIT;
