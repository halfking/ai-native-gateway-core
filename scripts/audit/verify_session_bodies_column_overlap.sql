-- ═══════════════════════════════════════════════════════════════════════════
-- verify_session_bodies_column_overlap.sql
--
-- 用途（§143.48，2026-10-21）：量化 `session_bodies` 族里
--       **`request_delta`（本轮增量请求）与 `outbound_body`（压缩后实际发往上游的完整正文）**
--       之间的**内容重叠度**，判断「同一份请求内容存了两遍」是否成立。
--
-- ★★★ 触发事实（§143.48 的 F 段先量到的体积分布，252 实测）：
--   `session_bodies` 族 = 4,703 MB，其中
--     request_delta   = **2,418 MB**（51.4%）
--     outbound_body   = **1,785 MB**（38.0%）
--     response_delta  =    54 MB
--   ⇒⇒ **request_delta 比 outbound_body 还大 633 MB** ⇒ 若内容重叠 ⇒ 同源双写。
--
-- ★★★ 三条纪律（都是本轮踩出来的）：
--   ① **jsonb 的「无正文」有两种形态**：SQL NULL 与 jsonb `null`（Go 侧 nil slice ⇒
--      `json.Marshal` 出 `"null"`）⇒ ⇒ **判「有没有正文」必须写
--      `col IS NULL OR col = 'null'::jsonb`**（产品自己的惯用形态见
--      `db/request_logs_view_schema.go:762`）。
--   ② **`jsonb_array_length()` 对 jsonb `null` 直接报错**
--      （`cannot get array length of a scalar`）⇒ ⇒ **必须先用 `jsonb_typeof()` 守卫**。
--   ③ **不要对含大 TOAST 列的表达式做多键 GROUP BY** ⇒ 会超时（实测 120s 不够）。
--      ⇒ 一律改成**单遍扫描 + FILTER 计数**。
--
-- ★★ 本脚本**只读**：全部语句为 SELECT，无 DDL/DML。
-- 用法：bash ~/.mavis-tools/lgwp-prod.sh <本文件> 900
-- ═══════════════════════════════════════════════════════════════════════════

SET statement_timeout = '240s';
SET lock_timeout = '5s';
SET work_mem = '64MB';
SET jit = off;

\echo '### A1. 三列的形态分布（★ 单遍 + FILTER，不做多键 GROUP BY）'
SELECT count(*) AS rows,
       count(*) FILTER (WHERE request_delta IS NULL) AS req_sql_null,
       count(*) FILTER (WHERE request_delta = 'null'::jsonb) AS req_json_null,
       count(*) FILTER (WHERE jsonb_typeof(request_delta) = 'array') AS req_is_array,
       count(*) FILTER (WHERE outbound_body IS NULL) AS out_sql_null,
       count(*) FILTER (WHERE outbound_body = 'null'::jsonb) AS out_json_null,
       count(*) FILTER (WHERE jsonb_typeof(outbound_body) = 'array') AS out_is_array,
       count(*) FILTER (WHERE response_delta IS NULL) AS resp_sql_null,
       count(*) FILTER (WHERE response_delta = 'null'::jsonb) AS resp_json_null,
       count(*) FILTER (WHERE jsonb_typeof(response_delta) = 'array') AS resp_is_array
  FROM public.session_bodies
 WHERE kind = 'turn_delta';

\echo '### A2. ★★ 消息条数与体积（★ 数组长度，已用 jsonb_typeof 守卫）'
SELECT count(*) AS rows,
       count(*) FILTER (WHERE jsonb_typeof(request_delta) = 'array') AS req_array_rows,
       count(*) FILTER (WHERE jsonb_typeof(outbound_body) = 'array') AS out_array_rows,
       avg(CASE WHEN jsonb_typeof(request_delta) = 'array' THEN jsonb_array_length(request_delta) END)::numeric(12,2) AS avg_req_msgs,
       avg(CASE WHEN jsonb_typeof(outbound_body) = 'array' THEN jsonb_array_length(outbound_body) END)::numeric(12,2) AS avg_out_msgs,
       avg(CASE WHEN jsonb_typeof(response_delta) = 'array' THEN jsonb_array_length(response_delta) END)::numeric(12,2) AS avg_resp_msgs,
       avg(pg_column_size(request_delta))::bigint AS avg_req_bytes,
       avg(pg_column_size(outbound_body))::bigint AS avg_out_bytes,
       avg(pg_column_size(response_delta))::bigint AS avg_resp_bytes
  FROM public.session_bodies
 WHERE kind = 'turn_delta';

\echo '### A3. ★★ 元素级包含判定（★ outbound 是否是 request_delta 的超集）'
-- ★ `@>` 是 jsonb 包含判定，对 jsonb `null` 操作数是安全的（不报错）。
SELECT count(*) AS rows,
       count(*) FILTER (WHERE outbound_body @> request_delta) AS outbound_contains_delta,
       count(*) FILTER (WHERE request_delta @> outbound_body) AS delta_contains_outbound,
       count(*) FILTER (WHERE request_delta = outbound_body) AS identical,
       count(*) FILTER (WHERE request_delta->0 = outbound_body->0) AS same_first_element,
       count(*) FILTER (WHERE request_delta->-1 = outbound_body->-1) AS same_last_element
  FROM public.session_bodies
 WHERE kind = 'turn_delta';

\echo '### A4. ★★ 若可去重，理论上能省多少（★ 删掉 request_delta 的体积）'
SELECT count(*) AS rows,
       pg_size_pretty(sum(pg_column_size(request_delta))) AS req_delta_total,
       pg_size_pretty(sum(pg_column_size(outbound_body))) AS outbound_total,
       pg_size_pretty(sum(pg_column_size(outbound_body)) - sum(pg_column_size(request_delta))) AS outbound_minus_delta,
       pg_size_pretty(sum(pg_column_size(request_delta)) - sum(pg_column_size(outbound_body))) AS delta_minus_outbound
  FROM public.session_bodies
 WHERE kind = 'turn_delta';

\echo '### A5. ★ 抽样目视（★ 同一行的两列长什么样；★ 用 LIMIT 采样，不做全表 jsonb_typeof 聚合）'
SELECT session_id, turn_no,
       jsonb_typeof(request_delta) AS t_req,
       jsonb_typeof(outbound_body) AS t_out,
       CASE WHEN jsonb_typeof(request_delta) = 'array' THEN jsonb_array_length(request_delta) END AS req_msgs,
       CASE WHEN jsonb_typeof(outbound_body) = 'array' THEN jsonb_array_length(outbound_body) END AS out_msgs,
       pg_column_size(request_delta) AS req_bytes,
       pg_column_size(outbound_body) AS out_bytes,
       left(request_delta::text, 350) AS req_head,
       left(outbound_body::text, 350) AS out_head
  FROM public.session_bodies
 WHERE kind = 'turn_delta'
   AND pg_column_size(request_delta) > 3000
 LIMIT 4;

\echo '### A6. ★ 索引与 TOAST 开销（★ 4,703 MB 里有多少不是数据本体）'
SELECT c.relname AS part,
       pg_size_pretty(pg_relation_size(c.oid)) AS heap,
       pg_size_pretty(pg_indexes_size(c.oid)) AS indexes,
       pg_size_pretty(pg_total_relation_size(c.oid)) AS total
  FROM pg_inherits i
  JOIN pg_class c ON c.oid = i.inhrelid
  JOIN pg_class p ON p.oid = i.inhparent
 WHERE p.relname = 'session_bodies'
 ORDER BY c.relname;
