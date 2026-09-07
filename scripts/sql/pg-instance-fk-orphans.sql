\set QUIET 1
\pset format unaligned
\pset tuples_only on

CREATE TEMP TABLE pg_instance_fk_orphans(
  constraint_name text,
  orphan_count bigint
);
DO $$
DECLARE
  fk record;
  join_sql text;
  nonnull_sql text;
  orphan_count bigint;
BEGIN
  FOR fk IN
    SELECT con.oid, con.conname, con.conrelid, con.confrelid,
      child_ns.nspname child_schema, child.relname child_table,
      parent_ns.nspname parent_schema, parent.relname parent_table
    FROM pg_constraint con
    JOIN pg_class child ON child.oid=con.conrelid
    JOIN pg_namespace child_ns ON child_ns.oid=child.relnamespace
    JOIN pg_class parent ON parent.oid=con.confrelid
    JOIN pg_namespace parent_ns ON parent_ns.oid=parent.relnamespace
    WHERE con.contype='f'
      AND child_ns.nspname NOT IN ('pg_catalog','information_schema')
  LOOP
    SELECT
      string_agg(format('c.%I IS NOT DISTINCT FROM p.%I',
        child_att.attname, parent_att.attname), ' AND ' ORDER BY key_pos.pos),
      string_agg(format('c.%I IS NOT NULL', child_att.attname),
        ' AND ' ORDER BY key_pos.pos)
    INTO join_sql, nonnull_sql
    FROM generate_subscripts(
      (SELECT conkey FROM pg_constraint WHERE oid=fk.oid), 1
    ) key_pos(pos)
    JOIN pg_constraint con ON con.oid=fk.oid
    JOIN pg_attribute child_att ON child_att.attrelid=con.conrelid
      AND child_att.attnum=con.conkey[key_pos.pos]
    JOIN pg_attribute parent_att ON parent_att.attrelid=con.confrelid
      AND parent_att.attnum=con.confkey[key_pos.pos];

    EXECUTE format(
      'SELECT count(*) FROM %I.%I c LEFT JOIN %I.%I p ON %s ' ||
      'WHERE %s AND p.tableoid IS NULL',
      fk.child_schema, fk.child_table, fk.parent_schema, fk.parent_table,
      join_sql, nonnull_sql
    ) INTO orphan_count;
    IF orphan_count > 0 THEN
      INSERT INTO pg_instance_fk_orphans
      VALUES (fk.child_schema || '.' || fk.child_table || '.' || fk.conname,
        orphan_count);
    END IF;
  END LOOP;
END
$$;
\set QUIET 0
SELECT constraint_name || '|' || orphan_count
FROM pg_instance_fk_orphans
ORDER BY constraint_name;
