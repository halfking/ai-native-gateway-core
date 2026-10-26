-- verify_session_bodies_e1_baseline_causality.sql
-- §143.76 轮（2026-10-22）的【只读】复算脚本：★★ E1 门与「形态 A」之间的【因果链】实测。
--
-- ★ 用法：在 252 共享库上以【只读】方式执行（可经 154 跳板）。
--   例：bash ~/.mavis-tools/lgwp-prod.sh scripts/audit/verify_session_bodies_e1_baseline_causality.sql 1200
--   ⚠ 本脚本只含 SELECT / SET（statement_timeout 等四个会话旋钮），不含任何写操作。
--   ⚠ **必须串行跑**（与本仓库其它生产只读脚本同款纪律）。
--
-- ★★ 本脚本要回答的问题（★ 每个都有明确的判读方式）：
--   ① 【E1 前后逐轮行的 outbound_body 带值率】变了没有？
--      ⇒ ★ 判读：★ 带值率在 E1 后【归零】⇒ ⇒ 形态 A 的成因就在 E1 上。
--   ② 【压缩器本来有没有产出】？（★ 用 final_full 行的存在性量，★ 不用 outbound_body 是否为空）
--      ⇒ ★ 判读：★ E1 后有一半会话【本来有出站】⇒ ⇒ 它们被 E1 打成了形态 A。
--   ③ 【因果链直证】：E1 后「有 final_full 的会话」，其逐轮行 outbound_body 带值率是多少？
--      ⇒ ★ 判读：★ 0.00% ⇒ ⇒ 【E1 显式置 nil】这条链坐实（★ 不是「压缩器没产出」）。
--   ④ 【基准可得性 → delta 体量】的因果（★ 控制会话轮数后分层 ⇒ 排除「长会话本来就大」）。
--      ⇒ ★ 判读：★ 同一层内「基准缺失」比「基准可得」大 ⇒ ⇒ 基准就是放大器。
--   ⑤ 【形态 B 的传播性】：B 轮的下一轮是不是也是 B？
--      ⇒ ★ 判读：★ 是 ⇒ ⇒ 形态 B 自我强化 ⇒ ⇒ E1 一刀切断它 ⇒ ⇒ 二次方从 30% 涨到 100%。
--   ⑥ 【E1 后的放大是否已启动】？（★ 按轮数分档 ⇒ 定位收益集中度）
--
-- ★★★ 五条关键口径（★ 别踩）：
--   ① ★★ **形态标记必须用 `pg_column_size(outbound_body) > 12`**，★ 不用 `jsonb_typeof`：
--      jsonb 'null' 的存储大小是 9 字节，数组则远大于此 ⇒ ⇒ **不 detoast ⇒ 可全量**；
--      ★ 而 `jsonb_typeof(outbound_body)` 会 detoast ⇒ 在 27 万行上全扫会超时。
--   ② ★★ **`outbound_body IS NULL` 是假判据** —— 库里存的是 jsonb 字面量 'null'，不是 SQL NULL。
--   ③ ★★ **跨时代比均值是危险的**：★ E1 前后两段的流量构成不同 ⇒ 平均值不可直接比
--      ⇒ ⇒ 必须用【同一 session 跨 E1 边界】的自然实验，或【控制会话轮数】后分层。
--   ④ ★★ **`lag(count(*) OVER ...)` 是非法 SQL**（窗口函数不能嵌套）⇒ ⇒ 分两层查询。
--   ⑤ ★★ **`pg_get_functiondef` 对聚合会报错** ⇒ 普查函数时必须加 `p.prokind <> 'a'`。
--
-- ★ 四条已知踩坑（别重试）：
--   · `jsonb_each(to_jsonb(t))` 在 27 万宽行上会物化整行 ⇒ 量具自身过重 ⇒ 改用逐列量；
--   · 大 TOAST 多键 GROUP BY 会超时 ⇒ 本脚本只用单键聚合 + 分组；
--   · `pg_total_relation_size(parent)` 恒 0 ⇒ 要用分区名；
--   · `pg_column_size` 不 detoast，但 `sum(pg_column_size(...))` 会读全部行 ⇒ 大分区上要控制超时。

