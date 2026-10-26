-- ===========================================================================
-- §143.38：迁移 890（v1 `request_logs` 族真删）的**执行前置核验**（只读）
--
-- ★ 为什么需要这条：
--   890 会 DROP 整个 v1 族（母表 / hot / bodies / archive / 视图 / 序列 / 13 个函数）。
--   而**生产上正在跑的二进制**（实测，2026-10-21）里的 auto-route 亲和度聚合
--   仍然是**三条 NOT EXISTS 臂**（`_request_logs_hot` / `_request_logs` / 会话族）——
--   仓内 HEAD 已改成**一条臂**（commit 5f64031cb「退役 v1 族 + 全仓读面切会话族」）。
--   ⇒ ★★ **执行顺序反了 ⇒ 那条聚合整句 42P01**（同一条 FROM 里任何被提及的
--      关系不存在 ⇒ 整句失败，不是那条臂失败）
--   ⇒ ⇒ 亲和度学习链断裂，且**静默**（stale-while-error ⇒ 仍服务旧快照）。
--
-- ★ 判定口径：
--   直接看**生产上活跃的那条语句本身**（而不是看部署的 commit 号）：
--   `pg_stat_statements` 里本库中引用 `auto_route_selections_all` 的语句，
--   若还有一条 `still_references_v1 = t` ⇒ **禁止执行 890**。
--   ⇒ 这条判据的好处：它测的是**真正在跑的东西**，不依赖版本号/部署记录。
--
-- ★ 边界（诚实）：
--   · `pg_stat_statements` 只记**成功**执行过的语句 ⇒ 若聚合查询已经失败
--     （42P01），它在统计表里**不可见** ⇒ 此时本核验会误报「已切换」。
--     ⇒ 所以下面第 ③ 段给了**不依赖统计表**的旁证。
--   · `pg_stat_statements` 有条目数上限 ⇒ 极冷门语句可能被淘汰
--     ⇒ 所以第 ③ 段用 `pg_stat_activity` 之外的办法（见下）交叉核对。
--
-- 用法（生产，走既有只读护栏）：
--   bash ~/.mavis-tools/lgwp-prod.sh scripts/audit/verify_890_cutover_precheck.sql 120
-- ===========================================================================
SET statement_timeout = '60s';
SET lock_timeout = '5s';

-- ── ① 本库里【活跃的】auto-route 亲和度聚合语句：还有几条引用 v1 族？─────
SELECT s.queryid,
       s.calls,
       round(s.total_exec_time::numeric / 1000, 1)                       AS total_s,
       (s.query LIKE '%_request_logs%')                                   AS still_references_v1,
       (s.query LIKE '%session_turns%')                                    AS uses_session_family,
       left(regexp_replace(s.query, '\s+', ' ', 'g'), 90)                  AS q
  FROM pg_stat_statements s
 WHERE s.dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
   AND s.query LIKE '%auto_route_selections_all%'
 ORDER BY s.calls DESC;

-- ── ② 汇总判定：★ still_references_v1 = t 的条数必须为 0 才允许执行 890 ──
SELECT count(*) FILTER (WHERE s.query LIKE '%_request_logs%')  AS v1_referencing_statements,
       count(*)                                               AS affinity_statements,
       CASE WHEN count(*) FILTER (WHERE s.query LIKE '%_request_logs%') = 0
            THEN 'OK: 生产已切会话族 ⇒ 可以执行 890'
            ELSE 'BLOCKED: 仍有活跃语句引用 v1 族 ⇒ 【禁止执行 890】，先部署新二进制'
       END                                                     AS verdict
  FROM pg_stat_statements s
 WHERE s.dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
   AND s.query LIKE '%auto_route_selections_all%';

-- ── ③ 旁证（不依赖 pg_stat_statements）：聚合查询【此刻】能不能跑通？───────
--    ★ 若生产二进制仍是三臂 ⇒ 这条三臂形态能跑（v1 表还在）
--      ⇒ 单臂形态也一定能跑 ⇒ 所以「能跑」**不能**证明已切换；
--      ⇒ 本段只用来反证「已经 42P01 断链」这一种情形。
SELECT '三臂形态(v1 还在时能跑)'                       AS shape,
       (SELECT count(*) FROM public._request_logs_hot) AS v1_hot_rows,
       (SELECT count(*) FROM public.request_logs_2026_10) AS v1_month_rows,
       (SELECT count(*) FROM public.session_turns_hot)   AS sess_hot_rows,
       CASE WHEN to_regclass('public._request_logs_hot') IS NULL
            THEN 'v1 族已不存在 ⇒ 生产若仍跑三臂 ⇒ 聚合已 42P01 断链（查网关日志）'
            ELSE 'v1 族仍在 ⇒ 890 的非空闸与顺序约束都要看第 ①② 段'
       END AS note;
