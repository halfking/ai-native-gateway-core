-- Migration 525: add the remaining scoped dimensions to session_turns.
-- PostgreSQL 14+ propagates ALTER TABLE ... ADD COLUMN from a partitioned
-- parent to every attached partition. The postcondition below verifies that
-- propagation instead of assuming it succeeded.

BEGIN;

DO $$
BEGIN
    IF current_setting('server_version_num')::integer < 140000 THEN
        RAISE EXCEPTION 'Migration 525 requires PostgreSQL 14 or newer';
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM pg_partitioned_table
        WHERE partrelid = 'public.session_turns'::regclass
    ) THEN
        RAISE EXCEPTION 'public.session_turns must be a partitioned table';
    END IF;
END $$;

ALTER TABLE public.session_turns
    ADD COLUMN IF NOT EXISTS project_id TEXT,
    ADD COLUMN IF NOT EXISTS namespace TEXT,
    ADD COLUMN IF NOT EXISTS parent_request_id TEXT,
    ADD COLUMN IF NOT EXISTS task_type TEXT;

COMMENT ON COLUMN public.session_turns.project_id IS
    'Tenant-scoped project identifier for direct turn queries.';
COMMENT ON COLUMN public.session_turns.namespace IS
    'Tenant-scoped project namespace for direct turn queries.';
COMMENT ON COLUMN public.session_turns.parent_request_id IS
    'Request identifier of the parent turn for derived or subordinate work.';
COMMENT ON COLUMN public.session_turns.task_type IS
    'Task classification copied onto the turn to avoid a sessions join.';

-- These are partitioned parent indexes. PostgreSQL creates or attaches matching
-- child indexes for existing partitions and propagates them to new partitions.
CREATE INDEX IF NOT EXISTS idx_session_turns_tenant_project
    ON public.session_turns (tenant_id, project_id, ts DESC)
    WHERE project_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_session_turns_tenant_namespace
    ON public.session_turns (tenant_id, namespace, ts DESC)
    WHERE namespace IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_session_turns_tenant_parent_request
    ON public.session_turns (tenant_id, parent_request_id, ts DESC)
    WHERE parent_request_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_session_turns_tenant_task_type
    ON public.session_turns (tenant_id, task_type, ts DESC)
    WHERE task_type IS NOT NULL;

DO $$
DECLARE
    v_column TEXT;
    v_partition REGCLASS;
    v_parent_index TEXT;
    v_indexed_column TEXT;
BEGIN
    FOREACH v_column IN ARRAY ARRAY[
        'project_id', 'namespace', 'parent_request_id', 'task_type'
    ] LOOP
        IF NOT EXISTS (
            SELECT 1
            FROM pg_attribute
            WHERE attrelid = 'public.session_turns'::regclass
              AND attname = v_column
              AND atttypid = 'text'::regtype
              AND attnum > 0
              AND NOT attisdropped
              AND NOT attnotnull
        ) THEN
            RAISE EXCEPTION 'public.session_turns.% must exist as nullable TEXT', v_column;
        END IF;

        FOR v_partition IN
            SELECT relid
            FROM pg_partition_tree('public.session_turns'::regclass)
            WHERE isleaf
        LOOP
            IF NOT EXISTS (
                SELECT 1
                FROM pg_attribute
                WHERE attrelid = v_partition
                  AND attname = v_column
                  AND atttypid = 'text'::regtype
                  AND attnum > 0
                  AND NOT attisdropped
                  AND NOT attnotnull
            ) THEN
                RAISE EXCEPTION 'PG14+ parent propagation failed: %.% is missing or incompatible',
                    v_partition, v_column;
            END IF;
        END LOOP;
    END LOOP;

    FOR v_parent_index, v_indexed_column IN
        SELECT *
        FROM (VALUES
            ('idx_session_turns_tenant_project', 'project_id'),
            ('idx_session_turns_tenant_namespace', 'namespace'),
            ('idx_session_turns_tenant_parent_request', 'parent_request_id'),
            ('idx_session_turns_tenant_task_type', 'task_type')
        ) expected(index_name, indexed_column)
    LOOP
        IF NOT EXISTS (
            SELECT 1
            FROM pg_class i
            JOIN pg_index x ON x.indexrelid = i.oid
            JOIN pg_attribute tenant_key
              ON tenant_key.attrelid = x.indrelid
             AND tenant_key.attnum = (x.indkey::smallint[])[0]
            JOIN pg_attribute scoped_key
              ON scoped_key.attrelid = x.indrelid
             AND scoped_key.attnum = (x.indkey::smallint[])[1]
            WHERE i.relnamespace = 'public'::regnamespace
              AND i.relname = v_parent_index
              AND i.relkind = 'I'
              AND x.indrelid = 'public.session_turns'::regclass
              AND x.indisvalid
              AND tenant_key.attname = 'tenant_id'
              AND scoped_key.attname = v_indexed_column
              AND pg_get_expr(x.indpred, x.indrelid) =
                  format('(%I IS NOT NULL)', v_indexed_column)
        ) THEN
            RAISE EXCEPTION 'partitioned parent index public.% is missing or malformed',
                v_parent_index;
        END IF;

        IF EXISTS (
            SELECT 1
            FROM pg_partition_tree('public.session_turns'::regclass) p
            WHERE p.isleaf
              AND NOT EXISTS (
                  SELECT 1
                  FROM pg_class parent_i
                  JOIN pg_inherits inherited_index
                    ON inherited_index.inhparent = parent_i.oid
                  JOIN pg_index child_x
                    ON child_x.indexrelid = inherited_index.inhrelid
                  WHERE parent_i.relnamespace = 'public'::regnamespace
                    AND parent_i.relname = v_parent_index
                    AND child_x.indrelid = p.relid
                    AND child_x.indisvalid
              )
        ) THEN
            RAISE EXCEPTION 'partitioned index public.% is not attached on every leaf partition',
                v_parent_index;
        END IF;
    END LOOP;
END $$;

COMMIT;
