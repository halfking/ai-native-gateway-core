-- Migration 632 DOWN: drop the audit_attachments_filesystem_cleanup table.
-- This is destructive: any existing rows are lost. Operators should export
-- the rows to a CSV/JSONL dump before rolling back.

BEGIN;

DROP TABLE IF EXISTS public.audit_attachments_filesystem_cleanup;

COMMIT;
