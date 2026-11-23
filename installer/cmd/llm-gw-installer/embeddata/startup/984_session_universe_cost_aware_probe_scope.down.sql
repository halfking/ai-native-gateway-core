-- 984 回滚：把会话宇宙谓词恢复成 994 的形态（**无**「真实计费豁免」），
-- 并删除 984 的台账行。
--
-- ★ 为什么回滚目标是 **994 形态**而不是 981 形态：
--   994（owner 键统一 COALESCE 双源 + DISTINCT ON 去重子查询 + end_user_count）
--   是本迁移落地时的**已应用基线**⇒ 回滚必须回到它，而不是更早的 981
--   （994 自己的 down.sql 回滚到 981 形态，那是 994 自己的历史）。
--
-- ★ 回滚后「有真实计费的 deployment-gate 会话」重新被剔掉（984 修掉的误杀
--   回归）⇒ 属预期：回滚＝回到修复前的口径。
SET lock_timeout = '5s';
SET statement_timeout = '30min';
SET max_parallel_workers_per_gather = 0;

DROP MATERIALIZED VIEW IF EXISTS public.user_session_aggregates;
CREATE MATERIALIZED VIEW IF NOT EXISTS public.user_session_aggregates AS
SELECT
    NULLIF(COALESCE(s.owner_user, sd.api_key_owner_user), '') AS owner,
    ss.tenant_id                                   AS tenant_id,
    (ss.first_request_at AT TIME ZONE 'Asia/Shanghai')::date AS date,
    COUNT(*)                                       AS session_count,
    COUNT(DISTINCT ss.gw_project_id) FILTER (WHERE ss.gw_project_id IS NOT NULL) AS project_count,
    COUNT(DISTINCT sd.task_id) FILTER (
        WHERE sd.task_id IS NOT NULL AND sd.task_id <> '' AND sd.task_id <> 'default'
    )                                              AS task_count,
    COALESCE(SUM(ss.total_tokens), 0)              AS total_tokens,
    COALESCE(SUM(ss.total_cost_usd), 0)            AS internal_cost,
    COALESCE(SUM(ss.total_credits_charged), 0)     AS external_cost,
    COALESCE(AVG(ss.duration_seconds), 0)          AS avg_duration_seconds,
    COALESCE(AVG(ss.request_count), 0)             AS avg_turns,
    MAX(ss.last_request_at)                        AS last_active_at,
    COALESCE(SUM(ss.request_count), 0)             AS request_count,
    COALESCE(SUM(ss.success_count), 0)             AS success_count,
    COALESCE(SUM(ss.error_count), 0)               AS error_count,
    COALESCE(AVG(ss.avg_latency_ms), 0)            AS avg_latency_ms,
    COUNT(DISTINCT sd.end_user_id) FILTER (
        WHERE sd.end_user_id IS NOT NULL AND sd.end_user_id <> ''
    )                                              AS end_user_count,
    NOW()                                          AS refreshed_at
FROM session_summaries ss
JOIN (
  SELECT DISTINCT ON (session_id, tenant_id)
    session_id, tenant_id, is_probe, owner_user, client_type, title
  FROM sessions
  ORDER BY session_id, tenant_id, partition_date DESC
) s
  ON s.session_id = ss.session_key
 AND s.tenant_id = ss.tenant_id
LEFT JOIN session_dim sd
  ON sd.session_key = ss.session_key
 AND sd.tenant_id = ss.tenant_id
WHERE NULLIF(COALESCE(s.owner_user, sd.api_key_owner_user), '') IS NOT NULL
  AND NOT COALESCE(s.is_probe, false)
  AND NOT session_analytics_is_probe(
      COALESCE(s.owner_user, sd.api_key_owner_user), s.client_type, s.title)
GROUP BY NULLIF(COALESCE(s.owner_user, sd.api_key_owner_user), ''), ss.tenant_id,
         (ss.first_request_at AT TIME ZONE 'Asia/Shanghai')::date;

