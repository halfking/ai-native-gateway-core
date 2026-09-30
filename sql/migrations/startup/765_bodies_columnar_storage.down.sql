-- 765 down: 元数据回退（死索引重建、lz4→default、ensure 函数回 heap 版）。
--
-- ⚠️ columnar 分区一旦承接数据即不可无损回转（heap 列存互转需重写全表，且
--    columnar 无 UPDATE/DELETE 路径）。本 down 不触碰分区，仅供回滚演练，
--    不要在生产执行。
--
-- 回退 request_stage_events 索引（baseline 01-schema.sql 原形状）。
CREATE INDEX IF NOT EXISTS idx_stage_events_request_id
    ON public.request_stage_events USING btree (request_id, seq);
CREATE INDEX IF NOT EXISTS idx_stage_events_tenant_ts
    ON public.request_stage_events USING btree (tenant_id, event_timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_stage_events_stage_status
    ON public.request_stage_events USING btree (stage, status, event_timestamp DESC)
    WHERE (status = ANY (ARRAY['failed'::text, 'timeout'::text]));

-- 压缩策略回默认（pglz）。'default' 回退到 default_toast_compression。
ALTER TABLE public.request_logs_bodies ALTER COLUMN request_body SET COMPRESSION default;
ALTER TABLE public.request_logs_bodies ALTER COLUMN outbound_body SET COMPRESSION default;
ALTER TABLE public.request_logs_bodies ALTER COLUMN response_body SET COMPRESSION default;
ALTER TABLE public.request_logs_bodies_hot ALTER COLUMN request_body SET COMPRESSION default;
ALTER TABLE public.request_logs_bodies_hot ALTER COLUMN outbound_body SET COMPRESSION default;
ALTER TABLE public.request_logs_bodies_hot ALTER COLUMN response_body SET COMPRESSION default;
ALTER TABLE public.session_bodies ALTER COLUMN request_delta SET COMPRESSION default;
ALTER TABLE public.session_bodies ALTER COLUMN response_delta SET COMPRESSION default;
ALTER TABLE public.session_bodies ALTER COLUMN outbound_body SET COMPRESSION default;
ALTER TABLE public.session_bodies ALTER COLUMN request_attachments SET COMPRESSION default;
ALTER TABLE public.session_bodies ALTER COLUMN response_attachments SET COMPRESSION default;
ALTER TABLE public.session_bodies_hot ALTER COLUMN request_delta SET COMPRESSION default;
ALTER TABLE public.session_bodies_hot ALTER COLUMN response_delta SET COMPRESSION default;
ALTER TABLE public.session_bodies_hot ALTER COLUMN outbound_body SET COMPRESSION default;
ALTER TABLE public.session_bodies_hot ALTER COLUMN request_attachments SET COMPRESSION default;
ALTER TABLE public.session_bodies_hot ALTER COLUMN response_attachments SET COMPRESSION default;

-- ensure 函数回退 heap 版（765 之前形状）。
CREATE OR REPLACE FUNCTION public.ensure_request_logs_bodies_partition(target_ts timestamp with time zone DEFAULT now()) RETURNS void
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
    partition_name := 'request_logs_bodies_' || to_char(month_start, 'YYYY_MM');
    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = partition_name
                     AND relnamespace = 'public'::regnamespace) THEN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF request_logs_bodies
             FOR VALUES FROM (%L) TO (%L)',
            partition_name, month_start, month_end
        );
        RAISE NOTICE 'ensure_request_logs_bodies_partition: created % as heap', partition_name;
    END IF;
END;
$$;
