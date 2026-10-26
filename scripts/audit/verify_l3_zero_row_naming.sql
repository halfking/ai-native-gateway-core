-- =====================================================================
-- §143.78 复算脚本：L3 兜底腿「25,492 次全返 0 行」的成因定案
--
-- 目的（可复现地回答四个问题）：
--   Q1 生产上这条 L3 查询烧了多少 DB 时间、返了几行？（★ rows 全 0）
--   Q2 「0 行」在语义上等价于什么？（★★ 等价于「查询那一刻该 id 还没有轮次」
--      ⇒ ★⇒ 0 行 ⇒ 那个会话的首轮 ⇒ ⇒ 它必然没有任何既往轮次）
--   Q3 「首轮即 0 行」的那批会话占多大比例、来自哪里？
--      （★★ 10 月实测：99.60% 的一次性会话）
--   Q4 「父表看不到当月分区」这个假设成立吗？（★ 已证伪 ⇒ 排除）
--
-- 用法（★ 只读；★ 必须串行；★ 生产 252 共享库）：
--   bash ~/.mavis-tools/lgwp-prod.sh scripts/audit/verify_l3_zero_row_naming.sql 900
--
-- ★ 自证：本脚本**从 pg_stat_statements 取生产 SQL 原文**再替换参数，
--   不手抄 ⇒ 不会与生产漂移；★ 每一项结论都打印「量 + 观测值」。
-- ★ 量具前提：pg_stat_statements 未开 track_planning ⇒ 规划成本在本量具里不可见。
-- ★ 只读性：本脚本只做 SELECT / EXPLAIN ANALYZE / set_config（会话级），
--   不建对象、不写业务表。
-- =====================================================================
SET statement_timeout = '900s';
SET lock_timeout = '10s';

\echo '=== 1/6: ★★★ 量具前提（★ track_planning 未开 ⇒ 规划成本不可见）==='
SELECT name, setting FROM pg_settings
WHERE name IN ('track_planning', 'compute_query_id', 'max_parallel_workers_per_gather', 'jit');

\echo '=== 2/6: ★★★ Q1 生产 L3 查询的消耗与行数（★ 按 queryid）==='
SELECT queryid, calls,
       round(total_exec_time::numeric, 1) AS total_ms,
       round(mean_exec_time::numeric, 3)   AS mean_ms,
       max_exec_time, rows,
       (query LIKE '%COALESCE(ff.outbound_body%') AS has_final_full_leg,
       (query LIKE '%LEFT JOIN LATERAL%')       AS has_lateral,
       (query LIKE '%session_turns_hot%')       AS has_hot_arm
FROM pg_stat_statements
WHERE query LIKE '%outbound_body%'
  AND (query LIKE '%session_turns%' OR query LIKE '%session_bodies%')
ORDER BY total_exec_time DESC;

\echo '=== 3/6: ★★★★★ Q2 决定性语义对拍：★「0 行」严格等价于「该 id 没有轮次」==='
-- ★ 三类样本：
--   (a) not_exists        —— 库里不存在的 id            ⇒ 期望 rows = 0
--   (b) one_shot_has_turn —— 一次性会话（现在已有 1 轮）  ⇒ 期望 rows = 1（body 为 NULL）
--   (c) multi_turn        —— 787 轮的大会话              ⇒ 期望 rows = 1
-- ★ ⇒ ⇒ (b) 证明【有轮次 ⇒ 行数不为 0】⇒ ⇒ 「0 行」只可能来自【没有轮次】。
-- ★ ⇒ ⇒ 而「每个请求新建会话」的首轮【就是没有轮次】⇒ ⇒ 0 行是结构性必然。
DO $do$
DECLARE
  q0 TEXT; q TEXT; j JSONB; sid TEXT; tid TEXT; n INT; ord INT; orig_par TEXT; rec RECORD;
