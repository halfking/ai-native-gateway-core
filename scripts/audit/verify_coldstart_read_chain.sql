-- =====================================================================
-- §143.79 复算脚本 v2：压缩器【冷启动读链】的成本对账
--
-- ★★★ v2 相对 v1 的改动（★ 全部来自本轮实测的三条发现）：
--
--   ① ★★★★★ v1 的腿指纹【写窄了】⇒ 【漏掉了最大那条腿】
--      v1 用 `query LIKE '%COALESCE(ff.outbound_body%'` 认「L3 兜底腿」
--      ⇒ 它只认到 §143.55/§143.56 之后的那一版（COALESCE 形态）
--      ⇒ 【完全漏掉】§143.55 之前的旧内 JOIN 形态
--      （生产实测那条腿：calls=17,424 / total=2,619,553.7 ms / mean=150.342 ms
--      —— ★ 它是【单条最贵的腿】，比 COALESCE 形态贵 2.2 倍）
--      ⇒ ⇒ v1 把 L3 读成 8,870 次，实际是 26,218 次 ⇒ 【少算 66%】
--      ⇒ ⇒ v1 的「pct_v2_hit = 75.33%」是【错的】（真实是 27.3%）
--      ⇒ ★★ 教训：**指纹写窄 ⇒ 读数系统性偏低 ⇒ 结论方向被带偏**
--      ⇒ ⇒ 本版改成【形态普查】：先列出全部候选形态，再逐条点名认领
--
--   ② ★★★★ v1 把【多个快照】的读数相加（★ 违反「同一次快照」纪律）
--      ⇒ 本版把全部读数放进【同一条 SELECT】⇒ 数字自洽
--
--   ③ ★★★★ v1 漏了【第五腿】（写侧 getLatestBodies）
--      ⇒ 生产实测：calls=63,017 / rows=7,074 / total=435,392.5 ms / mean=6.909 ms
--      ⇒ ★ 它的 rows/calls = 11.2% ⇒ 【88.8% 的调用什么也没拿到】
--      ⇒ ★ 它【不在冷启动读链上】（它在写路径上）⇒ ⇒ 但它读的是同一张表
--
-- 目的（可复现地回答六个问题）：
--   Q1 量具前提：统计窗口多长？有没有条目被淘汰？表打满了吗？
--   Q2 ★ 形态普查：与 bodies 族相关的【全部】语句形态及其成本
--   Q3 ★ 六条腿的【单快照】对账（★ 同一组数字来自同一次快照）
--   Q4 落空瀑布的自洽核对（★ v2 命中率）
--   Q5 bodies 侧「有正文」的真实分布（★ 字节阈值口径，不 detoast）
--   Q6 现时速率（两次采样取增量 ⇒ 它此刻还在跑吗）
--   Q7 三类会话的各腿成本（★ 取第二次运行 = 缓存已热）
--
-- 用法（★ 只读；★ 必须串行；★ 生产 252 共享库）：
--   bash ~/.mavis-tools/lgwp-prod.sh scripts/audit/verify_coldstart_read_chain.sql 900
--
-- ★★ 量具纪律（本脚本内置的四条自保）：
--   ① 【形态判定一律用 `pg_column_size(outbound_body) > 12`】——
--      ★ `jsonb_typeof()` 会 detoast ⇒ 29 万行全扫必超时（★ 已实测踩过）。
--   ② 【一律不建任何对象】——★ 生产护栏拒绝 `CREATE TEMP TABLE`
--      ⇒ 用查询驱动的 FOR 循环代替（★ 这也是本轮实测到的护栏行为）。
--   ③ 【全部读数在同一条 SELECT 里】——★ 跨快照相加会算出自相矛盾的数
--      （★ v1 踩过：pct 算出来 75.33%，而分项相加是 27.41%）。
--   ④ 【指纹必须能认全形态】——★ 用 queryid 点名认领（★ queryid 是 PG 内部
--      稳定标识，比 LIKE 指纹精确），★ 且每条腿都【逐条点名】而不是【猜一条】。
--
-- =====================================================================
-- ★★★ 形态 → queryid → 仓内归属 的【映射表】（★ 复核用的唯一依据）
--
-- ★ 【仓内 SQL 形态】是【代码里的字面量】；【pg_stat_statements 里的形态】
--   是【归一化后的指纹】（★ 常量被参数化成 $N ⇒ ★ 这就是「kind = $3」的来历）。
--
-- 腿①  final_full 快照
--   仓内：`domains/session/v2/turn_reader.go` 的 LoadLatestOutbound 第①条
--   生产指纹：`kind = $3` + `ORDER BY ts DESC LIMIT $4`
--   queryid = -7576398701530481720
--
-- 腿①b 回退腿（★ 无 kind 条件 ⇒ 这正是它的成因）
--   仓内：同上函数的第②条
--   生产指纹：`ORDER BY turn_no DESC`（★ 无 kind）
--   queryid = -8278521907442285636
--
-- 腿②  delta 链
--   仓内：`domains/session/v2/turn_reader.go` 的 LoadChain
--   生产指纹：`SELECT b.turn_no`
--   queryid = -202669097973246213
--
-- 腿③  L3 兜底（★ 【三个形态】，★ 一个都不能少 —— v1 漏了最贵的那个）
--   仓内：`cmd/gateway/main_v3_wiring.go` 的 lastOutboundSessionSQL
--   形态 α（★ 最贵）：旧内 JOIN（§143.55 之前）
--     生产指纹：`COALESCE` 缺席 + `ON sb.request_id = rl.request_id` + `sb.outbound_body IS NOT NULL`
--     queryid = 8794675561828039885   ★ calls 17,424 / total 2,619,553.7 ms / mean 150.342 ms
--   形态 β：§143.55/§143.56 的 COALESCE(ff,sb) + final_full LATERAL 腿
--     生产指纹：`COALESCE(ff.outbound_body, sb.outbound_body)`
--     queryid = 3955058327330889760   ★ calls 8,068 / total 1,175,058.2 ms / mean 145.644 ms
--   形态 γ：同 β 的另一个参数形态（★ 生产上并存）
--     queryid = -7776241007544382269  ★ calls 806 / total 134,460.7 ms / mean 166.825 ms
--
-- 腿⑤  写侧 getLatestBodies（★ 【不在冷启动读链上】，★ 但读的是同一张表）
--   仓内：`domains/session/v2/bodies_writer.go` 的 getLatestBodies
--   生产指纹：`SELECT session_id, turn_no, ... ORDER BY turn_no DESC LIMIT $3`（10 列投影）
--   queryid = 5347544359269580322     ★ calls 63,105 / total 435,733.0 ms / mean 6.905 ms
--   ★ 与腿①b 的区别：腿①b 只投影 `outbound_body` 一列 ⇒ ★ 指纹不同、形态不同
--
-- ★ 伴生形态（★ 不计入成本，但必须知道它存在）：
--   queryid = 663814830982256851 —— v1 族的 `_request_logs` 读法，calls 与形态 α 完全相同
--   （17,424）⇒ ⇒ 它与形态 α 是【同一批调用里的两半】⇒ ⇒ 它的 rows 也是 0。
-- =====================================================================
-- =====================================================================
SET statement_timeout = '900s';
SET lock_timeout = '10s';

