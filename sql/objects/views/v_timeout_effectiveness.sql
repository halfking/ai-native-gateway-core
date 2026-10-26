-- dbinit:baseline-v1-fallback —— 本文件是**基线形态**的 v1 fallback 体：基线 schema 是
--   **逐语句**应用的，而 CREATE VIEW 会**急切校验**它引用的关系 ⇒ 若这里写成会话族形态
--   （session_turns_hot / session_turn_details*），全新安装会在基线阶段报
--   42P01: relation "public.session_turns_hot" does not exist 并中止
--   ⇒ **这里保留 v1 臂是基线必需，不是缺陷**（§19.10 跨会话订正）。
-- dbinit:baseline-v1-fallback-switch: 884_session_family_v_analytics_views.sql —— 部署形态（会话族）由这个迁移换上；
--   判据据此分「基线时点 / 部署时点」两个时点（见 cmd/tools/sql_source_indirection_audit/
--   v1arm_deployment.go）。⚠ 本文件**不可单独 psql -f 应用**（v1 族退役后必然 42P01）。
-- ⚠️ **本文件不是部署形态的视图定义**，它是 **v1 fallback 体**（读 v1 族
-- `request_logs`），只在「基线时点」成立。
--
-- 为什么基线必须留 v1 体（2026-10-20 实测）：基线 schema 是**逐语句**应用的，
-- 而 `CREATE VIEW` 会**急切校验**它引用的关系 ⇒ 若这里写成会话族形态
-- （`session_turns_hot` / `session_turn_details*`），新装环境会在此处报
-- **42P01: relation "public.session_turns_hot" does not exist** 并中止。
-- 升级路径绿是因为那些表早就存在 ⇒ **「升级路径绿」≠「新装路径绿」**。
--
-- 部署形态（会话族）由 **迁移 884** 换上：
-- `sql/migrations/startup/884_session_family_v_analytics_views.sql`。
-- 两条路径跑完启动序列后**形态一致**。
--
--
-- Name: v_timeout_effectiveness; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.v_timeout_effectiveness AS
 SELECT date_trunc('hour'::text, ts) AS time_bucket,
    timeout_mode,
    count(*) AS total_requests,
    count(*) FILTER (WHERE (success = true)) AS success_count,
    count(*) FILTER (WHERE ((success = false) AND (error_kind ~~ '%timeout%'::text))) AS timeout_count,
    round(((100.0 * (count(*) FILTER (WHERE ((success = false) AND (error_kind ~~ '%timeout%'::text))))::numeric) / (count(*))::numeric), 2) AS timeout_rate,
    round(avg(effective_timeout_seconds), 2) AS avg_effective_timeout,
    round((avg(latency_ms) / 1000.0), 2) AS avg_latency_seconds
   FROM public.request_logs
  WHERE ((ts > (now() - '24:00:00'::interval)) AND (effective_timeout_seconds IS NOT NULL))
  GROUP BY (date_trunc('hour'::text, ts)), timeout_mode
  ORDER BY (date_trunc('hour'::text, ts)) DESC;

