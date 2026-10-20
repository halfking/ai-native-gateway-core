-- 883_session_family_cost_view_metrics.sql
-- 2026-10-20：把 auto-route 成本仪表盘的**两个活指标**从 v1 冻结族切到会话族。
--
-- -----------------------------------------------------------------------
-- 缺陷本体（2026-10-20 本机实测，非推理）
--
-- `customer_cost_view` 与 `model_cost_per_task_view` 的定义体里各有一个
-- **相关子查询**读 v1 母表：
--
--   customer_cost_view.active_concurrent:
--     SELECT count(*) FROM request_logs rl
--      WHERE rl.api_key_id = akmc.api_key_id AND rl.is_auto_request
--        AND rl.ts >= now() - interval '5 minutes' AND rl.success IS NOT NULL
--
--   model_cost_per_task_view.avg_latency_ms:
--     SELECT avg(rl.latency_ms) FROM request_logs rl
--      WHERE rl.outbound_model = mcp.raw_model AND rl.success
--        AND rl.ts >= now() - interval '7 days'
--
-- 860 已把 v1 母表改名为 `_request_logs`（视图按 OID 跟随 ⇒ 真库读数确为
-- `_request_logs`），而 v1 自 S4 起**停写**（`storage.request_logs_write_enabled
-- =false`，2026-10-13 起）⇒ 该族**冻结**、不再有任何新行。
--
-- ⇒ 后果（两个都是**活指标**，都**无错误信号**）：
--   ① `active_concurrent` 是 **5 分钟窗**。冻结族里最新的行也早于 5 分钟
--      ⇒ 这个计数**在任何环境都恒为 0**（不是「本机没数据」）——
--      它是「当前有多少个 auto 请求在跑」的实时并发数，恒 0 ⇒ 并发压力
--      看板与由它派生的 `pressure_ratio`/`avg_pressure_1h` 全部失去意义；
--   ② `avg_latency_ms` 是 **7 天窗**。冻结族在窗口内只可能给出**截止
--      停写那一刻**的历史值（7 天后变 NULL）⇒ 一个叫「平均延迟」的活指标
--      实际在读**快照**。
--
-- 三个判别读数（真库，见 §5.12 取数命令）：
--   · `public._request_logs` count(*) = 0、`_request_logs_hot` count(*) = 0
--   · `pg_trigger` 全量清点：**没有任何**触发器往 `_request_logs` 写，
--     ⇒ 那族**冻结**、不再有任何新行
--     （⚠ 不要用 `pg_stat_user_tables.n_tup_ins` 当「有没有被写过」的证据：
--      PG **计入回滚事务里的插入**，任何跑过 `defer tx.Rollback()` 夹具的
--      测试都会把它写高 ⇒ 本轮实测同一张表「计数 13 / 行数 0」）
--   · 两个 admin 端点 `/api/admin/auto-route/cost/customer` 与
--     `/api/admin/auto-route/cost/model` 均返回 `[]`
--
-- -----------------------------------------------------------------------
-- 这条迁移做什么（以及刻意不做什么）
--
-- 做：**把这两个子查询的源换成会话族**，逐条对应 v1 的列语义：
--
--   v1 `outbound_model`  →  会话族 `raw_model_name`
--       （`domains/streaming/executors/executor.go` 的 `candidateRawModel(c)`
--        ⇒ 候选的**上游原始模型**，与 v1 `outbound_model` 同义；
--        `session_turns.model` 是**客户端**模型，语义不同，不能用）
--   v1 `api_key_id` / `is_auto_request` / `success` / `ts` / `latency_ms`
--       →  会话族同名同义列（`session_turns` 与 `session_turns_hot` 都有）
--
-- ⚠ **一处类型差异（不查类型就会踩）**：会话族 `api_key_id` 是 **text**
--   （写侧 `domains/session/v2/turn_writer.go` 的 `APIKeyID string`，落库是
--   十进制文本 `'2'`/`'10'`），而 `api_key_model_cost.api_key_id` 是
--   **integer**（v1 侧 `_request_logs.api_key_id` 是 bigint）⇒ 直接比较报
--   `operator does not exist: text = integer` ⇒ 必须显式 `::text`。
--
-- 两条腿的选择（各自有依据，不是随手挑）：
--
--   ① `active_concurrent`（5 分钟窗）= **hot 单腿** `public.session_turns_hot`
--       · **完整**：在线写路径只写 hot（`domains/session/v2/turn_writer.go`
--         插 `session_turns_hot`），晋级（promote）延迟远大于 5 分钟
--         ⇒ 5 分钟窗内的行**必然**在 hot；
--       · **无重复**：hot 单腿没有「同一行被数两次」的可能（下面 ② 的
--         UNION ALL 才有那个问题），而这是**计数**指标；
--       · **便宜**：本仓既有先例——`db/request_logs_view_schema.go` 的
--         `SessionFamilyTurnsHotSourceSQL` 明确写「短窗仪表盘用 hot-only，
--         免得父表分区扫描与并发 MV REFRESH 把 `/api/logs/top-models`
--         顶爆 10s 预算（2026-10-15 生产 500 实证）」。
--
--   ② `avg_latency_ms`（7 天窗）= **hot ∪ parent** 两条腿 UNION ALL
--       · 7 天窗跨过了 hot 的保留期（名义 8h）⇒ 必须带 parent 腿，否则
--         窗口里 7 天前到 8 小时前那一段**整段丢失**；
--       · 形态与本仓规范同款（`db/request_logs_view_schema.go` 的
--         `SessionFamilyTurnsSourceSQL` = hot ∪ parent UNION ALL）；
--       · **为什么这里可以朴素 UNION ALL**（而 `admin/usage_enhanced.go`
--         那条计数指标必须走族视图）：promote 会**排空 hot** ⇒ 同一行不会
--         同时在两腿 ⇒ 不会重复；且本指标是**均值**，对偶发重复不敏感
--         （计数指标对重复敏感，所以那条债是另一回事）。
--       · 复用现成族视图 `session_turns_with_current_month` **不可行**：
--         它不投影 `raw_model_name`/`api_key_id`/`is_auto_request`
--         （实测 information_schema），而本子查询需要 `raw_model_name`。
--
-- **刻意不做**：不复活 `api_key_model_cost` 的聚合链（trigger + 唯一索引）。
-- 那条链**从未接线**：正典与全部 startup 迁移里都没有 trigger，**真库全触发器
-- 清点也确认 `trg_update_api_key_model_cost` 没挂在任何表上**（函数
-- `update_api_key_model_cost()` 存在但无调用方），该表**0 行**、**0 索引**
-- ⇒ 两个端点本来就返回 `[]`，与本迁移无关。
-- （⚠ 同样不要用 `n_tup_ins` 当证据——见文件头注释里那条订正。）
-- 复活它是**新功能**（还要写侧补 `canonical_id`，实测会话族该列 0/NULL），
-- 已登记为遗留 L11 单独排期。
-- ⇒ **本迁移的可见效果是「读源正确」，不是「端点有数」**；两者不要混。
--
-- 锁与时长：`CREATE OR REPLACE VIEW` 只取 ACCESS EXCLUSIVE 于视图本身
-- （不锁底层表）⇒ 秒级。lock_timeout=60s：抢不到锁快速失败、整体回滚。
-- 幂等：`CREATE OR REPLACE VIEW` 天然幂等（列名与列序不变 ⇒ 允许替换）。
-- Down：还原成读 v1 母表的旧定义体（列名/列序/类型完全一致 ⇒ 可替换）。
--
-- 台账/通道：canonical 与 installer embeddata 两副本 SHA 现场一致；
-- installer go:embed + map、dbinit StartupFiles、TSV、部署通道 files=()、
-- .down.sql 均已就位。契约门：migration_883_session_family_cost_view_metrics_test.go
-- （钉「视图体不再含 v1 母表读」+「两条腿的选型理由」）。

