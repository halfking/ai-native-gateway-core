ALTER TABLE offline_activation_requests
    DROP CONSTRAINT IF EXISTS offline_activation_requests_status_check,
    DROP COLUMN IF EXISTS reject_reason,
    DROP COLUMN IF EXISTS status;
