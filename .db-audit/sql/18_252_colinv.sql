-- ============================================================================
-- 2026-10-02-252-colinv.sql
--
-- 252 生产库的权威结构清单(SQL 直查口径,不用正则解析 pg_dump 文本)。
-- 与 .db-audit/sql/16_colinv.sql(34 侧)口径完全一致,输出可直接喂给
-- .db-audit/schemadiff/main.go -live-tsv 做结构比对。
--
-- 口径说明:
--   - 只取 public schema
--   - 列清单只含基表(relkind='r')与分区父表(relkind='p'),排除分区叶子,
--     使形状与仓库 bootstrap 基线 sql/schema/01-schema.sql 可比
--   - 类型一律用 format_type(atttypid, atttypmod),这是数据库自己的答案,
--     不做任何文本解析
--   - 纯 SELECT,不建表、不取锁、不改数据
-- ============================================================================

\pset pager off

\echo '===COLINV:BEGIN==='

SELECT c.relname || '|' || a.attname || '|' || format_type(a.atttypid, a.atttypmod)
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN pg_attribute a
  ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
WHERE n.nspname = 'public'
  AND c.relkind IN ('r', 'p')
  AND NOT EXISTS (SELECT 1 FROM pg_inherits i WHERE i.inhrelid = c.oid)
ORDER BY c.relname, a.attname;

\echo '===COLINV:PARTITIONED_PARENTS==='

SELECT c.relname || '|' || pg_get_partkeydef(c.oid)
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public' AND c.relkind = 'p'
ORDER BY 1;

\echo '===COLINV:INDEXES==='

SELECT i.relname || '|' || t.relname || '|' || pg_get_indexdef(i.oid)
FROM pg_class i
JOIN pg_index x ON x.indexrelid = i.oid
JOIN pg_class t ON t.oid = x.indrelid
JOIN pg_namespace n ON n.oid = i.relnamespace
WHERE n.nspname = 'public'
ORDER BY t.relname, i.relname;

\echo '===COLINV:END==='
