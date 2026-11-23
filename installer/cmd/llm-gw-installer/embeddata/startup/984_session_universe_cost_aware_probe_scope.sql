-- ★ 984（2026-11-24 P1 会话宇宙探测误杀修复轮）：会话宇宙谓词加「真实计费豁免」
--
-- ── 为什么要有本迁移（211 生产实测取证，不是推断）──────────────────────────
--
-- ① 会话统计（/dashboard 会话统计面板）与看板（usage_facts，business only）
--    的费用差 **$7,021.91（79%）**、请求数差 2.3 倍。逐项拆解后，差额的主体
--    落在「**会话宇宙把真实业务会话当探测剔掉**」这一项上。
--
-- ② 唯一误杀根因 = **owner 名字正则命中** `(^deployment-gate)`：
--    211 生产实测 30 天窗口内，被剔会话中带真实计费的部分，
--    **99.9% 集中在 owner 前缀 deployment-gate**（16,698 会话 / $7,163.51）。
--
-- ③ 该账号承载的**不是探测流量**（实测，不是猜测）：
--    · request_preview 是 MCode/minimax-code 的**真实 agent 系统提示词**
--      （"Plugin discovery: …" / "You run inside MiniMax Code…"）；
--    · 模型是 claude-opus-5-5 / sonnet-5-5 / opus-4-8（顶级档）；
--    · 16,292 个会话里 16,290 个是**单请求会话**（avg 1.01 req/session），
--      且 24 小时连续分布（不是批处理尖峰）；
--    · 与真实探针臂形态截然不同：真探测 origin_stage='node_probe'
--      已被迁移 928 **物理隔离**进 session_turns_probe_hot，
--      与 sessions 表**交集 = 0**（实测 329,970 行 / 109,479 会话）。
--    ⇒ 结论：**(^deployment-gate) 这条正则把真实业务流量整片误杀。**
--
-- ── 修法：把「名字命中」与「会话是否有真实计费」解耦 ──────────────────────
--
--   新谓词（会话级三视图 + Go 侧逐字同形）：
--     WHERE owner IS NOT NULL
--       AND (
--           COALESCE(ss.total_cost_usd, 0) > 0        -- 有真实计费 ⇒ 计入
--           OR (
--               NOT COALESCE(s.is_probe, false)      -- 零成本会话：沿用旧排除规则
--               AND NOT session_analytics_is_probe(...)
--           )
--       )
--
--   ★ 这是**收窄**而不是**删正则**：零成本的 deployment-gate 探测会话仍然被剔
--     （「人员榜不被零成本门禁流量霸榜」的原意保留）；只有**真实花了钱**的会话
--     不再因为名字像探测而消失。
--   ★ 请求级视图（model_usage_aggregates）没有 ss 可用 ⇒ 按请求自身的
--     rl.cost_usd 判定（同一语义：花了钱的请求是真实使用）。
--
-- ★ 为什么不去改 sessions.is_probe 缓存（实测后放弃）：
--   sessions.is_probe 是 981 时代旧正则的**物化缓存**（30 天实测：
--   is_probe=1 且函数不命中 = **0 行** ⇒ 纯缓存、无运维手工标记）。
--   若把「有计费」做成 is_probe 的新语义，就必须 UPDATE 一张按天分区的
--   大表（十万级行）⇒ 生产写风险；且会抹掉「运维手工排除位」这一语义。
--   ⇒ 改成在**谓词层**豁免：零写、语义更清楚、手工标记位原样保留
--     （仅对**有真实计费**的会话不再生效——30 天实测该情形为 0 行）。
--
-- ── 为什么沿用 994 的 DROP+CREATE 形态、而不是「先建后换」──────────────
--
--   ★ 不引入临时 MV 名：仓里的对象普查门
--     （scripts/ursmcheck/db_object_census_gate_test.go）会从迁移文件反解
--     「期望索引集」⇒ 临时名会**永久**进入期望集、而生产上这些对象从来没
--     存在过 ⇒ 普查底账被污染（结构性问题，比一次窗口期降级更坏）。
--   ★ 窗口期（视图重建期间）读这三个视图的只有 API-only 端点
--     （/admin/session-analytics 的 overview / user-ranking，web 零调用）
--     与 MV 刷新器（失败有指标+告警）；/dashboard 会话统计面板的**自己的腿**
--     直读 session_summaries（不碰物化视图）⇒ 不受影响（同 994 头注）。
--
-- ── 锁与超时守卫（两条投递通道都生效，994 同款）──────────────────────────
-- SET（会话级）而不是 SET LOCAL：docker 通道
-- （scripts/apply-db-revision-sequence.sh）是非事务流式 psql，SET LOCAL
-- 静默失效（981 实测记录）。lock_timeout=5s：强锁 DDL 等锁上限、超时
-- 快速失败可重试。statement_timeout=30min：视图级 SELECT 实测 90s~197s。
-- max_parallel_workers_per_gather=0：关并行 ⇒ 不申请 DSM 共享内存段
-- （211 的 /dev/shm 只有 64MB 且长期滞留 DSM 孤儿段 ⇒ 并行 worker 一 resize
-- 就报 SQLSTATE 53100，994 头注登记的同款雷）。
SET lock_timeout = '5s';
SET statement_timeout = '30min';
SET max_parallel_workers_per_gather = 0;

