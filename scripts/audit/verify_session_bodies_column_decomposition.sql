-- =====================================================================
-- §143.92 正文族【按列拆解 + 形态分类 + 内容级包含关系 + 重复字节占比】只读复算脚本
--
-- ★★★★★★ 为什么要有它（★ 本轮的核心教训）：
--   ★ §143.91 用【outbound_body 一列】的体积给「一个会话的全部正文体积」下了结论
--     ⇒ ⇒ 得出 508 KiB ⇒「不是存储问题」
--   ★ ⇒ ⇒ 而该会话的 request_delta 是 480,981,980 B ⇒ ⇒ 它其实是【全表第 1 大会话】
--   ★ ⇒ ⇒ ★★★ 根因 = 【按【列】测的量】被当成了【按【对象】测的量】
--   ★ ⇒ ⇒ ⇒ 所以本脚本【必须】逐列给体积，★ 并且【每个数字旁边都要写清它测的是哪一列】。
--
-- ★ 用法（★ 在仓库根目录）：
--   bash scripts/audit/run_prod_sql.sh scripts/audit/verify_session_bodies_column_decomposition.sql 900 /private/tmp/p14392/cols
--
-- ★ ★★ 纪律：★ 只读；★ 不建任何对象（★ 连 TEMP 都不建）；★ 必须串行；
-- ★ ★★ 必须用真实退出码执行器（★ `| tail` 之后 `$?` 是 tail 的退出码）。
--
-- ★ ★★ 四条已知踩坑（★ 别重试）：
--   ① `jsonb_array_length()` 遇到非数组会直接报错 ⇒ 必须先 `jsonb_typeof(...)='array'`；
--   ② `jsonb_typeof(...)` 会 detoast ⇒ 全表扫会超时 ⇒ 内容级口径只扫指定会话；
--   ③ `pg_column_size(jsonb)` 是【存储体积】不是【内容体积】⇒ 恒为下界；
--   ④ ★ `\echo` 行里不能有多余的引号/冒号（★ 否则 psql 报「未结束的引用字符串」）。
-- =====================================================================
SET statement_timeout = '600s';

\echo '=== 1/10: ★★★★★★ 正文族按 kind × 列 的体积分解（★ 每列单独给，★ 不许只挑一列代表）==='
SELECT kind,
       count(*) AS rows,
       sum(pg_column_size(request_delta))  AS req_delta_bytes,
       sum(pg_column_size(response_delta)) AS resp_delta_bytes,
       sum(pg_column_size(outbound_body))  AS outbound_bytes,
       sum(pg_column_size(request_attachments))  AS req_att_bytes,
       sum(pg_column_size(response_attachments)) AS resp_att_bytes,
       max(pg_column_size(request_delta))  AS max_req,
       max(pg_column_size(response_delta)) AS max_resp,
       max(pg_column_size(outbound_body))  AS max_out
  FROM public.session_bodies_unified
 GROUP BY kind
 ORDER BY 2 DESC;

\echo '=== 2/10: ★★★★ turn_delta 的 request_delta 单行体积直方图（★ 行数占比 + 体积占比）==='
SELECT CASE
         WHEN pg_column_size(request_delta) < 1024 THEN '01 <1KB'
         WHEN pg_column_size(request_delta) < 10240 THEN '02 1-10KB'
         WHEN pg_column_size(request_delta) < 102400 THEN '03 10-100KB'
         WHEN pg_column_size(request_delta) < 1048576 THEN '04 100KB-1MB'
         WHEN pg_column_size(request_delta) < 10485760 THEN '05 1-10MB'
         ELSE '06 >=10MB'
       END AS req_bucket,
       count(*) AS rows,
       sum(pg_column_size(request_delta)) AS req_bytes,
       max(pg_column_size(request_delta)) AS max_req
  FROM public.session_bodies_unified
 WHERE kind = 'turn_delta'
 GROUP BY 1
 ORDER BY 1;

\echo '=== 3/10: ★★★★ turn_delta 的 outbound_body 直方图（★ E1 门前存量，★ 已停写）==='
SELECT CASE
         WHEN pg_column_size(outbound_body) < 1024 THEN '01 <1KB'
         WHEN pg_column_size(outbound_body) < 10240 THEN '02 1-10KB'
         WHEN pg_column_size(outbound_body) < 102400 THEN '03 10-100KB'
         WHEN pg_column_size(outbound_body) < 1048576 THEN '04 100KB-1MB'
         WHEN pg_column_size(outbound_body) < 10485760 THEN '05 1-10MB'
         ELSE '06 >=10MB'
       END AS out_bucket,
       count(*) AS rows,
       sum(pg_column_size(outbound_body)) AS out_bytes,
       max(pg_column_size(outbound_body)) AS max_out
  FROM public.session_bodies_unified
 WHERE kind = 'turn_delta'
 GROUP BY 1
 ORDER BY 1;

