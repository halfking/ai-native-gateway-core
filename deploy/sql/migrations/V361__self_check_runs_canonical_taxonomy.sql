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
            'parse_error', 'internal', 'unattributed', 'upstream_fail'
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
