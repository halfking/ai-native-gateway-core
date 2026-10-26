-- =====================================================================
-- §143.77 复算脚本：L3 冷启动兜底查询的「并行启动成本」定案
--
-- 目的（可复现地回答三个问题）：
--   Q1 生产上这条查询烧了多少 DB 时间、返了几行？
--   Q2 这 ~150 ms 花在哪个节点？（★ 结论：并行 worker 启动，不是 IO、不是数据量）
--   Q3 关掉并行能省多少？代价是什么？（★ 关并行会伤大会话 ⇒ 不是等价修法）
--
-- 用法（★ 只读；★ 必须串行；★ 生产 252 共享库）：
--   bash ~/.mavis-tools/lgwp-prod.sh scripts/audit/verify_l3_fallback_parallel_cost.sql 900
--
-- ★ 自证：本脚本**从 pg_stat_statements 取生产 SQL 原文**再替换参数，
--   不手抄 ⇒ 不会与生产漂移；★ 每一项结论都打印「量 + 观测值」。
-- ★ 量具前提：pg_stat_statements 未开 track_planning ⇒ 规划成本在本量具里不可见
--   （口径 1 会把这条前提打印出来，★ 别把它当成「规划很便宜」的证据）。
-- ★ 参数语义（★ 从生产原文逐个读出来的，不是猜的）：
--   $1 / $29        = id 常量（bigint，投影里的 `id` 列）
--   $2/$3/$4/$30/$31/$32 = 三个「像不像数字」的正则
--   $28/$56/$58     = session_id
--   $57/$63         = tenant_id
--   $59             = 'final_full'   $60 = 'null'::jsonb
--   $61/$64         = 1（LIMIT）      $62 = TRUE（LATERAL 的 ON）
-- =====================================================================
SET statement_timeout = '900s';
SET lock_timeout = '10s';

\echo '=== 1/6: ★★★ 量具前提（★ track_planning 未开 ⇒ 规划成本在量具里不可见）==='
SELECT name, setting FROM pg_settings
WHERE name IN ('track_planning', 'compute_query_id', 'max_parallel_workers_per_gather', 'jit');

\echo '=== 2/6: ★★★ Q1 生产实际形态与消耗（★ 按 queryid 排序）==='
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

\echo '=== 3/6: ★★★ 形态指纹聚类（★ 库里并存几种形态、主力是哪一种）==='
-- ★ 注意：PostgreSQL 不允许 `GROUP BY <位置>` 指向未出现在 select list 的表达式
--   ⇒ 指纹必须在内层先算出来再分组（★ 这是量具自身的坑，不是数据的坑）。
SELECT queryids, calls, total_ms, left(shape, 150) AS shape
FROM (
  SELECT count(*) AS queryids, sum(calls) AS calls,
         round(sum(total_exec_time)::numeric, 1) AS total_ms,
         regexp_replace(query, '\s+', ' ', 'g') AS shape
  FROM pg_stat_statements
  WHERE query LIKE '%outbound_body%'
    AND (query LIKE '%session_turns%' OR query LIKE '%session_bodies%')
  GROUP BY regexp_replace(query, '\s+', ' ', 'g')
) t
ORDER BY total_ms DESC;

\echo '=== 4/6: ★★★★★ 样本会话（★ E1 时代有 final_full 的 / 无 final_full 的大会话 / 库里不存在的）==='
-- ★ UNION 分支的 ORDER BY 只能引用【该分支自己的输出列】
--   ⇒ ts / count(*) 必须放进内层子查询的输出里（★ 违反时 PostgreSQL 报的是
--   「syntax error at or near UNION」，位置指向 UNION 而不指向真正的越界列）。
SELECT 'final_full' AS kind, session_id, tenant_id FROM (
  SELECT session_id, tenant_id, ts FROM public.session_bodies_2026_10
   WHERE kind = 'final_full' ORDER BY ts DESC LIMIT 1) a
UNION ALL
SELECT 'big_no_ff', session_id, tenant_id FROM (
  SELECT session_id, tenant_id, count(*) AS c FROM public.session_bodies_2026_10
   WHERE kind <> 'final_full' GROUP BY session_id, tenant_id ORDER BY c DESC LIMIT 1) b
UNION ALL
SELECT 'not_exists', 'gw_00000000-0000-4000-8000-000000000000', 'default';

\echo '=== 5/6: ★★★★★ Q2+Q3 决定性对照（★ 同一 SQL、同一会话、同一缓存，只切 max_parallel_workers_per_gather）==='
-- ★ 核心：两者的差值只能来自「并行 worker 启动」，不可能来自数据量 / IO / 规划
--   （规划不在 total_exec_time 里；数据量与缓存完全相同）。
DO $do$
DECLARE
  q0 TEXT; q TEXT; j JSONB; et NUMERIC; gt NUMERIC; ct NUMERIC;
  sid TEXT; tid TEXT; n INT; ord INT; orig_par TEXT; rec RECORD;
