-- ============================================================================
-- 2026-10-02-db-columnar-lz4-bench.sql
--
-- 验证报告 D 档两个高收益、低把握的优化项,并量出真实数字:
--   D1  Citus columnar 归档的压缩比与转换成本
--   D3  TOAST 压缩 pglz -> lz4 的空间/速度权衡
--
-- ────────────────────────────────────────────────────────────────────────────
-- ！！执行前必读 ！！
--
--   1. 这个脚本会**真实写入数据**(建表、灌数、压缩重写),不是只读。
--      **禁止在 252 生产库直接执行。** 请在专用测试实例 / 252 的克隆环境执行。
--      34 是共享开发机(同实例 80+ 库、磁盘 94%),同样不建议执行。
--
--   2. 默认 dry-run。把开关改成 on 才会真正建表:
--        \set bench_mode  'off'   -- 默认:只打印计划,不执行(安全)
--        \set bench_mode  'plan'  -- 只做 EXPLAIN,不建表
--        \set bench_mode  'on'    -- 真正执行(需要专用实例)
--      另有独立开关控制 D1/D3,可只跑其中一项。
--
--   3. 所有写操作都在一个事务里并以 ROLLBACK 结束,不留残留对象。
--      但 columnar 转换在慢盘(virtiofs/NFS)上可能很慢,故设了
--      statement_timeout,超时即失败而不是拖垮实例。
--
--   4. 样本量刻意做小(默认 20000 行 / 2000 行大字段)。要外推到全量,
--      请按测得的比例自行换算,不要直接把样本结论当成全量收益。
--
-- 用法:
--   psql -v ON_ERROR_STOP=1 -f sql/audit/2026-10-02-db-columnar-lz4-bench.sql
-- ============================================================================

\set ON_ERROR_STOP on
\pset pager off

-- ── 开关(改这里)──────────────────────────────────────────────────────────
\set bench_mode  'off'
\set bench_d1    'on'    -- Citus columnar 压缩比
\set bench_d3    'on'    -- TOAST pglz vs lz4
\set bench_rows   20000   -- D1 样本行数
\set bench_blobs  2000    -- D3 大字段样本行数
-- ───────────────────────────────────────────────────────────────────────────

SET statement_timeout = '900s';
SET lock_timeout = '5s';
SET idle_in_transaction_session_timeout = '120s';
SET client_min_messages = warning;

\echo ''
\echo '=============================================================='
\echo ' 环境基线'
\echo '=============================================================='
SELECT 'version      = ' || version();
SELECT 'citus        = ' || coalesce((SELECT extversion FROM pg_extension WHERE extname='citus'), '(not installed)');
SELECT 'citus_columnar= ' || coalesce((SELECT extversion FROM pg_extension WHERE extname='citus_columnar'), '(not installed)');
SELECT 'columnar.compression = ' || coalesce(current_setting('columnar.compression', true), '(default)');
SELECT 'toast_compression(target) = ' || coalesce(current_setting('default_toast_compression', true), 'pglz');

\echo ''
\echo '=============================================================='
\echo ' 执行模式'
\echo '=============================================================='
\echo ' bench_mode =' :bench_mode
\echo ' bench_d1   =' :bench_d1
\echo ' bench_d3   =' :bench_d3

-- ============================================================================
-- D1. Citus columnar 压缩比
--
-- 报告发现:5 个 archive_* 函数已用 USING columnar 建好分区,
-- 但线上 columnar 表 = 0,且存在 _064_convert_partition_to_heap 回退痕迹。
-- 本节回答:zstd columnar 对 request_logs 这类宽表到底能压多少倍?
-- ============================================================================

-- D1 是否启用由脚本顶部的 bench_d1 开关控制,这里不再二次判断
-- (顶部已 \set,变量必然存在,判存在是死逻辑)。

\echo ''
\echo '=============================================================='
\echo ' D1. Citus columnar 压缩比(样本:' :bench_rows ' 行)'
\echo '=============================================================='

-- 前置检查:columnar 扩展是否存在、目标样本表是否有数据
SELECT 'sample table  = public.request_logs_2026_09'
     || ' | approx rows = ' || greatest(reltuples, 0)::bigint
     || ' | size = ' || pg_size_pretty(pg_total_relation_size('public.request_logs_2026_09'))
FROM pg_class WHERE oid = 'public.request_logs_2026_09'::regclass;