SET statement_timeout = '1200s';
SET lock_timeout = '10s';
SET work_mem = '128MB';

\echo '=== 口径①：E1 前后逐轮行的 outbound_body 带值率（★ 形态标记不 detoast）==='
SELECT CASE WHEN ts >= timestamptz '2026-10-21 21:27:26+08' THEN 'after_E1' ELSE 'before_E1' END AS era,
       count(*) AS turn_delta_rows,
       count(*) FILTER (WHERE pg_column_size(outbound_body) > 12)  AS shape_b_rows,
       count(*) FILTER (WHERE pg_column_size(outbound_body) <= 12) AS shape_a_rows,
       round(100.0 * count(*) FILTER (WHERE pg_column_size(outbound_body) > 12) / count(*), 2) AS shape_b_pct,
       sum(pg_column_size(request_delta)) AS delta_bytes,
       sum(pg_column_size(outbound_body)) AS outbound_bytes
FROM public.session_bodies_2026_10
WHERE kind='turn_delta'
GROUP BY 1 ORDER BY 1;

\echo '=== 口径②：E1 后压缩器产出率（★ 用 final_full 行的存在性量，★ 不用 outbound_body 是否为空）==='
WITH sess AS (
  SELECT tenant_id, session_id,
         count(*) AS turns,
         max(CASE WHEN pg_column_size(outbound_body) > 12 THEN 1 ELSE 0 END) AS has_out
  FROM public.session_bodies_2026_10
  WHERE kind='turn_delta' AND ts >= timestamptz '2026-10-21 21:27:26+08'
  GROUP BY tenant_id, session_id
),
ff AS (
  SELECT tenant_id, session_id FROM public.session_bodies_2026_10
  WHERE kind='final_full' AND ts >= timestamptz '2026-10-21 21:27:26+08'
  GROUP BY tenant_id, session_id
)
SELECT count(*) AS sessions,
       count(*) FILTER (WHERE f.session_id IS NOT NULL) AS sessions_with_final_full,
       round(100.0*count(*) FILTER (WHERE f.session_id IS NOT NULL)/count(*),2) AS compressor_output_pct,
       count(*) FILTER (WHERE s.has_out=0 AND f.session_id IS NOT NULL) AS sessions_A_with_compressor_output,
       count(*) FILTER (WHERE s.has_out=0 AND f.session_id IS NULL)     AS sessions_A_without_compressor_output
FROM sess s LEFT JOIN ff f ON f.tenant_id=s.tenant_id AND f.session_id=s.session_id;

\echo '=== 口径③：因果链直证（★ E1 后有 final_full 的会话，其逐轮行 outbound_body 带值率）==='
SELECT count(*) AS turn_delta_rows,
       count(*) FILTER (WHERE pg_column_size(b.outbound_body) > 12)  AS rows_with_outbound,
       count(*) FILTER (WHERE pg_column_size(b.outbound_body) <= 12) AS rows_null_outbound,
       round(100.0*count(*) FILTER (WHERE pg_column_size(b.outbound_body) > 12)/count(*),2) AS out_pct
FROM public.session_bodies_2026_10 b
WHERE b.kind='turn_delta'
  AND b.ts >= timestamptz '2026-10-21 21:27:26+08'
  AND EXISTS (SELECT 1 FROM public.session_bodies_2026_10 f
              WHERE f.kind='final_full' AND f.tenant_id=b.tenant_id AND f.session_id=b.session_id);

