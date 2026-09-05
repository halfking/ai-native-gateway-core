-- Migration: 654 archive_credential_model_index 切到 DETACH+DROP 路径
--
-- Background:
--   2026-09-04 容器 PG 日志出现 SQLSTATE XX000
--   "ERROR: UPDATE and CTID scans not supported for ColumnarScan"，
--   触发位置：archive_credential_model_index('2026-07-03') 的
--   "DELETE FROM credential_model_index WHERE bucket >= month_start
--    AND bucket < month_end AND bucket < cutoff_ts"。
--
--   原因：credential_model_index 的月度分区 2026_07 / 2026_08 是 columnar
--   （Citus 列存），而 columnar 表不支持 UPDATE / DELETE（依赖 CTID 定位），
--   即使父表是 heap，规划器在分区裁剪到 columnar 子分区时也会触发限制。
--
--   修正口径：与 archive_request_logs（baseline/01-schema.sql）/ archive_request_wal /
--   archive_routing_decision_log 保持一致，使用 DETACH PARTITION + DROP TABLE
--   取代 DELETE（partition_dropped 语义本就如此）。本函数：
--     1. 确保目标 columnar 分区存在（IF NOT EXISTS）；
--     2. INSERT ... SELECT 从源分区（不论是 heap 还是 columnar）拷到目标
--        columnar（COPY 安全，columnar 接受 INSERT）；
--     3. ALTER TABLE ... DETACH PARTITION 把源分区解绑；
--     4. DROP TABLE 物理删除源分区（堆与列存皆可）。
--   若目标源分区不存在，partition_manager 期望返回 'skipped'（与
--   archive_request_logs 同语义，bg/partition_manager.go:750 case "skipped"）。
--
-- Idempotent: YES（CREATE OR REPLACE 安全覆盖）。

\set ON_ERROR_STOP on

DROP FUNCTION IF EXISTS public.archive_credential_model_index(date);

CREATE OR REPLACE FUNCTION public.archive_credential_model_index(archive_month date)
    RETURNS TABLE(status text, rows_migrated bigint, partition_dropped boolean)
    LANGUAGE plpgsql
    AS $$
        DECLARE
            month_start date := date_trunc('month', archive_month)::date;
            month_end   date := (date_trunc('month', archive_month) + interval '1 month')::date;
            src_part    text := 'credential_model_index_' || to_char(month_start, 'YYYY_MM');
            dst_part    text := 'credential_model_index_archive_' || to_char(month_start, 'YYYY_MM');
            row_count   bigint;
            source_count bigint;
            target_count bigint;
            col_list    text;
            rows_match  boolean;
            target_exists boolean;
        BEGIN
            -- Serialize retries for one month so two schedulers cannot both copy
            -- the same source partition before either one detaches it.
            PERFORM pg_advisory_xact_lock(hashtext('archive_credential_model_index:' || to_char(month_start, 'YYYY-MM')));

            IF NOT EXISTS (SELECT 1 FROM pg_class
                           WHERE relname = src_part AND relnamespace = 'public'::regnamespace) THEN
                RETURN QUERY SELECT 'skipped'::text, 0::bigint, false;
                RETURN;
            END IF;

            IF NOT EXISTS (SELECT 1 FROM pg_class
                           WHERE relname = dst_part AND relnamespace = 'public'::regnamespace) THEN
                EXECUTE format(
                    'CREATE TABLE %I PARTITION OF credential_model_index_archive FOR VALUES FROM (%L) TO (%L) USING columnar',
                    dst_part, month_start, month_end
                );
                target_exists := false;
            ELSE
                target_exists := true;
            END IF;

            -- Build a column list from the intersection of source partition and
            -- archive parent so the COPY works regardless of which side has the
            -- wider schema (columnar archive may carry extra aggregation columns).
            SELECT string_agg(a.column_name, ', ' ORDER BY a.ordinal_position)
            INTO col_list
            FROM information_schema.columns a
            JOIN information_schema.columns r
              ON a.table_schema = r.table_schema
             AND a.column_name  = r.column_name
            WHERE a.table_name = 'credential_model_index_archive'
              AND r.table_name = src_part
              AND a.table_schema = 'public'
              AND a.ordinal_position > 0;

            IF col_list IS NULL OR length(col_list) = 0 THEN
                RAISE EXCEPTION 'No common columns between % and credential_model_index_archive', src_part;
            END IF;

            IF target_exists THEN
                EXECUTE format('SELECT count(*) FROM %I', src_part) INTO source_count;
                EXECUTE format('SELECT count(*) FROM %I', dst_part) INTO target_count;
                IF target_count = 0 THEN
                    EXECUTE format(
                        'INSERT INTO %I (%s) SELECT %s FROM %I',
                        dst_part, col_list, col_list, src_part
                    );
                    GET DIAGNOSTICS row_count = ROW_COUNT;
                ELSE
                    EXECUTE format(
                        'SELECT NOT EXISTS ((SELECT %s FROM %I EXCEPT ALL SELECT %s FROM %I) UNION ALL (SELECT %s FROM %I EXCEPT ALL SELECT %s FROM %I))',
                        col_list, src_part, col_list, dst_part,
                        col_list, dst_part, col_list, src_part
                    ) INTO rows_match;
                    IF NOT rows_match THEN
                        RAISE EXCEPTION 'archive target % contains partial or mismatched rows (source %, target %)',
                            dst_part, source_count, target_count;
                    END IF;
                    row_count := 0;
                END IF;
            ELSE
                EXECUTE format(
                    'INSERT INTO %I (%s) SELECT %s FROM %I',
                    dst_part, col_list, col_list, src_part
                );
                GET DIAGNOSTICS row_count = ROW_COUNT;
            END IF;

            EXECUTE format('ALTER TABLE credential_model_index DETACH PARTITION %I', src_part);
            EXECUTE format('DROP TABLE %I', src_part);

            RETURN QUERY SELECT 'success'::text, row_count, true;
        END;
    $$;

COMMENT ON FUNCTION public.archive_credential_model_index(archive_month date) IS
    'Archive one month of credential_model_index data into credential_model_index_archive (columnar). Detaches and drops the source partition so the operation works for both heap and columnar monthly partitions (columnar tables reject DELETE/CTID scans — see migration 654). Returns (status, rows_migrated, partition_dropped); partition_manager calls daily and logs status.';
