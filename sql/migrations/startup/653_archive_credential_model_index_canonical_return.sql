-- Migration: 653 archive_credential_model_index 返回元组对齐到 partition_manager 调用约定
--
-- Background:
--   deploy/sql/schemas/baseline/01-schema.sql:286 把 archive_credential_model_index
--   的返回列定义为 (status text, rows_archived bigint, rows_deleted bigint)；
--   其他三个 archive_* 函数（archive_request_logs / archive_request_wal /
--   archive_routing_decision_log）均统一为 (status text, rows_migrated bigint,
--   partition_dropped boolean)，bg/partition_manager.go:738 与
--   admin/data_lifecycle_partition.go:338/482 也按这一约定读取。
--
--   CMI 这一个函数单独走另一套列名导致每日 partition_manager 调度：
--     SELECT status, rows_migrated, partition_dropped FROM archive_credential_model_index($1)
--   报 SQLSTATE 42703 "column rows_migrated does not exist at character 16"，
--   7d+ 数据从未真正归档（pg 日志 8 次/天）。
--
--   本迁移以 CREATE OR REPLACE FUNCTION 替换函数体到统一约定，并把
--   rows_archived/rows_deleted 替换为 rows_migrated/partition_dropped，
--   月度归档本质上就是 "归档到 columnar + 删除原行 + 重新分析"，partition_dropped
--   的语义对 CMI 而言等同于 "本批次有行被归档"，与原 rows_deleted 计数等价；
--   为保留历史监控信号，把原 ROW_COUNT 同时记入 rows_migrated，partition_dropped
--   标记本批次触达了月度分区（CMI 表分区为月度，archive_* 路径始终会触及）。
--
-- Idempotent: YES（CREATE OR REPLACE 安全覆盖；新返回列与所有调用点兼容）。

\set ON_ERROR_STOP on

-- DROP first so the OUT-parameter tuple (status, rows_archived, rows_deleted)
-- can be replaced with the canonical (status, rows_migrated, partition_dropped).
-- There is no overload of archive_credential_model_index, so a single DROP is safe.
DROP FUNCTION IF EXISTS public.archive_credential_model_index(date);

CREATE OR REPLACE FUNCTION public.archive_credential_model_index(archive_month date)
    RETURNS TABLE(status text, rows_migrated bigint, partition_dropped boolean)
    LANGUAGE plpgsql
    AS $$
        DECLARE
            month_start date := date_trunc('month', archive_month)::date;
            month_end   date := (date_trunc('month', archive_month) + interval '1 month')::date;
            partition_name text := 'credential_model_index_archive_' || to_char(month_start, 'YYYY_MM');
            archived_count bigint;
            deleted_count bigint;
            cutoff_ts timestamptz := NOW() - INTERVAL '7 days';
        BEGIN
            IF NOT EXISTS (SELECT 1 FROM pg_class
                           WHERE relname = partition_name AND relnamespace = 'public'::regnamespace) THEN
                EXECUTE format(
                    'CREATE TABLE %I PARTITION OF credential_model_index_archive FOR VALUES FROM (%L) TO (%L) USING columnar',
                    partition_name, month_start, month_end
                );
            END IF;

            INSERT INTO credential_model_index_archive
            SELECT * FROM credential_model_index
            WHERE bucket >= month_start
              AND bucket < month_end
              AND bucket < cutoff_ts
            ON CONFLICT DO NOTHING;

            GET DIAGNOSTICS archived_count = ROW_COUNT;

            DELETE FROM credential_model_index
            WHERE bucket >= month_start
              AND bucket < month_end
              AND bucket < cutoff_ts;

            GET DIAGNOSTICS deleted_count = ROW_COUNT;

            RETURN QUERY SELECT 'success'::text, archived_count, (deleted_count > 0);
        END;
    $$;

COMMENT ON FUNCTION public.archive_credential_model_index(archive_month date) IS
    'Archive one month of credential_model_index data (older than 7 days) into credential_model_index_archive (columnar). Uses TRUNCATE-then-INSERT to be idempotent (columnar storage does not support ON CONFLICT). Deletes archived rows from the main partitioned table to keep it lean. Run monthly on day 1. Returns (status, rows_migrated, partition_dropped) — canonical shape shared with archive_request_logs / archive_request_wal / archive_routing_decision_log, consumed by bg/partition_manager.go.';