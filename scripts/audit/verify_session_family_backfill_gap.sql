-- ═══════════════════════════════════════════════════════════════════════════
-- verify_session_family_backfill_gap.sql
--
-- 用途（§143.47，2026-10-21）：判定「**v1 族的数据有没有被 v2 会话族完整接住**」——
--       这是 v1 族退役（迁移 890）能否执行的前置判据，也是 §143.46 待查项
--       「9 月 session_bodies 未回填」的真伪判定。
--
-- ★★ 判定框架（本轮的核心）：**「缺失」类结论必须双向对账，不能只看目标侧**
--
--   ① **源侧有没有那些数据**？  ⇒ v1 族在某个时间窗之前到底有没有行
--      （若源侧本来就没有 ⇒ 「目标侧没有」是**必然**，不是缺口）
--   ② **重叠窗口内，目标侧有没有漏接**？  ⇒ 逐 request_id 半连接对账
--      （源侧 330K 行 vs 目标侧 269K 行 ⇒ 覆盖面更长却行数更少 ⇒ **必有漏接**）
--
-- ★ 本脚本**只读**：全部语句为 SELECT / SET（会话参数），无 DDL/DML。
--
-- 用法：
--   252（经 154 跳板）：bash ~/.mavis-tools/lgwp-prod.sh <本文件> 280
--   211（LAN docker）：   ssh 211 'docker exec -i acc-pg17 psql -U acc_super \
--                                          -d llm_gateway -v ON_ERROR_STOP=1 -P pager=off -f -' < <本文件>
-- ═══════════════════════════════════════════════════════════════════════════

SET statement_timeout = '240s';
SET lock_timeout = '5s';
SET work_mem = '32MB';
SET jit = off;

-- ★ 两个时间锚点（2026-10-21 实测得到，不是猜的）：
--   T_bodies = 2026-10-01 00:00:00+08 ⇒ **正文（bodies）链路的起点**（v1 与 v2 同刻）
--   T_turns  = 2026-09-06 11:03:50.985994+08 ⇒ **会话元数据族（turns/details/sessions）的起点**
\echo '### A. v1（源侧）族的行数与 ts 跨度'
SELECT 'v1._request_logs' AS fam, count(*) AS rows, min(ts) AS min_ts, max(ts) AS max_ts
  FROM public._request_logs
UNION ALL
SELECT 'v1._request_logs_bodies', count(*), min(ts), max(ts)
  FROM public._request_logs_bodies
UNION ALL
SELECT 'v1._request_logs_hot', count(*), min(ts), max(ts)
  FROM public._request_logs_hot
UNION ALL
SELECT 'v1._request_logs_bodies_hot', count(*), min(ts), max(ts)
  FROM public._request_logs_bodies_hot
ORDER BY 1;

\echo '### B. v2（目标侧）族的行数与 ts 跨度'
SELECT 'v2.session_turns' AS fam, count(*) AS rows, min(ts) AS min_ts, max(ts) AS max_ts
  FROM public.session_turns
UNION ALL
SELECT 'v2.session_turn_details', count(*), min(ts), max(ts)
  FROM public.session_turn_details
UNION ALL
SELECT 'v2.sessions', count(*), min(created_at), max(created_at)
  FROM public.sessions
UNION ALL
SELECT 'v2.session_bodies', count(*), min(ts), max(ts)
  FROM public.session_bodies
ORDER BY 1;

\echo '### C. 两个锚点之前的行数（★ 判「源侧本来有没有」）'
SELECT 'v1._request_logs' AS fam,
       count(*) FILTER (WHERE ts < timestamptz '2026-10-01 00:00:00+08') AS before_bodies_start,
       count(*) FILTER (WHERE ts < timestamptz '2026-09-06 11:03:50.985994+08') AS before_turns_start,
       count(*) AS total
  FROM public._request_logs