BEGIN
  SELECT query INTO q0 FROM pg_stat_statements
   WHERE queryid = (SELECT queryid FROM pg_stat_statements
                     WHERE query LIKE '%outbound_body%' AND query LIKE '%COALESCE(ff.outbound_body%'
                     ORDER BY total_exec_time DESC LIMIT 1);
  IF q0 IS NULL THEN RAISE NOTICE 'SKIP: 生产库里没有 COALESCE(ff,sb) 形态的 queryid'; RETURN; END IF;

  SELECT current_setting('max_parallel_workers_per_gather') INTO orig_par;

  FOR rec IN
    SELECT 'not_exists' AS kind, 'gw_00000000-0000-4000-8000-000000000000'::text AS session_id, 'default'::text AS tenant_id
    UNION ALL
    SELECT 'one_shot_has_turn', s.session_id, 'default'::text FROM (
      SELECT session_id FROM public.session_turns_2026_10
       GROUP BY session_id HAVING count(*) = 1
       ORDER BY session_id LIMIT 1) s
    UNION ALL
    SELECT 'multi_turn', s.session_id, 'default'::text FROM (
      SELECT session_id FROM public.session_turns_2026_10
       GROUP BY session_id ORDER BY count(*) DESC LIMIT 1) s
  LOOP
    sid := rec.session_id; tid := rec.tenant_id;
    -- ★ 参数替换：先把 $N 整体转成无歧义标记 <<PN>>，再逐个替换。
    --   ★ 为什么不用「倒序 replace」：$1 是 $10 的前缀 ⇒ 倒序防不住 ⇒ 必须先隔离标记。
    --   ★ 标记化之后顺序已不重要 ⇒ 升序替换。
    q := regexp_replace(q0, '\$([0-9]+)', '<<P\1>>', 'g');
    FOR ord IN 1..64 LOOP
      IF ord IN (1,29) THEN
        q := replace(q, '<<P' || ord || '>>', 'NULL::bigint');
      ELSIF ord IN (28,56,58) THEN
        q := replace(q, '<<P' || ord || '>>', quote_literal(sid));
      ELSIF ord IN (57,63) THEN
        q := replace(q, '<<P' || ord || '>>', quote_literal(tid));
      ELSIF ord = 59 THEN q := replace(q, '<<P' || ord || '>>', quote_literal('final_full'));
      ELSIF ord = 60 THEN q := replace(q, '<<P' || ord || '>>', quote_literal('null') || '::jsonb');
      ELSIF ord IN (61,64) THEN q := replace(q, '<<P' || ord || '>>', '1');
      ELSIF ord = 62 THEN q := replace(q, '<<P' || ord || '>>', 'TRUE');
      ELSIF ord IN (2,3,4,30,31,32) THEN q := replace(q, '<<P' || ord || '>>', quote_literal('^[0-9]+$'));
      ELSIF ord IN (5,6,7,33,34,35,23,51) THEN q := replace(q, '<<P' || ord || '>>', '0');
      ELSIF ord IN (8,36) THEN q := replace(q, '<<P' || ord || '>>', quote_literal('sys:%'));
      ELSIF ord IN (27,55) THEN q := replace(q, '<<P' || ord || '>>', quote_literal('inet'));
      ELSIF ord IN (26,54) THEN q := replace(q, '<<P' || ord || '>>', 'NULL::double precision');
      ELSIF ord IN (11,39) THEN q := replace(q, '<<P' || ord || '>>', quote_literal('success'));
      ELSIF ord IN (14,42) THEN q := replace(q, '<<P' || ord || '>>', quote_literal('failed'));
      ELSIF ord IN (15,43) THEN q := replace(q, '<<P' || ord || '>>', '500');
      ELSIF ord IN (19,47) THEN q := replace(q, '<<P' || ord || '>>', 'NULL::text[]');
      ELSIF ord IN (22,50,24,52,25,53) THEN q := replace(q, '<<P' || ord || '>>', quote_literal('false'));
      ELSE q := replace(q, '<<P' || ord || '>>', 'NULL');
      END IF;
    END LOOP;

    IF q ~ '\$[0-9]+' THEN
      RAISE NOTICE 'WARN: 参数替换后仍有 $N 残留（★ 量具自坑，勿当成数据问题）';
    END IF;

    FOR n IN 1..1 LOOP
      EXECUTE 'EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) ' || q INTO j;
      -- ★★ 「Actual Rows」在 Plan 里面（j->0->'Plan'->>'Actual Rows'）
      --   ★ 写成 j->0->>'Actual Rows' 会【恒为 NULL】⇒ 量具自己失败、读数恒空
      --   ⇒ ★ 这就是「0 命中的第一嫌疑是量具坏了」的那一类。
      RAISE NOTICE 'CASE=% rows=% exec_ms=% parallel_plan=% (★ 期望：not_exists⇒0，其余⇒1)',
        rec.kind, (j->0->'Plan'->>'Actual Rows'), round((j->0->>'Execution Time')::numeric, 3),
        (j::text LIKE '%Gather%')::text;
    END LOOP;
  END LOOP;
