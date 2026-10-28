-- 915: recent_cache_hit_rate — P1-B-③ (audit 2026-10-28 §4): the routing
-- score gains a prompt-cache-affinity dimension. The (credential, model)
-- historical cached_tokens share is the gateway-side cache-aware routing
-- signal (llm-d / SGLang do this within one serving fleet; this repo does it
-- across providers): a credential whose upstream honors prompt caching for
-- this model is worth more than its raw price suggests (cached input tokens are
-- billed at the discount, and TTFT drops). Sticky retention and this score
-- corroborate each other: sticky = keep the affinity; score = quantify it.
--
-- Shape mirrors recent_success_rate (migration 428): a STABLE LATERAL-friendly
-- function reading the last-N business rows from request_logs_hot so the
-- candidate scan stays an index descent per candidate. Same probe exclusions.
-- Return contract: (rate, samples); rate = share of INPUT tokens served from
-- cache (cache_read_tokens / prompt_tokens, clamped to [0,1]), NULL when the
-- window has no rows or no input tokens (caller treats NULL/low-samples as
-- "no signal → neutral", fail-open).
--
-- Denominator note: prompt_tokens ⊇ cached_tokens (OpenAI convention, see
-- domains/streaming/usage.go). Rows where prompt_tokens IS NULL OR <= 0 are
-- excluded from the ratio's numerator AND denominator (a cached_tokens value
-- without a positive prompt_tokens total is a provider quirk, not a rate).
\set ON_ERROR_STOP on

CREATE OR REPLACE FUNCTION public.recent_cache_hit_rate(
    p_credential_id bigint,
    p_raw_model text,
    p_sample_n integer DEFAULT 50,
    p_window_hours integer DEFAULT 3
) RETURNS TABLE(rate double precision, samples integer)
LANGUAGE sql
STABLE
AS $$
    WITH recent AS (
        SELECT prompt_tokens, cache_read_tokens
        FROM public.request_logs_hot
        WHERE credential_id = p_credential_id
          AND lower(COALESCE(outbound_model, client_model)) = lower(p_raw_model)
          AND ts > NOW() - (p_window_hours || ' hours')::interval
          AND COALESCE(task_type, '') <> 'probe_triggered'
          AND COALESCE(origin_stage, '') NOT IN ('self_check', 'node_probe', 'system_health')
          AND request_id NOT LIKE 'probe-%'
          AND prompt_tokens IS NOT NULL AND prompt_tokens > 0
        ORDER BY ts DESC
        LIMIT p_sample_n
    )
    SELECT (SUM(LEAST(GREATEST(COALESCE(cache_read_tokens, 0), 0), prompt_tokens))::double precision
            / SUM(prompt_tokens))::double precision,
           COUNT(*)::int
    FROM recent;
$$;

COMMENT ON FUNCTION public.recent_cache_hit_rate(bigint, text, integer, integer) IS
    'Recent business-route input-token cache-hit share (cache_read/prompt, token-weighted) excluding probe and self-check rows; NULL when no qualified rows. P1-B-③ prompt-cache-aware routing.';
