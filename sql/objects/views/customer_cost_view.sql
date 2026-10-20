-- dbinit:baseline-v1-fallback —— 本文件是**基线形态**的 v1 fallback 体：基线 schema 是
--   **逐语句**应用的，而 CREATE VIEW 会**急切校验**它引用的关系 ⇒ 若这里写成会话族形态
--   （session_turns_hot / session_turn_details*），全新安装会在基线阶段报
--   42P01: relation "public.session_turns_hot" does not exist 并中止
--   ⇒ **这里保留 v1 臂是基线必需，不是缺陷**（§19.10 跨会话订正）。
-- dbinit:baseline-v1-fallback-switch: 883_session_family_cost_view_metrics.sql —— 部署形态（会话族）由这个迁移换上；
--   判据据此分「基线时点 / 部署时点」两个时点（见 cmd/tools/sql_source_indirection_audit/
--   v1arm_deployment.go）。⚠ 本文件**不可单独 psql -f 应用**（v1 族退役后必然 42P01）。
-- ⚠️ **本文件不是部署形态的视图定义**，它是 **v1 fallback 体**（两个活指标子查询
-- 仍读 v1 族 `request_logs`），只在「基线时点」成立。
--
-- 为什么基线必须留 v1 体（2026-10-20 实测）：基线 schema 是**逐语句**应用的，
-- 而 `CREATE VIEW` 会**急切校验**它引用的关系 ⇒ 若这里写成会话族形态
-- （`session_turns_hot` / `session_turn_details*`），新装环境会在此处报
-- **42P01: relation "public.session_turns_hot" does not exist** 并中止
-- （那两张表由**启动序列**的 `session_turns_hot_bootstrap.sql` 与迁移 734 建，
-- 基线时点还不存在）。升级路径绿是因为那些表早就存在
-- ⇒ **「升级路径绿」≠「新装路径绿」**。
--
-- 部署形态（会话族）由 **迁移 883** 换上：
-- `sql/migrations/startup/883_session_family_cost_view_metrics.sql`
-- （5 分钟窗 = hot 单腿、7 天窗 = hot ∪ parent；匹配键 `raw_model_name`；
--  会话族 `api_key_id` 是 text ⇒ 显式 `::text`）。
-- 两条路径（全新安装 / 存量升级）跑完启动序列后**形态一致**。
--
--
-- Name: customer_cost_view; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.customer_cost_view AS
 SELECT akmc.api_key_id,
    ak.key_alias,
    ak.tenant_id,
    ak.application_id,
    sum(
        CASE
            WHEN (akmc.bucket >= (now() - '01:00:00'::interval)) THEN akmc.cost_usd
            ELSE (0)::numeric
        END) AS cost_usd_1h,
    sum(
        CASE
            WHEN (akmc.bucket >= (now() - '24:00:00'::interval)) THEN akmc.cost_usd
            ELSE (0)::numeric
        END) AS cost_usd_24h,
    sum(
        CASE
            WHEN (akmc.bucket >= (now() - '7 days'::interval)) THEN akmc.cost_usd
            ELSE (0)::numeric
        END) AS cost_usd_7d,
    sum(akmc.requests_total) AS total_auto_requests,
    sum(akmc.requests_success) AS total_auto_success,
    ( SELECT count(*) AS count
           FROM public.request_logs rl
          WHERE ((rl.api_key_id = akmc.api_key_id) AND (rl.is_auto_request = true) AND (rl.ts >= (now() - '00:05:00'::interval)) AND (rl.success IS NOT NULL) AND (rl.ts IS NOT NULL))) AS active_concurrent,
    max(akmc.concurrency_limit) AS concurrency_limit,
    avg(
        CASE
            WHEN (akmc.bucket >= (now() - '01:00:00'::interval)) THEN akmc.pressure_ratio
            ELSE NULL::numeric
        END) AS avg_pressure_1h,
    max(akmc.score_smart) AS best_score_smart,
    max(akmc.score_speed_first) AS best_score_speed_first,
    max(akmc.score_cost_first) AS best_score_cost_first,
    max(akmc.last_request_at) AS last_request_at
   FROM (public.api_key_model_cost akmc
     JOIN public.api_keys ak ON ((ak.id = akmc.api_key_id)))
  GROUP BY akmc.api_key_id, ak.key_alias, ak.tenant_id, ak.application_id;


--
-- Name: VIEW customer_cost_view; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON VIEW public.customer_cost_view IS 'Auto route: per-API-Key customer cost dashboard (1h/24h/7d windows + concurrency + scores). active_concurrent is computed live from request_logs (5min window).';

