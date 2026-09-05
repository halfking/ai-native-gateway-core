-- Rollback Migration 629: drop audit_attachments_cleanup table.
BEGIN;
DROP TABLE IF EXISTS public.audit_attachments_cleanup;
COMMIT;
