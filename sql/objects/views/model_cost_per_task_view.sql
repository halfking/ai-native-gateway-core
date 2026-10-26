-- dbinit:baseline-v1-fallback —— 本文件是**基线形态**的 v1 fallback 体：基线 schema 是
--   **逐语句**应用的，而 CREATE VIEW 会**急切校验**它引用的关系 ⇒ 若这里写成会话族形态
--   （session_turns_hot / session_turn_details*），全新安装会在基线阶段报
--   42P01: relation "public.session_turns_hot" does not exist 并中止
--   ⇒ **这里保留 v1 臂是基线必需，不是缺陷**（§19.10 跨会话订正）。
-- dbinit:baseline-v1-fallback-switch: 883_session_family_cost_view_metrics.sql —— 部署形态（会话族）由这个迁移换上；
--   判据据此分「基线时点 / 部署时点」两个时点（见 cmd/tools/sql_source_indirection_audit/
--   v1arm_deployment.go）。⚠ 本文件**不可单独 psql -f 应用**（v1 族退役后必然 42P01）。
-- ⚠️ **本文件不是部署形态的视图定义**，它是 **v1 fallback 体**（7 天窗的平均延迟
-- 子查询仍读 v1 族 `request_logs`），只在「基线时点」成立。
--
-- 为什么基线必须留 v1 体（2026-10-20 实测）：基线 schema 是**逐语句**应用的，
-- 而 `CREATE VIEW` 会**急切校验**它引用的关系 ⇒ 若这里写成会话族形态
-- （`session_turns_hot` / `session_turn_details*`），新装环境会在此处报
-- **42P01: relation "public.session_turns_hot" does not exist** 并中止。
-- 升级路径绿是因为那些表早就存在 ⇒ **「升级路径绿」≠「新装路径绿」**。
--
-- 部署形态（会话族）由 **迁移 883** 换上：
-- `sql/migrations/startup/883_session_family_cost_view_metrics.sql`。
-- 两条路径跑完启动序列后**形态一致**。
--
--
-- Name: model_cost_per_task_view; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.model_cost_per_task_view AS
 SELECT canonical_id,
    raw_model,
    sum(cost_usd) AS total_cost_usd,
    sum((tokens_input + tokens_output)) AS total_tokens,
        CASE
            WHEN (sum((tokens_input + tokens_output)) > (0)::numeric) THEN ((sum(cost_usd) / sum((tokens_input + tokens_output))) * (1000000)::numeric)
            ELSE (0)::numeric
        END AS avg_cost_per_1m_usd,
        CASE
            WHEN (sum(requests_total) > 0) THEN ((sum(requests_success))::numeric / (sum(requests_total))::numeric)
            ELSE (0)::numeric
        END AS success_rate,
    ( SELECT avg(rl.latency_ms) AS avg
           FROM public.request_logs rl
          WHERE ((rl.outbound_model = mcp.raw_model) AND (rl.success = true) AND (rl.ts >= (now() - '7 days'::interval)))) AS avg_latency_ms,
    sum(requests_total) AS total_requests,
    count(DISTINCT api_key_id) AS unique_api_keys
   FROM public.api_key_model_cost mcp
  WHERE (bucket >= (now() - '7 days'::interval))
  GROUP BY canonical_id, raw_model;


--
-- Name: VIEW model_cost_per_task_view; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON VIEW public.model_cost_per_task_view IS 'Auto route: per-model aggregated cost for last 7 days';

