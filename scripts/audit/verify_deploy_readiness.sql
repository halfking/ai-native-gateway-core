-- =====================================================================
-- scripts/audit/verify_deploy_readiness.sql
-- §143.53 —— 「能不能部署 / 部署值不值」的前置读数（两实例同口径）
--
-- 回答五个问题，每个都对应一次真实踩过的坑：
--   R1 迁移水位：这个库落后几个版本？落后的是哪些？
--   R2 退役台账：890（删 v1 族）在自动重放序列里是否已被记账？
--       ⇒ 若未记账 ⇒ 自动重放会【真的执行】它 ⇒ 撞它的非空闸 ⇒ 部署中止
--   R3 v1 非空闸：890 要删的族现在有多少行？⇒ 决定 890 会不会 RAISE
--   R4 种子签名命中：§143.44 选项 A 的判据在本实例命中多少？
--       ⇒ 决定「部署后的可测收益」落在哪个实例（252 命中 24~25% / 211 命中 0）
--   R5 usage_facts 现状：按日分区有哪些？最老分区是哪天？
--       ⇒ 决定 TTL=365 是否已有存量影响（预期：无，只给未来划界）
--
-- 只读：全文件仅 SELECT / SET。零 DDL、零 DML。
-- 用法：
--   # 252（经 154 跳板）
--   bash ~/.mavis-tools/lgwp-prod.sh scripts/audit/verify_deploy_readiness.sql 300 > out.log 2>&1
--   grep -c '^### ' out.log      # 期望 11 段
--   grep -niE 'error|fatal|42P' out.log   # 期望 0 行
--   ⚠⚠ 2026-10-21 实踩：psql 的报错前缀是 `psql:<stdin>:110: ERROR:`
--      ⇒ **不能锚到行首**（`^ERROR` 匹配不到）⇒ ⇒ 会把「有报错」读成「零 ERROR」
--      ⇒ ⇒ 匹配式里不要加 ^，并且**必须**同时看脚本退出码（psql 遇错退出码 ≠ 0）。
--   # 211（docker）
--   ssh 211 'docker exec -i acc-pg17 psql -U acc_super -d llm_gateway \
--     -v ON_ERROR_STOP=1 -P pager=off -f -' < scripts/audit/verify_deploy_readiness.sql
-- =====================================================================
SET statement_timeout='120s';
SET jit=off;

\echo '### R1. 迁移水位（schema_migrations）—— ★ 字典序口径 vs 数字口径'
-- ⚠⚠⚠ 2026-10-21 实测坑（三层，一层比一层深）：
--   ① `version` 是 **text** ⇒ `max(version)` 是**字典序**最大（252 上 = 'V359'）
--   ② 直接取数字前缀也是错的（252 上 3 行非纯数字 version，其中一行数字前缀是 "2026"）
--   ③ ★★ **连「纯数字的 max」也不是水位** —— 252 上 `max(version::bigint)` = **999**
--      ⇒ 库里存在**哨兵版本号** ⇒ ⇒ **任何 max() 聚合都可能落到哨兵上**。
--   ⇒ ⇒ ★ 唯一可靠的水位口径：**按 applied_at 排序的第一条**。
SELECT version, applied_at FROM public.schema_migrations ORDER BY applied_at DESC LIMIT 6;

\echo '### R1a. ★ 真水位（按 applied_at 排序的第一条）+ 数字口径的 top5（暴露哨兵）'
SELECT version AS latest_version_by_applied_at, applied_at
  FROM public.schema_migrations ORDER BY applied_at DESC LIMIT 1;
SELECT version, applied_at FROM public.schema_migrations
 WHERE version ~ '^[0-9]+$' ORDER BY version::bigint DESC LIMIT 5;

\echo '### R1b. ★ 非纯数字的 version 行（不能被数字口径静默吞掉）'
SELECT version, applied_at FROM public.schema_migrations
 WHERE version !~ '^[0-9]+$'
 ORDER BY applied_at DESC LIMIT 10;

\echo '### R1c. ★★ 第二条链的水位：部署自动重放链（gateway_db_revision_sequences）'
-- ★★ 「迁移水位」有**两个**答案，取决于问哪条链 —— 两者可以差很远且方向相反：
--   链 A = schema_migrations        ⇒ 安装器 / 启动链（installer + startup 迁移）
--   链 B = gateway_db_revision_sequences ⇒ **部署脚本的自动重放链**
--                                       （scripts/apply-db-revision-sequence.sh）
-- ⚠ 2026-10-21 实测：211 的链 A 停在 869，而链 B 已到 880/884（2026-10-20 那轮）
--   ⇒ ★ 「211 落后 20 个版本」这句话**问错了台账**。
SELECT max((regexp_match(sequence_name, ':([0-9]+)_'))[1]::bigint) AS deploy_chain_max_version,
       count(*) AS ledger_rows,
       count(*) FILTER (WHERE sequence_name LIKE '%:890_%') AS ledger_has_890
  FROM public.gateway_db_revision_sequences;

