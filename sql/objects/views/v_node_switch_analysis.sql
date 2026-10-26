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
-- 改读之后这张视图从**恒 0 行**变成**真实数据**（2026-10-20 真库实测 16 个小时桶）。
-- 两条路径跑完启动序列后**形态一致**。
--
--
-- Name: v_node_switch_analysis; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.v_node_switch_analysis AS
 SELECT date_trunc('hour'::text, ts) AS time_bucket,
    node_switch_count AS switches,
    count(*) AS request_count,
    count(*) FILTER (WHERE (success = true)) AS success_count,
    round(((100.0 * (count(*) FILTER (WHERE (success = true)))::numeric) / (count(*))::numeric), 2) AS success_rate,
    round((avg(latency_ms) / 1000.0), 2) AS avg_latency_seconds
   FROM public.request_logs
  WHERE ((ts > (now() - '24:00:00'::interval)) AND (node_switch_count >= 0))
  GROUP BY (date_trunc('hour'::text, ts)), node_switch_count
  ORDER BY (date_trunc('hour'::text, ts)) DESC, node_switch_count;

