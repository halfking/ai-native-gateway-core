-- 884_session_family_v_analytics_views.sql
-- 2026-10-20：把 3 张 **v1 裸母表** 分析视图改读会话族（request_logs 退役的最后一块读源）。
--
-- -----------------------------------------------------------------------
-- 缺陷本体（2026-10-20 真库实测，非推理）
--
-- 上一轮记的 L4 说「5 张 `v_*` 视图仍读 v1 裸母表」。**本轮用精确仪器重判后
-- 是 3 张，不是 5 张** —— 详见下面「量具订正」一节。
--
-- 真正读 v1 裸母表（`_request_logs`）的是这 3 张：
--
--   v_continuation_effectiveness   FROM _request_logs   （24h 窗）
--   v_node_switch_analysis         FROM _request_logs   （24h 窗）
--   v_timeout_effectiveness        FROM _request_logs   （24h 窗）
--
-- 而 v1 自 2026-10-13 停写、且历史数据已归档/清空 ⇒ 真库读数：
--
--   SELECT count(*) FROM public._request_logs;   -- 0
--   SELECT count(*) FROM public.v_continuation_effectiveness;  -- 0
--   SELECT count(*) FROM public.v_node_switch_analysis;        -- 0
--   SELECT count(*) FROM public.v_timeout_effectiveness;       -- 0
--
-- ⇒ 这 3 张视图**永久为空**，且**不报任何错**。
-- ⇒ 依赖盘点（pg_depend，真库）：`_request_logs` 被 **7 个视图**依赖，
--   其中这 3 张是**唯一没有任何 API/Go 代码消费**的（grep 全仓只有
--   `internal/sqlreadguard` 的 DEBT 登记与文档提到它们）⇒ 它们同时是
--   「后期删除 `_request_logs`」这一步的**依赖阻塞项**。
--
-- -----------------------------------------------------------------------
-- 这条迁移做什么
--
-- 把 3 张视图的定义体从 `_request_logs` 换到会话族
-- （`session_turns` ∪ `session_turns_hot` LEFT JOIN details），**列名、列序、
-- 列类型全部不变** ⇒ `CREATE OR REPLACE VIEW` 可直接替换，且**不丢能力**。
--
-- 为什么能换（逐列查 information_schema，2026-10-20 实测）：
--
--   语义列            v1 `_request_logs`      会话族 details 表        一致？
--   is_continuation   boolean                 boolean                 ✓
--   cached_response_id bigint                bigint                  ✓
--   context_size_tokens integer               integer                 ✓
--   node_switch_count  integer                 integer                 ✓
--   timeout_mode       character varying      character varying       ✓
--   effective_timeout_seconds integer        integer                 ✓
--   error_kind         text                    text（turns 侧）        ✓
--   latency_ms         integer                 integer（turns 侧）      ✓
--   success            boolean                 boolean（turns 侧）      ✓
--   ts                 timestamp with time zone timestamp with time zone ✓
--
-- ⇒ 会话族的 6 个「细节语义列」在 `session_turn_details` / `_hot` 上**都有**
--   （真库实测：877 / 312 行，与 turns 行数 1:1），所以这 3 个分析**能**
--   从会话族重算，不必随 v1 一起退役。
--
-- ⚠ **`request_logs_with_current_month`（canonical）不能复用**：它 118 列里
--   **不投影**这 6 个细节列（实测 information_schema）⇒ 只能用会话族直连形态。
--
-- -----------------------------------------------------------------------
-- 两条腿的选择：**hot ∪ parent UNION ALL**（与 `db/request_logs_view_schema.go`
-- 的 `SessionFamilyTurnsSourceSQL` 同款），join 键与 canonical 视图的会话臂
-- **逐字相同**：`d.tenant_id::text = t.tenant_id::text AND d.request_id =
-- t.request_id AND d.partition_date = t.partition_date`。
--
-- 前提（真库实测，2026-10-20；这三条也是判据里的前提断言）：
--
--   ① hot ∩ parent **无重叠**（turns 0 行、details 0 行）
--      ⇒ UNION ALL **不会**把同一行数两次。
--      （这 3 张都是**计数型**指标，若两条腿有重叠就会重复计数；
--        5 分钟窗那种指标之所以只取 hot 单腿，正是同一个理由。）
--   ② details 覆盖率 **100%**（hot 侧 0 行缺 details、parent 侧同样 0 行）
--      ⇒ LEFT JOIN 与 INNER JOIN 在本机等价；仍用 **LEFT JOIN**，
--        以免将来 details 缺失时把 `count(*)` 少算（v1 形态是每行都有这些列）。
--   ③ 语义列的**空**要能区分两种成因：改读源后
--      `v_continuation_effectiveness` / `v_timeout_effectiveness` 仍为空，
--      但那是**底层就没有这种流量**
--      （`session_turn_details` 里 `is_continuation` 0 行、
--        `effective_timeout_seconds` 非 NULL 0 行），**不是**读源错。
--      `v_node_switch_analysis` 则从 0 行变成**真实数据**（16 个小时桶、
--      314 个请求）⇒ 读源确实通了。
--
-- -----------------------------------------------------------------------
-- ★ 量具订正（本轮发现，**上一轮和我自己都踩了**）
--
-- 判定「视图是否还读 v1 裸母表」时，上一轮用的写法是
--
--     pg_get_viewdef(v) LIKE '%_request_logs%'
--
-- 而 SQL 的 `LIKE` 里 **`_` 是单字符通配符** ⇒ 这条判据会把
-- `request_logs_with_current_month`（**canonical 视图名**）也判成命中
-- ⇒ 上一轮「5 张 `v_*` 视图读 v1」里，**`v_model_health_dashboard` 与
-- `v_probe_system_health` 是假阳性**（实测它们定义体里**没有** `_request_logs`
-- 字面量，只是提到了 `request_logs_with_current_month`）。
--
-- 正确写法（本仓自己的门 `internal/sqlreadguard` 用的就是正则 + 词边界，
-- 那个门是**对的**）：
--
--     strpos(pg_get_viewdef(v), 'request_logs') > 0   -- 任何 request_logs 族引用
--     strpos(pg_get_viewdef(v), '_request_logs') > 0  -- 仅 v1 族字面量
--
-- ⇒ 本条的 POST_CONDITION 一律用 `strpos`，不再用 `LIKE`。
--
-- -----------------------------------------------------------------------
-- 锁与时长：`CREATE OR REPLACE VIEW` 只取 ACCESS EXCLUSIVE 于视图本身
-- （不锁底层表）⇒ 秒级。lock_timeout=60s：抢不到锁快速失败、整体回滚。
-- 幂等：`CREATE OR REPLACE VIEW` 天然幂等（列名与列序不变 ⇒ 允许替换）。
-- Down：还原成读 v1 母表的旧定义体（列名/列序/类型完全一致 ⇒ 可替换）。
-- ⚠ Down 里的表名必须用 **860 之后的物理名** `_request_logs`
--   （860 之前叫 `request_logs`；基线 schema 里的旧写法 `public.request_logs`
--   是**建视图当时**的名字，860 之后 PG 按 OID 跟随 ⇒ 视图实际读 `_request_logs`）。
--
-- 台账/通道：canonical 与 installer embeddata 两副本 SHA 现场一致；
-- installer go:embed + map、dbinit StartupFiles、TSV、部署通道 files=()、
-- .down.sql 均已就位。契约门：migration_884_session_family_v_analytics_views_test.go。

