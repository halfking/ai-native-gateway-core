-- ★ 994（2026-11-20 会话分析闭环审计轮）：owner 键统一双源 + 981 视图族重建
--   + session_dim 关闭路径补列。
--
-- ── 为什么要有本迁移（三件事，逐件证据）────────────────────────────────
--
-- ① owner 键统一为 COALESCE(s.owner_user, sd.api_key_owner_user) 双源键。
--    迁移 981 的三个会话级视图 owner 键 = NULLIF(s.owner_user,'') 单源；
--    而 request_logs 侧真值（自有提交 95dcbddf1 实测）：当前流量 owner_user
--    大面积为空、真实身份在 api_key_owner_user。981 之后落地的读面全部已
--    换双源键（projects 下钻 3745f26f9 / 窗口用量 95dcbddf1 / 981 请求级
--    model_usage_aggregates:368）——只有三个会话级视图还停在单源键，同一
--    页面两套身份口径。本迁移把三个会话级视图统一到双源键（与 model 视图
--    的 :368 逐字同族表达式），users 列表/详情同步换锚（user_profile.go）。
--
-- ② 自愈「台账记了 applied、视图不存在」的生产状态。
--    252（154/245 共享 PG）实测（2026-11-20）：schema_migrations 有 981 行
--    （applied 2026-11-20），但 981 的四个物化视图 **一个都不存在**
--    （pg_matviews 零命中；对照 357 族三个视图在）。机制：981 的视图级
--    SELECT 在 252 实测 >90s（user_session_aggregates 形状）/ ~197s
--    （model_usage_aggregates 形状），共享 PG 服务端 statement_timeout
--    30s 掐断 CREATE 语句 ⇒ 视图没建出来 ⇒ 154/245 的 session-analytics
--    端点族 42P01、MV 刷新器每轮必失败。本迁移用 DROP IF EXISTS +
--    CREATE IF NOT EXISTS 重建四视图 ⇒ 252 上首次创建（自愈）、211 上
--    原地重建（换键）。会话级 SELECT 在本文件里重放（session_analytics
--    迁移族纪律：物化视图列名必须对着真库 information_schema 抄）。
--
-- ③ session_dim 补 stop_reason 列。
--    admin/session_health_api.go 的手动回收与 bg/session_lifecycle_worker.go
--    的闲置回收都写 stop_reason（「为什么关」是有价值的会话元数据），而该列
--    在表上不存在（904 迁移头部已记录：真库 42703 ⇒ 关闭路径全坏、
--    status 恒 'active'、closed_at 全 NULL——252 实测 25 万行全 active）。
--    updated_at 一并从两条语句移除而不是补列：session_dim 没有维护它的
--    触发器/写入方，写一个只有两个 UPDATE 会碰的 updated_at 是假元数据。
--
-- ── 锁与超时守卫（两条投递通道都生效，981 先例）────────────────────────
-- SET（会话级）而不是 SET LOCAL：docker 通道
--（scripts/apply-db-revision-sequence.sh）是非事务流式 psql，SET LOCAL
-- 静默失效（981 实测记录）。lock_timeout=5s：强锁 DDL 等锁上限，超时
-- 快速失败可重试。statement_timeout=30min：共享 PG 服务端默认 30s 会掐断
-- 视图级 SELECT（本迁移的核心语句实测 90s~197s）——这正是 981 在 252
-- 半途而废的机制，不设守卫等于把同一颗雷原样再埋一次。
SET lock_timeout = '5s';
SET statement_timeout = '30min';

-- ============================================================================
-- Part 1: session_dim.stop_reason（关闭路径元数据）
-- ============================================================================
ALTER TABLE public.session_dim ADD COLUMN IF NOT EXISTS stop_reason text;

COMMENT ON COLUMN public.session_dim.stop_reason IS
    '会话关闭原因（idle/absolute_lifetime/evicted/manual_admin/...；由 bg session lifecycle worker 与 admin 手动回收写入；迁移 994 补建——此前两处写入方引用不存在的列致 42703）';

