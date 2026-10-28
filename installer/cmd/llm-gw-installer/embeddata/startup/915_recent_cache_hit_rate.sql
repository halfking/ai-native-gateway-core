-- 915: recent_cache_hit_rate — P1-B-③ (audit 2026-10-28 §4): the routing
-- score gains a prompt-cache-affinity dimension. The (credential, model)
-- historical cached_tokens share is the gateway-side cache-aware routing
-- signal (llm-d / SGLang do this within one serving fleet; this repo does it
-- across providers): a credential whose upstream honors prompt caching for
-- this model is worth more than its raw price suggests (cached input tokens
-- are billed at the discount, and TTFT drops). Sticky retention and this
-- score corroborate each other: sticky = keep the affinity; score = quantify
-- it.
--
-- ★ 211 生产实证修正（2026-10-28 routing_schema_error 轮）：首版照抄 428
-- 文件里的 request_logs_hot 形态，但 211 上 v1 族已整体退役（public 下
-- 0 张 request_logs* 表），PG17 创建时校验函数体 ⇒ 42P01。生产在役的
-- recent_success_rate 函数体早已改查会话族（public.session_turns_hot：
-- credential_id 为 text 存数字、model/canonical_model 列）。本版镜像该
-- 在役形态 —— 同一粒度、同一探针排除、同一列名来源。
--
-- Return contract: (rate, samples); rate = token-weighted share of INPUT
-- tokens served from cache (cache_read_tokens / prompt_tokens, per-row
-- clamped), NULL when the window has no qualified rows or no input tokens
-- (caller treats NULL/low-samples as "no signal → neutral", fail-open).
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
        SELECT prompt_tokens, cache_read_tokens, ts
        FROM public.session_turns_hot
        WHERE credential_id ~ '^[0-9]+$'
          AND credential_id::bigint = p_credential_id
          AND lower(COALESCE(model, canonical_model)) = lower(p_raw_model)
          AND ts > NOW() - (p_window_hours || ' hours')::interval
          AND COALESCE(task_type, '') <> 'probe_triggered'
          AND COALESCE(origin_stage, '') NOT IN ('self_check', 'node_probe', 'system_health')
          AND request_id NOT LIKE 'probe-%'
          AND prompt_tokens IS NOT NULL AND prompt_tokens > 0
        ORDER BY ts DESC
        LIMIT p_sample_n
    )
    SELECT (SUM(LEAST(GREATEST(COALESCE(cache_read_tokens, 0), 0), prompt_tokens))::double precision
            / NULLIF(SUM(prompt_tokens), 0))::double precision,
           COUNT(*)::int
    FROM recent;
$$;

COMMENT ON FUNCTION public.recent_cache_hit_rate(bigint, text, integer, integer) IS
    'Recent business-route input-token cache-hit share (cache_read/prompt, token-weighted) over session_turns_hot, excluding probe and self-check rows; NULL when no qualified rows. P1-B-③ prompt-cache-aware routing.';