BEGIN;

SET LOCAL lock_timeout = '60s';
SET LOCAL statement_timeout = '5min';

CREATE OR REPLACE VIEW public.v_continuation_effectiveness AS
 SELECT date_trunc('hour'::text, t.ts) AS time_bucket,
    count(*) FILTER (WHERE (t.is_continuation = true)) AS continuation_requests,
    count(*) FILTER (WHERE (t.cached_response_id IS NOT NULL)) AS cache_hits,
    count(*) FILTER (WHERE ((t.is_continuation = true) AND (t.cached_response_id IS NULL))) AS cache_misses,
    round(((100.0 * (count(*) FILTER (WHERE (t.cached_response_id IS NOT NULL)))::numeric) / (NULLIF(count(*) FILTER (WHERE (t.is_continuation = true)), 0))::numeric), 2) AS cache_hit_rate,
    sum(COALESCE(t.context_size_tokens, 0)) FILTER (WHERE (t.cached_response_id IS NOT NULL)) AS tokens_saved
   FROM (SELECT t_hot.ts,
    d_hot.is_continuation,
    d_hot.cached_response_id,
    d_hot.context_size_tokens
    FROM public.session_turns_hot t_hot
    LEFT JOIN public.session_turn_details_hot d_hot
    ON ((d_hot.tenant_id::text = t_hot.tenant_id::text) AND (d_hot.request_id = t_hot.request_id) AND (d_hot.partition_date = t_hot.partition_date))
  UNION ALL
  SELECT t_par.ts,
    d_par.is_continuation,
    d_par.cached_response_id,
    d_par.context_size_tokens
    FROM public.session_turns t_par
    LEFT JOIN public.session_turn_details d_par
    ON ((d_par.tenant_id::text = t_par.tenant_id::text) AND (d_par.request_id = t_par.request_id) AND (d_par.partition_date = t_par.partition_date))) t
  WHERE (t.ts > (now() - '24:00:00'::interval))
  GROUP BY (date_trunc('hour'::text, t.ts))
 HAVING (count(*) FILTER (WHERE (t.is_continuation = true)) > 0)
  ORDER BY (date_trunc('hour'::text, t.ts)) DESC;

