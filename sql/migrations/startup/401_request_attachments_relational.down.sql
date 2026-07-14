-- Migration 401 rollback
DROP INDEX IF EXISTS idx_request_attachments_created_at;
DROP INDEX IF EXISTS idx_request_attachments_status_time;
DROP INDEX IF EXISTS idx_request_attachments_hash;
DROP INDEX IF EXISTS idx_request_attachments_request_id;
DROP TABLE IF EXISTS public.request_attachments;