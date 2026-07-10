-- 378_license_offline_status.sql
-- Persist offline activation request state and rejection reason.

ALTER TABLE offline_activation_requests
    ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'pending',
    ADD COLUMN IF NOT EXISTS reject_reason TEXT;

UPDATE offline_activation_requests
SET status = 'approved'
WHERE approved_at IS NOT NULL AND status = 'pending';

ALTER TABLE offline_activation_requests
    DROP CONSTRAINT IF EXISTS offline_activation_requests_status_check;

ALTER TABLE offline_activation_requests
    ADD CONSTRAINT offline_activation_requests_status_check
    CHECK (status IN ('pending', 'approved', 'rejected'));