\echo '=== 口径④：★ 同一 session 跨 E1 边界的自然实验（★ 排除流量构成差异）==='
WITH per_sess AS (
  SELECT tenant_id, session_id,
         count(*) FILTER (WHERE ts <  timestamptz '2026-10-21 21:27:26+08') AS n_before,
         count(*) FILTER (WHERE ts >= timestamptz '2026-10-21 21:27:26+08') AS n_after,
         sum(pg_column_size(request_delta)) FILTER (WHERE ts <  timestamptz '2026-10-21 21:27:26+08') AS delta_before,
         sum(pg_column_size(request_delta)) FILTER (WHERE ts >= timestamptz '2026-10-21 21:27:26+08') AS delta_after,
         count(*) FILTER (WHERE ts <  timestamptz '2026-10-21 21:27:26+08' AND pg_column_size(outbound_body) > 12) AS b_before,
         count(*) FILTER (WHERE ts >= timestamptz '2026-10-21 21:27:26+08' AND pg_column_size(outbound_body) > 12) AS b_after
  FROM public.session_bodies_2026_10
  WHERE kind='turn_delta'
  GROUP BY tenant_id, session_id
)
SELECT count(*) AS sessions_spanning_E1,
       sum(n_before) AS turns_before, sum(n_after) AS turns_after,
       sum(delta_before) AS delta_bytes_before, sum(delta_after) AS delta_bytes_after,
       sum(b_before) AS shape_b_before, sum(b_after) AS shape_b_after,
       round(sum(delta_before)::numeric / NULLIF(sum(n_before),0), 0) AS avg_delta_per_turn_before,
       round(sum(delta_after)::numeric  / NULLIF(sum(n_after),0),  0) AS avg_delta_per_turn_after,
       round(100.0*sum(b_before)/NULLIF(sum(n_before),0),2) AS shape_b_pct_before,
       round(100.0*sum(b_after) /NULLIF(sum(n_after),0),2)  AS shape_b_pct_after
FROM per_sess WHERE n_before > 0 AND n_after > 0;

\echo '=== 口径⑤：★★ 基准可得性 → delta 体量（★ 控制会话轮数后分层 ⇒ 排除「长会话本来就大」）==='
WITH base AS (
  SELECT tenant_id, session_id, turn_no,
         pg_column_size(request_delta) AS dsz,
         count(*) OVER (PARTITION BY tenant_id, session_id) AS sess_turns,
         lag(pg_column_size(outbound_body) > 12) OVER w AS prev_has_out
  FROM public.session_bodies_2026_10
  WHERE kind='turn_delta' AND ts < timestamptz '2026-10-21 21:27:26+08'
  WINDOW w AS (PARTITION BY tenant_id, session_id ORDER BY turn_no)
),
r AS (
  SELECT CASE WHEN sess_turns=1 THEN '1' WHEN sess_turns<=3 THEN '2-3'
              WHEN sess_turns<=10 THEN '4-10' ELSE '>10' END AS sess_len_bucket,
         CASE WHEN prev_has_out IS NULL AND sess_turns>1 THEN '0_nonfirst_no_prev'
              WHEN prev_has_out IS NULL THEN '1_first_turn'
              WHEN prev_has_out THEN '2_baseline_available'
              ELSE '3_baseline_missing' END AS baseline_state,
         dsz
  FROM base
)
SELECT sess_len_bucket, baseline_state,
       count(*) AS turns,
       round(sum(dsz)::numeric/count(*), 0) AS avg_delta_bytes
FROM r GROUP BY 1,2 ORDER BY 1,2;

\echo '=== 口径⑥：★★ 形态 B 的传播性（★ B 轮的下一轮是不是也是 B）==='
WITH r AS (
  SELECT tenant_id, session_id, turn_no,
         pg_column_size(outbound_body) > 12 AS has_out,
         lag(pg_column_size(outbound_body) > 12) OVER w AS prev_has_out
  FROM public.session_bodies_2026_10
  WHERE kind='turn_delta' AND ts < timestamptz '2026-10-21 21:27:26+08'
  WINDOW w AS (PARTITION BY tenant_id, session_id ORDER BY turn_no)
)
SELECT prev_has_out, has_out, count(*) AS turns
FROM r WHERE prev_has_out IS NOT NULL GROUP BY 1,2 ORDER BY 1,2;

