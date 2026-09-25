-- Down for 746: narrow the source CHECK back to hook/backfill.
-- Rows with source='claim' must be drained (replayed) before downgrading.

BEGIN;

DELETE FROM public.session_mirror_outbox WHERE source = 'claim' AND status = 'pending';

ALTER TABLE public.session_mirror_outbox
    DROP CONSTRAINT IF EXISTS session_mirror_outbox_source_check;

ALTER TABLE public.session_mirror_outbox
    ADD CONSTRAINT session_mirror_outbox_source_check
    CHECK (source IN ('hook', 'backfill'));

COMMIT;
