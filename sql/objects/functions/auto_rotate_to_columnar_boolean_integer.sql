--
-- Name: auto_rotate_to_columnar(boolean, integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.auto_rotate_to_columnar(dry_run boolean DEFAULT false, age_days integer DEFAULT 30) RETURNS TABLE(action text, table_name text, access_method text, size_before text, size_after text, status text, message text)
    LANGUAGE plpgsql
    AS $_$
DECLARE
    partition_rec record;
    parent_name text;
    partition_pattern text;
    partition_age interval;
    col_table_name text;
    original_size bigint;
    new_size bigint;
    total_migrated int := 0;
    batch_size int := 150;
    cur_id bigint;
    max_id bigint;
    migrated_count int;
BEGIN
    partition_age := age_days || ' days';
    
    FOR partition_rec IN
        SELECT 
            parent.relname as parent_table,
            child.relname as child_table,
            am.amname as access_method,
            pg_total_relation_size(child.oid) as size_bytes,
            (SELECT pg_get_expr(c.relpartbound, c.oid) 
             FROM pg_class c WHERE c.oid = child.oid) as partition_bounds
        FROM pg_inherits i
        JOIN pg_class parent ON parent.oid = i.inhparent
        JOIN pg_class child ON child.oid = i.inhrelid
        JOIN pg_am am ON child.relam = am.oid
        WHERE parent.relname IN ('request_logs', 'credential_model_index', 'usage_ledger', 'request_wal')
          AND child.relname ~ '_2026_(0[1-9]|1[0-2])$'  -- YYYY_MM 格式
          AND am.amname = 'heap'  -- 只处理 heap 表
        ORDER BY parent.relname, child.relname
    LOOP
        
        
        IF dry_run THEN
            RETURN QUERY SELECT 
                'WOULD_MIGRATE'::text,
                partition_rec.child_table,
                partition_rec.access_method,
                pg_size_pretty(partition_rec.size_bytes),
                'N/A'::text,
                'DRY_RUN'::text,
                format('Parent: %s, Size: %s', partition_rec.parent_table, pg_size_pretty(partition_rec.size_bytes));
        ELSE
            RETURN QUERY SELECT 
                'SKIP'::text,
                partition_rec.child_table,
                partition_rec.access_method,
                pg_size_pretty(partition_rec.size_bytes),
                'N/A'::text,
                'TODO'::text,
                'Auto migration logic not yet implemented - use manual script'::text;
        END IF;
    END LOOP;
    
    RETURN;
END;
$_$;

