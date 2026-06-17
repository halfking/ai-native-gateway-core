-- 013_compression_columns.sql
-- Round 47 (2026-06-18) — compression v7 T1: parent-child request chain tracking.
-- See docs/llm-gateway-go/2026-06-18-compression-v7-final.md §3.1.
--
-- 4 new columns on request_logs:
--   parent_request_id   TEXT  — the pre-compression request_id (NULL = no compression)
--   compression_reason  TEXT  — 'mode_1_auto_threshold' | 'mode_2_on_4xx' | NULL
--   compression_strategy TEXT  — 'mechanical_trim' | 'memora_l1_inject' | 'llm_summary' | 'noop'
--   compression_meta    JSONB — {tokens_before, tokens_after, ...} see v7 doc §3.2
--
-- 1 partial index on parent_request_id for the "show me this compressed request's
-- downstream chain" SQL query path.
--
-- 1 CHECK constraint enforcing the v7 §6 invariant:
--   parent_request_id IS NULL OR compression_reason IS NOT NULL
-- (a child row must explain WHY it was created)
--
-- Idempotency: ADD COLUMN IF NOT EXISTS / CREATE INDEX IF NOT EXISTS / CREATE
-- OR REPLACE for the constraint via DO block (PG 9.6+ supports ADD CONSTRAINT
-- only as table rewrite; safer to drop+recreate here since 007 already proved
-- the pattern).
--
-- RLS: request_logs is already tenant_isolation_request_logs (007_maas_billing.sql
-- line 121). The 4 new columns are covered by the existing policy (it filters
-- whole rows by tenant_id, not by column).

ALTER TABLE public.request_logs
    ADD COLUMN IF NOT EXISTS parent_request_id   TEXT,
    ADD COLUMN IF NOT EXISTS compression_reason  TEXT,
    ADD COLUMN IF NOT EXISTS compression_strategy TEXT,
    ADD COLUMN IF NOT EXISTS compression_meta    JSONB;

-- CHECK: parent_request_id requires a reason. Single-level chain invariant.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'chk_compression_parent_single'
          AND conrelid = 'public.request_logs'::regclass
    ) THEN
        ALTER TABLE public.request_logs
            ADD CONSTRAINT chk_compression_parent_single
            CHECK (parent_request_id IS NULL OR compression_reason IS NOT NULL);
    END IF;
END$$;

-- Partial index: only rows with a parent (i.e. actually compressed). The hot
-- query is "show me all child requests compressed from this parent".
CREATE INDEX IF NOT EXISTS idx_request_logs_parent_ts
    ON public.request_logs (parent_request_id, ts DESC)
    WHERE parent_request_id IS NOT NULL;

COMMENT ON COLUMN public.request_logs.parent_request_id IS
    'Round 47 (2026-06-18): the pre-compression request_id when compressor rewrote the body. NULL for uncompressed rows. Single-level chain only (child has at most 1 parent).';
COMMENT ON COLUMN public.request_logs.compression_reason IS
    'Round 47 (2026-06-18): why compression fired. mode_1_auto_threshold = body > cand.ContextWindow × 0.8 × 3.5 (LLM_GATEWAY_COMPRESSION_MODE=1). mode_2_on_4xx = upstream 4xx context_length_exceeded (LLM_GATEWAY_COMPRESSION_MODE=2). NULL = no compression.';
COMMENT ON COLUMN public.request_logs.compression_strategy IS
    'Round 47 (2026-06-18): which decompression path succeeded. mechanical_trim = oldest-pair drop (transform/ctx_compress.go). memora_l1_inject = dynamic_context user message from Memora /product/search. llm_summary = 1M-context model summary. noop = attempted but skipped (e.g. warmup_min_facts guard).';
COMMENT ON COLUMN public.request_logs.compression_meta IS
    'Round 47 (2026-06-18): compression telemetry. Schema: {tokens_before, tokens_after, bytes_before, bytes_after, context_window_used, threshold_bytes, dropped_messages, summary_chars, model_used, latency_ms, memora_facts_used, warmup_skipped, first_user_retained, system_retained, reason_detail}. See v7 §3.2.';