UNION ALL
SELECT 'v1._request_logs_bodies',
       count(*) FILTER (WHERE ts < timestamptz '2026-10-01 00:00:00+08'),
       count(*) FILTER (WHERE ts < timestamptz '2026-09-06 11:03:50.985994+08'),
       count(*)
  FROM public._request_logs_bodies
UNION ALL
SELECT 'v2.session_turns',
       count(*) FILTER (WHERE ts < timestamptz '2026-10-01 00:00:00+08'),
       count(*) FILTER (WHERE ts < timestamptz '2026-09-06 11:03:50.985994+08'),
       count(*)
  FROM public.session_turns
UNION ALL
SELECT 'v2.session_bodies',
       count(*) FILTER (WHERE ts < timestamptz '2026-10-01 00:00:00+08'),
       count(*) FILTER (WHERE ts < timestamptz '2026-09-06 11:03:50.985994+08'),
       count(*)
  FROM public.session_bodies
ORDER BY 1;

\echo '### D. 逐分区：session_bodies（9 月分区到底装了什么）'
SELECT c.relname AS part,
       count(*) AS rows,
       min(b.ts) AS min_ts,
       max(b.ts) AS max_ts,
       pg_size_pretty(pg_total_relation_size(c.oid)) AS size
  FROM public.session_bodies b
  JOIN pg_class c ON c.oid = b.tableoid
 GROUP BY c.relname, c.oid
 ORDER BY c.relname;

\echo '### E. v1 _request_logs_bodies 的分区清单（★ 该族是 columnar ⇒ 不能用 tableoid 扫）'
-- ★ 量具坑：columnar（citus columnar）分区**不支持 CTID / UPDATE 扫描**
--   ⇒ `JOIN pg_class c ON c.oid = b.tableoid` 在该族上报
--   「UPDATE and CTID scans not supported for ColumnarScan」⇒ **读数作废**。
-- ⇒ 改法：**不碰行**，改从 pg_inherits 枚举分区 + reltuples 估行数 + 体积；
--   精确行数与 ts 跨度由 A 段（整族 min/max，已实测）给出。
SELECT c.relname AS part,
       c.reltuples::bigint AS approx_rows,
       pg_size_pretty(pg_total_relation_size(c.oid)) AS size
  FROM pg_inherits i
  JOIN pg_class c ON c.oid = i.inhrelid
  JOIN pg_class parent ON parent.oid = i.inhparent
 WHERE parent.relname = '_request_logs_bodies'
 ORDER BY c.relname;

\echo '### F. 9 月分区的 ts 落在哪个 UTC 日 / 哪个 +08 日（★ 分区归属口径）'
SELECT (b.ts AT TIME ZONE 'UTC')::date AS utc_day,
       (b.ts AT TIME ZONE 'Asia/Shanghai')::date AS cn_day,
       count(*) AS rows
  FROM public.session_bodies b
  JOIN pg_class c ON c.oid = b.tableoid
 WHERE c.relname = 'session_bodies_2026_09'
 GROUP BY 1, 2
 ORDER BY 1;

\echo '### G. ★★ 逐 request_id 对账：v1 bodies → v2 bodies（重叠窗口有没有漏接）'
SELECT count(*) AS v1_rows,
       count(*) FILTER (WHERE EXISTS
         (SELECT 1 FROM public.session_bodies e WHERE e.request_id = b.request_id)) AS present_in_v2,
       count(*) FILTER (WHERE NOT EXISTS
         (SELECT 1 FROM public.session_bodies e WHERE e.request_id = b.request_id)) AS missing_in_v2,
       min(b.ts) AS v1_min_ts,
       max(b.ts) AS v1_max_ts
  FROM public._request_logs_bodies b;

\echo '### H. 缺口行的逐日分布（缺口集中在早期还是全程）'
SELECT b.ts::date AS day,
       count(*) AS missing_in_v2
  FROM public._request_logs_bodies b
 WHERE NOT EXISTS (SELECT 1 FROM public.session_bodies e WHERE e.request_id = b.request_id)
 GROUP BY 1
 ORDER BY 1;

