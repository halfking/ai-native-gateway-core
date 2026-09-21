-- Migration 605: guard tool_calls partial index against JSON scalar values.
--
-- The old predicate called jsonb_array_length(tool_calls) whenever the value
-- was non-NULL. JSON null and object values are valid JSONB but are not arrays,
-- so inserts containing either value failed while PostgreSQL evaluated the
-- partial index predicate during request-log promotion.
--
-- Status: active
-- Idempotent: YES
-- Rollback: 605_fix_tool_calls_index_predicate.down.sql

\set ON_ERROR_STOP on
BEGIN;

-- Drop the partitioned index shell. PostgreSQL removes its attached child
-- indexes; the CREATE below recreates the shell and all current partitions.
DROP INDEX IF EXISTS public.idx_request_logs_provider_tool_calls;

CREATE INDEX IF NOT EXISTS idx_request_logs_provider_tool_calls
    ON ONLY public.request_logs (provider_id, ts DESC)
    WHERE tool_calls IS NOT NULL
      AND jsonb_typeof(tool_calls) = 'array'
      AND jsonb_array_length(tool_calls) > 0;

-- Rebuild the child indexes for every existing request_logs partition. The
-- index shell is intentionally created before this loop so ATTACH is valid.
DO $$
DECLARE
    partition_name text;
    child_index_name text;
BEGIN
    FOR partition_name IN
        SELECT child.relname
        FROM pg_inherits inheritance
        JOIN pg_class child ON child.oid = inheritance.inhrelid
        WHERE inheritance.inhparent = 'public.request_logs'::regclass
        ORDER BY child.relname
    LOOP
        child_index_name := partition_name || '_provider_id_ts_idx';

        EXECUTE format(
            'CREATE INDEX IF NOT EXISTS %I ON %s (provider_id, ts DESC)
             WHERE tool_calls IS NOT NULL
               AND jsonb_typeof(tool_calls) = ''array''
               AND jsonb_array_length(tool_calls) > 0',
            child_index_name,
            format('public.%I', partition_name)
        );

        IF NOT EXISTS (
            SELECT 1
            FROM pg_inherits attached
            WHERE attached.inhparent = 'public.idx_request_logs_provider_tool_calls'::regclass
              AND attached.inhrelid = child_index_name::regclass
        ) THEN
            EXECUTE format(
                'ALTER INDEX public.idx_request_logs_provider_tool_calls
                 ATTACH PARTITION public.%I',
                child_index_name
            );
        END IF;
    END LOOP;
END $$;

COMMIT;