\echo '=== 1/7: ★★★ Q1 量具前提（★ 窗口长度 / 淘汰数 / 条目上限 / 是否打满）==='
SELECT * FROM pg_stat_statements_info;
SHOW pg_stat_statements.max;
-- ★★ 「表是否打满」是本轮的关键前提（★ 被打满 ⇒ 低频语句被淘汰 ⇒ 读数会缺）
SELECT count(*) AS entries_in_table,
       count(*) FILTER (WHERE queryid < 0) AS evicted_entries,
       count(DISTINCT dbid) AS distinct_dbids
FROM pg_stat_statements;

\echo '=== 2/7: ★★★★★ Q2 形态普查（★ 先列形态，再点名认领 —— v1 就是在这里漏了腿）==='
-- ★ 口径：凡「读 bodies 族」的语句都列出来（含 0 行的、含只读元数据的）
SELECT queryid, calls, rows,
       round(total_exec_time::numeric, 1) AS total_ms,
       round(mean_exec_time::numeric, 3) AS mean_ms,
       left(regexp_replace(coalesce(query,'<NULL>'), '\s+', ' ', 'g'), 96) AS shape
FROM pg_stat_statements
WHERE calls > 50
  AND (query LIKE '%outbound_body%' OR query LIKE 'SELECT b.turn_no%')