\echo '### I. 同口径对账：v1 _request_logs → v2 session_turns（请求元数据有没有漏接）'
SELECT count(*) AS v1_rows,
       count(*) FILTER (WHERE EXISTS
         (SELECT 1 FROM public.session_turns t WHERE t.request_id = l.request_id)) AS present_in_v2,
       count(*) FILTER (WHERE NOT EXISTS
         (SELECT 1 FROM public.session_turns t WHERE t.request_id = l.request_id)) AS missing_in_v2
  FROM public._request_logs l;

\echo '### J. 重叠窗口逐日：v1 bodies vs v2 bodies 行数对比'
WITH v1 AS (
  SELECT b.ts::date AS d, count(*) AS n FROM public._request_logs_bodies b GROUP BY 1
), v2 AS (
  SELECT b.ts::date AS d, count(*) AS n FROM public.session_bodies b GROUP BY 1
)
SELECT coalesce(v1.d, v2.d) AS day,
       coalesce(v1.n, 0) AS v1_bodies,
       coalesce(v2.n, 0) AS v2_bodies,
       coalesce(v1.n, 0) - coalesce(v2.n, 0) AS diff
  FROM v1 FULL JOIN v2 ON v1.d = v2.d
 ORDER BY 1;

\echo '### K. ★ 缺口的性质：探针 / 失败 / 成功非探针（★ 决定「漏接」还是「语义不同」）'
-- ★★★ 关键判据：**v1 是「全量请求日志」（含探针、含失败），v2 是「会话族」（只收会话请求）**
--   ⇒⇒ 两边**本就不是同一个语义集合** ⇒⇒ **行数差不能当「漏接」**。
--   ⇒⇒ 只有「成功且非探针」的缺口才可能是真缺口候选。
SELECT CASE WHEN l.request_id LIKE 'probe-%' THEN '1_probe'
            WHEN l.success IS FALSE THEN '2_failed'
            ELSE '3_success_no_probe' END AS kind,
       count(*) AS missing_rows
  FROM public._request_logs l
 WHERE NOT EXISTS (SELECT 1 FROM public.session_turns t WHERE t.request_id = l.request_id)
 GROUP BY 1
 ORDER BY 1;

\echo '### K2. 对照：已被 v2 接住的行的同一分类'
SELECT CASE WHEN l.request_id LIKE 'probe-%' THEN '1_probe'
            WHEN l.success IS FALSE THEN '2_failed'
            ELSE '3_success_no_probe' END AS kind,
       count(*) AS present_rows
  FROM public._request_logs l
 WHERE EXISTS (SELECT 1 FROM public.session_turns t WHERE t.request_id = l.request_id)
 GROUP BY 1
 ORDER BY 1;

\echo '### K3. ★ request_id 形态对拍（★ 排除「join 键不对」这个替代解释）'
SELECT 'missing' AS bucket, l.request_id, l.ts, l.tenant_id, l.application_id,
       l.virtual_ip, l.success, l.error_kind, l.client_model
  FROM public._request_logs l
 WHERE NOT EXISTS (SELECT 1 FROM public.session_turns t WHERE t.request_id = l.request_id)
 ORDER BY l.ts
 LIMIT 6;

SELECT 'present' AS bucket, t.request_id, t.ts, t.tenant_id, t.session_id
  FROM public.session_turns t
 ORDER BY t.ts DESC
 LIMIT 5;

\echo '### L. ★★ 分区边界与真实行数（pg_get_expr + 直接计数）'
SELECT p.relname AS parent,
       c.relname AS part,
       pg_get_expr(c.relpartbound, c.oid) AS bound,
       c.reltuples::bigint AS approx_rows
  FROM pg_inherits i
  JOIN pg_class c ON c.oid = i.inhrelid
  JOIN pg_class p ON p.oid = i.inhparent
 WHERE p.relname IN ('session_turns', 'session_bodies', 'sessions', 'session_turn_details')
 ORDER BY p.relname, c.relname;

