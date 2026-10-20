-- Rollback for 884_session_family_v_analytics_views.sql
-- 还原 3 张 v_* 分析视图的定义体：会话族 → v1 裸母表。
--
-- ⚠ 表名用 **860 之后的物理名** `_request_logs`（860 之前叫 `request_logs`）。
--   基线 schema 里的旧写法 `public.request_logs` 是**建视图当时**的名字；
--   860 改名后 PG 按 OID 跟随 ⇒ 视图实际读的是 `_request_logs`。
--   所以 down 必须是 `_request_logs`，否则回滚会因「表不存在」而失败。
--
-- 列名/列序/列类型与 884 之前完全一致 ⇒ `CREATE OR REPLACE VIEW` 可直接替换。
-- ⚠ 回滚后这 3 张视图**会变回空**（v1 族已停写并清空）——这是回滚的**预期**，
--   不是回滚失败；判据要按「定义体里重新出现 v1 读」来判，不要按行数判。

BEGIN;

SET LOCAL lock_timeout = '60s';
SET LOCAL statement_timeout = '5min';

CREATE OR REPLACE VIEW public.v_continuation_effectiveness AS
 SELECT date_trunc('hour'::text, ts) AS time_bucket,
    count(*) FILTER (WHERE (is_continuation = true)) AS continuation_requests,
    count(*) FILTER (WHERE (cached_response_id IS NOT NULL)) AS cache_hits,
    count(*) FILTER (WHERE ((is_continuation = true) AND (cached_response_id IS NULL))) AS cache_misses,
    round(((100.0 * (count(*) FILTER (WHERE (cached_response_id IS NOT NULL)))::numeric) / (NULLIF(count(*) FILTER (WHERE (is_continuation = true)), 0))::numeric), 2) AS cache_hit_rate,
    sum(COALESCE(context_size_tokens, 0)) FILTER (WHERE (cached_response_id IS NOT NULL)) AS tokens_saved
   FROM public._request_logs
  WHERE (ts > (now() - '24:00:00'::interval))
  GROUP BY (date_trunc('hour'::text, ts))
 HAVING (count(*) FILTER (WHERE (is_continuation = true)) > 0)
  ORDER BY (date_trunc('hour'::text, ts)) DESC;

COMMENT ON VIEW public.v_continuation_effectiveness IS 'Auto route: hourly continuation/cache-hit effectiveness. [rolled back by 884.down] Source restored to the v1 family (_request_logs).';

CREATE OR REPLACE VIEW public.v_node_switch_analysis AS
 SELECT date_trunc('hour'::text, ts) AS time_bucket,
    node_switch_count AS switches,
    count(*) AS request_count,
    count(*) FILTER (WHERE (success = true)) AS success_count,
    round(((100.0 * (count(*) FILTER (WHERE (success = true)))::numeric) / (count(*))::numeric), 2) AS success_rate,
    round((avg(latency_ms) / 1000.0), 2) AS avg_latency_seconds
   FROM public._request_logs
  WHERE ((ts > (now() - '24:00:00'::interval)) AND (node_switch_count >= 0))
  GROUP BY (date_trunc('hour'::text, ts)), node_switch_count
  ORDER BY (date_trunc('hour'::text, ts)) DESC, node_switch_count;

COMMENT ON VIEW public.v_node_switch_analysis IS 'Auto route: hourly node-switch analysis. [rolled back by 884.down] Source restored to the v1 family (_request_logs).';

CREATE OR REPLACE VIEW public.v_timeout_effectiveness AS
 SELECT date_trunc('hour'::text, ts) AS time_bucket,
    timeout_mode,
    count(*) AS total_requests,
    count(*) FILTER (WHERE (success = true)) AS success_count,
    count(*) FILTER (WHERE ((success = false) AND (error_kind ~~ '%timeout%'::text))) AS timeout_count,
    round(((100.0 * (count(*) FILTER (WHERE ((success = false) AND (error_kind ~~ '%timeout%'::text))))::numeric) / (count(*))::numeric), 2) AS timeout_rate,
    round(avg(effective_timeout_seconds), 2) AS avg_effective_timeout,
    round((avg(latency_ms) / 1000.0), 2) AS avg_latency_seconds
   FROM public._request_logs
  WHERE ((ts > (now() - '24:00:00'::interval)) AND (effective_timeout_seconds IS NOT NULL))
  GROUP BY (date_trunc('hour'::text, ts)), timeout_mode
  ORDER BY (date_trunc('hour'::text, ts)) DESC;

COMMENT ON VIEW public.v_timeout_effectiveness IS 'Auto route: hourly timeout effectiveness. [rolled back by 884.down] Source restored to the v1 family (_request_logs).';

COMMIT;

-- POST_CONDITION:
--   -- 回滚后定义体里**重新出现** v1 族引用（期望 true / true / true）：
--   SELECT strpos(pg_get_viewdef('public.v_continuation_effectiveness'::regclass, true), '_request_logs') > 0;
--   SELECT strpos(pg_get_viewdef('public.v_node_switch_analysis'::regclass, true), '_request_logs') > 0;
--   SELECT strpos(pg_get_viewdef('public.v_timeout_effectiveness'::regclass, true), '_request_logs') > 0;