ORDER BY total_exec_time DESC;

\echo '=== 3/7: ★★★★★ Q3 六条腿的【单快照】对账（★ 同一条 SELECT ⇒ 数字自洽）==='
-- ★★★★ 认领清单（★ 【可执行部分】的形态点名 —— ★ 与上面注释里的映射表是两个面）
--
-- ★★ 为什么必须在【可执行部分】也点名一次（★ 本轮变异台实测出来的）：
--   ★ 门若只查【剥注释后的代码】，★ 那它对「只在映射表里登记的形态」恒假
--   ⇒ ⇒ 变异 M1/M2/M3/M6（删/改/搬走 queryid）全绿 ⇒ ⇒ 门没牙。
--   ⇒ ⇒ 这里把六条腿的 queryid 放进【真正在算它们的那条 SELECT】的 CTE。
--
-- ★★ 另：★ 每条腿都必须【真的存在】⇒ ⇒ 若某条被淘汰或改名，这里会显示 NULL
--   ⇒ ⇒ 【NULL 就是「这条腿的读数已经不可信」的信号】⇒ ⇒ 不要静默跳过。
WITH legs AS (
  SELECT 'L1_final_full_snapshot'   AS label, -7576398701530481720 AS qid UNION ALL
  SELECT 'L1b_fallback_turnno',            -8278521907442285636 UNION ALL
  SELECT 'L2_delta_chain',                 -202669097973246213 UNION ALL
  SELECT 'L3_join_turns_oldform',           8794675561828039885 UNION ALL
  SELECT 'L3_coalesce_ff',                  3955058327330889760 UNION ALL
  SELECT 'L3_coalesce_ff_v2',              -7776241007544382269 UNION ALL
  SELECT 'L5_writepath_latest',             5347544359269580322
)
SELECT l.label, s.queryid, s.calls, s.rows,
       round(s.total_exec_time::numeric, 1) AS total_ms,
       round(s.mean_exec_time::numeric, 3) AS mean_ms,
       round(100.0 * s.calls / NULLIF((SELECT sum(calls) FROM pg_stat_statements
              WHERE queryid IN (SELECT qid FROM legs)), 0), 2) AS pct_of_total_calls
FROM legs l
LEFT JOIN pg_stat_statements s ON s.queryid = l.qid
ORDER BY s.total_exec_time DESC;