\echo '=== 4/10: ★★★★★★ 最大的 15 行（★ 三列都给，★ 定位超大行的归属）==='
SELECT session_id,
       turn_no,
       ts,
       pg_column_size(request_delta)  AS req,
       pg_column_size(response_delta) AS resp,
       pg_column_size(outbound_body)  AS out
  FROM public.session_bodies_unified
 WHERE kind = 'turn_delta'
 ORDER BY pg_column_size(request_delta) + pg_column_size(response_delta) + pg_column_size(outbound_body) DESC
 LIMIT 15;

\echo '=== 5/10: ★★★★★★ 体积 top 20 会话 + 二阶系数形态分类（★ b_per_turn2 与 §143.73/§143.75 同口径）==='
WITH per_sess AS (
  SELECT session_id, count(*) AS turns,
         sum(pg_column_size(request_delta)) AS req_bytes,
         sum(pg_column_size(response_delta) + pg_column_size(outbound_body)) AS other_bytes,
         max(pg_column_size(request_delta)) AS max_req,
         round(sum(pg_column_size(request_delta))::numeric / (count(*) * count(*))) AS b_per_turn2
    FROM public.session_bodies_unified
   WHERE kind='turn_delta'
   GROUP BY session_id
)
SELECT session_id, turns,
       (req_bytes + other_bytes) AS body_bytes,
       round((req_bytes + other_bytes)::numeric / turns) AS avg_bytes_per_turn,
       max_req,
       b_per_turn2,
       CASE WHEN b_per_turn2 > 400 THEN 'A_quadratic'
            WHEN b_per_turn2 > 100 THEN 'B_mixed'
            ELSE 'C_linear' END AS shape
  FROM per_sess
 ORDER BY body_bytes DESC
 LIMIT 20;

\echo '=== 6/10: ★★★★★★ 口径③ 复算：★ >=10 轮会话的二阶系数分桶（★ 与 §143.73/§143.75 同口径）==='
WITH per_sess AS (
  SELECT tenant_id, session_id, count(*) AS turns,
         sum(pg_column_size(request_delta)) AS delta_bytes,
         round(sum(pg_column_size(request_delta))::numeric / (count(*) * count(*))) AS b_per_turn2
  FROM public.session_bodies_2026_10
  WHERE kind='turn_delta'
  GROUP BY tenant_id, session_id
  HAVING count(*) >= 10
)
SELECT CASE WHEN b_per_turn2 > 400 THEN 'A_quadratic_>400B_per_turn2'
            WHEN b_per_turn2 > 100 THEN 'B_mixed_100-400'
            ELSE 'C_linear_<=100' END AS shape,
       count(*) AS sessions, sum(turns) AS turns,
       sum(delta_bytes) AS delta_bytes,
       round(avg(b_per_turn2)) AS avg_b_per_turn2
FROM per_sess
GROUP BY 1 ORDER BY sum(delta_bytes) DESC;

\echo '=== 7/10: ★★★★★★ 内容级包含关系（★ 每轮是否把上一轮整份重存）==='
-- ★ ★★ 判定式：request_delta(t) @> request_delta(t-1) ⇒ 上一轮的全部消息仍逐条存在于这一轮
WITH s AS (
  SELECT session_id, turn_no, request_delta,
         lag(request_delta) OVER (PARTITION BY session_id ORDER BY turn_no) AS prev
    FROM public.session_bodies_unified
   WHERE kind='turn_delta'
     AND session_id IN ('gw_omp-global-kxpms',
                        'gw_4b5c2d17-6ca9-4fa0-b882-2126a28ccdb6',
                        'gw_1d807424-b624-4d7a-b166-0ee48d83d19d',
                        'gw_d70f891a-c9e8-434a-97ed-4bd3bb651d57',
                        'gw_ee70837b-727e-44da-984a-4b6098040582')
)
SELECT session_id,
       count(*) FILTER (WHERE prev IS NOT NULL) AS pairs,
       count(*) FILTER (WHERE prev IS NOT NULL
                          AND jsonb_typeof(request_delta)='array'
                          AND jsonb_typeof(prev)='array'
                          AND request_delta @> prev) AS superset_pairs,
       round(avg(jsonb_array_length(request_delta)) FILTER (WHERE jsonb_typeof(request_delta)='array'), 1) AS avg_msgs_now,
       round(avg(jsonb_array_length(prev)) FILTER (WHERE jsonb_typeof(prev)='array'), 1) AS avg_msgs_prev,
       round(avg(jsonb_array_length(request_delta) - jsonb_array_length(prev))
             FILTER (WHERE jsonb_typeof(request_delta)='array' AND jsonb_typeof(prev)='array'), 1) AS avg_new_msgs
  FROM s
 GROUP BY session_id
 ORDER BY 1;

