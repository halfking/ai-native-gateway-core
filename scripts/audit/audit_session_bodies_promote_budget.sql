-- ═══════════════════════════════════════════════════════════════════════
-- §143.98 会话正文族（session_bodies）promote【预算不变式】只读审计
--
-- ★ 用途：把「排水搬 0 行」的**根因算术**变成**可复跑的读数**。
-- ★ 本脚本**全程只读**：只有 SELECT；无 TEMP TABLE；无 DDL；无写。
-- ★ 用法：生产经跳板执行（见 §143.97 的执行器形态）：
-- ★   bash ~/.mavis-tools/lgwp-prod.sh <本文件> <ssh 超时秒>
--
-- ═══════════════════════════════════════════════════════════════════════
-- 【为什么要有这个脚本（2026-10-23 §143.98 轮实测）】
--
--   现象：154 上 `session_bodies_hot` 的 promote 批【每小时必败一次】
--   （timeout: context deadline exceeded），而失败前的批【成功】。
--
--   算术（两个独立量具互证）：
--     ① 每行写成本 ≈ 29.9 ms —— 由 154 的 24h promote 日志做
--        「首批批次：行数 vs 耗时」过原点回归（9 个样本）得出；
--     ② 每行写成本 ≈ 28.2 ms —— 由生产 pg_stat_statements 的
--        `INSERT INTO public.session_bodies_hot`（77,886 次 / 77,886 行）
--        的 mean_exec_time 得出；
--     ⇒ ⇒ 60 s 批次预算 ⇒ **最大可行 batch ≈ 2,000 行**；
--     ⇒ ⇒ 而生产 `lifecycle.promote_batch_size` **没有 settings_kv 行**
--     ⇒ ⇒ 走代码默认 **5,000** ⇒ ⇒ **需要 ≈ 150 s = 2.5× 预算**
--     ⇒ ⇒ **每个满批必然超时 ⇒ 整批回滚 ⇒ 搬 0 行**。
--
-- ★★ 2026-10-30（R184 轮）**改了两个数** ⇒ ⇒ ⇒ **本脚本的口径必须跟着改**，
--   ★★ 否则它会用【旧预算】现场算出一个【已经过期】的结论（★ 量具说谎）：
--
--   ① **批次预算 60 s → 150 s**（`bg/partition_manager.go` 的 `promoteBatchBudget`；
--      Go ctx 与 `SET LOCAL statement_timeout` 同值）⇒ ⇒ 150 s 内最大可行
--      batch ≈ **5,154 行**（按 29.1 ms/行）；
--   ② **周期预算 5 min → 10 min**（`promoteCycleTimeout`）；
--   ③ ★ **`session_bodies_hot` 改走专用旋钮**
--      `lifecycle.session_bodies_hot_promote_batch_size`（代码 fallback **500**）
--      ⇒ ⇒ **不再读 `lifecycle.promote_batch_size`** ⇒ ⇒ Q3/Q9 必须读新键。
--
--   ★★ **但 ① 并没有让这个族变得可提交** ⇒ ⇒ ⇒ ⇒ 因为它的每行成本不是 29.1 ms：
--   ★★ R184 的 252 实测显示正文表是 **TOAST 重行**（均值 ≈34 KB/行），
--   ★★ 单批 500 行要 **35~90 s** ⇒ ⇒ **折算 70~180 ms/行**（比通用族贵 2.4~6.2×）
--   ★★ ⇒ ⇒ 5,000 行在 150 s 预算下仍需 350~900 s ⇒ ⇒ **仍然必超时**
--   ★★ ⇒ ⇒ ⇒ ★ **⇒ 这才是这条不变式的现形形态：「批大小 vs 预算」要按【族】估行宽，
--   ★★ 不能用一个全局 29.1 ms/行** ⇒ ⇒ ⇒ 修法 = 给该族一个专用键（500 行）。
--   ★★ ⇒ ⇒ （§143.98 原文按 60 s 预算的算术全部保留在 runbook，那段是**当时**的实测。）
--
--   本脚本把这套算术的**每一个输入**都现场量一遍（窗口/速率/成本/积压），
--   ⇒ 任何一项漂移都能被立刻看到 ⇒ ⇒ 而不必重新翻日志。
-- ═══════════════════════════════════════════════════════════════════════

\pset pager off