\echo '### L2. ★ 直接计数（★★ 不要用 `PARTITION (...)` 子句）'
-- ★★ 量具坑：在本库 `FROM tbl PARTITION (tbl_2026_09)` 返回**整表**（实测 1,063,889 = 全族），
--   而直接 `FROM public.tbl_2026_09` 返回该分区的真实行数（798,278）⇒ ⇒ **PARTITION 子句读数作废**。
-- ★★ 形态：按「**实际存在**的分区」用 \gexec 生成精确计数语句 ⇒ 换实例不会 42P01
--   （211 上 `session_bodies` 只有 `_2026_10`，写死分区名会让整脚本中止）。
--   `reltuples` 是估计值；这里要的是**精确行数**，所以逐分区 count(*)。
SELECT format(
         'SELECT %L AS how, count(*) AS rows FROM public.%I',
         c.relname, c.relname)
  FROM pg_inherits i
  JOIN pg_class c ON c.oid = i.inhrelid
  JOIN pg_class p ON p.oid = i.inhparent
 WHERE p.relname IN ('session_turns', 'session_turn_details', 'session_bodies', 'sessions')
   AND (c.relname LIKE '%\_2026\_09' OR c.relname LIKE '%\_2026\_10')
 ORDER BY c.relname
\gexec

\echo '### M. ★★★ 8 小时切片的规模（★ 直接 FROM 分区表，不用 PARTITION 子句）'
-- ★★★ 会话族的 partition_date 是 **UTC 日**（domains/session/v2/partition_date.go 的 calendarDate()），
--   而 841 的过期线 `cur_month := date_trunc('month', now())::date` 是**本地时区**
--   ⇒⇒ **每个 UTC 月分区都多装着「下一个本地月的头 8 小时」**
--   ⇒⇒ 按分区名做月度留存时，**DROP 一个分区会连带吃掉被保留月份的头 8 小时**。
--
-- ★★ 形态说明（2026-10-21 补）：本段用 **\gexec** 逐个「实际存在」的分区跑。
--   分区名是**数据驱动**的（211 上 session_bodies 只有 _2026_10、没有 _2026_09），
--   写死分区名会让本脚本在另一个实例上 42P01 中止
--   ⇒ ⇒ 「按文档操作就会炸」那一族（§143.64 的教训）。
--   \gexec 是 psql 原生命令；每行生成的仍是纯 SELECT，无 DDL/DML。
SELECT format(
         'SELECT %L AS fam, %L AS part, count(*) FILTER (WHERE ts >= timestamptz %L) AS sliver_rows, count(*) AS part_rows FROM public.%I',
         p.relname, c.relname, '2026-10-01 00:00:00+08', c.relname)
  FROM pg_inherits i
  JOIN pg_class c ON c.oid = i.inhrelid
  JOIN pg_class p ON p.oid = i.inhparent
 WHERE p.relname IN ('session_turns', 'session_turn_details', 'session_bodies')
   AND c.relname LIKE '%\_2026\_09'
 ORDER BY p.relname, c.relname
\gexec

SELECT format(
         'SELECT %L AS fam, %L AS part, count(*) FILTER (WHERE created_at >= timestamptz %L) AS sliver_rows, count(*) AS part_rows FROM public.%I',
         p.relname, c.relname, '2026-10-01 00:00:00+08', c.relname)
  FROM pg_inherits i
  JOIN pg_class c ON c.oid = i.inhrelid
  JOIN pg_class p ON p.oid = i.inhparent
 WHERE p.relname = 'sessions'
   AND c.relname LIKE '%\_2026\_09'
\gexec

\echo '### N0. 列存在性预检（★ 产品的分类表达式要读这几列）'
SELECT column_name, data_type
  FROM information_schema.columns
 WHERE table_schema = 'public' AND table_name = '_request_logs'
   AND column_name IN ('is_auto_request','request_type','origin_actor','task_type',
                       'request_status','error_kind','success','gw_session_id','work_type')
 ORDER BY column_name;

