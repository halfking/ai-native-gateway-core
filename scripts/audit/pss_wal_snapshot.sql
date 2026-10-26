-- ═══════════════════════════════════════════════════════════════════════════
-- pss_wal_snapshot.sql（§143.50，2026-10-21）
--
-- 用途：给「同窗口 WAL 覆盖率」测量提供**快照**。
--
-- ★★★ 为什么要【两个快照 + 本地差分】（而不是一条 SQL 自比）：
--   ① `pg_stat_wal` 的统计快照**按事务缓存** ⇒ **同一事务内读两次，差值恒为 0**
--      （§143.49 实测踩过：252 与 211 都读到 0.000 MB，第一反应是「没有写入」）
--      ⇒ ⇒ 两次采样必须落在**两个不同事务** ⇒ ⇒ 两条独立语句（psql 自动提交）。
--   ② `pg_stat_statements` 只有**累计值**，没有增量 ⇒ ⇒ 同样只能靠两次快照做差。
--   ③ ★ 覆盖率必须**同窗口**：pss 的统计窗口（实测 2.42 天）与 `pg_stat_wal` 的
--      统计窗口（实测 10.66 天）**不同** ⇒ ⇒ 拿两个累计值相除是**错的**
--      （§143.49 曾据此得出「覆盖率 16.23%」，实测同窗口是 76%~87% ⇒ 已在
--      §143.50 订正横幅二十七就地作废）。
--
-- ★★ 用法（252，只读，★ 两次之间【必须跨连接跨事务】）：
--   bash ~/.mavis-tools/lgwp-prod.sh scripts/audit/pss_wal_snapshot.sql 300 > t0.log 2>&1
--   sleep 90
--   bash ~/.mavis-tools/lgwp-prod.sh scripts/audit/pss_wal_snapshot.sql 300 > t1.log 2>&1
--   python3 scripts/audit/pss_wal_same_window_coverage.py t0.log t1.log
--
-- ★★ 本脚本**只读**：全部语句为 SELECT。
-- ═══════════════════════════════════════════════════════════════════════════

SET statement_timeout = '120s';
SET lock_timeout = '5s';
SET work_mem = '32MB';
SET jit = off;

\echo '### PSS_SNAPSHOT'
-- ★ 键：queryid | dbid | userid | toplevel | calls | rows | wal_bytes | total_exec_time
-- ★ ★ 注意：queryid 可为负数（int64）；toplevel 是字符 't'/'f'；
--   数值带千分位逗号 ⇒ 差分器会先去掉逗号。
-- ★ ★★ 差分器**按本文件里的段标记切分**（PSS_SNAPSHOT / WAL_SNAPSHOT / WINDOW_MARKS）
--   ⇒ ⇒ 改动段名会破坏解析 ⇒ 差分器与本文件必须成对使用。
SELECT queryid, dbid, userid, toplevel, calls, rows, wal_bytes, total_exec_time
  FROM pg_stat_statements
 ORDER BY queryid, dbid, userid, toplevel;

\echo '### WAL_SNAPSHOT'
-- ★ wal_bytes 是【实例级】累计值（与 dbid 无关）⇒ 它是「实际 WAL」的分母来源。
-- ★ ★★ 差分器只解析本段 ⇒ 本段【必须】是 5 列且末尾 3 个是数字字段。
SELECT now() AS read_at,
       (SELECT stats_reset FROM pg_stat_wal) AS wal_stats_reset,
       (SELECT wal_bytes FROM pg_stat_wal) AS wal_bytes_total,
       (SELECT wal_records FROM pg_stat_wal) AS wal_records,
       (SELECT wal_fpi FROM pg_stat_wal) AS wal_fpi;

\echo '### WINDOW_MARKS'
-- ★ 两个统计起点（★ 用来核对「pss 窗口」与「wal 窗口」确实不同）
SELECT (SELECT stats_reset FROM pg_stat_statements_info) AS pss_stats_reset,
       (SELECT stats_reset FROM pg_stat_wal) AS wal_stats_reset,
       (SELECT oid FROM pg_database WHERE datname = current_database()) AS current_db_oid;