\echo '=== 4/7: ★★★★★ Q4 落空瀑布的自洽核对（★ v2 命中率）==='
-- ★★ 口径说明：
--   L3 的 calls 必须【逐形态相加】（★ v1 只认了 COALESCE 一族 ⇒ 少算 66%）
WITH l1 AS (
  SELECT sum(calls) c, sum(rows) r FROM pg_stat_statements WHERE queryid = -7576398701530481720
), l1b AS (
  SELECT sum(calls) c, sum(rows) r FROM pg_stat_statements WHERE queryid = -8278521907442285636
), l2 AS (
  SELECT sum(calls) c, sum(rows) r FROM pg_stat_statements WHERE queryid = -202669097973246213
), l3 AS (
  SELECT sum(calls) c, sum(rows) r FROM pg_stat_statements
  WHERE queryid IN (8794675561828039885, 3955058327330889760, -7776241007544382269)
), l5 AS (
  SELECT sum(calls) c, sum(rows) r FROM pg_stat_statements WHERE queryid = 5347544359269580322
)
SELECT (SELECT c FROM l1)                                     AS l1_calls,
       (SELECT r FROM l1)                                     AS l1_rows,
       (SELECT c FROM l1b)                                    AS l1b_calls,
       (SELECT r FROM l1b)                                    AS l1b_rows,
       (SELECT c FROM l2)                                     AS l2_calls,
       (SELECT r FROM l2)                                     AS l2_rows_total,
       (SELECT c FROM l3)                                     AS l3_calls,
       (SELECT r FROM l3)                                     AS l3_rows_total,
       (SELECT c FROM l5)                                     AS l5_calls,
       (SELECT r FROM l5)                                     AS l5_rows_total,
       -- ★★★ v2 主路径命中率（★ 口径：以【腿①b 的未命中数】为分母的未命中率倒推）
       --   ★ 腿①b 是「腿① 未命中后才走的那条」⇒ 它的 calls ≈ 腿① 未命中数
       --   ⇒ 腿①b 未命中（calls - rows）⇒ 继续下走到腿② ⇒ ⇒ 腿② 的 calls
       round(100.0 * ((SELECT c FROM l1b) - (SELECT r FROM l1b)) / NULLIF((SELECT c FROM l1),0), 2)
                                                                  AS pct_three_legs_all_miss,
       -- ★ 走到 L3 的比例（L3calls / L1calls）
       round(100.0 * (SELECT c FROM l3) / NULLIF((SELECT c FROM l1),0), 2)
                                                                  AS pct_reaching_L3,
       -- ★★ 自洽等式（★ v1 写错过一次 ⇒ 这里改成【实测成立】的那条）：
       --   【腿①b 未命中数 == 腿② 调用数】⇒ 两者必须相等（差 0）
       --   ★ v1 写的是「腿① - 腿② == 腿①b 的 rows」⇒ 【不成立】（差 4,426）
       --   ⇒ ★ 教训：**等式要按【代码里的分支顺序】写**，不是按直觉写
       ((SELECT c FROM l1b) - (SELECT r FROM l1b)) = (SELECT c FROM l2)
                                                                  AS l1b_miss_eq_l2_calls,
       -- ★ 第二个自洽等式：腿② 落空后【全部】继续走到 L3 ⇒ 腿② calls == L3 calls
       (SELECT c FROM l2) = (SELECT c FROM l3)
                                                                  AS l2_calls_eq_l3_calls;

\echo '=== 5/7: ★★★★ Q5 bodies 侧「有正文」的真实分布（★ 字节阈值口径，不 detoast）==='
SELECT
  count(*) AS all_rows,
  count(*) FILTER (WHERE pg_column_size(outbound_body) > 12) AS real_outbound_rows,
  count(*) FILTER (WHERE pg_column_size(outbound_body) <= 12) AS nullish_rows,
  count(*) FILTER (WHERE kind = 'final_full') AS final_full_rows,
  count(*) FILTER (WHERE kind = 'final_full' AND pg_column_size(outbound_body) > 12) AS final_full_real,
  round(100.0 * count(*) FILTER (WHERE pg_column_size(outbound_body) > 12) / count(*), 2) AS real_pct
FROM public.session_bodies_unified;

\echo '=== 6/7: ★★★ Q6 现时速率（★ 两次采样，间隔 60s ⇒ 它此刻还在跑吗）==='
SELECT
  (SELECT sum(calls) FROM pg_stat_statements WHERE queryid = -7576398701530481720) AS l1_calls_t0,
  (SELECT sum(calls) FROM pg_stat_statements WHERE queryid = -8278521907442285636) AS l1b_calls_t0,
  (SELECT sum(calls) FROM pg_stat_statements WHERE queryid = 5347544359269580322) AS l5_calls_t0,
  now() AS t0;