\echo '=== Q1 统计窗口（★ 所有 calls/耗时都必须除以它才是速率）===';
SELECT stats_reset,
       now() AS now_ts,
       round(EXTRACT(epoch FROM (now() - stats_reset))::numeric / 3600, 2) AS window_hours
FROM pg_stat_statements_info;

\echo '=== Q2 批次预算的三个常量（代码侧）===';
\echo '--- ① 批次截止：Go ctx = min(150s, 周期剩余) + SET LOCAL statement_timeout = 150s（★ 2026-10-30 R184：60s → 150s）';
\echo '--- ② 周期预算：promoteCycleTimeout = 10min（18 个族共享）（★ R184：5min → 10min）';
\echo '--- ③ 每批上限：★ 本族走【专用键】lifecycle.session_bodies_hot_promote_batch_size（缺省 500）';
\echo '--- （全局键 lifecycle.promote_batch_size 默认 5000，★ 对本族不生效 —— R184 起 resolvePromoteConfig 分族取键）';

\echo '=== Q3 生产现值：promote 批大小两个键（★ 本族读专用键；空 = 走代码 fallback）===';
SELECT key, value, value_type, scope, category, updated_at, updated_by
FROM public.settings_kv
WHERE key IN ('lifecycle.promote_batch_size',
              'lifecycle.session_bodies_hot_promote_batch_size',
              'lifecycle.hot_retention_hours',
              'lifecycle.promote_interval_hours')
ORDER BY key;

\echo '=== Q4 每行写成本（★ 两个独立量具：热表单行 INSERT + promote 整批调用）===';
SELECT 'hot_insert_single_row' AS gauge,
       calls,
       rows,
       round(mean_exec_time::numeric, 2) AS mean_ms,
       round(total_exec_time::numeric / NULLIF(rows, 0), 2) AS ms_per_row,
       round(max_exec_time::numeric, 1) AS max_ms
FROM pg_stat_statements s
JOIN pg_database d ON d.oid = s.dbid
WHERE d.datname = current_database()
  AND s.query ILIKE 'INSERT INTO public.session_bodies_hot%'
  AND s.rows > 0
UNION ALL
SELECT 'promote_call_whole_batch',
       calls,
       NULL,
       round(mean_exec_time::numeric, 1),
       NULL,
       round(max_exec_time::numeric, 1)
FROM pg_stat_statements s
JOIN pg_database d ON d.oid = s.dbid
WHERE d.datname = current_database()
  AND s.query ILIKE 'SELECT promote_session_bodies_hot_to_partition(%'
ORDER BY 1;

\echo '=== Q5 到达率（★ hot 表 INSERT 计数 ÷ 窗口小时 ⇒ 每小时新到的行数）===';
SELECT s.query ~ 'VALUES' AS is_value_form,
       s.calls,
       round(EXTRACT(epoch FROM (now() - i.stats_reset))::numeric / 3600, 2) AS window_hours,
       round(s.calls::numeric / (EXTRACT(epoch FROM (now() - i.stats_reset)) / 3600), 0) AS rows_per_hour
FROM pg_stat_statements s
JOIN pg_database d ON d.oid = s.dbid
CROSS JOIN pg_stat_statements_info i
WHERE d.datname = current_database()
  AND s.query ILIKE 'INSERT INTO public.session_bodies_hot%'
  AND s.rows > 0
GROUP BY 1, s.calls, i.stats_reset;

\echo '=== Q6 积压（★ over_8h = 已越过保留窗却还没被搬走的行 = 排水缺口）===';
SELECT 'session_bodies_hot' AS fam,
       count(*) FILTER (WHERE ts < now() - interval '8 hours') AS over_8h,
       count(*)                                           AS total,
       min(ts) AS oldest_ts,
       max(ts) AS newest_ts
FROM public.session_bodies_hot
UNION ALL
SELECT 'session_turns_hot',
       count(*) FILTER (WHERE ts < now() - interval '8 hours'),
       count(*),
       min(ts),
       max(ts)
FROM public.session_turns_hot;

\echo '=== Q7 存储（★ hot 表体积 = 积压占用的空间；父表当月分区形态 = heap 还是 columnar）===';
SELECT 'hot' AS kind,
       relname AS name,
       pg_size_pretty(pg_total_relation_size(oid)) AS size,
       (SELECT reltuples::bigint FROM pg_class c2 WHERE c2.oid = c.oid) AS reltuples,
       NULL::text AS reloptions
