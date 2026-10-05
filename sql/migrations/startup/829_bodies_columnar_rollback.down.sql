-- ===========================================================================
-- File:          sql/migrations/startup/829_bodies_columnar_rollback.down.sql
-- Migration:     829 (down)
-- Database:      llm_gateway
-- Purpose:        把 ensure_request_logs_bodies_partition 恢复成 765 的
--                 「有 citus_columnar 就建列存」形态。
--
-- ⚠ **down 侧不把已转成 heap 的空分区改回 columnar，也不碰任何有数据的分区。**
--   理由三条，都不是省事：
--   1. 有数据的列存分区是**故意没动**的（走 TTL DROP），down 没有东西可回；
--   2. 已转 heap 的分区在本机只有 0 行那一个，为它做
--      DROP + `USING columnar` 重建是无收益的抖动，且会在有并发写入时
--      短暂丢分区存在性；
--   3. 765 的 `.down.sql` 自己就写着
--      「columnar 分区一旦承接数据即不可无损回转……**不要在生产执行**」。
--      照抄它的姿态是对的，抄它的「不转分区」也是对的。
--
-- 幂等：CREATE OR REPLACE FUNCTION，可安全重放。
-- ===========================================================================
BEGIN;

CREATE OR REPLACE FUNCTION public.ensure_request_logs_bodies_partition(
    target_ts timestamp with time zone DEFAULT now()
) RETURNS void
    LANGUAGE plpgsql
    AS $$
DECLARE
    month_start    date;
    month_end      date;
    partition_name text;
    use_columnar   boolean;
BEGIN
    SET LOCAL TIME ZONE 'Asia/Shanghai';
    month_start := date_trunc('month', target_ts)::date;
    month_end   := (date_trunc('month', target_ts) + interval '1 month')::date;
    partition_name := 'request_logs_bodies_' || to_char(month_start, 'YYYY_MM');
    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = partition_name
                     AND relnamespace = 'public'::regnamespace) THEN
        SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'citus_columnar')
            INTO use_columnar;
        IF use_columnar THEN
            EXECUTE format(
                'CREATE TABLE %I PARTITION OF request_logs_bodies
                 FOR VALUES FROM (%L) TO (%L) USING columnar',
                partition_name, month_start, month_end);
            RAISE NOTICE 'ensure_request_logs_bodies_partition: created % as columnar', partition_name;
        ELSE
            EXECUTE format(
                'CREATE TABLE %I PARTITION OF request_logs_bodies
                 FOR VALUES FROM (%L) TO (%L)',
                partition_name, month_start, month_end);
            RAISE NOTICE 'ensure_request_logs_bodies_partition: created % as heap', partition_name;
        END IF;
    END IF;
END;
$$;

COMMIT;
