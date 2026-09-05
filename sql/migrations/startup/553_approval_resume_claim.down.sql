-- Roll back only after the binary that reads/writes these columns is retired.
-- Durable lifecycle evidence is intentionally preserved unless an operator
-- explicitly archives or resets it before this destructive rollback.

BEGIN;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'approval_queue'
          AND column_name = 'resume_state'
    ) AND EXISTS (
        SELECT 1
        FROM public.approval_queue
        WHERE resume_state <> 'idle'
           OR resume_fencing_token <> 0
           OR resume_owner IS NOT NULL
           OR resume_lease_until IS NOT NULL
           OR resume_started_at IS NOT NULL
           OR resume_completed_at IS NOT NULL
           OR resume_error IS NOT NULL
    ) THEN
        RAISE EXCEPTION
            '549 rollback refused: approval resume lifecycle data exists; retire new code and archive or reset state deliberately';
    END IF;
END
$$;

DROP INDEX IF EXISTS public.idx_approval_queue_resume_claimable;
ALTER TABLE public.approval_queue DROP CONSTRAINT IF EXISTS approval_queue_resume_fencing_token_chk;
ALTER TABLE public.approval_queue DROP CONSTRAINT IF EXISTS approval_queue_resume_state_chk;
ALTER TABLE public.approval_queue
    DROP COLUMN IF EXISTS resume_error,
    DROP COLUMN IF EXISTS resume_completed_at,
    DROP COLUMN IF EXISTS resume_started_at,
    DROP COLUMN IF EXISTS resume_fencing_token,
    DROP COLUMN IF EXISTS resume_lease_until,
    DROP COLUMN IF EXISTS resume_owner,
    DROP COLUMN IF EXISTS resume_state;

COMMIT;