-- ============================================================================
-- Part 2: 三个会话级视图重建（owner 键 = COALESCE 双源键）
-- ============================================================================
-- ★ DROP + CREATE 而不是 ALTER：PG 物化视图没有 ALTER ... SET QUERY。
--   窗口期说明：DROP→CREATE 之间视图短暂不存在；此时读取它的只有
--   API-only 端点（overview/user-ranking，web 零调用）与 MV 刷新器
--   （失败有指标+告警）。旧实例（蓝绿在役侧）读的是 session_owners
--   旧锚，不受影响 ⇒ 部署窗口内零用户可见影响。

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

COMMENT ON MATERIALIZED VIEW public.user_session_aggregates IS
    '人员维度会话日聚合（会话级口径；源 session_summaries × sessions × session_dim；owner 键=COALESCE 双源（994）；已排除探测流量；end_user_count 为 994 新增）';

DROP MATERIALIZED VIEW IF EXISTS public.project_task_aggregates;

CREATE MATERIALIZED VIEW IF NOT EXISTS public.project_task_aggregates AS
SELECT
    NULLIF(COALESCE(s.owner_user, sd.api_key_owner_user), '') AS owner,
    ss.tenant_id                                   AS tenant_id,
    regexp_replace(ss.gw_project_id, '^app:', '')  AS project,
    -- ★ task 用 ''（而不是 NULL）表示「无具体任务」：唯一索引必须是纯列索引
    --   （REFRESH CONCURRENTLY 拒绝表达式索引），NULL 互不相等会破坏唯一性
    --   （981 :246-254 的原始论证，保持不变）。
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

COMMENT ON MATERIALIZED VIEW public.project_task_aggregates IS
    '项目+任务维度会话日聚合（会话级口径；源同上；owner 键=COALESCE 双源（994）；task '''' 归一论证见 981）';

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

COMMENT ON MATERIALIZED VIEW public.agent_usage_aggregates IS
    '智能体维度会话日聚合（会话级口径；源同上；user_count 键=COALESCE 双源（994））';

-- ============================================================================
-- Part 3: model_usage_aggregates 重建（定义与 981 逐字同族——自愈 252 缺失）
-- ============================================================================
-- 该视图是请求级（request_logs_with_current_month），owner 键本就是
-- COALESCE 双源（981:368），本迁移不改它的语义，只 DROP+CREATE：
-- 在 211 上是等价重建，在 252 上是**首次创建**（修复「台账记了、视图没有」）。

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
WHERE NOT (rl.api_key_owner_user IS NOT NULL AND session_analytics_is_probe(rl.api_key_owner_user, NULL, NULL))
GROUP BY COALESCE(NULLIF(rl.client_model, ''), NULLIF(rl.outbound_model, ''), 'unknown'),
         COALESCE(p.code, 'unknown'),
         rl.tenant_id,
         (rl.ts AT TIME ZONE 'Asia/Shanghai')::date;

COMMENT ON MATERIALIZED VIEW public.model_usage_aggregates IS
    '模型维度请求日聚合（请求级口径；源 request_logs_with_current_month × providers；994 仅重建以自愈缺失，不改口径）';

-- ============================================================================
-- Part 4: 索引（DROP MV 时索引随视图一起消失，这里全部重建）
-- ============================================================================

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

-- ============================================================================
-- Part 5: 自登记（≥952 双账本纪律）
-- ============================================================================
INSERT INTO public.schema_migrations (version, description)
VALUES ('994', 'session_analytics_owner_key_and_close_path: 981 会话级三视图 owner 键统一 COALESCE 双源 + end_user_count 列；四视图 DROP+CREATE 重建（自愈 252 台账记 applied 而视图缺失）；session_dim 补 stop_reason 列')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;
