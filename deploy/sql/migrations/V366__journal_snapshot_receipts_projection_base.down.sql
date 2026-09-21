-- V366 down: remove the stable JournalSnapshot projection base.
BEGIN;

ALTER TABLE public.journal_snapshot_receipts
    DROP CONSTRAINT IF EXISTS journal_snapshot_receipts_projection_base_seq_chk;
ALTER TABLE public.journal_snapshot_receipts
    DROP COLUMN IF EXISTS projection_base_seq;

COMMIT;
