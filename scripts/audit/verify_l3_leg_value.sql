-- =====================================================================
-- §143.81 复算脚本 v3：L3 腿的【现时成本】与【累计成本】必须分开
--
-- ★★★★ 本脚本要解决的【头号问题】：
--
--   §143.79 / §143.80 把 pg_stat_statements 的【累计量】当成了【现时成本】——
--   「读链合计 74.4 分钟、L3 占 88.1%」。
--   ★ 但生产实测：★ `public._request_logs`（v1 族）**已经被 DROP**
--     （to_regclass 三个名字全 NULL）⇒ ⇒ 而 `FROM _request_logs` 的那条语句
--     **在 stats 里仍有 17,424 次调用记录**⇒ ⇒ ★★ 【那 17,424 次必然发生在
--     表被 DROP 之前】⇒ ⇒ ★★ **这条腿早已跑不动了**⇒ ⇒ 它的累计耗时
--     【是历史成本，不是现时成本】。
--   ⇒ ⇒ ⇒ **必须用【两次采样取增量】判定每条腿此刻还在不在跑。**
--
--   ★★★ 这与「累计型量只增不减」是同一条纪律的另一半：
--     累计量【变大】不代表【现在还在变多】⇒ ⇒ 必须看增量。
--
-- ★★★★★★ 本脚本 v3 新增【结构性判据】（★ 比时间窗口更强，不依赖采样间隔）：
--
--   pg_stat_statements 的 queryid = hash(规范化后的 query 文本)
--   ⇒ ⇒ 【代码里的 SQL 形态一改，queryid 就换一个】
--   ⇒ ⇒ ⇒ **「某条 queryid 的 delta=0」的根因可以被【形态比对】直接判出来**：
--          ★ 形态文本 ≠ 仓内当前代码的形态 ⇒ ⇒ 【它属于历史形态】⇒ ⇒ 已死；
--          ★ 形态文本 = 仓内当前代码的形态 ⇒ ⇒ 【它是当前形态】⇒ ⇒ 仍在跑。
--   ⇒ ⇒ ⇒ 于是本脚本把「当前形态」的三条特征（LATERAL / session_bodies_unified /
--          COALESCE(ff,sb)）作为【形态指纹】登记在案（Q5/Q6）。
--
-- ★★★★ 本脚本回答：
--   Q1 ★★★★★ 现时速率（★ 两次采样取增量 ⇒ 哪些腿【此刻】还在跑）
--   Q2 ★★★★ L3 三形态的【驱动侧表名】（★ 从 query 抽，不靠推理）
--   Q3 ★★★★ v1 族在生产上的现状（★ 存在性）
--   Q4 ★★★★ 窗口内【一次性会话】按前缀分桶（★ 落空请求数的量级）
--   Q5 ★★★★★★ 【形态指纹全谱】（★ 按 md5(query) 分组 ⇒ 当前形态只有一条）
--   Q6 ★★★★★★ 【当前形态 query 全文】（★ 供与仓内源码逐字比对）
--
-- ★★★ 护栏纪律（★ 本轮实测）：
--   ★ 生产护栏【拒绝行首的 CREATE / DDL】⇒ ⇒ 本脚本【不建任何对象】
--     ⇒ ⇒ 取样用「查询驱动的 FOR 循环 + DO 块」完成
--   ★ `round(double precision, integer)` 不存在 ⇒ ⇒ 一律先 ::numeric
--   ★★ psql 变量【不会在 dollar-quoted 字符串（DO 块）内被替换】
--     ⇒ ⇒ 「用 psql 变量参数化窗口」这条路在 PL/pgSQL 块里【走不通】
--     ⇒ ⇒ ★ 本脚本因此分两条路：
--        ① 【块内】60s 双采样（写死）⇒ ⇒ 一次运行即得瞬时增量；
--        ② 【跨运行】累计快照行（SNAPSHOT）⇒ ⇒ 任意两次运行的差分
--           ⇒ ⇒ 拿到【任意长窗口】的增量（★ 用 scripts/audit/diff_l3_snapshots.py）
--     ⇒ ⇒ ★★ 【块外】的普通语句里 psql 变量【是会】替换的（★ 参数化只落在这里）
--
-- 用法（★ 只读；★ 必须串行；★ 生产 252 共享库）：
--   bash ~/.mavis-tools/lgwp-prod.sh scripts/audit/verify_l3_leg_value.sql 900
--   ★ 长窗口增量复核（★ 两次运行 + 本地差分）：
--   bash ~/.mavis-tools/lgwp-prod.sh scripts/audit/verify_l3_leg_value.sql 900 > /tmp/a.txt
--   ...（等 N 分钟）...
--   bash ~/.mavis-tools/lgwp-prod.sh scripts/audit/verify_l3_leg_value.sql 900 > /tmp/b.txt
--   python3 scripts/audit/diff_l3_snapshots.py /tmp/a.txt /tmp/b.txt
-- =====================================================================
SET statement_timeout = '1200s';
SET lock_timeout = '10s';

