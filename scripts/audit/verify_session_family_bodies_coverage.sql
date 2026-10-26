-- ===========================================================================
-- File:          scripts/audit/verify_session_family_bodies_coverage.sql
-- Purpose:       会话族「正文覆盖」读数（★ 只读，不写任何东西）
-- Origin:         §143.68（2026-10-22）—— 复核并订正 §143.67 的「正文丢失」归因
--
-- Status:        active（可重复执行；全部 SELECT）
-- Idempotent:    YES（纯读数）
-- Run:            bash ~/.mavis-tools/lgwp-prod.sh verify_session_family_bodies_coverage.sql 1200
--                 （252 共享库，经 154 跳板；★ 必须串行）
--                 211：ssh 211 'docker exec -i acc-pg17 psql -U acc_super -d llm_gateway -f -' < 本文件
--
-- ★★ 为什么需要这份脚本（它是 §143.67 与 §143.68 的**分歧点**）：
--
--	§143.67 判「2026-09 的 159,831 个会话正文已被删除」；
--	§143.68 用本脚本的读数把该归因**推翻** ⇒ 改成「**从未被持久保存**」。
--	⇒ ⇒ 三条硬证据（都在本脚本里）：
--	  ① 9 月分区里**只有 partition_date = 2026-09-30 一天**的 3,749 行，
--	     且 ts 全在 +08 的 2026-10-01 00:00~07:59 ⇒ ⇒ 它们是 **v2 正文路径的首批**，
--	     不是「9 月的幸存邻居」⇒ ⇒ §143.67 的「同分区同期幸存邻居行」论证**前提失效**；
--	  ② 9 月分区物理体积只有 **18 MB** ⇒ DELETE 不还空间 ⇒ ⇒ **「被 DELETE」被物理体积证伪**；
--	  ③ 10 月逐日「turn 行数 == bodies 的 turn_delta 行数」逐日精确相等
--	     （10-21 多出的 1,086 行恰是 E1 门开后的 final_full 行）⇒ ⇒ 写侧不变式成立。
--
-- ⚠ 时区纪律：PG 会话时区为 Asia/Shanghai(+08)；而**分区键 `partition_date`
--   取的是 UTC 日历日**（`domains/session/v2/partition_date.go` 的 `calendarDate`）
--   ⇒ ⇒ 库里「9 月分区」实际覆盖 +08 的 09-01 08:00 ~ 10-01 08:00
--   ⇒ ⇒ **下面所有「月份」口径必须写清是 partition_date 月还是 ts 月**。
-- ===========================================================================

SET statement_timeout = '900s';
SET lock_timeout = '30s';

-- ── 1. 分区键与各分区的边界 / 存储引擎 / 物理体积 ─────────────────────────
\echo '=== 1. session_bodies / session_turns 的分区键 ==='
SELECT pg_get_partkeydef('public.session_bodies'::regclass) AS bodies_partkey,
       pg_get_partkeydef('public.session_turns'::regclass) AS turns_partkey;

\echo '=== 2. session_bodies 各分区：边界 / am / toast / 物理体积 ==='
SELECT c.relname AS partition,
       pg_get_expr(c.relpartbound, c.oid) AS bounds,
       am.amname AS am,
       (c.reltoastrelid <> 0) AS has_toast,
       c.relpages AS relpages,
       pg_size_pretty(pg_total_relation_size(c.oid)) AS total_size,
       pg_size_pretty(pg_total_relation_size(c.oid) - pg_relation_size(c.oid)) AS toast_size
  FROM pg_inherits i
  JOIN pg_class c ON c.oid = i.inhrelid
  JOIN pg_am am ON am.oid = c.relam
 WHERE i.inhparent = 'public.session_bodies'::regclass
 ORDER BY c.relname;

-- ── 2. 正文覆盖：逐 partition_date 的行数 / 会话数 / ts 范围 ───────────────
-- ★ 关键：**按 partition_date 逐日**看（不是按 ts 归月）⇒ 才能看清
--   「哪一天真的写了正文」，以及边界那 8 小时落在哪个月分区。
\echo '=== 3. session_bodies 逐 partition_date（★ v2 正文覆盖的真实日粒度） ==='
SELECT partition_date, count(*) AS rows, count(DISTINCT session_id) AS sessions,
       count(*) FILTER (WHERE kind = 'final_full') AS final_full_rows,
       count(*) FILTER (WHERE kind = 'turn_delta') AS turn_delta_rows,
       min(ts) AS min_ts, max(ts) AS max_ts
  FROM public.session_bodies
 WHERE partition_date >= '2026-09-01' AND partition_date < '2026-11-01'
 GROUP BY 1 ORDER BY 1;

\echo '=== 4. 对照：session_turns 逐 partition_date（★ turn 行数应 == bodies 的 turn_delta 行数） ==='
SELECT partition_date, count(*) AS rows, min(ts) AS min_ts, max(ts) AS max_ts
  FROM public.session_turns
 WHERE partition_date >= '2026-09-01' AND partition_date < '2026-11-01'
 GROUP BY 1 ORDER BY 1;

-- ── 3. hot 表残余（★ 可恢复性的最后一查） ─────────────────────────────────
\echo '=== 5. hot 表残余：9 月正文是否还留在 hot 里（可恢复性） ==='
SELECT 'bodies_hot' AS tbl, count(*) AS rows, count(DISTINCT session_id) AS sessions,
       min(ts) AS min_ts, max(ts) AS max_ts
  FROM public.session_bodies_hot
UNION ALL
SELECT 'turns_hot', count(*), count(DISTINCT session_id), min(ts), max(ts)
  FROM public.session_turns_hot;

-- ── 4. 两条 DROP 通道的排除证据（★ §143.68 的三查之一） ───────────────────
\echo '=== 6. 841 通道：审计表与配置表（0 行 ⇒ 841 从未 DROP 过任何月分区） ==='
SELECT count(*) AS drop_log_rows FROM public.llm_gateway_partition_drop_log;
SELECT count(*) AS retention_cfg_rows FROM public.llm_gateway_partition_retention;

-- ── 5. 迁移台账（★ 正文族与 v1 族的启用/退役时序） ─────────────────────────
\echo '=== 7. 迁移台账：正文族与 v1 族时序 ==='
SELECT version, description, applied_at
  FROM public.schema_migrations
 WHERE version IN ('430','513','614','615','626','638','708','765','806','810','811','841','890')
 ORDER BY applied_at;

-- ── 6. v1 族是否还在（★ 890 的非空闸放行 ⇒ 早已 0 行） ─────────────────────
\echo '=== 8. v1 族存在性（NULL ⇒ 该表已不存在） ==='
SELECT to_regclass('public.request_logs_bodies')        AS v1_bodies,
       to_regclass('public._request_logs_bodies')       AS v1_bodies_u,
       to_regclass('public.request_logs')               AS v1_logs,
       to_regclass('public._request_logs')              AS v1_logs_u;

-- ★★ 口径提醒（发布本脚本的任何数字时必须一起带上）：
--   · 「9 月」= partition_date 落在 2026-09 的分区日 = +08 的 09-01 08:00 ~ 09-30 08:00；
--   · 「9 月的 turn 侧会话」若按 ts 归月，会与 partition_date 口径**差 8 小时**；
--   · 两侧必须用**同一个口径**再对账，否则会得出「缺 8 小时」或「多 8 小时」的假差异。
