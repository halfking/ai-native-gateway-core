-- Migration 618 down: remove durable JournalSnapshot receipts.
BEGIN;
DROP POLICY IF EXISTS journal_snapshot_receipts_super_admin_bypass ON public.journal_snapshot_receipts;
DROP POLICY IF EXISTS journal_snapshot_receipts_tenant_isolation ON public.journal_snapshot_receipts;
DROP TABLE IF EXISTS public.journal_snapshot_receipts;
COMMIT;