\echo '=== 8/10: ★★★★★★★★ 口径②【重复字节占比 prev_share】—— ★ 只用 pg_column_size ⇒ 不 detoast ⇒ ⇒ 【可全量跑】⇒ ⇒ 这是唯一能给出【族级】重复量的尺子'
-- ★ ★★★ 为什么要这把尺子（★ 本轮的核心发现）：
--   ★ 内容级口径（`@>`）需要 detoast ⇒ ⇒ 只能扫指定会话 ⇒ ⇒ 拿不到族级；
--   ★ 而「每轮重发整份上下文」这件事在族级上是普遍行为 ⇒ ⇒ 必须有一把能全量跑的尺子。
--   ★ ⇒ ⇒ prev_share = Σ|上一轮字节| / Σ|本轮字节|（★ 分子分母用同一个口径）
WITH s AS (
  SELECT session_id, turn_no, request_delta,
         lag(request_delta) OVER (PARTITION BY session_id ORDER BY turn_no) AS prev
    FROM public.session_bodies_2026_10
   WHERE kind = 'turn_delta'
), b AS (
  SELECT session_id, turn_no,
         pg_column_size(request_delta) AS cur_bytes,
         pg_column_size(prev)          AS prev_bytes
    FROM s
), per_sess AS (
  SELECT session_id,
         count(*)            AS turns,
         sum(cur_bytes)      AS sess_cur_bytes,
         sum(prev_bytes)     AS sess_prev_bytes
    FROM b
   GROUP BY session_id
), cls AS (
  SELECT session_id, turns, sess_cur_bytes, sess_prev_bytes,
         CASE WHEN turns >= 10 THEN '1_ge10_turns'
              WHEN turns >= 2  THEN '2_2to9_turns'
              ELSE '3_single_turn' END AS turn_class
    FROM per_sess
)
SELECT turn_class,
       count(*)              AS sessions,
       sum(turns)            AS turns_total,
       sum(sess_cur_bytes)   AS cur_bytes,
       sum(sess_prev_bytes)  AS prev_bytes,
       round(sum(sess_prev_bytes)::numeric * 100 / NULLIF(sum(sess_cur_bytes), 0), 1) AS prev_share_pct
  FROM cls
 GROUP BY turn_class
 ORDER BY turn_class;

-- ★ ★★★ 同一把尺子、★ 换一把【切法】：★ 按形态（★ 二阶系数）分桶
-- ★ ⇒ ⇒ 实测三个形态桶的 prev_share 几乎一样（98.6% / 98.5% / 98.2%）
-- ★ ⇒ ⇒ ⇒ 二阶系数【完全区分不出「重复」与「不重复」】⇒ ⇒ 真正的分界线是【轮次数】
WITH s AS (
  SELECT session_id, turn_no, request_delta,
         lag(request_delta) OVER (PARTITION BY session_id ORDER BY turn_no) AS prev
    FROM public.session_bodies_2026_10
   WHERE kind = 'turn_delta'
), b AS (
  SELECT session_id, turn_no,
         pg_column_size(request_delta) AS cur_bytes,
         pg_column_size(prev)          AS prev_bytes
    FROM s
), per_sess AS (
  SELECT session_id,
         count(*)        AS turns,
         sum(cur_bytes)  AS sess_cur_bytes,
         sum(prev_bytes) AS sess_prev_bytes
    FROM b
   GROUP BY session_id
   HAVING count(*) >= 10
), shaped AS (
  SELECT session_id, turns, sess_cur_bytes, sess_prev_bytes,
         round(sess_cur_bytes::numeric / (turns * turns)) AS b_per_turn2
    FROM per_sess
)
SELECT CASE WHEN b_per_turn2 > 400 THEN 'A_quadratic_>400B_per_turn2'
            WHEN b_per_turn2 > 100 THEN 'B_mixed_100-400'
            ELSE 'C_linear_<=100' END AS shape,
       count(*)             AS sessions,
       sum(turns)           AS turns_total,
       sum(sess_cur_bytes)  AS cur_bytes,
       sum(sess_prev_bytes) AS prev_bytes,
       round(sum(sess_prev_bytes)::numeric * 100 / NULLIF(sum(sess_cur_bytes), 0), 1) AS prev_share_pct
  FROM shaped
 GROUP BY 1
 ORDER BY sum(sess_cur_bytes) DESC;

\echo '=== 9/10: ★★★★ outbound_body 的写入时点分布 + E1 门时点（★ 验证「已停写」）==='
SELECT (ts AT TIME ZONE 'Asia/Shanghai')::date AS day,
       count(*) FILTER (WHERE pg_column_size(outbound_body) > 1024) AS rows_with_out,
       count(*) AS rows_total,
       sum(pg_column_size(outbound_body)) AS out_bytes
  FROM public.session_bodies_unified
 WHERE kind='turn_delta'
   AND ts >= '2026-10-19'::timestamptz
 GROUP BY 1
 ORDER BY 1;

SELECT key, value, updated_at
  FROM settings_kv
 WHERE key = 'storage.session_final_full_enabled';

\echo '=== 10/10: ★★★★ 单会话的消息条数增长曲线（★ 抽样，★ 看「上下文是否被压缩」）==='
SELECT turn_no,
       pg_column_size(request_delta) AS req,
       jsonb_array_length(request_delta) AS msgs
  FROM public.session_bodies_unified
 WHERE session_id = 'gw_omp-global-kxpms'
   AND kind='turn_delta'
   AND jsonb_typeof(request_delta)='array'
   AND turn_no IN (1,5,20,50,100,150,200,250,300,400,500,700,1000,1500,2000,2500,3000,3500,4000,4300,4432)
 ORDER BY turn_no;
-- =====================================================================