\echo '=== 1/7: ★★★★★ Q1 现时速率（★ 两次采样取增量 ⇒ 判定每条腿【此刻】还在不在跑）==='
-- ★★★★ 2026-10-23（§143.84）★ ★★ 窗口纪律（★ 本轮实测踩到）：
--   ★ 本块内的窗口写死 60 s ⇒ ⇒ 对【突发型】腿【不足以判生死】。
--   ★ 实测（252 共享库，同一时刻）：
--     ★ 60 s 内采样   ⇒ L3_gamma delta_calls = 0  ⇒ 读成「已死」
--     ★ 49 min 跨运行 ⇒ L3_gamma delta_calls = 293 ⇒ 实为「活的」
--   ★ ⇒ ⇒ ★★ 「delta=0 ⇒ 死」这个结论在 60 s 窗口上【会给假死】。
--   ★ ⇒ ⇒ ★★ 判生死必须用【≥10 分钟】窗口
--   ★ ⇒ ⇒ ★★ 工具：★ scripts/audit/diff_l3_snapshots.py（★ 取首尾两份 SNAPSHOT 行做差）。
--   ★ ⇒ ⇒ ★★ 下方 ALIVE 列对 60 s 窗口只作【参考】，★ 结论以长窗口为准。
\echo '★ ★ 判生死请用【≥10 分钟窗口】（★ diff_l3_snapshots.py）⇒ ⇒ 本块 60 s 采样只作【参考】★'
DO $do$
DECLARE
  t0 JSONB; t1 JSONB; rec RECORD; k TEXT;
  c0 BIGINT; c1 BIGINT; r0 BIGINT; r1 BIGINT; m0 NUMERIC; m1 NUMERIC;
  dc BIGINT; dr BIGINT; dms NUMERIC;