-- ============================================================================
-- Part 1: user_session_aggregates（人员维度会话日聚合）重建
-- ============================================================================
-- ★ 除 WHERE 里的探测排除项外，与 994 的定义**逐字相同**（含 DISTINCT ON
--   去重子查询、owner 双源键、end_user_count 列）。
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
  AND (
      COALESCE(ss.total_cost_usd, 0) > 0
      OR (
          NOT COALESCE(s.is_probe, false)
          AND NOT session_analytics_is_probe(
              COALESCE(s.owner_user, sd.api_key_owner_user), s.client_type, s.title)
      )
  )
GROUP BY NULLIF(COALESCE(s.owner_user, sd.api_key_owner_user), ''), ss.tenant_id,
         (ss.first_request_at AT TIME ZONE 'Asia/Shanghai')::date;

COMMENT ON MATERIALIZED VIEW public.user_session_aggregates IS
    '人员维度会话日聚合（会话级口径；源 session_summaries × sessions × session_dim；owner 键=COALESCE 双源（994）；★ 984：探测排除加「真实计费豁免」（有真实计费的会话不因 owner 名字命中探测正则而被剔，零成本会话的排除语义不变）；end_user_count 为 994 新增）';

-- ============================================================================
-- Part 2: project_task_aggregates（项目+任务维度会话日聚合）重建
-- ============================================================================
-- ★ 除 WHERE 里的探测排除项外，与 994 的定义逐字相同。
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
  AND (
      COALESCE(ss.total_cost_usd, 0) > 0
      OR (
          NOT COALESCE(s.is_probe, false)
          AND NOT session_analytics_is_probe(
              COALESCE(s.owner_user, sd.api_key_owner_user), s.client_type, s.title)
      )
  )
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
    '项目+任务维度会话日聚合（会话级口径；源同上；owner 键=COALESCE 双源（994）；★ 984：探测排除加「真实计费豁免」；task '''' 归一论证见 981）';

-- ============================================================================
-- Part 3: agent_usage_aggregates（智能体维度会话日聚合）重建
-- ============================================================================
-- ★ 除 WHERE 里的探测排除项外，与 994 的定义逐字相同。
-- ★ 注意：该视图的 WHERE 首项是 agent_name（非空），**没有** owner 非空合取项
--   （它按 agent_name 聚合、user_count 用 COALESCE 双源键）⇒ 与 Go 侧
--   SessionUniverseWhere 的形状**不同**，这一点 981/994 起就是如此。
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
  AND (
      COALESCE(ss.total_cost_usd, 0) > 0
      OR (
          NOT COALESCE(s.is_probe, false)
          AND NOT session_analytics_is_probe(
              COALESCE(s.owner_user, sd.api_key_owner_user), s.client_type, s.title)
      )
  )
GROUP BY NULLIF(s.agent_name, ''), ss.tenant_id,
         (ss.first_request_at AT TIME ZONE 'Asia/Shanghai')::date;

COMMENT ON MATERIALIZED VIEW public.agent_usage_aggregates IS
    '智能体维度会话日聚合（会话级口径；源同上；user_count 键=COALESCE 双源（994）；★ 984：探测排除加「真实计费豁免」）';

-- ============================================================================
-- Part 4: model_usage_aggregates（模型维度请求日聚合）重建
-- ============================================================================
-- ★ 该视图是**请求级**（源 request_logs_with_current_month）⇒ 没有 ss 可用 ⇒
--   「真实计费豁免」按**请求自身**的 rl.cost_usd 判定（同一语义：
--   花了钱的请求是真实使用；零成本请求仍按 owner 名字正则排除）。
-- ★ 其余部分与 994 的定义逐字相同（994 仅重建以自愈缺失，不改口径）。
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
    AND COALESCE(rl.cost_usd, 0) = 0
    AND session_analytics_is_probe(rl.api_key_owner_user, NULL, NULL)
)
GROUP BY COALESCE(NULLIF(rl.client_model, ''), NULLIF(rl.outbound_model, ''), 'unknown'),
         COALESCE(p.code, 'unknown'),
         rl.tenant_id,
         (rl.ts AT TIME ZONE 'Asia/Shanghai')::date;

COMMENT ON MATERIALIZED VIEW public.model_usage_aggregates IS
    '模型维度请求日聚合（请求级口径；源 request_logs_with_current_month × providers；★ 984：探测排除加「真实计费豁免」（按 rl.cost_usd））';

-- ============================================================================
-- Part 5: 索引（DROP MV 时索引随视图一起消失，这里全部重建）
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
-- Part 6: 台账自登记（≥952 双账本纪律）
-- ============================================================================
INSERT INTO public.schema_migrations (version, description)
VALUES ('984', 'session_universe_cost_aware_probe_scope: 会话宇宙探测排除加「真实计费豁免」——有真实计费（ss.total_cost_usd>0 / rl.cost_usd>0）的会话不因 owner 名字命中探测正则而被剔（根因：(^deployment-gate) 正则误杀真实 agent 流量，211 实测 30 天 16,698 会话/$7,163.51）；零成本会话的探测排除语义逐字不变；四个物化视图重建 + 15 索引重建')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;