COMMENT ON VIEW public.v_continuation_effectiveness IS 'Auto route: hourly continuation/cache-hit effectiveness. Source migrated off the frozen v1 family by 884 — now computed live from the session family (session_turns_hot ∪ session_turns LEFT JOIN session_turn_details).';

CREATE OR REPLACE VIEW public.v_node_switch_analysis AS
 SELECT date_trunc('hour'::text, t.ts) AS time_bucket,
    t.node_switch_count AS switches,
    count(*) AS request_count,
    count(*) FILTER (WHERE (t.success = true)) AS success_count,
    round(((100.0 * (count(*) FILTER (WHERE (t.success = true)))::numeric) / (count(*))::numeric), 2) AS success_rate,
    round((avg(t.latency_ms) / 1000.0), 2) AS avg_latency_seconds
   FROM (SELECT t_hot.ts,
    t_hot.success,
    t_hot.latency_ms,
    d_hot.node_switch_count
    FROM public.session_turns_hot t_hot
    LEFT JOIN public.session_turn_details_hot d_hot
    ON ((d_hot.tenant_id::text = t_hot.tenant_id::text) AND (d_hot.request_id = t_hot.request_id) AND (d_hot.partition_date = t_hot.partition_date))
  UNION ALL
  SELECT t_par.ts,
    t_par.success,
    t_par.latency_ms,
    d_par.node_switch_count
    FROM public.session_turns t_par
    LEFT JOIN public.session_turn_details d_par
    ON ((d_par.tenant_id::text = t_par.tenant_id::text) AND (d_par.request_id = t_par.request_id) AND (d_par.partition_date = t_par.partition_date))) t
  WHERE ((t.ts > (now() - '24:00:00'::interval)) AND (t.node_switch_count >= 0))
  GROUP BY (date_trunc('hour'::text, t.ts)), t.node_switch_count
  ORDER BY (date_trunc('hour'::text, t.ts)) DESC, t.node_switch_count;

COMMENT ON VIEW public.v_node_switch_analysis IS 'Auto route: hourly node-switch analysis. Source migrated off the frozen v1 family by 884 — now computed live from the session family (session_turns_hot ∪ session_turns LEFT JOIN session_turn_details).';

