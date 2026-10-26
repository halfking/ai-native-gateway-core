-- ═══════════════════════════════════════════════════════════════════════════
-- verify_830_precheck_211.sql —— 211 上执行迁移 830（快照族分区化）前的**只读**预检
--
-- ★★ 为什么必须有预检：830 是 **manual-by-design** 的一次性迁移，它做的是
--    「RENAME 旧表 + 建空父表 + 建首个分区」——**它不搬数据**：
--    旧表整体变成 `ursm_node_snapshot_min_legacy`（回滚锚点），
--    **新的分区父表从零开始**，历史行留在 _legacy 里（活动表读不到）。
--    ⇒ 执行后「活动表里的历史 = 0」⇒ 凡是读活动表历史的读者会看到**数据消失**
--    （数据没丢，迁到了 _legacy；那一侧没有任何读路径）。
--    ⇒ ⇒ 必须先量化「有多少行会迁走、迁走什么时间跨度」。
--
-- ★ 本脚本**只读**：全部为 SELECT / WITH / SET（会话参数）。
--
-- 用法（211，LAN docker）：
--   ssh 211 'docker exec -i acc-pg17 psql -U acc_super -d llm_gateway \
--            -v ON_ERROR_STOP=1 -P pager=off -f -' < scripts/audit/verify_830_precheck_211.sql
-- ═══════════════════════════════════════════════════════════════════════════

SET statement_timeout = '120s';
SET lock_timeout = '10s';
SET work_mem = '64MB';
SET jit = off;

\echo '########## A. 830 的三条前置守卫（与迁移文件里的 DO 块同口径）##########'
SELECT to_regclass('public.ursm_node_snapshot_min_legacy') IS NULL AS legacy_absent_ok,
       to_regclass('public.ursm_node_snapshot_min') IS NOT NULL AS table_exists_ok,
       (SELECT c.relkind = 'p' FROM pg_class c
         WHERE c.oid = to_regclass('public.ursm_node_snapshot_min')) IS NOT TRUE AS not_partitioned_yet_ok;

\echo '########## B. 当前形态与规模（迁走后 _legacy 的样子）##########'
SELECT c.relkind,
       c.reltuples::bigint AS est_rows,
       pg_total_relation_size(c.oid) AS total_bytes,
       s.n_live_tup,
       s.n_dead_tup
  FROM pg_class c
  LEFT JOIN pg_stat_user_tables s ON s.relid = c.oid
 WHERE c.oid = to_regclass('public.ursm_node_snapshot_min');

\echo '########## C. 列清单（★ 必须与 830 建父表的 56 列逐一对照）##########'
SELECT a.attnum,
       a.attname,
       format_type(a.atttypid, a.atttypmod) AS col_type,
       a.attnotnull AS not_null
  FROM pg_attribute a
 WHERE a.attrelid = to_regclass('public.ursm_node_snapshot_min')
   AND a.attnum > 0
   AND NOT a.attisdropped
 ORDER BY a.attnum;

\echo '########## D. 主键与索引（writer 的 ON CONFLICT arbiter 必须原样保留）##########'
SELECT con.conname,
       con.contype,
       pg_get_constraintdef(con.oid) AS definition
  FROM pg_constraint con
 WHERE con.conrelid = to_regclass('public.ursm_node_snapshot_min')
 ORDER BY con.contype, con.conname;

\echo '########## E. 主键列顺序（必须是 snapshot_ts, tenant_id, credential_id, raw_model_name）##########'
SELECT pg_get_indexdef(i.indexrelid) AS pk_indexdef
  FROM pg_index i
 WHERE i.indrelid = to_regclass('public.ursm_node_snapshot_min')
   AND i.indisprimary;

\echo '########## F. 依赖对象（视图 / 触发器 / 函数体引用）—— 迁走旧表会不会打断它们 ##########'
SELECT DISTINCT c.relkind,
       n.nspname AS schema,
       c.relname
  FROM pg_depend d
  JOIN pg_class c ON c.oid = d.objid
  JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE d.refobjid = to_regclass('public.ursm_node_snapshot_min')
   AND c.relkind IN ('v', 'm', 'r', 'S')
 ORDER BY c.relkind, n.nspname, c.relname;

\echo '########## G. ★ 会迁走的行：时间跨度与行数（= 执行后活动表里「消失」的那部分）##########'
SELECT min(snapshot_ts) AS ts_min,
       max(snapshot_ts) AS ts_max,
       count(*) AS rows_that_move,
       count(DISTINCT tenant_id) AS tenants,
       count(*) FILTER (WHERE tenant_id = '') AS empty_tenant_rows,
       count(*) FILTER (WHERE tenant_id <> '') AS real_tenant_rows
  FROM public.ursm_node_snapshot_min;

\echo '########## H. ensure 函数是否已存在（830 会重建它；boot ensure 依赖它）##########'
SELECT to_regprocedure('public.ensure_ursm_node_snapshot_min_daily_partition(date)') AS ensure_fn;

\echo '########## I. 磁盘余量（新父表从零开始，几乎不占空间）##########'
SELECT pg_size_pretty(pg_database_size(current_database())) AS db_size,
       (SELECT pg_size_pretty(pg_tablespace_size(t.spcname)) FROM pg_tablespace t WHERE t.spcname = 'pg_default') AS tablespace_size;

\echo '########## J. 判读要点 ##########'
\echo '  A 三条守卫全 t ⇒ 830 未被执行过且目标表存在（可执行）'
\echo '  C 列清单必须**覆盖** 830 建父表的 56 列（缺列 ⇒ writer 的 INSERT 会 42703）'
\echo '  D/E 主键四列与顺序必须不变（变了 ⇒ ON CONFLICT 失去 arbiter ⇒ 全量写入瘫痪）'
\echo '  F 视图/触发器依赖：旧表改名会让它们指向 _legacy（不会断，但语义变了）'
\echo '  G ★ 会迁走的行数与跨度 —— 执行后活动表里这批行**读不到**（数据在 _legacy）'
\echo '  H ensure 函数存在与否（830 会重建；boot ensure 用 to_regprocedure 探测）'
