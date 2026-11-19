-- ★ 981（2026-11-19 会话分析系统基础设施）：会话分析 4 个聚合物化视图 + 刷新函数 + 探测判定函数。
--
-- ── 关于「要不要 dbinit:no-transaction 豁免」的实测结论（2026-11-19）──
-- 首版给本文件加了 dbinit:no-transaction 标记，理由是「含 REFRESH MATERIALIZED
-- VIEW CONCURRENTLY」。仓库的门禁（installer/internal/dbinit/no_transaction_test.go）
-- 把这条判红了——**门禁判得对**：实测（211 / PG 17.10）证明
--   REFRESH MATERIALIZED VIEW CONCURRENTLY **可以在事务块内执行**：
--     BEGIN; REFRESH MATERIALIZED VIEW CONCURRENTLY <有唯一索引的视图>; COMMIT;  → 成功
--     BEGIN; REFRESH MATERIALIZED VIEW CONCURRENTLY <无唯一索引的视图>; COMMIT;  → 报
--       "cannot refresh materialized view ... concurrently / Create a unique index"
--       （说明确实走了 CONCURRENTLY 分支，而不是静默降级成普通 REFRESH）
--     对照组：BEGIN; CREATE INDEX CONCURRENTLY ...; COMMIT;  → "cannot run inside a
--       transaction block"（证明测法有效，不是"事务块里什么都行"）
-- ⇒ 真正不可事务化的是 CREATE/DROP INDEX CONCURRENTLY / REINDEX CONCURRENTLY /
--   CREATE DATABASE / VACUUM 这一族，REFRESH CONCURRENTLY 不在其中
--   ⇒ 本文件**不需要**豁免，走 installer 的 --single-transaction 通道即可
--   ⇒ 好处：刷新与建视图在**同一个事务**里 ⇒ 失败不留半套视图（原子性）。
--
-- ── lock_timeout 守卫（仓内 §44 纪律）────────────────────────────────
-- 本文件含强锁 DDL（ALTER TABLE sessions 加列 / CREATE INDEX）：
-- 若在等锁期间无限期挂住，会把整库堵住。⇒ 设 5s 上限，超时即失败
--   （失败可重试，而"堵住全库"不可重试）。
--
-- ★ 用 SET（会话级）而**不是** SET LOCAL（事务局部）：实测（211 / PG 17.10）
--     事务块外执行 SET LOCAL ⇒ 只有一句
--       WARNING: SET LOCAL can only be used in transaction blocks
--       且 lock_timeout 仍是 0（**没生效**）。
--   而本文件有**两条投递通道**，其中 211 docker 走的
--   scripts/apply-db-revision-sequence.sh 是**非事务流式**执行
--   （psql -X -v ON_ERROR_STOP=1 "$DATABASE_URL" -f -，没有 --single-transaction）
--   ⇒ SET LOCAL 在那条通道上会**静默失效**，守卫形同虚设。
--   ⇒ SET（会话级）在两条通道上都生效；作用域止于本 psql 会话
--   （psql_file 每次独立起会话）⇒ 不污染其它迁移。
SET lock_timeout = '5s';
--
-- ─────────────────────────────────────────────────────────────────────────
-- ★★ 口径真源（2026-11-19 真库实测后重写；首版按幻写 schema 写的，列名全部不存在）
-- ─────────────────────────────────────────────────────────────────────────
-- 真库（211 / acc-pg17）实测，本迁移的底座是**会话分析既有三件套**，不是 sessions 裸表：
--
--   ① session_summaries（250,575 行）= 会话级指标真源
--      first_request_at / last_request_at / duration_seconds / request_count /
--      success_count / error_count / total_cost_usd（内部成本）/
--      total_credits_charged（外部成本）/ total_prompt_tokens / total_completion_tokens /
--      total_tokens / avg_latency_ms / primary_model / client_models[] / providers[] /
--      gw_project_id（项目·应用口径）/ gw_task_id / title
--   ② session_dim（250,552 行）= 维度真源
--      owner_user（人员）/ task_id（任务）/ client_id（应用）/ api_key_owner_user
--   ③ sessions（249,943 行）= 智能体 + 探测标记
--      agent_name（智能体：go-client / pi / minimax-code / claude-code / codex / curl）/
--      owner_user / title / is_probe（本迁移新增的运维手工排除位）
--
-- ★ 首版为何炸：首版按「sessions 有 owner / total_input_tokens / total_cost /
--   turn_count」的幻写 schema 写，实测真库这些列**全部不存在**（真名是
--   owner_user / total_tokens / total_cost_usd / total_turns，且无输入输出拆分、
--   无外部成本列）⇒ 建视图当场 42703。request_logs 侧同理：真名是
--   client_model / outbound_model / provider_id / success(bool) / cache_read_tokens /
--   cost_usd / ts（不是 model / provider / status / cache_read / cost / created_at）。
--   ⇒ **通用式：物化视图的列名必须对着真库的 information_schema 抄，不许照设计文档抄。**
--
-- ── 日桶口径 ───────────────────────────────────────────────────────────
-- 日期桶 = `(ts AT TIME ZONE 'Asia/Shanghai')::date`，**显式写时区**而不是依赖会话
-- TimeZone：迁移 992 已经把「面向用户的统计 API 用 CST 业务函数」定成产品口径
-- （cst_today / cst_date 等价于此式），这里显式写死是为了让刷新连接无论带什么
-- TimeZone 都落到同一日界 ⇒ 物化视图的日桶与看板/对账面天然对齐（口径不打架）。
-- ★ 会话级三视图的日桶取 **first_request_at**（会话开始日）而不是 last_request_at：
--   last_request_at 会随每次刷新把行**搬家**到别的日期 ⇒ 物化视图每次刷新都在
--   增删行，REFRESH CONCURRENTLY 的差分成本与抖动都变高；first_request_at 稳定。
--
-- ── 探测流量排除 ───────────────────────────────────────────────────────
-- 实测 25 万会话里 **80% 是非人工流量**：self-check-worker 11.1 万 + deployment-gate
-- 8.9 万 + 各类 verify/e2e/smoke 约 1 万。不排除它们，「人员排行」的前两名永远是
-- 自检与部署门禁，这个功能就没有意义。
-- ⇒ 判定收敛到**单一函数** session_analytics_is_probe(owner, client, title)：
--   四个视图**共用**同一判定 ⇒ 四个维度的人数/请求数互相自洽，不会一个排除了
--   另一个没排除。sessions.is_probe 列是该判定的物化缓存 + 运维手工排除位
--   （手工置真 ⇒ 该会话在会话级三视图里被排除；请求级模型视图按账号模式判定）。
--
-- ── 视图族契约（admin/session_analytics 包读取）────────────────────────
--   user_session_aggregates   ：人员维度（会话级）owner / tenant_id / date /
--                                session_count / project_count / task_count /
--                                total_tokens / internal_cost / external_cost /
--                                avg_duration_seconds / avg_turns / last_active_at
--   project_task_aggregates   ：项目+任务维度（会话级）owner / tenant_id / project /
--                                task / date / session_count / turn_count / total_tokens /
--                                internal_cost / external_cost / last_active_at
--   agent_usage_aggregates    ：智能体维度（会话级）agent_name / tenant_id / date /
--                                user_count / session_count / task_count / total_tokens /
--                                internal_cost / external_cost / avg_duration_seconds /
--                                last_used_at
--   model_usage_aggregates    ：模型维度（**请求级**）model / provider / tenant_id /
--                                date / request_count / success_count / cache_hit_count /
--                                user_count / input_tokens / output_tokens / total_tokens /
--                                internal_cost / external_cost / avg_duration_ms / last_used_at
--
-- ★ 口径分层（**必须写下来，否则两个面会互相打架**）：
--   前三个视图是**会话级**（一个会话一行聚合）；第四个是**请求级**（一次请求一行聚合）。
--   原因：缓存命中率、成功率、延迟只有请求级才有（session_summaries 没有缓存列）。
--   ⇒ 「会话数/会话成本」看前三视图；「请求数/成功率/缓存率」看第四视图。
--   ⇒ 两者**不可相加**（会话成本 ≠ 请求成本之和，同一语义下也不该加）。

