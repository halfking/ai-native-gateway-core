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
-- `sql/migrations/startup/884_session_family_v_analytics_views.sql`
-- （两条腿 hot ∪ parent UNION ALL；6 个细节语义列在 `session_turn_details` 上，
--  类型与 v1 逐列一致 ⇒ 能改读且不丢能力）。
-- 两条路径跑完启动序列后**形态一致**。
--
--
-- Name: v_continuation_effectiveness; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.v_continuation_effectiveness AS
 SELECT date_trunc('hour'::text, ts) AS time_bucket,
    count(*) FILTER (WHERE (is_continuation = true)) AS continuation_requests,
    count(*) FILTER (WHERE (cached_response_id IS NOT NULL)) AS cache_hits,
    count(*) FILTER (WHERE ((is_continuation = true) AND (cached_response_id IS NULL))) AS cache_misses,
    round(((100.0 * (count(*) FILTER (WHERE (cached_response_id IS NOT NULL)))::numeric) / (NULLIF(count(*) FILTER (WHERE (is_continuation = true)), 0))::numeric), 2) AS cache_hit_rate,
    sum(COALESCE(context_size_tokens, 0)) FILTER (WHERE (cached_response_id IS NOT NULL)) AS tokens_saved
   FROM public.request_logs
  WHERE (ts > (now() - '24:00:00'::interval))
  GROUP BY (date_trunc('hour'::text, ts))
 HAVING (count(*) FILTER (WHERE (is_continuation = true)) > 0)
  ORDER BY (date_trunc('hour'::text, ts)) DESC;

