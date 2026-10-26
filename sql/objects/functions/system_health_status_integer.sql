--
-- Name: system_health_status(integer); Type: FUNCTION; Schema: public; Owner: -
--

-- ★ 2026-10-20（迁移 890，v1 族最终删除）：本函数原先读 `request_logs_hot`
--   ⇒ 30s 窗口每 30s 调一次 ⇒ 42P01 ⇒ 健康徽章永久 "suspect" + 每 30s 一条
--   WARN 刷屏。改成读 `session_turns_hot`（会话族的"热窗"表，语义与 v1 hot
--   对位）。
--
--   权威来源：sql/migrations/startup/890_drop_request_logs_v1_family.sql
--   （同一段函数体）与 db/request_logs_view_schema.go 所在包的运行时安装路径。
--
    CREATE OR REPLACE FUNCTION public.system_health_status(p_window_seconds integer DEFAULT 30)
    RETURNS TABLE(status text, success_rate numeric, sample_count bigint, failure_count bigint, last_check_at timestamp with time zone)
    LANGUAGE sql
    STABLE
    AS $function$
        WITH win AS (
            SELECT
                COUNT(*)::bigint                AS n,
                COUNT(*) FILTER (WHERE success)::bigint AS ok,
                COUNT(*) FILTER (WHERE NOT success)::bigint AS fail
            FROM public.session_turns_hot
            WHERE ts >= now() - make_interval(secs => p_window_seconds)
        )
        SELECT
            CASE
                WHEN n = 0                                  THEN 'suspect'
                WHEN (ok::numeric / NULLIF(n,0)) >= 0.80    THEN 'ok'
                ELSE 'degraded'
            END                                            AS status,
            ROUND( (ok::numeric / NULLIF(n,0))::numeric, 4) AS success_rate,
            n                                              AS sample_count,
            fail                                           AS failure_count,
            now()                                          AS last_check_at
        FROM win;
    $function$