-- ============================================================================
-- Part 1: sessions 表元数据列
-- ============================================================================
-- ★ 保留这四列的理由：它们是「Phase 3 元数据抽取」的落地区（会话创建时由客户端
--   显式标注 project / task / task_category，或由后台抽取任务回填）。
--   本迁移只建列 + 建索引 + 标记探测会话；**不负责抽取**（抽取是另一条列车）。
--   抽取落地后，project_task_aggregates 的 project / task 口径会从
--   「gw_project_id / task_id 的既有值」升级为「抽取出的真实项目/任务名」。

ALTER TABLE public.sessions ADD COLUMN IF NOT EXISTS project VARCHAR(255);
ALTER TABLE public.sessions ADD COLUMN IF NOT EXISTS task TEXT;
ALTER TABLE public.sessions ADD COLUMN IF NOT EXISTS task_category VARCHAR(100);
ALTER TABLE public.sessions ADD COLUMN IF NOT EXISTS is_probe BOOLEAN DEFAULT false;

CREATE INDEX IF NOT EXISTS idx_sessions_project
    ON public.sessions(project)
    WHERE project IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_sessions_task_category
    ON public.sessions(task_category)
    WHERE task_category IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_sessions_is_probe
    ON public.sessions(is_probe)
    WHERE is_probe = true;