CREATE OR REPLACE VIEW public.v_timeout_effectiveness AS
 SELECT date_trunc('hour'::text, t.ts) AS time_bucket,
    t.timeout_mode,
    count(*) AS total_requests,
    count(*) FILTER (WHERE (t.success = true)) AS success_count,
    count(*) FILTER (WHERE ((t.success = false) AND (t.error_kind ~~ '%timeout%'::text))) AS timeout_count,
    round(((100.0 * (count(*) FILTER (WHERE ((t.success = false) AND (t.error_kind ~~ '%timeout%'::text))))::numeric) / (count(*))::numeric), 2) AS timeout_rate,
    round(avg(t.effective_timeout_seconds), 2) AS avg_effective_timeout,
    round((avg(t.latency_ms) / 1000.0), 2) AS avg_latency_seconds
   FROM (SELECT t_hot.ts,
    t_hot.success,
    t_hot.latency_ms,
    t_hot.error_kind,
    d_hot.timeout_mode,
    d_hot.effective_timeout_seconds
    FROM public.session_turns_hot t_hot
    LEFT JOIN public.session_turn_details_hot d_hot
    ON ((d_hot.tenant_id::text = t_hot.tenant_id::text) AND (d_hot.request_id = t_hot.request_id) AND (d_hot.partition_date = t_hot.partition_date))
  UNION ALL
  SELECT t_par.ts,
    t_par.success,
    t_par.latency_ms,
    t_par.error_kind,
    d_par.timeout_mode,
    d_par.effective_timeout_seconds
    FROM public.session_turns t_par
    LEFT JOIN public.session_turn_details d_par
    ON ((d_par.tenant_id::text = t_par.tenant_id::text) AND (d_par.request_id = t_par.request_id) AND (d_par.partition_date = t_par.partition_date))) t
  WHERE ((t.ts > (now() - '24:00:00'::interval)) AND (t.effective_timeout_seconds IS NOT NULL))
  GROUP BY (date_trunc('hour'::text, t.ts)), t.timeout_mode
  ORDER BY (date_trunc('hour'::text, t.ts)) DESC;

COMMENT ON VIEW public.v_timeout_effectiveness IS 'Auto route: hourly timeout effectiveness. Source migrated off the frozen v1 family by 884 — now computed live from the session family (session_turns_hot ∪ session_turns LEFT JOIN session_turn_details).';

COMMIT;

-- POST_CONDITION:
--   -- ① 3 张视图的定义体里**任何** request_logs 族引用都不该存在
--   --    （用 strpos 精确匹配；⚠ 不要用 LIKE '%_request_logs%'——
--   --     `_` 是单字符通配符，会把 canonical 视图名 request_logs_with_current_month
--   --     也判成命中 ⇒ 上一轮就是因此多算了 2 张视图）
--   --    期望 false / false / false：
--   SELECT strpos(pg_get_viewdef('public.v_continuation_effectiveness'::regclass, true), 'request_logs') > 0;
--   SELECT strpos(pg_get_viewdef('public.v_node_switch_analysis'::regclass, true), 'request_logs') > 0;
--   SELECT strpos(pg_get_viewdef('public.v_timeout_effectiveness'::regclass, true), 'request_logs') > 0;
--   -- ② 会话族两腿都在（期望 true / true / true；pg_get_viewdef 渲染时**不带
--   --    public. 前缀**，核对要用渲染后的形态）：
--   SELECT pg_get_viewdef('public.v_node_switch_analysis'::regclass, true) LIKE '%FROM session_turns_hot t_hot%';
--   SELECT pg_get_viewdef('public.v_node_switch_analysis'::regclass, true) LIKE '%FROM session_turns t_par%';
--   SELECT pg_get_viewdef('public.v_timeout_effectiveness'::regclass, true) LIKE '%session_turn_details_hot d_hot%';
--   -- ③ 前提：hot ∩ parent 无重叠（计数型指标不能被数两次）；期望 0 / 0：
--   SELECT (SELECT count(*) FROM public.session_turns_hot h
--             JOIN public.session_turns p USING (session_id, turn_no));
--   SELECT (SELECT count(*) FROM public.session_turn_details_hot h
--             JOIN public.session_turn_details p USING (session_id, turn_no));
