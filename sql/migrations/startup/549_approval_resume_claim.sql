-- 549_approval_resume_claim.sql
--
-- Keep the human approval decision separate from the execution that resumes
-- the request. An approved row can be claimed by one resumer at a time. A
-- lease allows recovery after a process dies; the fencing token prevents a
-- stale owner from changing the durable resume state after takeover.

BEGIN;

ALTER TABLE approval_queue
    ADD COLUMN IF NOT EXISTS resume_state TEXT NOT NULL DEFAULT 'idle',
    ADD COLUMN IF NOT EXISTS resume_owner TEXT,
    ADD COLUMN IF NOT EXISTS resume_lease_until TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS resume_fencing_token BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS resume_started_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS resume_completed_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS resume_error TEXT;

ALTER TABLE approval_queue
    DROP CONSTRAINT IF EXISTS approval_queue_resume_state_chk;

ALTER TABLE approval_queue
    ADD CONSTRAINT approval_queue_resume_state_chk CHECK (
        resume_state IN ('idle', 'running', 'completed', 'failed')
    );

ALTER TABLE approval_queue
    DROP CONSTRAINT IF EXISTS approval_queue_resume_fencing_token_chk;

ALTER TABLE approval_queue
    ADD CONSTRAINT approval_queue_resume_fencing_token_chk CHECK (
        resume_fencing_token >= 0
    );

CREATE INDEX IF NOT EXISTS idx_approval_queue_resume_claimable
    ON approval_queue (resume_lease_until, created_at)
    WHERE status = 'approved' AND resume_state IN ('idle', 'running', 'failed');

COMMIT;