FROM pg_class c
WHERE relname IN ('session_bodies_hot', 'session_turns_hot', 'session_turn_details_hot')
UNION ALL
SELECT 'parent_partition',
       c.relname,
       pg_size_pretty(pg_total_relation_size(c.oid)),
       NULL,
       coalesce(c.reloptions::text, '(heap — 无 reloptions)')
FROM pg_class c
JOIN pg_inherits inh ON inh.inhrelid = c.oid
JOIN pg_class p ON p.oid = inh.inhparent
WHERE p.relname = 'session_bodies'
ORDER BY 1, 2;

\echo '=== Q8 索引清单（★ 写侧每行要维护多少个索引 ⇒ 每行成本的主要成分）===';
SELECT t.relname AS table_name,
       c.relname AS index_name,
       pg_get_indexdef(c.oid) AS index_def,
       pg_size_pretty(pg_relation_size(c.oid)) AS index_size
FROM pg_class c
JOIN pg_index i ON i.indexrelid = c.oid
JOIN pg_class t ON t.oid = i.indrelid
WHERE t.relname = 'session_bodies_hot'
   OR t.oid IN (SELECT inhrelid FROM pg_inherits inh
                JOIN pg_class p ON p.oid = inh.inhparent
                WHERE p.relname = 'session_bodies')
ORDER BY t.relname, pg_relation_size(c.oid) DESC;

\echo '=== Q9 预算不变式自检（★ 把算术摊开：现场值 vs 阈值）===';
WITH win AS (
    SELECT EXTRACT(epoch FROM (now() - stats_reset))::numeric AS secs FROM pg_stat_statements_info
),
cost AS (
    SELECT sum(total_exec_time)::numeric / NULLIF(sum(rows), 0) AS ms_per_row
    FROM pg_stat_statements s
    JOIN pg_database d ON d.oid = s.dbid
    WHERE d.datname = current_database()
      AND s.query ILIKE 'INSERT INTO public.session_bodies_hot%'
      AND s.rows > 0
),
cur AS (
    -- ★ 2026-10-30（R184 轮）：本族改走【专用键】
    --   lifecycle.session_bodies_hot_promote_batch_size（代码 fallback = 500），
    --   ★ 不再读全局键 lifecycle.promote_batch_size（★ resolvePromoteConfig
    --   分族取键 ⇒ 读全局键会量错旋钮 ⇒ ⇒ 量具指错对象）。
    SELECT COALESCE((SELECT (value::text)::int FROM public.settings_kv
                      WHERE key = 'lifecycle.session_bodies_hot_promote_batch_size'), 500) AS batch_size
)
-- ★★ 诚实边界（★ 2026-10-30 R184 订正）：本列的 ms_per_row 量的是
--   **写热表**（INSERT INTO session_bodies_hot，每次一行 ⇒ mean/rows 即每行成本），
--   ★★ **不是** promote 拷贝（把 TOAST 重行搬进月度分区）的每行成本。
--   ★★ R184 的 252 实测：promote 拷贝 500 行要 35~90 s ⇒ 折算 **70~180 ms/行**
--   ★★ （TOAST 重行，均值 ≈34 KB/行 ⇒ 比本列贵 2.4~6.2×）⇒ ★★ **⇒ 本行的
--   ★★ need_seconds / max_feasible_batch 是【乐观口径】** ⇒ ★★ 真正的余量要看
--   ★★ R184 的 EXPLAIN ANALYZE 读数（§143.98 的 29.1 ms/行也是同一类乐观口径）。
--   ★★ 要现场量 promote 拷贝轴需要按【每次调用的批行数】换算（★ 不能用
--   ★★ pg_stat_statements 的 rows ——那个是函数**返回行数**，不是拷贝行数）。
SELECT cur.batch_size                                          AS current_batch_size,
       round(cost.ms_per_row::numeric, 1)                     AS measured_ms_per_row,
       round(cur.batch_size * cost.ms_per_row::numeric / 1000, 1) AS need_seconds,
       150                                                     AS budget_seconds,
       (cur.batch_size * cost.ms_per_row::numeric / 1000) > 150 AS will_time_out,
       round((150000 / cost.ms_per_row)::numeric, 0)            AS max_feasible_batch
FROM cur, cost, win;
