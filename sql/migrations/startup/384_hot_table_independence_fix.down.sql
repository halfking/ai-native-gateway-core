DROP INDEX IF EXISTS public.idx_request_logs_hot_client_request_id;
DROP INDEX IF EXISTS public.idx_request_logs_hot_has_attachments;
ALTER TABLE public.request_logs_hot
    DROP COLUMN IF EXISTS attachments,
    DROP COLUMN IF EXISTS stream_chunks_sent,
    DROP COLUMN IF EXISTS stream_chunk_errors,
    DROP COLUMN IF EXISTS client_endpoint,
    DROP COLUMN IF EXISTS client_timeout,
    DROP COLUMN IF EXISTS upstream_status_code,
    DROP COLUMN IF EXISTS client_request_id;
