-- Migration 611 DOWN: undo canonical model context_window + modality corrections.
--
-- Scope: restores the buggy context_window values that 611 corrected, so
--   running this script is exactly equivalent to skipping the up migration.
--   Modality and multimodal_caps are also restored to their pre-611 values
--   where the change came from 611 (NOT 352 / 354 / 356).
--
-- WARNING: this down is for development parity only. Production should
--   NOT roll back to the buggy values; instead, run the up of 611 again.
--
-- Idempotent: each block only touches the post-611 state.

BEGIN;

-- A: kimi-k3 / kimi-k2.6 / kimi-k2.7-code(-highspeed) — restore 1000 / 256
UPDATE public.models_canonical
SET context_window = 1000, modality = 'multimodal', updated_at = NOW()
WHERE canonical_name = 'kimi-k3' AND context_window = 1048576;

UPDATE public.models_canonical
SET context_window = 256, modality = 'vision', updated_at = NOW()
WHERE canonical_name = 'kimi-k2.6' AND context_window = 262144;

UPDATE public.models_canonical
SET context_window = 262144, modality = 'text', updated_at = NOW()
WHERE canonical_name IN ('kimi-k2.7-code', 'kimi-k2.7-code-highspeed')
  AND modality = 'text' AND context_window = 262144;
UPDATE public.models_canonical
SET context_window = 256, modality = 'text', updated_at = NOW()
WHERE canonical_name = 'kimi-k2.7-code' AND context_window = 256;
UPDATE public.models_canonical
SET context_window = NULL, modality = 'text', updated_at = NOW()
WHERE canonical_name = 'kimi-k2.7-code-highspeed' AND context_window IS NULL;

-- A: grok-4.6 → 500 / vision
UPDATE public.models_canonical
SET context_window = 500, modality = 'vision', updated_at = NOW()
WHERE canonical_name = 'grok-4.6' AND context_window = 262144;

-- A: glm-5.3 → 1000
UPDATE public.models_canonical
SET context_window = 1000, updated_at = NOW()
WHERE canonical_name = 'glm-5.3' AND context_window = 1048576;

-- A: glm-5 / glm-5.1 → 2097152
UPDATE public.models_canonical
SET context_window = 2097152, updated_at = NOW()
WHERE canonical_name IN ('glm-5', 'glm-5.1') AND context_window = 131072;

-- A: glm-5.2 → 128000
UPDATE public.models_canonical
SET context_window = 128000, updated_at = NOW()
WHERE canonical_name = 'glm-5.2' AND context_window = 131072;

-- B/C: Claude placeholders → NULL / original modality
UPDATE public.models_canonical
SET context_window = NULL, modality = 'text', updated_at = NOW()
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
) AND context_window = 200000;

UPDATE public.models_canonical
SET context_window = NULL, updated_at = NOW()
WHERE canonical_name IN (
    'claude-opus-4-5','claude-opus-4-6','claude-opus-4-7','claude-opus-4-8',
    'claude-sonnet-4','claude-sonnet-4-5','claude-sonnet-4-6','claude-haiku-4-5'
) AND context_window = 200000;

-- D: OpenAI o-series back to text
UPDATE public.models_canonical
SET modality = 'text', multimodal_caps = '{}', updated_at = NOW()
WHERE canonical_name IN ('o1','o3','o3-mini','o4-mini') AND modality = 'multimodal';

UPDATE public.models_canonical
SET modality = 'audio', multimodal_caps = '{}', updated_at = NOW()
WHERE canonical_name = 'gpt-4o-audio-preview' AND modality = 'multimodal';

-- E: DeepSeek — restore NULLs
UPDATE public.models_canonical
SET context_window = NULL, updated_at = NOW()
WHERE canonical_name IN ('deepseek-chat', 'deepseek-coder') AND context_window = 65536;

UPDATE public.models_canonical
SET context_window = NULL, modality = 'text', updated_at = NOW()
WHERE canonical_name IN ('deepseek-v3.2-exp','deepseek-v3.2','deepseek-v4-flash',
                         'deepseek-v4-pro','deepseek-v4-flash-vision-exp')
  AND context_window = 131072;

UPDATE public.models_canonical
SET context_window = NULL, modality = 'text', updated_at = NOW()
WHERE canonical_name = 'deepseek-v4-pro-cn' AND context_window = 131072;

-- F: Gemini placeholders
UPDATE public.models_canonical
SET context_window = NULL, modality = 'text', updated_at = NOW()
WHERE canonical_name = 'gemini-2.0-flash-exp' AND context_window = 1048576;

UPDATE public.models_canonical
SET context_window = NULL, updated_at = NOW()
WHERE canonical_name LIKE 'gemini-3%' AND context_window = 1048576;

UPDATE public.models_canonical
SET context_window = NULL, updated_at = NOW()
WHERE canonical_name = 'gemini-omni-flash' AND context_window = 1048576;

-- G: Llama 4 preview
UPDATE public.models_canonical
SET context_window = NULL, modality = 'text', updated_at = NOW()
WHERE canonical_name = 'llama-4-preview' AND context_window = 10485760;

-- H: Qwen placeholders
UPDATE public.models_canonical
SET context_window = NULL, modality = 'text', updated_at = NOW()
WHERE canonical_name IN ('qwen2-72b','qwen3-235b','qwq-32b-preview')
  AND context_window IN (32768,131072);

-- I/J/K/L/M: misc placeholders
UPDATE public.models_canonical
SET context_window = NULL, modality = 'text', updated_at = NOW()
WHERE canonical_name IN ('kimi-chat','ernie-3.5-8k','ernie-4.0-turbo-128k',
                         'codestral','ministral-8b','mixtral-8x22b',
                         'chatglm-turbo','codegeex-4','glm-4-plus')
  AND context_window IS NOT NULL;

UPDATE public.models_canonical
SET context_window = NULL, modality = 'text', updated_at = NOW()
WHERE canonical_name = 'glm-4v-plus' AND context_window = 8192;

UPDATE public.models_canonical
SET context_window = NULL, modality = 'text', updated_at = NOW()
WHERE canonical_name IN ('grok-1','grok-2') AND context_window = 131072;

-- N: orphan rows → family=NULL
UPDATE public.models_canonical
SET family = NULL, context_window = NULL, modality = 'text', updated_at = NOW()
WHERE canonical_name = 'doubao-seed-2-0-code-preview-260215';

UPDATE public.models_canonical
SET family = NULL, context_window = NULL, modality = 'text', updated_at = NOW()
WHERE canonical_name = 'glm-5-2-260617';

UPDATE public.models_canonical
SET family = NULL, context_window = NULL, modality = 'text', updated_at = NOW()
WHERE canonical_name = 'text-embedding-3';

-- O: mimo-v2.5-pro back to text
UPDATE public.models_canonical
SET modality = 'text', multimodal_caps = '{}', updated_at = NOW()
WHERE canonical_name = 'mimo-v2.5-pro' AND modality = 'multimodal';

-- Clear override provenance so the down is fully reversible
UPDATE public.models_canonical
SET context_window_source = 'catalog',
    context_window_updated_at = NULL,
    updated_at = NOW()
WHERE context_window_source = 'manual';

NOTIFY auto_route_refresh, 'models_canonical:context_window_correction_rollback';

COMMIT;
