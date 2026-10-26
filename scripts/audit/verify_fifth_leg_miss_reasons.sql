-- =====================================================================
-- §143.80 复算脚本：压缩器【第五腿】的落空机制 + 冷启动读链的库侧口径对齐
--
-- ★★★ 本脚本回答的问题（★ 全部来自 §143.79 遗留的问题 + §143.80 的新发现）：
--
--   Q1 ★★★ 第五腿（写侧 `getLatestBodies`）的规模与命中率（★ 单快照）
--   Q2 ★★★ 【第五腿落空的三分类】—— ★★ 本轮的核心发现：
--        ★ 它的落空【不是】单一原因，而是【两类性质完全不同】的：
--          ① 【时序性】：一次性会话的【首轮】⇒ 该 session 在 bodies 族还没行
--          ② 【结构性】：★ 分支会话（`gt_gw_` / `gs_gw_`）⇒ ★★ 【永久不写 bodies】
--             ⇒ ⇒ 对它们，第五腿【每一次都必然 0 行】⇒ ⇒ ★ 这是【结构性缺失】，
--             ⇒ ⇒ ★★ 不是「首轮还没写」那种时序问题 ⇒ ⇒ 【短路判据完全不同】
--   Q3 ★★★ 分支会话的规模与形态（★ 前缀分布 / 轮数 / bodies 覆盖）
--   Q4 ★★ bodies 族 ⊆ turns 族（★ 【当前状态】下 bodies 无行的会话在 turns 里都有行）
--   Q5 ★★ 冷启动读链的库侧口径（★ 与 154 服务侧日志对拍用的分母）
--
-- 用法（★ 只读；★ 必须串行；★ 生产 252 共享库）：
--   bash ~/.mavis-tools/lgwp-prod.sh scripts/audit/verify_fifth_leg_miss_reasons.sql 900
--
-- ★★ 量具纪律（★ 沿用 §143.79 的四条 + 本轮新增一条）：
--   ① 【形态判定用 `pg_column_size(...) > 12`】——★ 不用 `jsonb_typeof()`（会 detoast）。
--   ② 【不建任何对象】——★ 生产护栏拒绝 `CREATE TEMP TABLE`。
--   ③ 【全部读数放在同一条 SELECT / 同一批 CTE 里】——★ 跨快照相加会算出矛盾的数。
--   ④ 【逐形态点名认领】——★ 指纹写窄 ⇒ 系统性少算（★ §143.79 的教训）。
--   ⑤ ★★ 【「当前状态」与「查询那一刻」必须分开写】——★ 本轮的核心区分：
--        ★ X5 类查询答的是【当前 bodies 族里有没有行】⇒ ⇒ 它【看不到时序性落空】
--        ⇒ ⇒ 时序性落空只能靠【服务侧日志】或【写入时序】证。
-- =====================================================================
SET statement_timeout = '900s';
SET lock_timeout = '10s';

\echo '=== 1/5: ★★★ Q1 第五腿与读链各腿的【单快照】规模 ==='
WITH legs AS (
  SELECT 'L5_writepath_latest'  AS label, 5347544359269580322 AS qid UNION ALL
  SELECT 'L1_final_full',             -7576398701530481720 UNION ALL
  SELECT 'L1b_fallback',             -8278521907442285636 UNION ALL
  SELECT 'L2_delta_chain',           -202669097973246213 UNION ALL
  SELECT 'L3_join_turns',             8794675561828039885 UNION ALL
  SELECT 'L3_coalesce_ff',            3955058327330889760 UNION ALL
  SELECT 'L3_coalesce_ff_v2',        -7776241007544382269
)
SELECT l.label, s.queryid, s.calls, s.rows,
       round(s.total_exec_time::numeric, 1) AS total_ms,
       round(s.mean_exec_time::numeric, 3) AS mean_ms,
       -- ★ 命中率（★ rows/calls）★ ★ 与 §143.80 的服务侧口径对拍时要看这一列
       round(100.0 * s.rows / NULLIF(s.calls,0), 2) AS hit_pct
FROM legs l LEFT JOIN pg_stat_statements s ON s.queryid = l.qid
ORDER BY s.total_exec_time DESC;

\echo '=== 2/5: ★★★★★ Q2 第五腿落空的【三分类】（★ ★ 本轮的核心）==='
-- ★ 分类依据（★ 全部来自实测，不是推断）：
--   分支会话：`gt_gw_` / `gs_gw_` / `gt_` / `gs_` 前缀
--     ⇒ ★ 仓内自证：`admin/auto_title_generator.go`（gt_）与
--     ⇒ ★ `admin/auto_summary_generator.go`（gs_）⇒ ★ 它们是【后台 worker 自己开的
--     ⇒ ★ LLM 会话】⇒ ⇒ ★ 实测：bodies 族里【一条 branch 行都没有】
--   一次性会话：turns 族 1 轮 ⇒ 【首轮】⇒ bodies 还没写 ⇒ 时序性落空
WITH t AS (
  SELECT tenant_id, session_id, count(*) AS turns_n
  FROM public.session_turns_with_current_month
  GROUP BY tenant_id, session_id
), b AS (
  SELECT tenant_id, session_id FROM public.session_bodies_unified
  GROUP BY tenant_id, session_id
), j AS (
  SELECT t.tenant_id, t.session_id, t.turns_n,
         CASE WHEN b.session_id IS NULL THEN 0 ELSE 1 END AS has_bodies,
         CASE WHEN t.session_id LIKE 'gt_%' OR t.session_id LIKE 'gs_%'
              THEN 'branch' ELSE 'plain' END AS kind
  FROM t LEFT JOIN b ON b.tenant_id=t.tenant_id AND b.session_id=t.session_id
)
SELECT
  CASE WHEN kind = 'branch' AND has_bodies = 0 THEN '1_分支会话_结构性永久0行'
       WHEN kind = 'branch'                   THEN '1b_分支会话_有bodies(实测为0)'
       WHEN has_bodies = 1                   THEN '2_有bodies_第五腿必然命中'
       WHEN turns_n = 1                      THEN '3_一次性_时序性首轮0行'
       ELSE '4_多轮但无bodies_异常'
  END AS miss_reason,
  count(*) AS sessions,
  round(100.0 * count(*) / sum(count(*)) OVER (), 2) AS pct_of_sessions