COMMENT ON COLUMN sessions.project IS '会话关联的项目名称；Phase 3 元数据抽取的落地区';
COMMENT ON COLUMN sessions.task IS '会话的任务描述；Phase 3 元数据抽取的落地区';
COMMENT ON COLUMN sessions.task_category IS '任务分类（如 feature_development / bug_fix / research）';
COMMENT ON COLUMN sessions.is_probe IS '是否为探测/测试会话：1=排除出统计；由 session_analytics_is_probe 判定 + 运维手工置位';

-- ============================================================================
-- Part 2: 探测会话判定函数（四个视图共用的单一真源）
-- ============================================================================
-- ★ 为什么是函数而不是各视图各写一段 WHERE：
--   四个视图的排除口径必须**逐字一致**——否则会出现「人员榜排除了这批流量、
--   模型榜没排除」⇒ 同一个用户在不同页面看到两套数字。
--   收敛到函数后，改规则只需改这一处，下一次刷新四个视图同时生效。
--
-- ★ 判定依据（账号 / 客户端 / 标题三个维度）：
--   账号：自检、部署门禁、验证脚本、e2e、smoke、临时/调试账号、探针账号
--   客户端：测试类客户端（modelhub-test / e2e-* / *-test / *-smoke / *probe*）
--   标题：test / probe / verify / e2e / 测试 / 探测
--
-- ★ 空串 owner 的坑（真库实测）：owner_user 有 9,720 条是**空字符串**（不是 NULL），
--   对应「无归属会话」（客户端是 curl / python-client / opencode 等没带 owner 的）。
--   若只判 `IS NOT NULL`，这批会话会聚成一个 owner='' 的**假人**出现在人员排行里
--   （实测：9,717 会话 / 736M tokens / 340 美元）⇒ 一律用 NULLIF(...,'') 归一。
--
-- ★ 真实用户不会被误伤（实测 211 的 34 个账号里，人工账号
--   huangxt / lubinbin / kaixuan-1 / admin / gateway-ops / zcode / asm-session-agent
--   / pi-autollm-211 均不匹配下列模式）。

CREATE OR REPLACE FUNCTION public.session_analytics_is_probe(
    p_owner  text,
    p_client text,
    p_title  text
) RETURNS boolean
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $fn$
    SELECT
        COALESCE(p_owner ~* '(^self-check)|(^deployment-gate)|(^test@)|(verify)|(^audit)|(^e2e)|(-e2e$)|(probe)|(-debug$)|(-tmp$)|(^bot$)|(-bot$)', false)
     OR COALESCE(p_client ~* '(^test)|(-test$)|(probe)|(smoke)|(selfcheck)|(^e2e)', false)
     OR COALESCE(p_title ~* '(^test)|(^probe)|(^verify)|(^e2e)|(测试)|(探测)', false)
$fn$;

COMMENT ON FUNCTION public.session_analytics_is_probe(text, text, text)
    IS '判定会话是否属于探测/测试流量：账号、客户端、标题三维度模式匹配；会话分析四个物化视图共用此判定';

-- ============================================================================
-- Part 3: 人员维度（会话级）
-- ============================================================================
-- 数据源：session_summaries（指标）× sessions（owner / 手工探测标记）× session_dim（任务）
-- 日桶：first_request_at 的 CST 日
-- 唯一索引：(owner, tenant_id, date) —— REFRESH CONCURRENTLY 的硬前提

CREATE MATERIALIZED VIEW IF NOT EXISTS public.user_session_aggregates AS
SELECT
    NULLIF(s.owner_user, '')                       AS owner,
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
    NOW()                                          AS refreshed_at
FROM session_summaries ss
JOIN sessions s
  ON s.session_id = ss.session_key
 AND s.tenant_id = ss.tenant_id