DROP MATERIALIZED VIEW IF EXISTS public.project_task_aggregates;
CREATE MATERIALIZED VIEW IF NOT EXISTS public.project_task_aggregates AS
SELECT
    NULLIF(COALESCE(s.owner_user, sd.api_key_owner_user), '') AS owner,
    ss.tenant_id                                   AS tenant_id,
    regexp_replace(ss.gw_project_id, '^app:', '')  AS project,
    COALESCE(
        CASE
            WHEN sd.task_id IS NULL OR sd.task_id = '' THEN NULL
            WHEN sd.task_id = 'default' THEN NULL
            WHEN sd.task_id LIKE 'auto-title:%' THEN NULL
            ELSE sd.task_id
        END,
        ''
    )                                             AS task,
    (ss.first_request_at AT TIME ZONE 'Asia/Shanghai')::date AS date,
    COUNT(*)                                       AS session_count,
    COALESCE(SUM(ss.request_count), 0)             AS turn_count,
    COALESCE(SUM(ss.total_tokens), 0)              AS total_tokens,
    COALESCE(SUM(ss.total_cost_usd), 0)            AS internal_cost,
    COALESCE(SUM(ss.total_credits_charged), 0)     AS external_cost,
    MAX(ss.last_request_at)                        AS last_active_at,
    NOW()                                          AS refreshed_at
FROM session_summaries ss
JOIN (
  SELECT DISTINCT ON (session_id, tenant_id)
    session_id, tenant_id, is_probe, owner_user, client_type, title
  FROM sessions
  ORDER BY session_id, tenant_id, partition_date DESC
) s
  ON s.session_id = ss.session_key
 AND s.tenant_id = ss.tenant_id
LEFT JOIN session_dim sd
  ON sd.session_key = ss.session_key
 AND sd.tenant_id = ss.tenant_id
WHERE ss.gw_project_id IS NOT NULL
  AND NULLIF(COALESCE(s.owner_user, sd.api_key_owner_user), '') IS NOT NULL
  AND NOT COALESCE(s.is_probe, false)
  AND NOT session_analytics_is_probe(
      COALESCE(s.owner_user, sd.api_key_owner_user), s.client_type, s.title)
GROUP BY NULLIF(COALESCE(s.owner_user, sd.api_key_owner_user), ''), ss.tenant_id,
         regexp_replace(ss.gw_project_id, '^app:', ''),
         COALESCE(
             CASE
                 WHEN sd.task_id IS NULL OR sd.task_id = '' THEN NULL
                 WHEN sd.task_id = 'default' THEN NULL
                 WHEN sd.task_id LIKE 'auto-title:%' THEN NULL
                 ELSE sd.task_id
             END,
             ''
         ),
         (ss.first_request_at AT TIME ZONE 'Asia/Shanghai')::date;

DROP MATERIALIZED VIEW IF EXISTS public.agent_usage_aggregates;
CREATE MATERIALIZED VIEW IF NOT EXISTS public.agent_usage_aggregates AS
SELECT
    NULLIF(s.agent_name, '')                       AS agent_name,
    ss.tenant_id                                   AS tenant_id,
    (ss.first_request_at AT TIME ZONE 'Asia/Shanghai')::date AS date,
    COUNT(DISTINCT NULLIF(COALESCE(s.owner_user, sd.api_key_owner_user), '')) AS user_count,
    COUNT(*)                                       AS session_count,
    COUNT(DISTINCT sd.task_id) FILTER (
        WHERE sd.task_id IS NOT NULL AND sd.task_id <> '' AND sd.task_id <> 'default'
    )                                              AS task_count,
    COALESCE(SUM(ss.total_tokens), 0)              AS total_tokens,
    COALESCE(SUM(ss.total_cost_usd), 0)            AS internal_cost,
    COALESCE(SUM(ss.total_credits_charged), 0)     AS external_cost,
    COALESCE(AVG(ss.duration_seconds), 0)          AS avg_duration_seconds,
    MAX(ss.last_request_at)                        AS last_used_at,
    NOW()                                          AS refreshed_at
FROM session_summaries ss
JOIN (
  SELECT DISTINCT ON (session_id, tenant_id)
    session_id, tenant_id, is_probe, owner_user, client_type, title, agent_name
  FROM sessions
  ORDER BY session_id, tenant_id, partition_date DESC
) s
  ON s.session_id = ss.session_key
 AND s.tenant_id = ss.tenant_id
LEFT JOIN session_dim sd
  ON sd.session_key = ss.session_key
 AND sd.tenant_id = ss.tenant_id
WHERE NULLIF(s.agent_name, '') IS NOT NULL
  AND NOT COALESCE(s.is_probe, false)
  AND NOT session_analytics_is_probe(
      COALESCE(s.owner_user, sd.api_key_owner_user), s.client_type, s.title)
