-- verify_session_bodies_delta_amplification.sql
-- §143.73 轮（2026-10-22）的【只读】复算脚本：v2 正文表 `request_delta` 的「二次方放大」。
--
-- ★ 用法：在 252 共享库上以【只读】方式执行（可经 154 跳板）。
--   例：bash ~/.mavis-tools/lgwp-prod.sh scripts/audit/verify_session_bodies_delta_amplification.sql 900
--   ⚠ 本脚本只含 SELECT / SET（statement_timeout 等四个会话旋钮），不含任何写操作。
--
-- ★★ 为什么要有这个脚本：§143.73 的结论是【两口径】的，缺一不可 ——
--   口径①【形状】：每轮落盘量相对「轮次」的二阶系数 ⇒ 区分「二次方」与「线性」；
--   口径②【基线】：上一轮的 `outbound_body` 在库里是不是【数组】——
--     ★ 是 jsonb 字面量 'null' ⇒ 差集基线读空 ⇒ delta 退化成【客户端完整历史】；
--     ★ 是数组 ⇒ 基线有效 ⇒ delta 真只存【本轮新增】。
--   ⇒★ 只看形状会误判（形态 B 也有异常轮）；只看基线会漏掉放大。
--
-- ★ 三条已知踩坑（别重试）：
--   ① `jsonb_typeof(...)` 会 **detoast** ⇒ 在 27 万行的 10 月分区上全扫会超时
--      ⇒ 形状口径一律只用 `pg_column_size()`（不 detoast）；
--   ② `jsonb_array_length()` 遇到非数组会直接报错 ⇒ 必须先 `jsonb_typeof(...)='array'`；
--   ③ ★ `outbound_body IS NULL` 是【假判据】——库里存的是 jsonb 字面量 'null'，
--      它【不是】 SQL NULL ⇒ ⇒ 必须用 `jsonb_typeof(outbound_body)` 判定。
--      （★ 这就是 §143.73 腿C1 里 out_null=0 而 out_array=0 的原因。）

SET statement_timeout = '300s';
SET lock_timeout = '10s';
SET work_mem = '64MB';

\echo '=== 口径0：10 月分区总量（delta / outbound / response 三列的字节占比）===' 
SELECT count(*) FILTER (WHERE kind='turn_delta')                              AS turn_rows,
       sum(pg_column_size(request_delta))  FILTER (WHERE kind='turn_delta')   AS delta_bytes,
       sum(pg_column_size(outbound_body))  FILTER (WHERE kind='turn_delta')   AS out_bytes,
       sum(pg_column_size(response_delta)) FILTER (WHERE kind='turn_delta')   AS resp_bytes,
       pg_total_relation_size('public.session_bodies_2026_10')                AS partition_total_bytes
FROM public.session_bodies_2026_10;

\echo '=== 口径0b：按天剖面（★ E1 门 storage.session_final_full_enabled 在 2026-10-21 21:27:26 开启 ⇒ 之后 out_bytes 塌零）===' 
SELECT (ts AT TIME ZONE 'Asia/Shanghai')::date                                AS day,
       count(*) FILTER (WHERE kind='turn_delta')                              AS turn_rows,
       count(*) FILTER (WHERE kind='final_full')                              AS ff_rows,
       sum(pg_column_size(request_delta))  FILTER (WHERE kind='turn_delta')   AS delta_bytes,
       sum(pg_column_size(outbound_body))  FILTER (WHERE kind='turn_delta')   AS out_bytes,
       sum(pg_column_size(response_delta)) FILTER (WHERE kind='turn_delta')   AS resp_bytes
FROM public.session_bodies_2026_10
GROUP BY 1 ORDER BY 1;

\echo '=== 口径①：≥10 轮会话按「二阶系数」分桶（★ 只用 pg_column_size ⇒ 不 detoast ⇒ 可全量）===' 
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
            ELSE 'C_linear_<=100' END                                      AS shape,
       count(*)                                                            AS sessions,
       sum(turns)                                                          AS turns,
       sum(delta_bytes)                                                    AS delta_bytes,
       round(avg(b_per_turn2))                                             AS avg_b_per_turn2
FROM per_sess
GROUP BY 1 ORDER BY sum(delta_bytes) DESC;

\echo '=== 口径①b：二阶系数最高的会话（★ 放大在【短会话】上就已经明显）===' 
WITH per_sess AS (
  SELECT tenant_id, session_id, count(*) AS turns,
         sum(pg_column_size(request_delta)) AS delta_bytes,
         round(sum(pg_column_size(request_delta))::numeric / (count(*) * count(*))) AS b_per_turn2
  FROM public.session_bodies_2026_10
  WHERE kind='turn_delta'
  GROUP BY tenant_id, session_id
  HAVING count(*) >= 10
)
SELECT session_id, turns, delta_bytes, b_per_turn2
FROM per_sess ORDER BY b_per_turn2 DESC LIMIT 10;

\echo '=== 口径②：基线形态（★ 必须逐会话查；全局全扫会超时 ⇒ 只查指定会话）===' 
-- ★ 用法：把下面的会话 ID 换成要核的会话（形态 A / 形态 B 各一个）。
-- ★ 判读：out_array=0 且 out_null=0 ⇒ 库里存的是 jsonb 'null' ⇒ ★ 基线读空 ⇒ delta 退化为整份历史。
SELECT session_id,
       count(*) FILTER (WHERE kind='turn_delta')                                        AS turns,
       count(*) FILTER (WHERE kind='final_full')                                        AS ff_rows,
       count(*) FILTER (WHERE kind='turn_delta' AND outbound_body IS NULL)             AS out_sql_null,
       count(*) FILTER (WHERE kind='turn_delta' AND jsonb_typeof(outbound_body)='array') AS out_array,
       count(*) FILTER (WHERE kind='turn_delta' AND jsonb_typeof(outbound_body)='null')  AS out_json_null,
       min(ts) FILTER (WHERE kind='turn_delta')                                         AS first_turn,
       max(ts) FILTER (WHERE kind='turn_delta')                                         AS last_turn
FROM public.session_bodies_2026_10
WHERE session_id IN (
        'gw_1d807424-b624-4d7a-b166-0ee48d83d19d',   -- 形态 A（基线恒空 ⇒ 每轮整份重存）
        'gw_c68285e7-b509-42b6-8f46-c1814c4ebfb0',   -- 形态 A
        'gw_4b5c2d17-6ca9-4fa0-b882-2126a28ccdb6',   -- 形态 B（基线有效 ⇒ 只存新增）
        'gw_d70f891a-c9e8-434a-97ed-4bd3bb651d57'    -- 形态 B
      )
GROUP BY session_id ORDER BY session_id;
