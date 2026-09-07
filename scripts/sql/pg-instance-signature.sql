\set QUIET 1
\pset format unaligned
\pset tuples_only on
\set QUIET 0

WITH user_ns AS (
  SELECT oid, nspname, nspowner, nspacl
  FROM pg_namespace
  WHERE nspname NOT IN ('pg_catalog', 'information_schema', 'columnar_internal', 'citus')
    AND nspname NOT IN ('citus_internal', 'columnar')
    AND nspname NOT LIKE 'pg_toast%'
    AND nspname NOT LIKE 'pg_temp_%'
    AND (coalesce(current_setting('pg_instance.schema_regex', true), '') = ''
      OR nspname ~ current_setting('pg_instance.schema_regex', true))
    AND (coalesce(current_setting('pg_instance.exclude_schema_regex', true), '') = ''
      OR nspname !~ current_setting('pg_instance.exclude_schema_regex', true))
), user_rel AS (
  SELECT c.*
  FROM pg_class c JOIN user_ns n ON n.oid=c.relnamespace
  WHERE NOT EXISTS (
    SELECT 1 FROM pg_depend d
    WHERE d.classid='pg_class'::regclass AND d.objid=c.oid AND d.deptype='e'
  )
), records AS (
    SELECT 'SCHEMA' kind, n.nspname schema_name, n.nspname object_name, '' sub_name,
           '' definition
  FROM user_ns n
    UNION ALL
    SELECT 'OWNER', n.nspname, n.nspname, 'SCHEMA',
           pg_get_userbyid(n.nspowner)
    FROM user_ns n
  UNION ALL
  SELECT 'EXTENSION', n.nspname, e.extname, '',
         e.extversion
  FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace
  UNION ALL
  SELECT 'TYPE', n.nspname, t.typname, '',
           concat_ws('|', t.typtype, t.typcategory, t.typnotnull,
           coalesce((SELECT string_agg(e.enumlabel, ',' ORDER BY e.enumsortorder)
                     FROM pg_enum e WHERE e.enumtypid=t.oid), ''))
  FROM pg_type t JOIN user_ns n ON n.oid=t.typnamespace
  WHERE t.typtype IN ('d','e','r','m') AND t.typrelid=0
    UNION ALL
    SELECT 'OWNER', n.nspname, t.typname, 'TYPE',
           pg_get_userbyid(t.typowner)
    FROM pg_type t JOIN user_ns n ON n.oid=t.typnamespace
    WHERE t.typtype IN ('d','e','r','m') AND t.typrelid=0
  UNION ALL
  SELECT 'RELATION', n.nspname, c.relname, '',
           concat_ws('|', c.relkind, c.relpersistence,
           coalesce(am.amname, ''), c.relrowsecurity, c.relforcerowsecurity,
           coalesce(pg_get_partkeydef(c.oid), ''), coalesce(pg_get_expr(c.relpartbound,c.oid),''))
  FROM user_rel c JOIN user_ns n ON n.oid=c.relnamespace
  LEFT JOIN pg_am am ON am.oid=c.relam
  WHERE c.relkind IN ('r','p','v','m','S','f')
    UNION ALL
    SELECT 'OWNER', n.nspname, c.relname, 'RELATION',
           pg_get_userbyid(c.relowner)
    FROM user_rel c JOIN user_ns n ON n.oid=c.relnamespace
    WHERE c.relkind IN ('r','p','v','m','S','f')
  UNION ALL
  SELECT 'COLUMN', n.nspname, c.relname, a.attname,
         concat_ws('|', format_type(a.atttypid,a.atttypmod), a.attnotnull,
           a.attidentity, a.attgenerated, coalesce(coll.collname,''),
           coalesce(pg_get_expr(d.adbin,d.adrelid),''))
  FROM user_rel c JOIN user_ns n ON n.oid=c.relnamespace
  JOIN pg_attribute a ON a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped
  LEFT JOIN pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum
  LEFT JOIN pg_collation coll ON coll.oid=a.attcollation
  WHERE c.relkind IN ('r','p','v','m','f')
  UNION ALL
  SELECT 'CONSTRAINT', n.nspname, c.relname, con.conname,
         con.contype::text || '|' || pg_get_constraintdef(con.oid,true)
  FROM pg_constraint con JOIN user_rel c ON c.oid=con.conrelid
  JOIN user_ns n ON n.oid=c.relnamespace
  UNION ALL
  SELECT 'INDEX', n.nspname, tbl.relname, idx.relname,
         pg_get_indexdef(idx.oid)
  FROM pg_index i JOIN user_rel tbl ON tbl.oid=i.indrelid
  JOIN pg_class idx ON idx.oid=i.indexrelid
  JOIN user_ns n ON n.oid=tbl.relnamespace
  UNION ALL
  SELECT CASE c.relkind WHEN 'm' THEN 'MATVIEW' ELSE 'VIEW' END,
         n.nspname, c.relname, '', md5(pg_get_viewdef(c.oid,true))
  FROM user_rel c JOIN user_ns n ON n.oid=c.relnamespace
  WHERE c.relkind IN ('v','m')
  UNION ALL
  SELECT 'FUNCTION', n.nspname, p.proname,
         pg_get_function_identity_arguments(p.oid),
         md5(pg_get_functiondef(p.oid))
  FROM pg_proc p JOIN user_ns n ON n.oid=p.pronamespace
  WHERE p.prokind IN ('f','p')
    AND NOT EXISTS (
      SELECT 1 FROM pg_depend d
      WHERE d.classid='pg_proc'::regclass AND d.objid=p.oid AND d.deptype='e'
    )
    UNION ALL
    SELECT 'OWNER', n.nspname, p.proname,
           'FUNCTION(' || pg_get_function_identity_arguments(p.oid) || ')',
           pg_get_userbyid(p.proowner)
    FROM pg_proc p JOIN user_ns n ON n.oid=p.pronamespace
    WHERE p.prokind IN ('f','p')
      AND NOT EXISTS (
        SELECT 1 FROM pg_depend d
        WHERE d.classid='pg_proc'::regclass AND d.objid=p.oid AND d.deptype='e'
      )
  UNION ALL
  SELECT 'TRIGGER', n.nspname, c.relname, t.tgname,
         md5(pg_get_triggerdef(t.oid,true))
  FROM pg_trigger t JOIN user_rel c ON c.oid=t.tgrelid
  JOIN user_ns n ON n.oid=c.relnamespace
  WHERE NOT t.tgisinternal
  UNION ALL
  SELECT 'POLICY', n.nspname, c.relname, p.polname,
         concat_ws('|', p.polcmd, p.polpermissive,
           coalesce(pg_get_expr(p.polqual,p.polrelid),''),
           coalesce(pg_get_expr(p.polwithcheck,p.polrelid),''))
  FROM pg_policy p JOIN user_rel c ON c.oid=p.polrelid
  JOIN user_ns n ON n.oid=c.relnamespace
  UNION ALL
  SELECT 'SEQUENCE', n.nspname, c.relname, '',
         concat_ws('|', s.seqtypid::regtype, s.seqstart, s.seqincrement,
           s.seqmax, s.seqmin, s.seqcache, s.seqcycle)
  FROM pg_sequence s JOIN user_rel c ON c.oid=s.seqrelid
  JOIN user_ns n ON n.oid=c.relnamespace
  UNION ALL
  SELECT 'GRANT', n.nspname, c.relname, x.grantee::regrole::text,
         concat_ws('|', x.privilege_type, x.is_grantable)
  FROM user_rel c JOIN user_ns n ON n.oid=c.relnamespace
  CROSS JOIN LATERAL aclexplode(c.relacl) x
  WHERE c.relacl IS NOT NULL
)
SELECT kind || '|' || schema_name || '|' || object_name || '|' || sub_name || '|' ||
       md5(definition)
FROM records
ORDER BY kind, schema_name, object_name, sub_name, md5(definition);