GROUP BY NULLIF(s.agent_name, ''), ss.tenant_id,
         (ss.first_request_at AT TIME ZONE 'Asia/Shanghai')::date;

DROP MATERIALIZED VIEW IF EXISTS public.model_usage_aggregates;
CREATE MATERIALIZED VIEW IF NOT EXISTS public.model_usage_aggregates AS
SELECT
    COALESCE(NULLIF(rl.client_model, ''), NULLIF(rl.outbound_model, ''), 'unknown') AS model,
    COALESCE(p.code, 'unknown')                    AS provider,
    rl.tenant_id                                   AS tenant_id,
    (rl.ts AT TIME ZONE 'Asia/Shanghai')::date     AS date,
    COUNT(*)                                       AS request_count,
    COUNT(*) FILTER (WHERE rl.success)             AS success_count,
    COUNT(*) FILTER (WHERE rl.cache_read_tokens > 0) AS cache_hit_count,
    COUNT(DISTINCT NULLIF(COALESCE(rl.owner_user, rl.api_key_owner_user), '')) AS user_count,
    COALESCE(SUM(rl.prompt_tokens), 0)             AS input_tokens,
    COALESCE(SUM(rl.completion_tokens), 0)         AS output_tokens,
    COALESCE(SUM(rl.total_tokens), 0)              AS total_tokens,
    COALESCE(SUM(rl.cost_usd), 0)                  AS internal_cost,
    0::numeric                                     AS external_cost,
    COALESCE(AVG(rl.latency_ms), 0)                AS avg_duration_ms,
    MAX(rl.ts)                                     AS last_used_at,
    NOW()                                          AS refreshed_at
FROM request_logs_with_current_month rl
LEFT JOIN providers p ON p.id = rl.provider_id
WHERE NOT (
    rl.api_key_owner_user IS NOT NULL
    AND session_analytics_is_probe(rl.api_key_owner_user, NULL, NULL)
)
GROUP BY COALESCE(NULLIF(rl.client_model, ''), NULLIF(rl.outbound_model, ''), 'unknown'),
         COALESCE(p.code, 'unknown'),
         rl.tenant_id,
         (rl.ts AT TIME ZONE 'Asia/Shanghai')::date;

CREATE UNIQUE INDEX IF NOT EXISTS idx_user_session_agg_pk
    ON public.user_session_aggregates(owner, tenant_id, date);
CREATE INDEX IF NOT EXISTS idx_user_session_agg_date
    ON public.user_session_aggregates(date DESC);
CREATE INDEX IF NOT EXISTS idx_user_session_agg_tenant
    ON public.user_session_aggregates(tenant_id, date DESC);
CREATE INDEX IF NOT EXISTS idx_user_session_agg_owner
    ON public.user_session_aggregates(owner, date DESC);

CREATE UNIQUE INDEX IF NOT EXISTS idx_project_task_agg_pk
    ON public.project_task_aggregates(owner, tenant_id, project, task, date);
CREATE INDEX IF NOT EXISTS idx_project_task_agg_owner
    ON public.project_task_aggregates(owner, date DESC);
CREATE INDEX IF NOT EXISTS idx_project_task_agg_project
    ON public.project_task_aggregates(project, date DESC);
CREATE INDEX IF NOT EXISTS idx_project_task_agg_tenant
    ON public.project_task_aggregates(tenant_id, date DESC);

CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_usage_agg_pk
    ON public.agent_usage_aggregates(agent_name, tenant_id, date);
CREATE INDEX IF NOT EXISTS idx_agent_usage_agg_agent
    ON public.agent_usage_aggregates(agent_name, date DESC);
CREATE INDEX IF NOT EXISTS idx_agent_usage_agg_tenant
    ON public.agent_usage_aggregates(tenant_id, date DESC);

CREATE UNIQUE INDEX IF NOT EXISTS idx_model_usage_agg_pk
    ON public.model_usage_aggregates(model, provider, tenant_id, date);
CREATE INDEX IF NOT EXISTS idx_model_usage_agg_model
    ON public.model_usage_aggregates(model, date DESC);
CREATE INDEX IF NOT EXISTS idx_model_usage_agg_provider
    ON public.model_usage_aggregates(provider, date DESC);
CREATE INDEX IF NOT EXISTS idx_model_usage_agg_tenant
    ON public.model_usage_aggregates(tenant_id, date DESC);

DELETE FROM public.schema_migrations WHERE version = '984';
