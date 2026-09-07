\set QUIET 1
\pset format unaligned
\pset tuples_only on
\set QUIET 0

WITH user_rel AS (
  SELECT c.*, n.nspname
  FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
  WHERE c.relkind IN ('r','p')
    AND n.nspname NOT IN (
      'pg_catalog','information_schema','columnar_internal',
      'citus','citus_internal','columnar'
    )
    AND n.nspname NOT LIKE 'pg_toast%'
    AND (coalesce(current_setting('pg_instance.exclude_schema_regex', true), '') = ''
      OR n.nspname !~ current_setting('pg_instance.exclude_schema_regex', true))
    AND NOT EXISTS (
      SELECT 1 FROM pg_depend d
      WHERE d.classid='pg_class'::regclass AND d.objid=c.oid AND d.deptype='e'
    )
), records AS (
  SELECT 'RELATION' kind, nspname, relname object_name, '' sub_name,
         relkind::text definition
  FROM user_rel
  UNION ALL
  SELECT 'COLUMN', c.nspname, c.relname, a.attname,
         concat_ws('|', format_type(a.atttypid,a.atttypmod), a.attnotnull,
           a.attidentity, a.attgenerated,
           coalesce(pg_get_expr(d.adbin,d.adrelid),''))
  FROM user_rel c
  JOIN pg_attribute a ON a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped
  LEFT JOIN pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum
)
SELECT kind || '|' || nspname || '|' || object_name || '|' || sub_name || '|' ||
       md5(definition)
FROM records
ORDER BY kind, nspname, object_name, sub_name;