BEGIN
  -- ★ t0 采样（★ 全部来自同一条 SELECT ⇒ 口径一致；★ 不建任何对象）
  SELECT jsonb_object_agg(l.label, jsonb_build_object(
           'calls', coalesce(s.calls,0), 'rows', coalesce(s.rows,0),
           'ms', coalesce(s.total_exec_time,0)))
    INTO t0
  FROM (VALUES
    ('L1_final_full_snapshot',   -7576398701530481720::bigint),
    ('L1b_fallback_turnno',       -8278521907442285636::bigint),
    ('L2_delta_chain',            -202669097973246213::bigint),
    ('L3_alpha_old_inner_join',    8794675561828039885::bigint),
    ('L3_beta_coalesce_ff',        3955058327330889760::bigint),
    ('L3_gamma_coalesce_ff_v2',   -7776241007544382269::bigint),
    ('L5_writepath_latest',        5347544359269580322::bigint),
    ('COMPANION_v1_request_logs',  663814830982256851::bigint)
  ) AS l(label, qid)
  LEFT JOIN pg_stat_statements s ON s.queryid = l.qid;

  -- ★ 块内窗口写死（★ psql 变量在 dollar-quoted 内不替换 ⇒ 参数化走不通）
  PERFORM pg_sleep(60);

  -- ★ t1 采样
  SELECT jsonb_object_agg(l.label, jsonb_build_object(
           'calls', coalesce(s.calls,0), 'rows', coalesce(s.rows,0),
           'ms', coalesce(s.total_exec_time,0)))
    INTO t1
  FROM (VALUES
    ('L1_final_full_snapshot',   -7576398701530481720::bigint),
    ('L1b_fallback_turnno',       -8278521907442285636::bigint),
    ('L2_delta_chain',            -202669097973246213::bigint),
    ('L3_alpha_old_inner_join',    8794675561828039885::bigint),
    ('L3_beta_coalesce_ff',        3955058327330889760::bigint),
    ('L3_gamma_coalesce_ff_v2',   -7776241007544382269::bigint),
    ('L5_writepath_latest',        5347544359269580322::bigint),
    ('COMPANION_v1_request_logs',  663814830982256851::bigint)
  ) AS l(label, qid)
  LEFT JOIN pg_stat_statements s ON s.queryid = l.qid;

  RAISE NOTICE '=== RATE (★ delta = t1 - t0) ===';
  FOR rec IN
    SELECT kk.key AS label,
           (t0->kk.key->>'calls')::bigint c0,
           (t0->kk.key->>'rows')::bigint r0,
           (t0->kk.key->>'ms')::numeric m0,
           (t1->kk.key->>'calls')::bigint c1,
           (t1->kk.key->>'rows')::bigint r1,
           (t1->kk.key->>'ms')::numeric m1
    FROM jsonb_object_keys(t1) kk(key)
    ORDER BY ((t1->kk.key->>'ms')::numeric - (t0->kk.key->>'ms')::numeric) DESC
  LOOP
    dc := rec.c1 - rec.c0; dr := rec.r1 - rec.r0; dms := rec.m1 - rec.m0;
    -- ★★ PL/pgSQL 的 RAISE【不支持宽度格式】（★ 本轮实测：`%-28s` 会被原样输出成
    --   `-28s`）⇒ ⇒ 一律用 `%`（★ 不用 `%-Ns` / `%Ns`）
    RAISE NOTICE 'RATE % | delta_calls=% delta_rows=% delta_ms=% | cumul_calls=% cumul_ms=% | ALIVE=%',
      rec.label, dc, dr, round(dms,1), rec.c0, round(rec.m0,1), (dc > 0);
  END LOOP;

  -- ★★★★★★ 【累计快照行】（★ 机器可解析）⇒ ⇒ 跨两次运行做差 ⇒ 任意长窗口增量
  --   ⇒ ⇒ ★ 这是「累计量 ≠ 现时成本」的【可复用量具】：
  --        单次运行的累计值只是【历史】⇒ ⇒ 两次运行的差分才是【现时】。
  RAISE NOTICE 'SNAPSHOT %', t1::text;
END
$do$;

\echo '=== 2/7: ★★★★ Q2 L3 三形态 + 伴生体的【驱动侧表名】（★ 从 query 抽，不靠推理）==='
SELECT queryid, calls, rows,
       (SELECT string_agg(DISTINCT m[1], ' | ')
          FROM regexp_matches(query, 'FROM\s+([a-zA-Z_][a-zA-Z0-9_.]*)', 'g') m) AS from_tables,
       (SELECT string_agg(DISTINCT m[1], ' | ')
          FROM regexp_matches(query, 'JOIN\s+([a-zA-Z_][a-zA-Z0-9_.]*)', 'g') m) AS join_tables,
       (query LIKE '%_request_logs%') AS mentions_v1_request_logs,
       (query LIKE '%session_turns%')  AS mentions_session_turns,
       (query LIKE '%session_bodies%') AS mentions_session_bodies,
       -- ★★★★★ 形态判别器（★ §143.56 的 jsonb 判据）⇒ ★ 当前仓内形态 = 本列为真的那条
       (query LIKE '%outbound_body <> %::jsonb%')              AS has_jsonb_predicate,
       (query LIKE '%COALESCE(ff.outbound_body%')               AS has_coalesce_ff,
       (query LIKE '%LATERAL%')                                  AS has_lateral
FROM pg_stat_statements
WHERE queryid IN (8794675561828039885, 3955058327330889760, -7776241007544382269, 663814830982256851)
ORDER BY total_exec_time DESC;

\echo '=== 3/7: ★★★★ Q3 v1 族在生产上的现状（★ 存在性）==='
SELECT to_regclass('public._request_logs')                    AS v1_parent,
       to_regclass('public._request_logs_with_current_month') AS v1_view,
       to_regclass('public._request_logs_bodies')             AS v1_bodies,
       to_regclass('public.session_bodies_unified')           AS session_family_bodies,
       (SELECT stats_reset FROM pg_stat_statements_info)      AS stats_reset;

