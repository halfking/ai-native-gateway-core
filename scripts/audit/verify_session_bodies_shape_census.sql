-- verify_session_bodies_shape_census.sql
-- §143.75 轮（2026-10-22）的【只读】复算脚本：v2 正文表的【形态普查】与【可回收量测算】。
--
-- ★ 用法：在 252 共享库上以【只读】方式执行（可经 154 跳板）。
--   例：bash ~/.mavis-tools/lgwp-prod.sh scripts/audit/verify_session_bodies_shape_census.sql 1200
--   ⚠ 本脚本只含 SELECT / SET（statement_timeout 等四个会话旋钮），不含任何写操作。
--
-- ★★ 本脚本要回答的三个问题（★ 每个都有明确的判读方式）：
--   ① 【形态占比】：库里有多少会话是「形态 A（逐轮 outbound_body 是 jsonb 'null' ⇒
--      差集基准恒空 ⇒ 每轮整份落盘）」？
--      ⇒ ★ 判读：形态 A 的会话占比 ⇒ ★ 形态 A 占比高 ⇒ 「二次方放大」是默认路径。
--   ② 【可回收量】：若「形态 A 的多轮会话只保留最后一轮」，能省多少字节？
--      ⇒ ★ 判读：★ **只有形态 A 可这样清理**（形态 B 的 delta 是真实新增，删了就丢历史）。
--   ③ 【收益集中度】：可回收量集中在哪些会话上？
--      ⇒ ★ 判读：★ 若高度集中在少数 >50 轮会话 ⇒ ⇒ 处置成本极低、收益几乎全部。
--
-- ★★★ 三条关键口径（★ 别踩）：
--   ① ★★ **形态标记必须用 `pg_column_size(outbound_body) > 12`**，★ 不用 `jsonb_typeof`：
--      jsonb 'null' 的存储大小是 9 字节，数组则远大于此 ⇒ ⇒ **不 detoast ⇒ 可全量**；
--      ★ 而 `jsonb_typeof(outbound_body)` 会 detoast ⇒ 在 27 万行上全扫会超时。
--   ② ★★ **`outbound_body IS NULL` 是假判据** —— 库里存的是 jsonb 字面量 'null'，不是 SQL NULL。
--   ③ ★★ **「只留一轮」这一列只对形态 A 有意义**（形态 B 的每一轮都是真实新增）⇒
--      ★★ ⇒ **不要把它当成「全库可省」** —— 形态 B 的那一列是【误算】。
--
-- ★ 四条已知踩坑（别重试）：
--   · 分区行数与 hot 表（8h 窗口）有重叠 ⇒ 计数口径必须写明是哪一张；
--   · `pg_total_relation_size(parent)` 恒 0 ⇒ 要用分区名；
--   · `pg_size_pretty` 不能带 FILTER；
--   · 大 TOAST 多键 GROUP BY 会超时 ⇒ 本脚本只用单键聚合 + 分组。

SET statement_timeout = '600s';
SET lock_timeout = '10s';
SET work_mem = '128MB';

\echo '=== 口径①：10 月分区形态普查（★ 不 detoast 的形态标记）===' 
WITH sess AS (
  SELECT tenant_id, session_id,
         count(*) AS turns,
         sum(pg_column_size(request_delta)) AS delta_bytes,
         max(CASE WHEN pg_column_size(outbound_body) > 12 THEN 1 ELSE 0 END) AS has_out,
         max(pg_column_size(request_delta)) AS keep_one_bytes
  FROM public.session_bodies_2026_10
  WHERE kind='turn_delta'
  GROUP BY tenant_id, session_id
)
SELECT CASE WHEN has_out=0 THEN 'A_no_outbound_形态A' ELSE 'B_has_outbound_形态B' END AS shape,
       count(*) AS sessions,
       sum(turns) AS turns,
       sum(delta_bytes) AS delta_bytes,
       -- ★ 形态 A 才可「只留一轮」；形态 B 的这一列是【误算】（每一轮都是真实新增）
       -- ★★ 用 FILTER 表达「只统计形态 A」⇒ 避免在聚合外引用 has_out（★ 会触发 GROUP BY 报错）
       sum(delta_bytes)  FILTER (WHERE has_out=0) - sum(keep_one_bytes) FILTER (WHERE has_out=0)
                                                                                     AS reclaimable_if_keep_one
FROM sess
GROUP BY 1 ORDER BY sum(delta_bytes) DESC;

\echo '=== 口径②：形态 A 的轮数分布（★ 判断收益形状：单轮会话不可回收）===' 
WITH sess AS (
  SELECT tenant_id, session_id,
         count(*) AS turns,
         sum(pg_column_size(request_delta)) AS delta_bytes,
         max(CASE WHEN pg_column_size(outbound_body) > 12 THEN 1 ELSE 0 END) AS has_out,
         max(pg_column_size(request_delta)) AS keep_one_bytes
  FROM public.session_bodies_2026_10
  WHERE kind='turn_delta'
  GROUP BY tenant_id, session_id
)
SELECT CASE WHEN turns = 1 THEN '1_turn'
            WHEN turns <= 3 THEN '2-3'
            WHEN turns <= 10 THEN '4-10'
            WHEN turns <= 50 THEN '11-50'
            ELSE '>50' END AS turn_bucket,
       count(*) AS sessions, sum(turns) AS turns, sum(delta_bytes) AS delta_bytes,
       sum(delta_bytes) - sum(keep_one_bytes) AS reclaimable
FROM sess WHERE has_out=0 GROUP BY 1 ORDER BY sum(delta_bytes) DESC;

\echo '=== 口径③：形态 A 里字节最大的会话（★ 收益集中度）===' 
WITH sess AS (
  SELECT tenant_id, session_id,
         count(*) AS turns,
         sum(pg_column_size(request_delta)) AS delta_bytes,
         max(CASE WHEN pg_column_size(outbound_body) > 12 THEN 1 ELSE 0 END) AS has_out,
         max(pg_column_size(request_delta)) AS keep_one_bytes
  FROM public.session_bodies_2026_10
  WHERE kind='turn_delta'
  GROUP BY tenant_id, session_id
)
SELECT session_id, turns, delta_bytes, delta_bytes - keep_one_bytes AS reclaimable
FROM sess WHERE has_out=0 ORDER BY delta_bytes DESC LIMIT 15;

\echo '=== 口径④：分区总量（★ 算占比的分母）===' 
SELECT count(*) FILTER (WHERE kind='turn_delta')                              AS turn_rows,
       count(*) FILTER (WHERE kind='final_full')                              AS ff_rows,
       sum(pg_column_size(request_delta))  FILTER (WHERE kind='turn_delta')   AS delta_bytes,
       sum(pg_column_size(outbound_body))  FILTER (WHERE kind='turn_delta')   AS out_bytes,
       sum(pg_column_size(response_delta)) FILTER (WHERE kind='turn_delta')   AS resp_bytes,
       pg_total_relation_size('public.session_bodies_2026_10')                AS partition_total_bytes
FROM public.session_bodies_2026_10;