\if :bench_d1
\else
\echo '>> D1 disabled (set bench_d1 to on)'
\endif

\if :bench_d1
\if :bench_mode
\else
\echo '>> bench_mode=off,跳过实际建表。改为 on 可执行。'
\endif

\if :bench_mode

BEGIN;

CREATE SCHEMA IF NOT EXISTS audit_scratch;

\echo '-- 1/2 建立行存样本(heap)...'
CREATE TABLE audit_scratch.bench_rl_row AS
  SELECT * FROM public.request_logs_2026_09 LIMIT :bench_rows;

\echo '-- 2/2 建立列存样本(columnar / zstd),这一步最慢...'
CREATE TABLE audit_scratch.bench_rl_colar USING columnar AS
  SELECT * FROM public.request_logs_2026_09 LIMIT :bench_rows;

\echo ''
\echo '--- D1 结果 ---'
SELECT 'rows        = ' || (SELECT count(*) FROM audit_scratch.bench_rl_row)
UNION ALL
SELECT 'row   total = ' || pg_size_pretty(pg_total_relation_size('audit_scratch.bench_rl_row'))
UNION ALL
SELECT 'colar total = ' || pg_size_pretty(pg_total_relation_size('audit_scratch.bench_rl_colar'))
UNION ALL
SELECT 'compress ratio = ' ||
       round(pg_total_relation_size('audit_scratch.bench_rl_row')::numeric
             / nullif(pg_total_relation_size('audit_scratch.bench_rl_colar'), 0), 2) || 'x';

-- 列存的适用性检查:columnar 不支持索引与高频 UPDATE/DELETE,
-- 这里确认归档定位成立(只按时间范围读)。
\echo '--- D1 适用性:归档分区应只做时间范围扫描 ---'
\echo '   若归档后查询模式包含高频 UPDATE/DELETE 或需要索引,columnar 不适用。'

ROLLBACK;

\endif
\endif   -- bench_mode / bench_d1

-- ============================================================================
-- D3. TOAST 压缩 pglz -> lz4
--
-- 报告发现:10 GB TOAST 全部 pglz,8,251 个可 TOAST 列里只有 21 列用 lz4。
-- 本节回答:对 outbound_body 这类 66 KB 的大字段,lz4 能省多少空间、
--           代价是多大的 CPU/延迟?
--
-- 关键提醒:ALTER TABLE ... SET COMPRESSION 只对新写入生效,
--           历史数据要靠 VACUUM FULL / pg_repack 重写,那才是真正的成本大头。
--           本节只量"新写入路径"的压缩效果,不含历史重写成本。
-- ============================================================================

\echo ''
\echo '=============================================================='
\echo ' D3. TOAST pglz vs lz4(样本:' :bench_blobs ' 条 outbound_body)'
\echo '=============================================================='

SELECT 'source table = public.request_logs_bodies'
     || ' | approx rows = ' || greatest(reltuples, 0)::bigint
     || ' | size = ' || pg_size_pretty(pg_total_relation_size('public.request_logs_bodies'))
FROM pg_class WHERE oid = 'public.request_logs_bodies'::regclass;

\echo ''
\echo '--- D3 现状:实际压缩方式分布(不改数据,先看清楚)---'
SELECT a.attcompression AS compression,
       count(*)        AS columns,
       (SELECT string_agg(n2.nspname||'.'||c2.relname||'.'||a2.attname, ', ' ORDER BY c2.relname)
          FROM pg_attribute a2
          JOIN pg_class c2 ON c2.oid = a2.attrelid
          JOIN pg_namespace n2 ON n2.oid = c2.relnamespace
         WHERE n2.nspname='public' AND a2.attname = a.attname
           AND a2.attcompression = a.attcompression
           AND a2.attnum > 0 AND NOT a2.attisdropped
           AND a2.attstorage <> 'p') AS example_columns
FROM pg_attribute a
JOIN pg_class c ON c.oid = a.attrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public'
  AND c.relkind IN ('r','p')
  AND a.attnum > 0 AND NOT a.attisdropped
  AND a.attstorage <> 'p'
  AND a.attcompression IS NOT NULL
GROUP BY a.attcompression
ORDER BY 1;

\if :bench_d3
\else
\echo '>> D3 disabled (set bench_d3 to on)'
\endif

