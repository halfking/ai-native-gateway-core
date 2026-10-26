-- verify_session_bodies_read_side_amplification.sql
-- §143.74 轮（2026-10-22）的【只读】复算脚本：v2 正文表「根因锚点 + 三个后果」的口径化复算。
--
-- ★ 用法：在 252 共享库上以【只读】方式执行（可经 154 跳板）。
--   例：bash ~/.mavis-tools/lgwp-prod.sh scripts/audit/verify_session_bodies_read_side_amplification.sql 900
--   ⚠ 本脚本只含 SELECT / SET（statement_timeout 等四个会话旋钮），不含任何写操作。
--
-- ★★ 为什么要这个脚本：§143.74 的根因链是【代码事实 + 生产读数】的组合，缺一不可：
--   代码侧（仓内，★ 由门 scripts/ursmcheck/section_14374_outbound_baseline_test.go 钉住）：
--     · internal/sessionv2mirror/hook.go：`.OutboundBody` 的赋值被
--       `len(entry.OutboundBody) > 0` 守卫 ⇒ ★ 出站正文只来自【压缩器】；
--     · domains/session/v2/turn_reader.go：LoadChain 是【纯拼接、无去重】；
--     · domains/hooks/compression/session_cache.go：loadFromDB 先 LoadLatestOutbound
--       再回落 LoadChain(..., 10) ⇒ ★ 形态 A 必然让压缩器输入被放大 10 倍。
--   生产侧（★ 就是下面这些查询）：
--     口径①【基线形态】：outbound_body 在库里是数组还是 jsonb 'null'；
--     口径②【流量形态】：submit_mode 分布（未压缩流量 ⇒ 压缩器无产物 ⇒ 基线恒空）。
--   ⇒★ 缺一个口径都不能复算本轮结论（★ 只看①看不出成因；只看②说不清库里那列）。
--
-- ★ 三条已知踩坑（别重试）：
--   ① `jsonb_typeof(...)` 会 **detoast** ⇒ 在 27 万行的 10 月分区上全扫会超时
--      ⇒ 口径①要【限量】（按天或按 session_id 子集），口径②（submit_mode）不 detoast ⇒ 可全量；
--   ② ★ `outbound_body IS NULL` 是【假判据】——库里存的是 jsonb 字面量 'null'，
--      它【不是】 SQL NULL ⇒ ⇒ 必须用 `jsonb_typeof(outbound_body)` 判定；
--   ③ ★ 10 月分区与 hot 表（8h 窗口）有【重叠】：刚写入的行可能还没 promote
--      ⇒ ⇒ 同一时刻「分区行数」与「E1 后行数」可能对不上（★ 2026-10-22 实测：
--      分区 3,737 行 vs E1 后 4,411 行 ⇒ 差额在 hot 表里）⇒ ⇒ 计数口径必须写明。

SET statement_timeout = '300s';
SET lock_timeout = '10s';
SET work_mem = '64MB';

\echo '=== 口径①-A：按天 —— outbound_body 的形态分布（★ jsonb 'null' = 基线恒空 = 形态 A）===' 
SELECT (ts AT TIME ZONE 'Asia/Shanghai')::date                                          AS day,
       count(*) FILTER (WHERE kind='turn_delta')                                       AS turn_rows,
       count(*) FILTER (WHERE kind='turn_delta' AND jsonb_typeof(outbound_body)='array') AS out_array,
       count(*) FILTER (WHERE kind='turn_delta' AND jsonb_typeof(outbound_body)='null')  AS out_json_null,
       count(*) FILTER (WHERE kind='final_full')                                       AS ff_rows,
       count(*) FILTER (WHERE kind='final_full' AND jsonb_typeof(outbound_body)='array') AS ff_out_array
FROM public.session_bodies_2026_10
WHERE ts >= TIMESTAMPTZ '2026-10-14 00:00:00+08'
GROUP BY 1 ORDER BY 1;

\echo '=== 口径①-B：final_full 行的 outbound 形态（★ 形态 A 的会话连 final_full 都不写）===' 
SELECT CASE WHEN jsonb_typeof(outbound_body)='array' THEN 'array'
            WHEN jsonb_typeof(outbound_body)='null'  THEN 'json_null'
            WHEN outbound_body IS NULL THEN 'sql_null'
            ELSE jsonb_typeof(outbound_body) END AS out_shape,
       count(*) AS ff_rows, count(DISTINCT session_id) AS sessions
FROM public.session_bodies_2026_10
WHERE kind='final_full'
GROUP BY 1 ORDER BY 2 DESC;

\echo '=== 口径②：submit_mode 分布（★ full = 未压缩 = 压缩器无产物 = 基线恒空）===' 
SELECT submit_mode, count(*) AS turns
FROM public.session_turns_2026_10
GROUP BY 1 ORDER BY 2 DESC;

\echo '=== 口径②-B：E1 门开启后（2026-10-21 21:27:26+08）的新流量形态 ==='
SELECT submit_mode, count(*) AS turns
FROM public.session_turns_2026_10
WHERE ts >= TIMESTAMPTZ '2026-10-21 21:27:26+08'
GROUP BY 1 ORDER BY 2 DESC;

\echo '=== 口径②-C：E1 开关的时点（★ 本轮 E1 门是我自己开的 ⇒ 订单事实源）==='
SELECT key, value, updated_at, prev_value, prev_updated_at
FROM settings_kv
WHERE key = 'storage.session_final_full_enabled';

\echo '=== 口径③：放大量级 —— ≥10 轮会话的二阶系数（★ 形状口径：只用 pg_column_size ⇒ 不 detoast）===' 
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
