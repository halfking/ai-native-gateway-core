-- Permit canonical errorsx categories and the current credential self-check
-- selection strategies while keeping historical rows readable.
ALTER TABLE public.self_check_runs
    DROP CONSTRAINT IF EXISTS self_check_runs_error_type_check,
    DROP CONSTRAINT IF EXISTS self_check_runs_selection_strategy_check;

ALTER TABLE public.self_check_runs
    ADD CONSTRAINT self_check_runs_error_type_check CHECK (
        error_type IS NULL OR error_type LIKE 'http_%' OR error_type = ANY (ARRAY[
            'none', 'timeout', 'network', 'transient', 'rate_limit', 'auth', 'auth_revoked',
            'quota', 'quota_periodic', 'quota_balance', 'quota_permanent', 'upstream_down',
            'upstream_overloaded', 'concurrent', 'stream_timeout', 'model_not_found',
            'model_deprecated', 'unsupported_feature', 'context_length_exceeded',
            'content_filter', 'tool_call_id_mismatch', 'empty_response', 'conversion_error',
            'upstream_context_loss', 'no_available_channel', 'canceled', 'client_bug',
            'parse_error', 'internal', 'unattributed', 'upstream_fail',
            -- 2026-11-11（12h 审计 C-1/C-2 轮）：与 errorsx 闭集对齐。
            -- ★ 本文件此前停在 **31 项** ⇒ 任何在 startup 链（929/930/957）
            -- ★ **之后**执行它的通道都会把约束**回退**到 31 项 ⇒ 上游 4xx 落
            -- ★ `rate_limit_observed` ⇒写入被 **23514** 拒 ⇒ run 行停在
            -- ★ `status='running'` / `error_type=NULL` 的**半写态**
            -- ★ （与 957 修的故障完全同形）。
            -- ★ 补齐的是 929/930/957 陆续登记的 8 个 kind。
            'request_rejected', 'model_unavailable', 'rate_limit_observed',
            'circuit_open', 'fp_slot_saturated', 'first_byte_timeout',
            'reply_loop', 'account_restricted'
        ])
    ) NOT VALID,
    ADD CONSTRAINT self_check_runs_selection_strategy_check CHECK (
        selection_strategy IS NULL OR selection_strategy LIKE 'fallback_%' OR selection_strategy = ANY (ARRAY[
            'most_used', 'random', 'featured', 'recent', 'common_7d', 'failed_model', 'no_eligible_model'
        ])
    ) NOT VALID;

COMMENT ON COLUMN public.self_check_runs.error_type IS
    'Canonical errorsx error kind or legacy http diagnostic from credential self-check.';
COMMENT ON COLUMN public.self_check_runs.selection_strategy IS
    'Primary selection source: common_7d/recent/featured plus failed-model follow-ups.';
