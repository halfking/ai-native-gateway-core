-- 962 down: 还原 962 之前的函数体（谓词回到「正则守卫 + 数值相等」形态）
--
-- ★ 语义面**完全等价**（见 up 文件的说明）：正则不通过时两种写法都被过滤掉。
--   ⇒ down 只是让 planner **用不上** idx_session_turns_hot_credential_ts
--      ⇒ 性能回到 13.2 秒/次（真库实测），功能与结果不变。
-- ★ 因此 down 存在的意义是**可回退性**，不是「修回一个错误」——
--   ★ 若回退后出现凭据监控摘要 500，那正是 962 要修的那个症状。
\set ON_ERROR_STOP on

BEGIN;

CREATE OR REPLACE FUNCTION public.recent_success_rate(
    p_credential_id bigint,
    p_raw_model text,
    p_sample_n integer DEFAULT 50,
    p_window_hours integer DEFAULT 3
) RETURNS TABLE(rate double precision, samples integer)
LANGUAGE sql
STABLE
AS $$
    WITH recent AS (
        SELECT success, ts
        FROM public.session_turns_hot
        WHERE credential_id ~ '^[0-9]+$'
          AND credential_id::bigint = p_credential_id
          AND lower(COALESCE(model, canonical_model)) = lower(p_raw_model)
          AND ts > NOW() - (p_window_hours || ' hours')::interval
          AND COALESCE(task_type, '') <> 'probe_triggered'
          AND COALESCE(origin_stage, '') NOT IN ('self_check', 'node_probe', 'system_health')
          AND request_id NOT LIKE 'probe-%'
    )
    SELECT AVG(CASE WHEN success THEN 1.0 ELSE 0.0 END)::double precision,
           COUNT(*)::int
    FROM (
        SELECT success FROM recent ORDER BY ts DESC LIMIT p_sample_n
    ) newest_samples;
$$;

COMMENT ON FUNCTION public.recent_success_rate(bigint, text, integer, integer) IS
    'Recent business-route success fraction excluding probe and self-check rows.';

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

-- 记账：down 不删记账行（与 959/960/961 同款口径：down 是「可回退」不是「抹掉历史」）。
-- ⇒ 迁移链的 version 连续性由 schema_migrations 读，不能因为 down 就少一行。

COMMIT;