BEGIN
  SELECT query INTO q0 FROM pg_stat_statements
   WHERE queryid = (SELECT queryid FROM pg_stat_statements
                     WHERE query LIKE '%outbound_body%' AND query LIKE '%COALESCE(ff.outbound_body%'
                     ORDER BY total_exec_time DESC LIMIT 1);
  IF q0 IS NULL THEN RAISE NOTICE 'SKIP: 生产库里没有 COALESCE(ff,sb) 形态的 queryid'; RETURN; END IF;

  SELECT current_setting('max_parallel_workers_per_gather') INTO orig_par;

  FOR rec IN
    SELECT 'final_full' AS kind, session_id, tenant_id FROM (
      SELECT session_id, tenant_id, ts FROM public.session_bodies_2026_10
       WHERE kind = 'final_full' ORDER BY ts DESC LIMIT 1) a
    UNION ALL
    SELECT 'big_no_ff', session_id, tenant_id FROM (
      SELECT session_id, tenant_id, count(*) AS c FROM public.session_bodies_2026_10
       WHERE kind <> 'final_full' GROUP BY session_id, tenant_id ORDER BY c DESC LIMIT 1) b
    UNION ALL
    SELECT 'not_exists', 'gw_00000000-0000-4000-8000-000000000000'::text, 'default'::text
  LOOP
    sid := rec.session_id; tid := rec.tenant_id;
    -- ★ 参数替换：先把 $N 整体转成无歧义标记 <<PN>>，再逐个替换。
    --   ★ 为什么不用「倒序 replace」：$1 是 $10 的前缀，而 $19 里又含 $1 ——
    --     ★ 倒序只能防住「后者含前者」，防不住「同一处含多个形态」⇒ 必须先隔离标记。
    q := regexp_replace(q0, '\$([0-9]+)', '<<P\1>>', 'g');
    -- ★★★ 升序！★★ PL/pgSQL 的 `FOR i IN a..b` 在 a > b 时【一次都不执行】（它不是倒序）
    --   ⇒ 写成 `64..1` 会让【全部参数都不替换】，症状是 SQL 里残留 `<<PN>>`
    --   ⇒ 而 `<<` 在 SQL 里是位移运算符 ⇒ 报「syntax error at or near "::"」。
    --   ★ 标记化之后顺序已不重要 ⇒ 直接升序。
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

    -- ★ ① 并行 ON：跑两遍取第二遍（缓存已热）
    FOR n IN 1..2 LOOP
      EXECUTE 'EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) ' || q INTO j;
      et := (j->0->>'Execution Time')::numeric;
      gt := COALESCE((j->0->'Plan'->>'Actual Total Time')::numeric, 0);
      ct := COALESCE((SELECT sum((x->>'Actual Total Time')::numeric)
                        FROM jsonb_array_elements(COALESCE(j->0->'Plan'->'Plans','[]'::jsonb)) x), 0);
      RAISE NOTICE 'CASE=% PARALLEL_ON run=% exec_ms=% top_actual_ms=% top_children_ms=% parallel_plan=% rows=%',
        rec.kind, n, round(et,3), round(gt,3), round(ct,3), (j::text LIKE '%Gather%')::text, (j->0->>'Actual Rows');
    END LOOP;

    -- ★ ② 只关并行（同一 SQL、同一会话、同一缓存）
    PERFORM set_config('max_parallel_workers_per_gather', '0', false);
    FOR n IN 1..2 LOOP
      EXECUTE 'EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) ' || q INTO j;
      et := (j->0->>'Execution Time')::numeric;
      RAISE NOTICE 'CASE=% PARALLEL_OFF run=% exec_ms=% parallel_plan=% rows=%',
        rec.kind, n, round(et,3), (j::text LIKE '%Gather%')::text, (j->0->>'Actual Rows');
    END LOOP;
    PERFORM set_config('max_parallel_workers_per_gather', orig_par, false);
  END LOOP;
END
$do$;

\echo '=== 6/6: ★★★★ RLS 运行时状态（★ 本库的应用角色是超级用户 ⇒ RLS 策略运行时整库失效）==='
SELECT current_user AS cur_user, r.rolsuper, r.rolbypassrls,
       current_setting('app.current_tenant', true) AS g_tenant,
       current_setting('app.current_user', true)   AS g_user,
       current_setting('app.current_role', true)   AS g_role
FROM pg_roles r WHERE r.rolname = current_user;