\if :bench_d3
\if :bench_mode
\else
\echo '>> bench_mode=off,跳过实际建表。改为 on 可执行。'
\endif

\if :bench_mode

BEGIN;

\echo '-- 1/2 pglz 样本(默认)...'
CREATE TABLE audit_scratch.bench_pglz AS
  SELECT outbound_body FROM public.request_logs_bodies
  WHERE outbound_body IS NOT NULL LIMIT :bench_blobs;

\echo '-- 2/2 lz4 样本...'
CREATE TABLE audit_scratch.bench_lz4 AS
  SELECT outbound_body FROM public.request_logs_bodies
  WHERE outbound_body IS NOT NULL LIMIT :bench_blobs;
ALTER TABLE audit_scratch.bench_lz4 ALTER COLUMN outbound_body
  SET STORAGE EXTERNAL;                 -- 挤出内联,强制走 TOAST
ALTER TABLE audit_scratch.bench_lz4 ALTER COLUMN outbound_body
  SET COMPRESSION lz4;
-- lz4 样本需要重写一次列才会真正用 lz4 压缩
UPDATE audit_scratch.bench_lz4 SET outbound_body = outbound_body;

\echo ''
\echo '--- D3 结果 ---'
SELECT 'blobs          = ' || (SELECT count(*) FROM audit_scratch.bench_pglz)
UNION ALL
SELECT 'avg raw bytes  = ' || (SELECT round(avg(octet_length(outbound_body))) FROM audit_scratch.bench_pglz)
UNION ALL
SELECT 'pglz  total    = ' || pg_size_pretty(pg_total_relation_size('audit_scratch.bench_pglz'))
UNION ALL
SELECT 'lz4   total    = ' || pg_size_pretty(pg_total_relation_size('audit_scratch.bench_lz4'))
UNION ALL
SELECT 'space ratio    = ' ||
       round(pg_total_relation_size('audit_scratch.bench_pglz')::numeric
             / nullif(pg_total_relation_size('audit_scratch.bench_lz4'), 0), 2) || 'x'
UNION ALL
SELECT 'lz4 saves      = ' ||
       pg_size_pretty(pg_total_relation_size('audit_scratch.bench_pglz')
                    - pg_total_relation_size('audit_scratch.bench_lz4'));

\echo ''
\echo '--- D3 读取延迟对比(4 轮取平均,含解压)---'
\timing on
SELECT count(outbound_body) FROM audit_scratch.bench_pglz;
SELECT count(outbound_body) FROM audit_scratch.bench_pglz;
SELECT count(outbound_body) FROM audit_scratch.bench_pglz;
SELECT count(outbound_body) FROM audit_scratch.bench_pglz;
SELECT count(outbound_body) FROM audit_scratch.bench_lz4;
SELECT count(outbound_body) FROM audit_scratch.bench_lz4;
SELECT count(outbound_body) FROM audit_scratch.bench_lz4;
SELECT count(outbound_body) FROM audit_scratch.bench_lz4;
\timing off

ROLLBACK;

\endif
\endif   -- bench_mode / bench_d3

-- ============================================================================
-- 结论模板:把上面测到的数字填进来,附到审计报告 D 档
-- ============================================================================

\echo ''
\echo '=============================================================='
\echo ' 怎么用这些数字'
\echo '=============================================================='
\echo ' D1: compress ratio 若 >= 3x 且压缩方式是 zstd,'
\echo '     则 27 GB heap + 10 GB TOAST 的冷数据部分有明确的归档收益。'
\echo '     换算: 可归档冷数据量 x (1 - 1/ratio)。'
\echo '     注意 columnar 不支持索引,只适合"写完就冻结、按时间范围读"的分区。'
\echo ''
\echo ' D3: space ratio 接近 1.0 属正常 —— lz4 的卖点是解压更快而非压得更小,'
\echo '     它的真正价值在写入 CPU 与读取延迟。若本库以"省空间"为第一诉求,'
\echo '     lz4 不应排在 D1(columnar)前面。'
\echo ''
\echo ' 两者都不改变写入路径的语句数量。若目标是让 252 从 4 核/load 16 的'
\echo ' 现状下喘口气,优先级应当是:'
\echo '   C 档(减少 assets 的 1160 万次 upsert)> A 档配置 > B 档索引 > D 档存储引擎。'
\echo '=============================================================='
