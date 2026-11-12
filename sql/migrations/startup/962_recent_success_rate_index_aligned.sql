-- 962: recent_success_rate / recent_cache_hit_rate —— 谓词改成与既有函数索引**逐字同形**
--     （修「凭据监控摘要在 245/154 上必 500」）
--
-- ★ 本轮（2026-11-12 48h 审计第五轮）在 245/154 的验收冒烟里抓到**同一个 500**
--   两台都复现：`GET /api/credentials/monitor-summary` ⇒ 500
--   响应体 {"error":{"detail":"internal error (see server logs)"}}
--   日志：`admin handler internal error` caller=credential_monitor.go:762
--         err="rows iteration failed: timeout: context deadline exceeded"
--         duration_ms=15002
-- ⇒ 根因链（三步实测，证据在主档 §六之三）：
--   ① 该端点的 context 是 15s（admin/credential_monitor.go:687）⇒ 慢于 15s 即 500；
--   ② 慢在哪：`admin/credential_monitor.go` 的 summary 模式对**每个
--      (凭据 × 模型) 行**执行一次 `CROSS JOIN LATERAL recent_success_rate(c.id, mo.raw_model_name, 50)`
--      ⇒ 共 1959 次函数调用（74 个活跃凭据 × 2015 条 model_offers）；
--   ③ 函数体在 `public.session_turns_hot` 上的过滤条件写成
--        WHERE credential_id ~ '^[0-9]+$'
--          AND credential_id::bigint = p_credential_id
--      而**既有索引**是
--        idx_session_turns_hot_credential_ts
--          ON session_turns_hot ((CASE WHEN credential_id ~ '^[0-9]+$' THEN credential_id::bigint END), ts DESC)
--      ⇒ ★ **索引表达式与谓词不等价 ⇒ planner 用不上这个索引**
--      ⇒ 每次调用退化成「扫 3 小时窗全量（实测 Rows Removed by Filter: 1876）再过滤」
--      ⇒ 1959 次 × 全窗扫描 ⇒ 单次查询 13.2 秒（真库 EXPLAIN ANALYZE 实测）
--      ⇒ 稳定越过 15s ⇒ 500。
--
-- ★★ 修法（**零语义变化**，只把谓词改写成与索引表达式同形）：
--   WHERE (CASE WHEN credential_id ~ '^[0-9]+$' THEN credential_id::bigint ELSE NULL::bigint END)
--         = p_credential_id
--   ⇒ 正则不通过时 CASE 落在 NULL 分支 ⇒ `NULL = p_credential_id` 为 NULL（非真）
--      ⇒ 被过滤掉 ⇒ **与原来「正则守卫 + 数值相等」完全等价**；
--   ⇒ planner 现在能命中 idx_session_turns_hot_credential_ts
--      （真库实测：Index Cond 命中、Rows Removed by Filter 从 1876 降到 18）
--      ⇒ 单次查询 13184 ms → **113 ms（116 倍）**。
--
-- ★★ 本迁移同时承担第二件事：**把在役函数体补回仓内**。
--   `recent_success_rate` 的**在役形态（查 session_turns_hot、credential_id 为 text）**
--   在本仓**任何迁移里都不存在**：仓内只有 428 的 v1 形态（查 request_logs_hot、
--   credential_id 为 bigint），而 v1 族已被 890 整体退役 ⇒
--   ⇒ **仓内无法复现在役函数体**（915 的注释自陈它不得不"镜像在役形态"绕开 42P01：
--      「211 上 v1 族已整体退役 ⇒ PG17 创建时校验函数体 ⇒ 42P01」）
--   ⇒ ★ 新装/重建库按 428 建函数会引用已不存在的表 ⇒ 迁移链**不可复现**
--   ⇒ 本迁移用「在役形态 + 索引同形谓词」重建两个函数 ⇒ 仓内重新成为真值源。
--
-- ★ 两条函数体一起改的理由：`recent_cache_hit_rate` 是**路由打分维度**
--   （domains/streaming/executors/router_scoring.go:983 引用），它用**同款**
--   不可命中索引的谓词 ⇒ 路由候选评估也在付同样的代价 ⇒ 只修一半＝缺陷仍在。
--
-- ★ 不改索引、不改表、不改函数签名、不改返回契约、不改采样窗口
--   （p_sample_n=50 / p_window_hours=3）⇒ 语义面零变化。
-- ★ 记账：自登记（≥952 起是约定不是可选项）。
\set ON_ERROR_STOP on

BEGIN;

-- ── ① recent_success_rate ────────────────────────────────────────────────
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
        WHERE (CASE WHEN credential_id ~ '^[0-9]+$' THEN credential_id::bigint ELSE NULL::bigint END)
              = p_credential_id
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
    'Recent business-route success fraction excluding probe and self-check rows. credential_id is stored as text; the predicate is written in the SAME shape as idx_session_turns_hot_credential_ts (CASE ... ELSE NULL::bigint END) so the planner can use that index — the previous form (regex guard AND credential_id::bigint = $1) could not match the index expression and degraded into a full 3h-window scan per call (2026-11-12 audit round 5).';

-- ── ② recent_cache_hit_rate（路由打分维度，同款谓词）────────────────────────
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
        WHERE (CASE WHEN credential_id ~ '^[0-9]+$' THEN credential_id::bigint ELSE NULL::bigint END)
              = p_credential_id
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
    'Recent business-route input-token cache-hit share (cache_read/prompt, token-weighted) over session_turns_hot, excluding probe and self-check rows; NULL when no qualified rows. P1-B-③ prompt-cache-aware routing. Predicate aligned with idx_session_turns_hot_credential_ts (2026-11-12 audit round 5).';

-- ── 记账 ────────────────────────────────────────────────────────────────
INSERT INTO public.schema_migrations (version, description)
VALUES ('962', 'recent_success_rate + recent_cache_hit_rate: predicate re-shaped to match idx_session_turns_hot_credential_ts (fixes credential-monitor-summary 500 on 245/154; 13.2s -> 0.113s measured) and restores the in-service session_turns_hot function bodies into the repo')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