LEFT JOIN session_dim sd
  ON sd.session_key = ss.session_key
 AND sd.tenant_id = ss.tenant_id
WHERE NULLIF(s.owner_user, '') IS NOT NULL
  AND NOT COALESCE(s.is_probe, false)
  AND NOT session_analytics_is_probe(s.owner_user, s.client_type, s.title)
GROUP BY NULLIF(s.owner_user, ''), ss.tenant_id,
         (ss.first_request_at AT TIME ZONE 'Asia/Shanghai')::date;

CREATE UNIQUE INDEX IF NOT EXISTS idx_user_session_agg_pk
    ON public.user_session_aggregates(owner, tenant_id, date);
CREATE INDEX IF NOT EXISTS idx_user_session_agg_date
    ON public.user_session_aggregates(date DESC);
CREATE INDEX IF NOT EXISTS idx_user_session_agg_tenant
    ON public.user_session_aggregates(tenant_id, date DESC);
CREATE INDEX IF NOT EXISTS idx_user_session_agg_owner
    ON public.user_session_aggregates(owner, date DESC);

COMMENT ON MATERIALIZED VIEW public.user_session_aggregates IS
    '人员维度会话日聚合（会话级口径；源 session_summaries × sessions × session_dim；已排除探测流量）';

-- ============================================================================
-- Part 4: 项目 + 任务维度（会话级）
-- ============================================================================
-- ★ project 口径 = session_summaries.gw_project_id（真库形如 app:default / app:system /
--   app:openpocket / app:hermes / app:ide / app:admin / app:applicant，共 18 个值）
--   —— 剥掉 'app:' 前缀后是「应用/项目」标识。Phase 3 抽取落地后可换成真实项目名。
-- ★ task 口径 = session_dim.task_id；'default' 与 'auto-title:*' 归一为 NULL
--   （前者是「无具体任务」，后者是自动标题任务的临时 id，都不是任务维度信号）。

CREATE MATERIALIZED VIEW IF NOT EXISTS public.project_task_aggregates AS
SELECT
    NULLIF(s.owner_user, '')                       AS owner,
    ss.tenant_id                                   AS tenant_id,
    regexp_replace(ss.gw_project_id, '^app:', '')  AS project,
    -- ★ task 用 ''（而不是 NULL）表示「无具体任务」：视图的唯一索引必须是
    --   **纯列**索引（PG 的 REFRESH CONCURRENTLY 明确拒绝表达式索引，见 Part 4
    --   索引段），而纯列唯一索引遇到 NULL 值时「NULL 互不相等」⇒ 同一
    --   (owner, tenant, project, date) 下的多个无任务行会被判为不重复 ⇒
    --   CONCURRENTLY 的差分阶段建不出唯一索引而失败。归一成 '' 后该列恒非空，
    --   唯一性成立。Go 侧读取时用 NULLIF(task,'') 还原「无任务」语义。
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
JOIN sessions s
  ON s.session_id = ss.session_key
 AND s.tenant_id = ss.tenant_id
LEFT JOIN session_dim sd
  ON sd.session_key = ss.session_key
 AND sd.tenant_id = ss.tenant_id
WHERE ss.gw_project_id IS NOT NULL
  AND NULLIF(s.owner_user, '') IS NOT NULL
  AND NOT COALESCE(s.is_probe, false)
  AND NOT session_analytics_is_probe(s.owner_user, s.client_type, s.title)
GROUP BY NULLIF(s.owner_user, ''), ss.tenant_id,
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

-- ★ 唯一索引必须是**纯列**索引：PG 的 REFRESH MATERIALIZED VIEW CONCURRENTLY
--   明确拒绝带表达式的索引（findCommittedUniqueIndex 会跳过 indexprs 非空的索引），
--   且要求索引列上不出现 NULL（NULL 互不相等 ⇒ 唯一性不成立 ⇒ 差分阶段失败）。
--   ⇒ 本视图把 task 归一成 ''（见上），四个键列全部恒非空 ⇒ 唯一索引纯列可用。
CREATE UNIQUE INDEX IF NOT EXISTS idx_project_task_agg_pk
    ON public.project_task_aggregates(owner, tenant_id, project, task, date);
CREATE INDEX IF NOT EXISTS idx_project_task_agg_owner
    ON public.project_task_aggregates(owner, date DESC);
CREATE INDEX IF NOT EXISTS idx_project_task_agg_project
    ON public.project_task_aggregates(project, date DESC);
