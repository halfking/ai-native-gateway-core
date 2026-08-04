--
-- Name: archive_request_wal(date); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.archive_request_wal(archive_month date) RETURNS TABLE(status text, rows_migrated bigint, partition_dropped boolean)
    LANGUAGE plpgsql
    AS $$
DECLARE
    month_start date := date_trunc('month', archive_month)::date;
    month_end   date := (date_trunc('month', archive_month) + interval '1 month')::date;
    src_part    text := 'request_wal_' || to_char(month_start, 'YYYY_MM');
    dst_part    text := 'request_wal_archive_' || to_char(month_start, 'YYYY_MM');
    row_count   bigint := 0;
    chunk_count bigint := 0;
    col_list    text;
    last_ts     timestamptz;
    batch_rows  bigint;
    CHUNK_SIZE  constant int := 1000;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = src_part
                     AND relnamespace = 'public'::regnamespace) THEN
        RETURN QUERY SELECT 'skipped'::text, 0::bigint, false;
        RETURN;
    END IF;

    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = dst_part
                     AND relnamespace = 'public'::regnamespace) THEN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF request_wal_archive
             FOR VALUES FROM (%L) TO (%L) USING columnar',
            dst_part, month_start, month_end
        );
    END IF;

    SELECT string_agg(a.column_name, ', ' ORDER BY a.ordinal_position)
    INTO col_list
    FROM information_schema.columns a
    JOIN information_schema.columns r
      ON a.table_schema = r.table_schema
     AND a.column_name  = r.column_name
    WHERE a.table_name = 'request_wal_archive'
      AND r.table_name = src_part
      AND a.table_schema = 'public'
      AND a.ordinal_position > 0;

    IF col_list IS NULL OR length(col_list) = 0 THEN
        RAISE EXCEPTION 'No common columns between % and request_wal_archive', src_part;
    END IF;

    last_ts := '-infinity'::timestamptz;
    LOOP
        EXECUTE format(
            'INSERT INTO %I (%s) SELECT %s FROM %I
             WHERE created_at > %L
             ORDER BY created_at
             LIMIT  %L',
            dst_part, col_list, col_list, src_part, last_ts, CHUNK_SIZE
        );
        GET DIAGNOSTICS batch_rows = ROW_COUNT;
        EXIT WHEN batch_rows = 0;

        row_count   := row_count + batch_rows;
        chunk_count := chunk_count + 1;

        EXECUTE format(
            'SELECT MAX(created_at) FROM (SELECT created_at FROM %I WHERE created_at > %L ORDER BY created_at LIMIT %L) s',
            src_part, last_ts, CHUNK_SIZE
        ) INTO last_ts;
    END LOOP;

    RAISE NOTICE 'Migrated % rows from % to % in % chunks (chunk size %)',
        row_count, src_part, dst_part, chunk_count, CHUNK_SIZE;

    EXECUTE format('ALTER TABLE request_wal DETACH PARTITION %I', src_part);
    EXECUTE format('DROP TABLE %I', src_part);

    RETURN QUERY SELECT 'success'::text, row_count, true;
END;
$$;