\echo '### N1. ★ 缺口成功行的性质（★ 探针 / 失败 / 成功非探针）与 session 线索'
-- ★★ 关键判据：v1 `_request_logs` 有 **`gw_session_id`** 列（见 890 的 down.sql 完整 DDL）。
--   v2 会话族是**按会话**组织的 ⇒ 若一行本来就没有 session 线索 ⇒ v2 不收它是设计如此。
WITH base AS MATERIALIZED (
  SELECT l.request_id, l.success, l.gw_session_id,
         (l.gw_session_id IS NOT NULL AND l.gw_session_id <> '') AS has_hint,
         EXISTS (SELECT 1 FROM public.session_turns t WHERE t.request_id = l.request_id) AS in_v2
    FROM public._request_logs l
   WHERE l.success IS TRUE AND l.request_id NOT LIKE 'probe-%'
)
SELECT in_v2, has_hint, count(*) AS rows, count(DISTINCT gw_session_id) AS distinct_sessions
  FROM base GROUP BY 1,2 ORDER BY 1,2;

\echo '### N2. ★ 有 session 线索的缺口行：那个 session 在 v2 里存在吗'
-- ★★ 若「有线索、但那个 session 在 v2 里根本没有」⇒⇒ **不是「某个 turn 写失败」，
--   而是「这个会话压根没进 v2」**（⇒ 需回到写入链路查）。
WITH gap AS MATERIALIZED (
  SELECT l.request_id, l.gw_session_id
    FROM public._request_logs l
   WHERE l.success IS TRUE
     AND l.request_id NOT LIKE 'probe-%'
     AND l.gw_session_id IS NOT NULL AND l.gw_session_id <> ''
     AND NOT EXISTS (SELECT 1 FROM public.session_turns t WHERE t.request_id = l.request_id)
)
SELECT count(*) AS gap_rows_with_hint,
       count(DISTINCT g.gw_session_id) AS distinct_sessions,
       count(DISTINCT g.gw_session_id) FILTER (
         WHERE EXISTS (SELECT 1 FROM public.session_turns t2 WHERE t2.session_id = g.gw_session_id)
       ) AS sessions_present_in_v2,
       count(*) FILTER (
         WHERE EXISTS (SELECT 1 FROM public.session_turns t2 WHERE t2.session_id = g.gw_session_id)
       ) AS gap_rows_whose_session_present_in_v2
  FROM gap g;

\echo '### N3. ★ 缺口成功行的画像（application_id / client_model）'
WITH base AS MATERIALIZED (
  SELECT l.application_id, l.client_model, l.request_mode, l.is_auto_request, l.work_type,
         EXISTS (SELECT 1 FROM public.session_turns t WHERE t.request_id = l.request_id) AS in_v2
    FROM public._request_logs l
   WHERE l.success IS TRUE AND l.request_id NOT LIKE 'probe-%'
)
SELECT in_v2, application_id, client_model, request_mode, is_auto_request, work_type, count(*) AS rows
  FROM base
 GROUP BY 1,2,3,4,5,6
 ORDER BY 7 DESC LIMIT 20;

\echo '### N4. ★ 缺口成功行在 v2 session_bodies 里存在吗（★ 排除「只漏 bodies」）'
WITH gap AS MATERIALIZED (
  SELECT l.request_id
    FROM public._request_logs l
   WHERE l.success IS TRUE
     AND l.request_id NOT LIKE 'probe-%'
     AND NOT EXISTS (SELECT 1 FROM public.session_turns t WHERE t.request_id = l.request_id)
)
SELECT (SELECT count(*) FROM gap) AS gap_success_rows,
       (SELECT count(*) FROM gap g JOIN public.session_bodies b ON b.request_id = g.request_id) AS in_v2_bodies;

