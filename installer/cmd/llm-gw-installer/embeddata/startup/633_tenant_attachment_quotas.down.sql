-- Down for 633: drop the tenant attachment quota table.
-- ⚠ 删表即删配额累计（current_bytes/files_today 归零）——重置语义，
--   不丢业务正文（附件本体在 attachments 存储里）。
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';
DROP TABLE IF EXISTS public.tenant_attachment_quotas;
DELETE FROM public.schema_migrations WHERE version = '633';
COMMIT;