END
$do$;

\echo '=== 4/6: ★★★★★ Q3 一次性会话的量化（★ 0 行调用的代理指标）==='
WITH per AS (
  SELECT session_id, count(*) AS n
  FROM public.session_turns_2026_10
  GROUP BY session_id
)
SELECT (SELECT count(*) FROM public.session_turns_2026_10) AS rows_total,
       count(*) AS distinct_sessions,
       count(*) FILTER (WHERE n = 1) AS one_shot_sessions,
       count(*) FILTER (WHERE n > 1) AS multi_turn_sessions,
       round(100.0 * count(*) FILTER (WHERE n = 1) / count(*), 2) AS one_shot_pct
FROM per;

\echo '=== 5/6: ★★★★★ Q3 一次性会话的来源分桶（★ 分桶之和必须等于总数）==='
WITH per AS (
  SELECT session_id, count(*) AS n
  FROM public.session_turns_2026_10
  GROUP BY session_id
), oneshot AS (
  SELECT session_id FROM per WHERE n = 1
)
SELECT t.origin_stage, t.origin_actor, count(*) AS rows_n
FROM public.session_turns_2026_10 t
JOIN oneshot o ON o.session_id = t.session_id
GROUP BY t.origin_stage, t.origin_actor
ORDER BY 3 DESC;

\echo '=== 6/6: ★★★★ Q4 排除「父表看不到当月分区」+ 抽样一次性 id 的全局行数 ==='
-- ★ Q4.1：同一 session_id，父表 vs 直查月分区 vs hot 三路对拍
--   ⇒ ★ 若 parent_rows == oct_rows ⇒ 「分区未 ATTACH」这个假设被证伪。
WITH sample AS (
  SELECT session_id FROM public.session_turns_2026_10
  GROUP BY session_id ORDER BY count(*) DESC LIMIT 3
)
SELECT s.session_id,
       (SELECT count(*) FROM public.session_turns x WHERE x.session_id = s.session_id)       AS parent_rows,
       (SELECT count(*) FROM public.session_turns_2026_10 x WHERE x.session_id = s.session_id) AS oct_rows,
       (SELECT count(*) FROM public.session_turns_hot x WHERE x.session_id = s.session_id)   AS hot_rows
FROM sample s;

-- ★ Q4.2：抽 5 个一次性 gw_ id ⇒ 它们在【整个父表】里各只出现 1 次
--   ⇒ ⇒ 证实「每个请求一个新 id、从不复用」⇒ ⇒ 每个 id 的首轮 L3 必然 0 行。
WITH per AS (
  SELECT session_id, count(*) AS n
  FROM public.session_turns_2026_10
  GROUP BY session_id
), oneshot AS (
  SELECT session_id FROM per WHERE n = 1 AND session_id LIKE 'gw_%' LIMIT 5
)
SELECT o.session_id,
       (SELECT count(*) FROM public.session_turns x WHERE x.session_id = o.session_id)      AS parent_rows,
       (SELECT count(*) FROM public.session_turns_hot x WHERE x.session_id = o.session_id)  AS hot_rows
FROM oneshot o;