BEGIN;

SET LOCAL lock_timeout = '60s';
SET LOCAL statement_timeout = '5min';

CREATE OR REPLACE VIEW public.customer_cost_view AS
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
           FROM public.session_turns_hot st
          WHERE ((st.api_key_id = akmc.api_key_id::text) AND (st.is_auto_request = true) AND (st.ts >= (now() - '00:05:00'::interval)) AND (st.success IS NOT NULL))) AS active_concurrent,
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

COMMENT ON VIEW public.customer_cost_view IS 'Auto route: per-API-Key customer cost dashboard (1h/24h/7d windows + concurrency + scores). active_concurrent is computed live from the session family (session_turns_hot, 5min window) — 883 migrated it off the frozen v1 family.';

CREATE OR REPLACE VIEW public.model_cost_per_task_view AS
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
    ( SELECT avg(st.latency_ms) AS avg
           FROM (SELECT st_hot.latency_ms,
                        st_hot.raw_model_name,
                        st_hot.success,
                        st_hot.ts
                   FROM public.session_turns_hot st_hot
                 UNION ALL
                 SELECT st_par.latency_ms,
                        st_par.raw_model_name,
                        st_par.success,
                        st_par.ts
                   FROM public.session_turns st_par) st
          WHERE ((st.raw_model_name = mcp.raw_model) AND (st.success = true) AND (st.ts >= (now() - '7 days'::interval)))) AS avg_latency_ms,
    sum(requests_total) AS total_requests,
    count(DISTINCT api_key_id) AS unique_api_keys
   FROM public.api_key_model_cost mcp
  WHERE (bucket >= (now() - '7 days'::interval))
  GROUP BY canonical_id, raw_model;

COMMENT ON VIEW public.model_cost_per_task_view IS 'Auto route: per-model aggregated cost for last 7 days. avg_latency_ms is computed live from the session family (session_turns_hot ∪ session_turns) — 883 migrated it off the frozen v1 family.';

COMMIT;

-- POST_CONDITION:
--   -- 两张视图的定义体里不再出现**任何** request_logs 族引用
--   -- （真库读数；期望 false / false）：
--   -- ⚠ 必须用 `strpos`（精确子串），**不要**用 `LIKE '%_request_logs%'`——
--   --   SQL 的 `LIKE` 里 `_` 是**单字符通配符** ⇒ 那条写法会把 canonical
--   --   视图名 `request_logs_with_current_month` 也判成命中 ⇒ 上一轮就是
--   --   因此把「5 张 v_* 视图读 v1」多算了 2 张（实为假阳性）。
--   --   本仓自己的门 `internal/sqlreadguard` 用的是正则 + 词边界，那个门是对的。
--   SELECT strpos(pg_get_viewdef('public.customer_cost_view'::regclass, true), 'request_logs') > 0;
--   SELECT strpos(pg_get_viewdef('public.model_cost_per_task_view'::regclass, true), 'request_logs') > 0;
--   -- 会话族两腿都在（期望 true / true / true；注意 pg_get_viewdef 渲染时
--   -- **不带 public. 前缀**，核对要用渲染后的形态）：
--   SELECT pg_get_viewdef('public.customer_cost_view'::regclass, true) LIKE '%FROM session_turns_hot st%';
--   SELECT pg_get_viewdef('public.model_cost_per_task_view'::regclass, true) LIKE '%FROM session_turns_hot st_hot%';
--   SELECT pg_get_viewdef('public.model_cost_per_task_view'::regclass, true) LIKE '%FROM session_turns st_par%';
