\set QUIET 1
\pset format unaligned
\pset tuples_only on
\set QUIET 0

SELECT format(
  'SELECT %L || ''|'' || count(*)::text || ''|'' || ' ||
  'coalesce(sum(hashtextextended(to_jsonb(x)::text,0)),0)::numeric::text || ''|'' || ' ||
  'coalesce(bit_xor(hashtextextended(to_jsonb(x)::text,0)),0)::bigint::text ' ||
  'FROM %I.%I x;',
  n.nspname || '.' || c.relname, n.nspname, c.relname
)
FROM pg_class c
JOIN pg_namespace n ON n.oid=c.relnamespace
WHERE c.relkind='r'
  AND n.nspname NOT IN (
    'pg_catalog', 'information_schema', 'columnar_internal',
    'citus', 'citus_internal', 'columnar'
  )
  AND n.nspname NOT LIKE 'pg_toast%'
  AND n.nspname NOT LIKE 'pg_temp_%'
  AND (coalesce(current_setting('pg_instance.exclude_schema_regex', true), '') = ''
    OR n.nspname !~ current_setting('pg_instance.exclude_schema_regex', true))
  AND (coalesce(current_setting('pg_instance.schema_regex', true), '') = ''
    OR n.nspname ~ current_setting('pg_instance.schema_regex', true))
  AND NOT EXISTS (
    SELECT 1 FROM pg_depend d
    WHERE d.classid='pg_class'::regclass AND d.objid=c.oid AND d.deptype='e'
  )
ORDER BY n.nspname, c.relname;
\gexec
