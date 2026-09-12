-- Migration 699: 固定 ensure_supplier_errors_partition 的会话时区
--
-- 694 钉扎了 13 个分区 ensure 函数，但 supplier_errors 的 ensure 不在其
-- 清单内（V371 定义于 deploy 轨）。它对 timestamptz 入参做
-- date_trunc('month', target_ts)：UTC 会话在每月 1 日 00:00–08:00+08
-- 窗口会把当月边界算到上个月，预建出偏移 8 小时的分区（473 缝隙同族），
-- 且其 ELSE 分支的 enforce_columnar_partition 幂等路径同样按偏移分区名
-- 工作而静默失效。本迁移按 694 定式收敛：SET LOCAL 为函数体第一条语句，
-- DECLARE 初始化器（先于 BEGIN 求值，SET LOCAL 管不到）改为体内赋值。
--
-- 存储语义不变：保持 V359 裸 USING columnar 模板 + enforce 兜底
-- （supplier_errors 是只读分层存储，562 的 heap 决策不适用）。
--
-- Idempotent: CREATE OR REPLACE FUNCTION + ledger upsert。

BEGIN;

CREATE OR REPLACE FUNCTION public.ensure_supplier_errors_partition(target_ts timestamp with time zone) RETURNS text
    LANGUAGE plpgsql
    AS $$
DECLARE
    month_start    date;
    month_end      date;
    partition_name text;
BEGIN
    SET LOCAL TIME ZONE 'Asia/Shanghai';
    month_start := date_trunc('month', target_ts)::date;
    month_end := (date_trunc('month', target_ts) + interval '1 month')::date;
    partition_name := 'supplier_errors_' || to_char(month_start, 'YYYY_MM');

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

INSERT INTO public.schema_migrations (version, description)
VALUES ('699', 'Pin ensure_supplier_errors_partition to Asia/Shanghai session timezone')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