\echo '=== 口径⑦：E1 后的放大是否已启动（★ 按轮数分档 ⇒ 定位收益集中度）==='
WITH per_sess AS (
  SELECT tenant_id, session_id,
         count(*) AS turns,
         sum(pg_column_size(request_delta)) AS delta_bytes,
         max(pg_column_size(request_delta)) AS last_row_bytes
  FROM public.session_bodies_2026_10
  WHERE kind='turn_delta' AND ts >= timestamptz '2026-10-21 21:27:26+08'
  GROUP BY tenant_id, session_id
)
SELECT CASE WHEN turns=1 THEN '1' WHEN turns<=3 THEN '2-3' WHEN turns<=10 THEN '4-10'
            WHEN turns<=50 THEN '11-50' ELSE '>50' END AS turn_bucket,
       count(*) AS sessions, sum(turns) AS turns, sum(delta_bytes) AS delta_bytes,
       round(sum(delta_bytes)::numeric/count(*),0) AS avg_delta_per_session,
       sum(delta_bytes) - sum(last_row_bytes) AS reclaimable_if_keep_one
FROM per_sess GROUP BY 1 ORDER BY sum(delta_bytes) DESC;

\echo '=== 口径⑧：★★ 跨分区形态普查（★ 补 §143.75 的单分区口径 ⇒ 证明形态 A 是长期默认路径）==='
WITH sess AS (
  SELECT '2026_07' AS part, tenant_id, session_id, count(*) AS turns,
         max(CASE WHEN pg_column_size(outbound_body) > 12 THEN 1 ELSE 0 END) AS has_out
  FROM public.session_bodies_2026_07 WHERE kind='turn_delta' GROUP BY tenant_id, session_id
  UNION ALL
  SELECT '2026_08', tenant_id, session_id, count(*),
         max(CASE WHEN pg_column_size(outbound_body) > 12 THEN 1 ELSE 0 END)
  FROM public.session_bodies_2026_08 WHERE kind='turn_delta' GROUP BY tenant_id, session_id
  UNION ALL
  SELECT '2026_09', tenant_id, session_id, count(*),
         max(CASE WHEN pg_column_size(outbound_body) > 12 THEN 1 ELSE 0 END)
  FROM public.session_bodies_2026_09 WHERE kind='turn_delta' GROUP BY tenant_id, session_id
  UNION ALL
  SELECT '2026_10', tenant_id, session_id, count(*),
         max(CASE WHEN pg_column_size(outbound_body) > 12 THEN 1 ELSE 0 END)
  FROM public.session_bodies_2026_10 WHERE kind='turn_delta' GROUP BY tenant_id, session_id
  UNION ALL
  SELECT '2026_11', tenant_id, session_id, count(*),
         max(CASE WHEN pg_column_size(outbound_body) > 12 THEN 1 ELSE 0 END)
  FROM public.session_bodies_2026_11 WHERE kind='turn_delta' GROUP BY tenant_id, session_id
  UNION ALL
  SELECT 'default', tenant_id, session_id, count(*),
         max(CASE WHEN pg_column_size(outbound_body) > 12 THEN 1 ELSE 0 END)
  FROM public.session_bodies_default WHERE kind='turn_delta' GROUP BY tenant_id, session_id
)
SELECT part,
       CASE WHEN has_out=0 THEN 'A_no_outbound' ELSE 'B_has_outbound' END AS shape,
       count(*) AS sessions, sum(turns) AS turns
FROM sess GROUP BY 1,2 ORDER BY 1, count(*) DESC;

\echo '=== 口径⑨：★★ hot 表与分区的重叠（★ 视图是 hot UNION ALL 分区 ⇒ 重叠 = 读侧重复）==='
SELECT count(*) AS hot_rows,
       count(*) FILTER (WHERE EXISTS (SELECT 1 FROM public.session_bodies b
                                      WHERE b.tenant_id=h.tenant_id AND b.session_id=h.session_id
                                        AND b.turn_no=h.turn_no)) AS hot_rows_also_in_partition,
       count(*) FILTER (WHERE EXISTS (SELECT 1 FROM public.session_bodies b
                                      WHERE b.id=h.id)) AS hot_rows_same_id_in_partition
FROM public.session_bodies_hot h;