\echo '=== 3b/7: ★★★★★★ Q3b 【v1 腿现在还能不能跑？】（★ 失败语句【不进 pg_stat_statements】）==='
DO $do$
DECLARE
  err TEXT;
BEGIN
  -- ★ 关键判别：★ v1 族已被迁移 890 整体退役 ⇒ ⇒ 任何引用它的语句
  --   必然 42P01 ⇒ ⇒ ★ 【失败的语句不会被 pg_stat_statements 记录】
  --   ⇒ ⇒ ⇒ 它的真实成本在 stats 里【完全隐形】⇒ ⇒ 「现时口径」是【下界】。
  BEGIN
    PERFORM 1 FROM public._request_logs LIMIT 1;
    err := 'NO_ERROR(★ 意外：v1 族居然还在 ⇒ 结论要改)';
  EXCEPTION WHEN others THEN
    err := SQLERRM;
  END;
  RAISE NOTICE 'V1 LEG VIABILITY: %', err;
END
$do$;

\echo '=== 4/7: ★★★★ Q4 窗口内【一次性会话】按前缀分桶（★ 落空请求数的量级）==='
DO $do$
DECLARE
  rec RECORD; n BIGINT;
BEGIN
  SELECT count(*) INTO n FROM (
    SELECT session_id FROM public.session_turns GROUP BY session_id HAVING count(*) = 1
  ) a;
  RAISE NOTICE 'ONE-SHOT SESSIONS (whole turns family, count(*)=1): %', n;

  FOR rec IN
    SELECT CASE
             WHEN session_id LIKE 'gt_gw_%' THEN 'branch_gt_gw'
             WHEN session_id LIKE 'gs_gw_%' THEN 'branch_gs_gw'
             WHEN session_id LIKE 'gw_%'    THEN 'plain_gw'
             ELSE 'other'
           END AS pfx,
           count(*) AS sessions
    FROM (
      SELECT session_id FROM public.session_turns GROUP BY session_id HAVING count(*) = 1
    ) a
    GROUP BY 1 ORDER BY 2 DESC
  LOOP
    RAISE NOTICE 'ONE-SHOT BY PREFIX: % sessions=%', rec.pfx, rec.sessions;
  END LOOP;
END
$do$;

\echo '=== 5/7: ★★★★★★ Q5 【形态指纹全谱】（★ 按 md5(query) 分组 ⇒ 当前代码形态只有一条）==='
SELECT md5(query)                                                        AS q_md5,
       count(DISTINCT queryid)                                            AS n_queryids,
       sum(calls)                                                         AS calls,
       sum(rows)                                                          AS rows,
       round(sum(total_exec_time)::numeric, 1)                            AS total_ms,
       round((sum(total_exec_time)/nullif(sum(calls),0))::numeric, 3)      AS mean_ms,
       round((sum(rows)::numeric/nullif(sum(calls),0)), 3)                AS rows_per_call,
       bool_or(query LIKE '%LATERAL%')                                    AS has_lateral,
       bool_or(query LIKE '%session_bodies_unified%')                     AS has_unified,
       bool_or(query LIKE '%COALESCE(ff.outbound_body%')                   AS has_coalesce_ff,
       bool_or(query LIKE '%_request_logs%')                              AS has_v1_request_logs
FROM pg_stat_statements
WHERE query LIKE '%outbound_body%'
   OR query LIKE '%_request_logs%'
GROUP BY md5(query)
ORDER BY sum(total_exec_time) DESC
LIMIT 30;

\echo '=== 6/7: ★★★★★★ Q6 【L3 家族形态全文】（★ 供与仓内源码逐字比对）==='
DO $do$
DECLARE
  cur_qid BIGINT; cur_calls BIGINT; cur_ms NUMERIC; cur_rows BIGINT;