CREATE INDEX IF NOT EXISTS idx_project_task_agg_tenant
    ON public.project_task_aggregates(tenant_id, date DESC);

COMMENT ON MATERIALIZED VIEW public.project_task_aggregates IS
    '项目+任务维度会话日聚合（会话级口径；已排除探测流量）';

-- ============================================================================
-- Part 5: 智能体维度（会话级）
-- ============================================================================
-- 智能体 = sessions.agent_name（go-client / pi / minimax-code / claude-code / codex /
-- curl / unknown）。★ sessions.agent_role 实测**全部**是 'main'（没有子代理会话），
--   所以 agent_name 就是「驱动这个会话的智能体」，不需要再按 role 过滤。

CREATE MATERIALIZED VIEW IF NOT EXISTS public.agent_usage_aggregates AS
SELECT
    NULLIF(s.agent_name, '')                        AS agent_name,
    ss.tenant_id                                   AS tenant_id,
    (ss.first_request_at AT TIME ZONE 'Asia/Shanghai')::date AS date,
    COUNT(DISTINCT NULLIF(s.owner_user, ''))       AS user_count,
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
JOIN sessions s
  ON s.session_id = ss.session_key
 AND s.tenant_id = ss.tenant_id
LEFT JOIN session_dim sd
  ON sd.session_key = ss.session_key
 AND sd.tenant_id = ss.tenant_id
WHERE NULLIF(s.agent_name, '') IS NOT NULL
  AND NOT COALESCE(s.is_probe, false)
  AND NOT session_analytics_is_probe(s.owner_user, s.client_type, s.title)
GROUP BY NULLIF(s.agent_name, ''), ss.tenant_id,
         (ss.first_request_at AT TIME ZONE 'Asia/Shanghai')::date;

CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_usage_agg_pk
    ON public.agent_usage_aggregates(agent_name, tenant_id, date);
CREATE INDEX IF NOT EXISTS idx_agent_usage_agg_agent
    ON public.agent_usage_aggregates(agent_name, date DESC);
CREATE INDEX IF NOT EXISTS idx_agent_usage_agg_tenant
    ON public.agent_usage_aggregates(tenant_id, date DESC);

COMMENT ON MATERIALIZED VIEW public.agent_usage_aggregates IS
    '智能体维度会话日聚合（会话级口径；源 sessions.agent_name；已排除探测流量）';

-- ============================================================================
-- Part 6: 模型维度（请求级）
-- ============================================================================
-- ★ 这个视图是**请求级**口径（与前三个会话级不同）：缓存命中率 / 成功率 / 延迟
--   只有请求级有。源 = request_logs_with_current_month（session_turns_hot ∪ session_turns）。
-- ★ provider = providers.code（provider_id 是 bigint 外键）；查不到 provider 时归
--   'unknown'（provider_id 为 NULL 或 provider 被删）。
-- ★ 人员维度用 api_key_owner_user：真库该列填充率 97.4%（owner_user 则是 0%）。
-- ★ 模型名取 client_model（客户端请求的模型，填充率 99.99%），空则回落 outbound_model。

CREATE MATERIALIZED VIEW IF NOT EXISTS public.model_usage_aggregates AS
SELECT
    COALESCE(NULLIF(rl.client_model, ''), NULLIF(rl.outbound_model, ''), 'unknown') AS model,
    COALESCE(p.code, 'unknown')                     AS provider,
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
LEFT JOIN providers p
  ON p.id = rl.provider_id
WHERE NOT (
    rl.api_key_owner_user IS NOT NULL
    AND session_analytics_is_probe(rl.api_key_owner_user, NULL, NULL)
)
GROUP BY COALESCE(NULLIF(rl.client_model, ''), NULLIF(rl.outbound_model, ''), 'unknown'),
         COALESCE(p.code, 'unknown'),
         rl.tenant_id,
         (rl.ts AT TIME ZONE 'Asia/Shanghai')::date;

CREATE UNIQUE INDEX IF NOT EXISTS idx_model_usage_agg_pk
    ON public.model_usage_aggregates(model, provider, tenant_id, date);
CREATE INDEX IF NOT EXISTS idx_model_usage_agg_model
    ON public.model_usage_aggregates(model, date DESC);
CREATE INDEX IF NOT EXISTS idx_model_usage_agg_provider
    ON public.model_usage_aggregates(provider, date DESC);