\echo '### N5. ★★★ 收口交叉表：产品判据 × 探针/失败/成功 × 是否被 v2 接住'
-- ★★★ 判据来源：**产品自己的** `db.MirrorDriftClassSQL`（db/request_logs_view_schema.go:869，逐字拷贝）。
--   internal_loopback = is_auto_request AND (request_type∈生成器 OR origin_actor∈生成器 OR task_type='')
--   non_terminal     = NOT success AND request_status∉{failure,rate_limited} AND error_kind=''
--   genuine_loss     = 其余全部
-- ★★ 口径：全量 v1 行（不是只取成功行）⇒ 与 A 段的 v1 总行数必须能对上。
WITH base AS MATERIALIZED (
  SELECT l.request_id, l.success, l.request_status, l.error_kind,
         l.is_auto_request, l.request_type, l.origin_actor, l.task_type,
         l.gw_session_id,
         (l.gw_session_id IS NOT NULL AND l.gw_session_id <> '') AS has_hint,
         EXISTS (SELECT 1 FROM public.session_turns t WHERE t.request_id = l.request_id) AS in_v2
    FROM public._request_logs l
)
SELECT CASE
         WHEN COALESCE(is_auto_request, false)
              AND (   TRIM(COALESCE(request_type, '')) IN ('title_gen', 'summary')
                   OR TRIM(COALESCE(origin_actor, '')) IN ('auto-title-generator', 'auto-summary-generator', 'session-summary')
                   OR TRIM(COALESCE(task_type, '')) = '')            THEN 'internal_loopback'
         WHEN NOT COALESCE(success, false)
              AND TRIM(COALESCE(request_status, '')) NOT IN ('failure', 'rate_limited')
              AND TRIM(COALESCE(error_kind, '')) = ''                 THEN 'non_terminal'
         ELSE 'genuine_loss'
       END AS product_class,
       CASE WHEN request_id LIKE 'probe-%' THEN '1_probe'
            WHEN NOT COALESCE(success, false) THEN '2_failed'
            ELSE '3_success_no_probe' END AS my_kind,
       in_v2,
       has_hint,
       count(*) AS rows
  FROM base
 GROUP BY 1,2,3,4 ORDER BY 1,2,3,4;

\echo '### N6. ★ 探针行的身份画像（★ 产品判据为什么认不出它们）'
SELECT CASE WHEN request_id LIKE 'probe-%' THEN '1_probe' ELSE '2_other' END AS kind,
       COALESCE(is_auto_request, false) AS is_auto_request,
       TRIM(COALESCE(origin_actor, '')) AS origin_actor,
       TRIM(COALESCE(task_type, '')) AS task_type,
       count(*) AS rows
  FROM public._request_logs l
 GROUP BY 1,2,3,4
 ORDER BY 5 DESC LIMIT 20;

\echo '### N7. ★★★ 修正后的五分类（★ 给属主的收益量化：给产品判据补两条臂）'
-- ★★ 现行产品判据只有三臂 ⇒ **认不出「探针」与「无会话语义」两类** ⇒ 把它们算进 genuine_loss。
--   本段把这两类单独分出来 ⇒ 看清「真漏写」到底有多小。
--   ⇒ 与 N5 的 genuine_loss 行数对拍 ⇒ 差额就是「补两条臂」的收益。
WITH base AS MATERIALIZED (
  SELECT l.request_id, l.success, l.request_status, l.error_kind,
         l.is_auto_request, l.request_type, l.origin_actor, l.task_type,
         l.gw_session_id,
         EXISTS (SELECT 1 FROM public.session_turns t WHERE t.request_id = l.request_id) AS in_v2
    FROM public._request_logs l
), cls AS MATERIALIZED (
  SELECT *,
         CASE
           WHEN COALESCE(is_auto_request, false)
                AND (   TRIM(COALESCE(request_type, '')) IN ('title_gen', 'summary')
                     OR TRIM(COALESCE(origin_actor, '')) IN ('auto-title-generator', 'auto-summary-generator', 'session-summary')
                     OR TRIM(COALESCE(task_type, '')) = '')            THEN 'internal_loopback'
           WHEN NOT COALESCE(success, false)
                AND TRIM(COALESCE(request_status, '')) NOT IN ('failure', 'rate_limited')
                AND TRIM(COALESCE(error_kind, '')) = ''                 THEN 'non_terminal'
           ELSE 'genuine_loss'
         END AS product_class
    FROM base
)
SELECT CASE
         WHEN product_class = 'internal_loopback'  THEN '1_internal_loopback(产品判据已有)'
         WHEN product_class = 'non_terminal'      THEN '2_non_terminal(产品判据已有)'
         WHEN request_id LIKE 'probe-%'           THEN '3_probe(★ 产品判据不认识这一类)'
         WHEN NOT (gw_session_id IS NOT NULL AND gw_session_id <> '')
                                                    THEN '4_no_session_hint(★ 本就没有会话)'
         ELSE '5_genuine_loss(★ 真信号)'
       END AS bucket,
       count(*) AS rows
  FROM cls
 WHERE NOT in_v2
 GROUP BY 1 ORDER BY 1;