\echo '### R2. ★ 退役台账：890（删 v1 族）在自动重放序列里的记账状态'
-- ★ 自动重放（scripts/apply-db-revision-sequence.sh）按【文件名 + 内容指纹】记账
--   ⇒ 未记账 ⇒ 每次部署都会真的执行它 ⇒ ⇒ 而 890 自带非空闸 ⇒ ⇒ 撞闸即中止
-- ⚠ 2026-10-21 实测：252 上本表【没有 content_sha256 列】⇒ 台账是旧形态
--   ⇒ ⇒ 脚本开头那几条 ALTER ADD COLUMN 没在 252 上跑过
--   ⇒ ⇒ 「内容指纹记账」在 252 上【根本没生效】⇒ 判据必须容错。
SELECT sequence_name, applied_at
  FROM public.gateway_db_revision_sequences
 WHERE sequence_name LIKE '%89%' OR sequence_name LIKE '%88%'
 ORDER BY applied_at DESC LIMIT 12;

\echo '### R2b. ★ 台账形态（列是否存在）—— 判定「内容指纹记账」是否在该实例生效'
SELECT c.relname AS ledger_table,
       a.attname AS column_name,
       format_type(a.atttypid, a.atttypmod) AS type
  FROM pg_class c
  JOIN pg_namespace n ON n.oid = c.relnamespace
  JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
 WHERE n.nspname = 'public' AND c.relname = 'gateway_db_revision_sequences'
 ORDER BY a.attnum;

\echo '### R2c. ★ 台账总行数与全部 sequence_name（判定「哪些文件被记过」）'
SELECT count(*) AS ledger_rows,
       count(DISTINCT sequence_name) AS distinct_sequences,
       min(applied_at) AS first_applied,
       max(applied_at) AS last_applied
  FROM public.gateway_db_revision_sequences;
SELECT sequence_name, applied_at
  FROM public.gateway_db_revision_sequences
 ORDER BY applied_at DESC LIMIT 15;

\echo '### R3. ★ v1 族非空闸：890 要删的那些表现在有多少行'
-- ★ 对象名按【两种物理名】都试（860 把族改名为 `_` 前缀形态）：
--   未改名的 request_logs*  ⇒ 已被 860 改名 ⇒ 这里是【改名后的】真实落点
SELECT c.relname AS relation,
       c.relkind,
       c.reltuples::bigint AS est_rows,
       pg_size_pretty(pg_total_relation_size(c.oid)) AS total_bytes
  FROM pg_class c
  JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname = 'public'
   AND c.relname IN ('_request_logs','_request_logs_hot','_request_logs_bodies',
                      '_request_logs_bodies_hot','_request_logs_archive',
                      'request_logs','request_logs_hot','request_logs_bodies',
                      'request_logs_bodies_hot','request_logs_archive')
   AND c.relkind IN ('r','p')
 ORDER BY c.relname;

\echo '### R4. ★ 种子签名命中（§143.44 选项 A 的判据在本实例的实测值）'
-- ⚠⚠ 2026-10-21 实踩：**这张表不是所有库都有** ⇒ 而 ON_ERROR_STOP=1 下
--   「表不存在」是**直接中止脚本**（不是跳过）⇒ ⇒ 后面的段落全部跑不到
--   ⇒ ⇒ 读数看起来像「脚本只跑到 R4」，其实是被打断了。
--   ⇒ 修法：用 \gexec 守卫 —— 表不存在时生成 0 行 ⇒ 一条命令都不执行 ⇒ 不中止。
--
-- ⚠⚠⚠ 第二条实测坑：R4 要在**三千多万行**的分区父表上做 count
--   ⇒ 252 上 120 s 超时（`canceling statement due to statement timeout`）
--   ⇒ ⇒ 单独给这一段放宽超时，事后恢复。
SET statement_timeout='900s';
SELECT 'SELECT ''ursm_node_snapshot_min'' AS relation, count(*) AS rows_total, count(*) FILTER (WHERE generation = 1 AND updated_at_ms = 0) AS seed_signed, count(*) FILTER (WHERE generation = 1) AS gen1, count(*) FILTER (WHERE updated_at_ms = 0) AS upd0, round(100.0 * count(*) FILTER (WHERE generation = 1 AND updated_at_ms = 0) / NULLIF(count(*),0), 2) AS pct_seed_signed FROM public.ursm_node_snapshot_min'
 WHERE to_regclass('public.ursm_node_snapshot_min') IS NOT NULL
\gexec

\echo '### R4b. ★ 种子签名命中（legacy 表；不存在时跳过，不中止）'
SELECT 'SELECT ''ursm_node_snapshot_min_legacy'' AS relation, count(*) AS rows_total, count(*) FILTER (WHERE generation = 1 AND updated_at_ms = 0) AS seed_signed, count(*) FILTER (WHERE generation = 1) AS gen1, count(*) FILTER (WHERE updated_at_ms = 0) AS upd0, round(100.0 * count(*) FILTER (WHERE generation = 1 AND updated_at_ms = 0) / NULLIF(count(*),0), 2) AS pct_seed_signed FROM public.ursm_node_snapshot_min_legacy'
 WHERE to_regclass('public.ursm_node_snapshot_min_legacy') IS NOT NULL
\gexec
SET statement_timeout='120s';

\echo '### R5. usage_facts 按日分区现状（TTL=365 是否有存量影响）'
SELECT c.relname AS partition,
       pg_get_expr(c.relpartbound, c.oid) AS bound,
       c.reltuples::bigint AS est_rows
  FROM pg_class c
  JOIN pg_inherits i ON i.inhrelid = c.oid
  JOIN pg_class p ON p.oid = i.inhparent
 WHERE p.relname = 'usage_facts'
 ORDER BY 1;