SELECT pg_sleep(60);
SELECT
  (SELECT sum(calls) FROM pg_stat_statements WHERE queryid = -7576398701530481720) AS l1_calls_t1,
  (SELECT sum(calls) FROM pg_stat_statements WHERE queryid = -8278521907442285636) AS l1b_calls_t1,
  (SELECT sum(calls) FROM pg_stat_statements WHERE queryid = 5347544359269580322) AS l5_calls_t1,
  now() AS t1;

\echo '=== 7/7: ★★★★★ 三类会话的各腿成本（★ 取第二次运行 = 缓存已热）==='
DO $do$
DECLARE
  j JSONB; rec RECORD; sid TEXT; nturn INT; run INT;
BEGIN
  FOR rec IN
    SELECT 'turns_1' AS label, session_id FROM (
      SELECT session_id FROM public.session_turns_2026_10
       GROUP BY session_id HAVING count(*) = 1 ORDER BY session_id LIMIT 1) a
    UNION ALL
    SELECT 'turns_60', session_id FROM (
      SELECT session_id FROM public.session_turns_2026_10
       GROUP BY session_id HAVING count(*) BETWEEN 55 AND 65
       ORDER BY count(*) DESC LIMIT 1) b
    UNION ALL
    SELECT 'turns_787', session_id FROM (
      SELECT session_id FROM public.session_turns_2026_10
       GROUP BY session_id ORDER BY count(*) DESC LIMIT 1) c
  LOOP
    sid := rec.session_id;
    SELECT count(*) INTO nturn FROM public.session_turns WHERE session_id = sid;
    FOR run IN 1..2 LOOP
      -- 腿①b：回退腿
      EXECUTE 'EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) '
        || 'SELECT outbound_body FROM public.session_bodies_unified '
        || 'WHERE tenant_id = ''default'' AND session_id = ' || quote_literal(sid)
        || ' AND outbound_body IS NOT NULL ORDER BY turn_no DESC, ts DESC LIMIT 1' INTO j;
      IF run = 2 THEN
        RAISE NOTICE 'LEG1b(run2) CASE=% turns=% rows=% exec_ms=% hit=% read=% top=%', rec.label, nturn,
          j->0->'Plan'->>'Actual Rows', round((j->0->>'Execution Time')::numeric,3),
          j->0->'Plan'->>'Shared Hit Blocks', j->0->'Plan'->>'Shared Read Blocks', j->0->'Plan'->>'Node Type';
      END IF;
      -- 腿①：final_full 快照
      EXECUTE 'EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) '
        || 'SELECT outbound_body FROM public.session_bodies_unified '
        || 'WHERE tenant_id = ''default'' AND session_id = ' || quote_literal(sid)
        || ' AND kind = ''final_full'' AND outbound_body IS NOT NULL ORDER BY ts DESC LIMIT 1' INTO j;
      IF run = 2 THEN
        RAISE NOTICE 'LEG1 (run2) CASE=% turns=% rows=% exec_ms=% hit=% read=% top=%', rec.label, nturn,
          j->0->'Plan'->>'Actual Rows', round((j->0->>'Execution Time')::numeric,3),
          j->0->'Plan'->>'Shared Hit Blocks', j->0->'Plan'->>'Shared Read Blocks', j->0->'Plan'->>'Node Type';
      END IF;
      -- 腿②：delta 链
      EXECUTE 'EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) '
        || 'SELECT b.turn_no, b.request_delta, b.response_delta FROM public.session_bodies_unified b '
        || 'WHERE b.tenant_id = ''default'' AND b.session_id = ' || quote_literal(sid)
        || ' ORDER BY b.turn_no ASC' INTO j;
      IF run = 2 THEN
        RAISE NOTICE 'LEG2  (run2) CASE=% turns=% rows=% exec_ms=% hit=% read=% top=%', rec.label, nturn,
          j->0->'Plan'->>'Actual Rows', round((j->0->>'Execution Time')::numeric,3),
          j->0->'Plan'->>'Shared Hit Blocks', j->0->'Plan'->>'Shared Read Blocks', j->0->'Plan'->>'Node Type';
      END IF;
    END LOOP;
  END LOOP;
END
$do$;