\echo '### N8. ★★★ 修正后的五分类（★ 探针按【内部 worker actor 名单】认，不按 request_id 前缀）'
-- ★★★ 教训（**211 对测逼出来的**）：探针**不止一种形态** ⇒
--   252：request_id 前缀内 163,965 ＋ 前缀外（node-probe-worker 20,684 /
--        credential-selfcheck-worker 2,369 / probe-service 51）= 23,104
--   211：前缀内 22,951 ＋ 前缀外（probe-service 11,460 / node-probe-worker 82）= 11,542
--   ⇒ ⇒ ★★ **只按 `request_id LIKE 'probe-%'` 认 ⇒ 211 上会漏 11,460 行**
--   ⇒ ⇒ **真信号会从 0 变成 11,460** ⇒ ⇒ **判据必须是「内部 worker actor 名单」，不是 request_id 前缀**。
--   ⇒ ⇒ 建议把 internal/internaltraffic 的 actor 名单从「生成器」提升为「内部 worker」。
WITH base AS MATERIALIZED (
  SELECT l.request_id, l.success, l.request_status, l.error_kind,
         l.is_auto_request, l.request_type, l.origin_actor, l.task_type,
         l.gw_session_id,
         EXISTS (SELECT 1 FROM public.session_turns t WHERE t.request_id = l.request_id) AS in_v2
    FROM public._request_logs l
), cls AS MATERIALIZED (
  SELECT *,
         CASE
           WHEN COALESCE(is_auto_request, false)
                AND (   TRIM(COALESCE(request_type, '')) IN ('title_gen', 'summary')
                     OR TRIM(COALESCE(origin_actor, '')) IN ('auto-title-generator', 'auto-summary-generator', 'session-summary')
                     OR TRIM(COALESCE(task_type, '')) = '')            THEN 'internal_loopback'
           WHEN NOT COALESCE(success, false)
                AND TRIM(COALESCE(request_status, '')) NOT IN ('failure', 'rate_limited')
                AND TRIM(COALESCE(error_kind, '')) = ''                 THEN 'non_terminal'
           ELSE 'genuine_loss'
         END AS product_class
    FROM base
)
SELECT CASE
         WHEN product_class = 'internal_loopback'  THEN '1_internal_loopback(产品判据已有)'
         WHEN product_class = 'non_terminal'      THEN '2_non_terminal(产品判据已有)'
         WHEN TRIM(COALESCE(origin_actor, '')) IN ('node-probe-worker', 'active-probe-worker',
                                                   'probe-service', 'credential-selfcheck-worker')
                                                   THEN '3_internal_worker(★ 产品判据不认识这一类)'
         WHEN NOT (gw_session_id IS NOT NULL AND gw_session_id <> '') THEN '4_no_session_hint(★ 本就没有会话)'
         ELSE '5_genuine_loss(★ 真信号)'
       END AS bucket,
       count(*) AS rows
  FROM cls
 WHERE NOT in_v2
 GROUP BY 1 ORDER BY 1;
