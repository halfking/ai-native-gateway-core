-- Roll back the approval resume claim state only with code that does not use
-- owner/token guarded resume writes.

BEGIN;

DROP INDEX IF EXISTS idx_approval_queue_resume_claimable;
ALTER TABLE approval_queue DROP CONSTRAINT IF EXISTS approval_queue_resume_fencing_token_chk;
ALTER TABLE approval_queue DROP CONSTRAINT IF EXISTS approval_queue_resume_state_chk;
ALTER TABLE approval_queue
    DROP COLUMN IF EXISTS resume_error,
    DROP COLUMN IF EXISTS resume_completed_at,
    DROP COLUMN IF EXISTS resume_started_at,
    DROP COLUMN IF EXISTS resume_fencing_token,
    DROP COLUMN IF EXISTS resume_lease_until,
    DROP COLUMN IF EXISTS resume_owner,
    DROP COLUMN IF EXISTS resume_state;

COMMIT;