CREATE INDEX IF NOT EXISTS idx_model_usage_agg_tenant
    ON public.model_usage_aggregates(tenant_id, date DESC);

COMMENT ON MATERIALIZED VIEW public.model_usage_aggregates IS
    '模型维度请求日聚合（**请求级**口径；源 request_logs_with_current_month；已排除探测流量）';

-- ============================================================================
-- Part 7: 刷新函数
-- ============================================================================
-- ★ 用 CONCURRENTLY：REFRESH 期间不锁住视图（读端点继续可用），代价是需要唯一索引
--   （上面四个视图都配了）。普通 REFRESH 会短暂 ExclusiveLock 住视图 ⇒ 看板会 503。
-- ★ view_name 是**参数**不是拼接的标识符 ⇒ 不会被 SQL 注入。
-- ★ 刷新失败不吞：记 WARNING 并把错误信息带出来（运维能从 NOTICE/WARNING 看到原因）。

CREATE OR REPLACE FUNCTION public.refresh_session_analytics_view(view_name text)
RETURNS void
LANGUAGE plpgsql
AS $fn$
BEGIN
    EXECUTE format('REFRESH MATERIALIZED VIEW CONCURRENTLY %I', view_name);
    RAISE NOTICE 'refreshed session analytics materialized view: %', view_name;
EXCEPTION
    WHEN OTHERS THEN
        RAISE WARNING 'failed to refresh session analytics materialized view %: %', view_name, SQLERRM;
        RAISE;
END;
$fn$;

COMMENT ON FUNCTION public.refresh_session_analytics_view(text) IS
    '并发刷新指定的会话分析物化视图（CONCURRENTLY；失败抛错不吞）';

CREATE OR REPLACE FUNCTION public.refresh_all_session_analytics_views()
RETURNS void
LANGUAGE plpgsql
AS $fn$
BEGIN
    PERFORM public.refresh_session_analytics_view('user_session_aggregates');
    PERFORM public.refresh_session_analytics_view('project_task_aggregates');
    PERFORM public.refresh_session_analytics_view('agent_usage_aggregates');
    PERFORM public.refresh_session_analytics_view('model_usage_aggregates');
END;
$fn$;

COMMENT ON FUNCTION public.refresh_all_session_analytics_views() IS
    '并发刷新全部会话分析物化视图（四个视图依次刷新）';

-- ============================================================================
-- Part 8: 探测会话回填（sessions.is_probe 列的初值）
-- ============================================================================
-- ★ 这是一次性回填：把判定为探测流量的历史会话标记出来，供运维在视图里排除。
--   视图本身用的是函数判定（自愈：新增的探测会话下次刷新自动被排除），
--   这一列是**物化缓存 + 手工覆盖位**（运维手工置 true ⇒ 会话级三视图排除它）。
-- ★ 用函数而不是内联模式 ⇒ 判定规则改一处，视图与回填同时生效。

UPDATE public.sessions s
   SET is_probe = true
 WHERE COALESCE(s.is_probe, false) = false
   AND session_analytics_is_probe(s.owner_user, s.client_type, s.title);

DO $$
DECLARE
    probe_count bigint;
BEGIN
    SELECT count(*) INTO probe_count FROM sessions WHERE is_probe = true;
    RAISE NOTICE 'session analytics: % sessions marked as probe', probe_count;
END $$;

-- ============================================================================
-- Part 9: 首次填充
-- ============================================================================
-- ★ 视图在 Part 3~6 已由 CREATE MATERIALIZED VIEW ... AS SELECT 建出并**已带数据**
--   ⇒ 直接 CONCURRENTLY 刷新是安全的（CONCURRENTLY 要求视图非空且有唯一索引，
--   两者此刻都满足）。
-- ★ 四个视图依次刷新；任一失败 ⇒ 整个迁移中止（不留半套视图）。

SELECT public.refresh_all_session_analytics_views();

DO $$
DECLARE
    u bigint; p bigint; a bigint; m bigint;
BEGIN
    SELECT count(*) INTO u FROM public.user_session_aggregates;
    SELECT count(*) INTO p FROM public.project_task_aggregates;
    SELECT count(*) INTO a FROM public.agent_usage_aggregates;
    SELECT count(*) INTO m FROM public.model_usage_aggregates;
    RAISE NOTICE 'session analytics initial rows: user=% project_task=% agent=% model=%', u, p, a, m;
END $$;

-- ============================================================================
-- 完成
-- ============================================================================
