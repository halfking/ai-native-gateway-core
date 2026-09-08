\set QUIET 1
\pset tuples_only on
\pset format unaligned
\set QUIET 0

SELECT format(
  'SELECT setval(%L, GREATEST((SELECT last_value FROM %s), ' ||
  'coalesce((SELECT max(%I) FROM %I.%I), 0)), true);',
  seq.oid::regclass::text, seq.oid::regclass::text,
  col.attname, ns.nspname, tbl.relname
)
FROM pg_class seq
JOIN pg_depend dep ON dep.objid=seq.oid
  AND dep.classid='pg_class'::regclass
  AND dep.refclassid='pg_class'::regclass
  AND dep.deptype IN ('a','i')
JOIN pg_class tbl ON tbl.oid=dep.refobjid
JOIN pg_namespace ns ON ns.oid=tbl.relnamespace
JOIN pg_attribute col ON col.attrelid=tbl.oid AND col.attnum=dep.refobjsubid
WHERE seq.relkind='S'
  AND ns.nspname NOT IN ('pg_catalog','information_schema','columnar_internal','citus')
ORDER BY ns.nspname, tbl.relname, col.attname;
\gexec
