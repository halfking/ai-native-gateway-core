SELECT 'table' || E'\t' || c.relname || E'\t' ||
  CASE WHEN c.relkind='p' THEN 'partitioned' ELSE 'plain' END
FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
WHERE n.nspname='public' AND c.relkind IN ('r','p') AND NOT c.relispartition
UNION ALL
SELECT 'view' || E'\t' || c.relname || E'\t' ||
  CASE WHEN c.relkind='m' THEN 'matview' ELSE 'view' END
FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
WHERE n.nspname='public' AND c.relkind IN ('v','m')
UNION ALL
SELECT 'column' || E'\t' || table_name || E'\t' || column_name || E'\t' ||
  data_type || E'\t' || is_nullable || E'\t' || COALESCE(column_default,'')
FROM information_schema.columns WHERE table_schema='public'
UNION ALL
SELECT 'index' || E'\t' || tablename || E'\t' || indexname || E'\t' || indexdef
FROM pg_indexes WHERE schemaname='public'
UNION ALL
SELECT 'constraint' || E'\t' || conrelid::regclass::text || E'\t' || conname || E'\t' || pg_get_constraintdef(oid)
FROM pg_constraint WHERE connamespace='public'::regnamespace
UNION ALL
SELECT 'function' || E'\t' || p.proname || E'\t' || pg_get_function_identity_arguments(p.oid) || E'\t' || md5(p.prosrc)
FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
WHERE n.nspname='public'
UNION ALL
SELECT 'trigger' || E'\t' || (tgrelid::regclass)::text || E'\t' || tgname || E'\t' ||
  (SELECT proname FROM pg_proc WHERE oid = tgfoid)
FROM pg_trigger t
WHERE NOT tgisinternal AND tgrelid::regclass::oid::regnamespace = 'public'::regnamespace;
