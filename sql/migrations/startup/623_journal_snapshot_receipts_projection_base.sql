-- Migration 623: persist the stable sequence base for durable JournalSnapshot retries.
BEGIN;

ALTER TABLE public.journal_snapshot_receipts
    ADD COLUMN IF NOT EXISTS projection_base_seq BIGINT NOT NULL DEFAULT 0;

ALTER TABLE public.journal_snapshot_receipts
    DROP CONSTRAINT IF EXISTS journal_snapshot_receipts_projection_base_seq_chk;
ALTER TABLE public.journal_snapshot_receipts
    ADD CONSTRAINT journal_snapshot_receipts_projection_base_seq_chk
    CHECK (projection_base_seq >= 0);

COMMIT;
