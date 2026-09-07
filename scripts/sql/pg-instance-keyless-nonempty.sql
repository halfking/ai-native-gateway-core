\set QUIET 1
\pset tuples_only on
\pset format unaligned
CREATE TEMP TABLE pg_instance_keyless_nonempty(name text);
DO $$
DECLARE
  item record;
  has_rows boolean;
BEGIN
  FOR item IN
    SELECT n.nspname, c.relname
    FROM pg_class c
    JOIN pg_namespace n ON n.oid=c.relnamespace
    WHERE c.relkind='r'
      AND n.nspname NOT IN ('pg_catalog','information_schema','columnar_internal','citus')
      AND n.nspname NOT LIKE 'pg_toast%'
      AND n.nspname NOT LIKE 'pg_temp_%'
      AND (coalesce(current_setting('pg_instance.exclude_schema_regex', true), '') = ''
        OR n.nspname !~ current_setting('pg_instance.exclude_schema_regex', true))
      AND NOT EXISTS (
        SELECT 1 FROM pg_index i
        WHERE i.indrelid=c.oid AND i.indisunique AND i.indisvalid
          AND i.indpred IS NULL AND 0 <> ALL(i.indkey)
      )
  LOOP
    EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I.%I LIMIT 1)',
      item.nspname, item.relname) INTO has_rows;
    IF has_rows THEN
      INSERT INTO pg_instance_keyless_nonempty
      VALUES (item.nspname || '.' || item.relname);
    END IF;
  END LOOP;
END
$$;
\set QUIET 0
SELECT name FROM pg_instance_keyless_nonempty ORDER BY name;