BEGIN
  -- ★★★ 判别原理（★ §143.81 实测）：
  --   L3_alpha（旧）: SELECT sb.outbound_body, ...            ⇒ 无 COALESCE / 无 LATERAL
  --   L3_beta （中）: 主 WHERE 是 ... IS NOT NULL OR ... IS NOT NULL ⇒ §143.56 之前
  --                   （★ 但它的 LATERAL 子查询里【已经有】jsonb 判据 ⇒ ★ 判别点不能落在这里）
  --   L3_gamma（现）: 主 WHERE 有【两处】... IS NOT NULL AND ... <> $n::jsonb ⇒ §143.56 之后
  -- ⇒ ⇒ 判别器 = 【主 WHERE 的完整形态】⇒ ⇒ ★ 与时间窗口无关。
  -- ★★★ 判别点必须落在【主 WHERE 子句】上（★ 本轮自纠）：
  --   ★ L3_beta 的 LATERAL 子查询里【已经有】`f.outbound_body <> $60::jsonb`
  --   ★ ⇒ ⇒ 用「query 里含 outbound_body <> $n::jsonb」当判别器 ⇒ ⇒ **beta 与 gamma 都被命中**
  --   ★ ⇒ ⇒ ★★ 真正的差异在【主 WHERE】：gamma 有**两处** jsonb 判据（sb 与 ff 各一处）
  --   ★ ⇒ ⇒ 判别器 = 主 WHERE 的完整形态：
  --        AND (sb.outbound_body IS NOT NULL AND sb.outbound_body <> $n::jsonb)
  SELECT s.queryid, s.calls, s.rows, s.total_exec_time
    INTO cur_qid, cur_calls, cur_rows, cur_ms
  FROM pg_stat_statements s
  WHERE s.queryid IN (8794675561828039885, 3955058327330889760, -7776241007544382269)
    -- ★★★★ 2026-10-23（§143.84）★ needle 修正（★ 本处与 Q7 两处【同样坏】⇒ 必须同改）：
    --   ★ 原来写的是 'AND (sb.outbound_body IS NOT NULL AND sb.outbound_body <>%'
    --   ★ ⇒ ⇒ 【少一个左括号】⇒ ⇒ 库内真实文本是
    --   ★   AND ((sb.outbound_body IS NOT NULL AND sb.outbound_body <> $64::jsonb)
    --   ★ ⇒ ⇒ 旧 needle 【恒不匹配】⇒ ⇒ 「自动判定当前形态」恒输出 NOT FOUND
    --   ★ ⇒ ⇒ ★★ 这条判据【从未真正工作过】（★ 量具坏了 ⇒ 「0 命中的第一嫌疑是量具坏了」）
    --   ★ ⇒ ⇒ 修法 = needle 与【主 WHERE 的真实形态】逐字对齐（★ 双左括号）
    AND s.query LIKE '%AND ((sb.outbound_body IS NOT NULL AND sb.outbound_body <>%'
  ORDER BY s.calls DESC
  LIMIT 1;

  IF cur_qid IS NULL THEN
    RAISE NOTICE 'CURRENT SHAPE: NOT FOUND (★ 三条 L3 形态里没有含 jsonb 判据的 ⇒ 请重查形态)';
  ELSE
    RAISE NOTICE 'CURRENT SHAPE: queryid=% calls=% rows=% total_ms=%',
      cur_qid, cur_calls, cur_rows, round(cur_ms, 1);
  END IF;
END
$do$;
DO $do$
DECLARE
  rec RECORD;
BEGIN
  FOR rec IN
    SELECT md5(s.query) AS q_md5, s.queryid, s.calls, s.rows,
           round(s.total_exec_time::numeric, 1) AS total_ms,
           round(s.mean_exec_time::numeric, 3)  AS mean_ms,
           s.query
    FROM pg_stat_statements s
    WHERE s.queryid IN (8794675561828039885, 3955058327330889760, -7776241007544382269, 663814830982256851)
       OR (s.query LIKE '%session_bodies_unified%' AND s.query LIKE '%LATERAL%')
    ORDER BY s.total_exec_time DESC
  LOOP
    -- ★★ RAISE 的 `%` 后面不能接 `>`（★ 会被当成非法格式说明符 ⇒
    --   `too many parameters specified for RAISE`）⇒ ⇒ 百分号放最后
    RAISE NOTICE 'SHAPE md5=% queryid=% calls=% rows=% total_ms=% mean_ms=%',
      rec.q_md5, rec.queryid, rec.calls, rec.rows, rec.total_ms, rec.mean_ms;
    RAISE NOTICE 'SHAPE QUERY: %', rec.query;
  END LOOP;
END
$do$;

\echo '=== 7/7: ★★★★★★ Q7 【当前形态自动判定】（★ 按 §143.56 的 jsonb 判据）==='
DO $do$
DECLARE
  cur_qid BIGINT; cur_calls BIGINT; cur_ms NUMERIC; cur_rows BIGINT;
BEGIN
  -- ★★★ 判别原理（★ §143.81 实测）：
  --   L3_alpha（旧）: SELECT sb.outbound_body, ...            ⇒ 无 COALESCE / 无 LATERAL
  --   L3_beta （中）: 主 WHERE 是 ... IS NOT NULL OR ... IS NOT NULL ⇒ §143.56 之前
  --                   （★ 但它的 LATERAL 子查询里【已经有】jsonb 判据 ⇒ ★ 判别点不能落在这里）
  --   L3_gamma（现）: 主 WHERE 有【两处】... IS NOT NULL AND ... <> $n::jsonb ⇒ §143.56 之后
  -- ⇒ ⇒ 判别器 = 【主 WHERE 的完整形态】⇒ ⇒ ★ 与时间窗口无关。
  -- ★★★ 判别点必须落在【主 WHERE 子句】上（★ 本轮自纠）：
  --   ★ L3_beta 的 LATERAL 子查询里【已经有】`f.outbound_body <> $60::jsonb`
  --   ★ ⇒ ⇒ 用「query 里含 outbound_body <> $n::jsonb」当判别器 ⇒ ⇒ **beta 与 gamma 都被命中**
  --   ★ ⇒ ⇒ ★★ 真正的差异在【主 WHERE】：gamma 有**两处** jsonb 判据（sb 与 ff 各一处）
  --   ★ ⇒ ⇒ 判别器 = 主 WHERE 的完整形态：
  --        AND (sb.outbound_body IS NOT NULL AND sb.outbound_body <> $n::jsonb)
  SELECT s.queryid, s.calls, s.rows, s.total_exec_time
    INTO cur_qid, cur_calls, cur_rows, cur_ms
  FROM pg_stat_statements s
  WHERE s.queryid IN (8794675561828039885, 3955058327330889760, -7776241007544382269)
    -- ★★★★ 2026-10-23（§143.84）★ needle 修正（★ 本处与 Q7 两处【同样坏】⇒ 必须同改）：
    --   ★ 原来写的是 'AND (sb.outbound_body IS NOT NULL AND sb.outbound_body <>%'
    --   ★ ⇒ ⇒ 【少一个左括号】⇒ ⇒ 库内真实文本是
    --   ★   AND ((sb.outbound_body IS NOT NULL AND sb.outbound_body <> $64::jsonb)
    --   ★ ⇒ ⇒ 旧 needle 【恒不匹配】⇒ ⇒ 「自动判定当前形态」恒输出 NOT FOUND
    --   ★ ⇒ ⇒ ★★ 这条判据【从未真正工作过】（★ 量具坏了 ⇒ 「0 命中的第一嫌疑是量具坏了」）
    --   ★ ⇒ ⇒ 修法 = needle 与【主 WHERE 的真实形态】逐字对齐（★ 双左括号）
    AND s.query LIKE '%AND ((sb.outbound_body IS NOT NULL AND sb.outbound_body <>%'
  ORDER BY s.calls DESC
  LIMIT 1;

  IF cur_qid IS NULL THEN
    RAISE NOTICE 'CURRENT SHAPE: NOT FOUND (★ 三条 L3 形态里没有含 jsonb 判据的 ⇒ 请重查形态)';
  ELSE
    RAISE NOTICE 'CURRENT SHAPE: queryid=% calls=% rows=% total_ms=%',
      cur_qid, cur_calls, cur_rows, round(cur_ms, 1);
  END IF;
END
$do$;

-- =====================================================================
-- §143.84 新增 Q8：【调用方穷举 + 调用结构分解】
--
-- ★★★★★★ 为什么新增（★ 本轮实测的三条）：
--
--   ① ★★★★★ 【库侧是否存在【第二个 L3 文本生产者】】
--      判据：query 文本里含 'ff.outbound_body'
--      （★ 仓内 `ff.outbound_body` 全文【只出现在】cmd/gateway/main_v3_wiring.go
--        的 lastOutboundSessionSQL ⇒ ⇒ 只有【一个】文本生产者）
--      ⇒ ⇒ 库里若出现多条命中 ⇒ ⇒ 那些就是【审计脚本自身】
--      ⇒ ⇒ ★★ 这是「观测手段污染被观测统计」的又一例（★ §143.81 已记过）
--      ⇒ ⇒ ★★ 判读口径：★ 只有【qlen ≈ 11.8 KB 且 calls 很大】的那几条才是 L3 形态
--                   ⇒ ⇒ 其余【qlen 小 / calls ≤ 几十】的 = 审计脚本 ⇒ ⇒ 不算贡献
--
--   ② ★★★★★★ 【各腿 queryid 的 SQL 身份】
--      ⇒ ⇒ 没有这一步 ⇒ ⇒ 「L1_final_full / L1b_fallback / L2_delta_chain」这三个
--         标签只是【名字】⇒ ⇒ 无法做【调用结构分解】
--      ⇒ ⇒ 还原成 SQL 形态后 ⇒ ⇒ 才能算出「谁先谁后、命中即止、恒 0 行」
--
--   ③ ★★★ 【分解的关键恒等式】
--      压缩路径（loadFromDB）是【命中即止】链：
--        腿① LoadLatestOutbound(#1 kind='final_full')
--          ↓ 未命中 ⇒ 腿② LoadLatestOutbound(#2 ORDER BY turn_no DESC)
--            ↓ 未命中 ⇒ 腿③ LoadChain
--              ↓ 未命中 ⇒ ⇒ L3
--      ⇒ ⇒ 而 LoadChain 在 252 上【恒 0 行】（★ 累计 27,439 次 / rows 0）
--      ⇒ ⇒ ★★ 于是【腿② 调用数 == 腿③ 调用数 == L3 调用数】必须成立
--      ⇒ ⇒ ★★ 实测 49 min 窗口：★ 293 == 293 == 293 ✔ ⇒ ⇒ ★★ 链被【实证】
--
-- ★ 只读；★ 不建任何对象；★ 必须串行
-- =====================================================================
\echo '=== 8/8: ★★★★★★ Q8a 【库侧含 ff.outbound_body 的全部 queryid】（★ 找第二个 L3 文本生产者）==='
SELECT s.queryid,
       s.calls,
       s.rows,
       round(s.total_exec_time::numeric, 1) AS total_ms,
       md5(s.query) AS q_md5,
       length(s.query) AS qlen,
       -- ★★ 判读列：★ L3 形态的 qlen ≈ 11.8 KB ⇒ ⇒ 小的是审计脚本
       CASE WHEN length(s.query) > 10000 THEN '★ L3 形态（★ 待人工确认）'
            ELSE '审计脚本/临时查询（★ 不算 L3 贡献）' END AS shape_hint
  FROM pg_stat_statements s
 WHERE s.query LIKE '%ff.outbound_body%'
 ORDER BY s.calls DESC;

\echo '=== 8/8: ★★★★★★ Q8b 【各腿 queryid 的 SQL 身份】（★ 分解的前提）==='
SELECT l.label,
       s.queryid,
       s.calls,
       s.rows,
       round(s.total_exec_time::numeric, 1) AS total_ms,
       left(regexp_replace(s.query, '\s+', ' ', 'g'), 400) AS shape
  FROM (VALUES
    ('L1_final_full_snapshot',   -7576398701530481720::bigint),
    ('L1b_fallback_turnno',       -8278521907442285636::bigint),
    ('L2_delta_chain',            -202669097973246213::bigint),
    ('L3_gamma_coalesce_ff_v2',   -7776241007544382269::bigint),
    ('L5_writepath_latest',        5347544359269580322::bigint)
  ) AS l(label, qid)
  LEFT JOIN pg_stat_statements s ON s.queryid = l.qid
 ORDER BY l.label;

\echo '=== 8/8: ★★★★★★ Q8c 【L3 gamma 主 WHERE 片段】（★ 供 Q7 needle 逐字核对）==='
SELECT substring(s.query from position('AND ((' in s.query) for 220) AS main_where
  FROM pg_stat_statements s
 WHERE s.queryid = -7776241007544382269;
