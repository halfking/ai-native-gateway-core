-- Migration 699 down: 恢复 ensure_supplier_errors_partition 的 pre-699 体
--
-- 与 694 down 同定式：全部使用 CREATE OR REPLACE——回滚运行时 up 侧定义
-- 已存在，裸 CREATE FUNCTION 会在事务内 42710 中断，把 ledger 行留在原地。
-- 恢复体 = V371 原始定义（无时区钉扎、DECLARE 初始化器），可读性取自
-- 权威库 pg_dump。699 的 ledger 行在此删除。

BEGIN;

CREATE OR REPLACE FUNCTION public.ensure_supplier_errors_partition(target_ts timestamp with time zone) RETURNS text
    LANGUAGE plpgsql
    AS $$
DECLARE
    month_start    date := date_trunc('month', target_ts)::date;
    month_end      date := (date_trunc('month', target_ts) + interval '1 month')::date;
    partition_name text := 'supplier_errors_' || to_char(month_start, 'YYYY_MM');
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = partition_name
                     AND relnamespace = 'public'::regnamespace) THEN
        -- 裸 USING columnar（对齐 V359 模板）：citus_columnar 11.2+ 的
        -- 压缩/stripe/chunk 参数走 columnar.* GUC（全局默认 zstd/
        -- 150000/10000），不再接受 WITH(...) reloption。
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF supplier_errors
             FOR VALUES FROM (%L) TO (%L) USING columnar',
            partition_name, month_start, month_end
        );
        RAISE NOTICE 'ensure_supplier_errors_partition: created % as columnar', partition_name;
    ELSE
        -- 幂等：确保既有分区保持 columnar（历史分区不可变语义）
        PERFORM enforce_columnar_partition(partition_name, 'supplier_errors');
    END IF;
    RETURN partition_name;
END;
$$;

DELETE FROM public.schema_migrations WHERE version = '699';

COMMIT;