FROM j
GROUP BY 1 ORDER BY 1;

\echo '=== 3/5: ★★★★ Q3 分支会话的规模与形态（★ 坐实「结构性」）==='
WITH t AS (
  SELECT tenant_id, session_id, count(*) AS turns_n
  FROM public.session_turns_with_current_month
  GROUP BY tenant_id, session_id
), b AS (
  SELECT tenant_id, session_id FROM public.session_bodies_unified
  GROUP BY tenant_id, session_id
)
SELECT
  CASE WHEN t.session_id LIKE 'gt_%' OR t.session_id LIKE 'gs_%' THEN 'branch' ELSE 'plain' END AS kind,
  count(*) AS sessions,
  count(*) FILTER (WHERE b.session_id IS NULL) AS sessions_without_bodies,
  round(100.0 * count(*) FILTER (WHERE b.session_id IS NULL) / count(*), 2) AS pct_without_bodies,
  round(avg(t.turns_n), 2) AS avg_turns,
  percentile_cont(0.5) WITHIN GROUP (ORDER BY t.turns_n) AS p50_turns,
  max(t.turns_n) AS max_turns
FROM t LEFT JOIN b ON b.tenant_id=t.tenant_id AND b.session_id=t.session_id
GROUP BY 1 ORDER BY 1;

\echo '=== 4/5: ★★★ Q4 bodies 族 ⊆ turns 族（★ 「当前状态」口径）==='
-- ★★ 诚实边界：★ 这一问答的是【当前】状态 ⇒ ⇒ 它【看不见】时序性落空
--   （★ 第五腿的调用发生在【写入之前】⇒ ⇒ 那时 bodies 族里还没有行）
WITH t AS (
  SELECT tenant_id, session_id FROM public.session_turns_with_current_month
  GROUP BY tenant_id, session_id
), b AS (
  SELECT tenant_id, session_id FROM public.session_bodies_unified
  GROUP BY tenant_id, session_id
)
SELECT (SELECT count(*) FROM t)                                    AS turns_sessions,
       (SELECT count(*) FROM b)                                    AS bodies_sessions,
       (SELECT count(*) FROM b WHERE NOT EXISTS
          (SELECT 1 FROM t WHERE t.tenant_id=b.tenant_id AND t.session_id=b.session_id))
                                                                      AS bodies_without_turns,
       (SELECT count(*) FROM t WHERE NOT EXISTS
          (SELECT 1 FROM b WHERE b.tenant_id=t.tenant_id AND b.session_id=t.session_id))
                                                                      AS turns_without_bodies;

\echo '=== 5/5: ★★ Q5 第五腿的【逐字复算】（★ 对 3 个 branch + 3 个 plain 各跑一次）==='
-- ★ 用第五腿的 SQL 形态逐字复算 ⇒ ⇒ 直接看它返几行
-- ★★★ ★ 为什么拆成两个独立的 FOR 循环（★ ★ 本轮又踩了一次同族坑）：
--   ★ `FOR rec IN SELECT … LIMIT 3 UNION ALL SELECT … LIMIT 3`
--   ★ ⇒ 报 `syntax error at or near "UNION"`（★ §143.77 已记录过，本轮又踩了一次）
--   ★ ⇒ ⇒ 【UNION 的分支里带 ORDER BY / LIMIT 必须加括号】
--   ★ ⇒ ⇒ 而这里加括号会很难读 ⇒ ⇒ **最稳的写法是【拆成两个独立循环】**
DO $do$
DECLARE rec RECORD; sid TEXT; n INT; BEGIN
  -- ★ branch 三个（★ 结构性：必然 0 行）
  FOR rec IN
    SELECT session_id FROM public.session_turns_with_current_month
      WHERE session_id LIKE 'gs_gw_%' GROUP BY session_id ORDER BY session_id LIMIT 3
  LOOP
    sid := rec.session_id;
    SELECT count(*) INTO n FROM (
      SELECT session_id FROM public.session_bodies_unified
       WHERE session_id = sid ORDER BY turn_no DESC LIMIT 1) x;
    RAISE NOTICE 'kind=branch SID=% fifth_leg_rows=% (★ 0 ⇒ 必然落空)', sid, n;
  END LOOP;
  -- ★ plain 三个（★ 时序性：有 0 也有 1）
  FOR rec IN
    SELECT session_id FROM public.session_turns_with_current_month
      WHERE session_id LIKE 'gw_%' AND session_id NOT LIKE 'gs_%' AND session_id NOT LIKE 'gt_%'
      GROUP BY session_id ORDER BY session_id LIMIT 3
  LOOP
    sid := rec.session_id;
    SELECT count(*) INTO n FROM (
      SELECT session_id FROM public.session_bodies_unified
       WHERE session_id = sid ORDER BY turn_no DESC LIMIT 1) x;
    RAISE NOTICE 'kind=plain  SID=% fifth_leg_rows=%', sid, n;
  END LOOP;
END
$do$;
