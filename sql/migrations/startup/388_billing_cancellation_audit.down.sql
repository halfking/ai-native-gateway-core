BEGIN;

DROP INDEX IF EXISTS idx_request_logs_billed_cancel;
DROP INDEX IF EXISTS idx_request_logs_hot_billed_cancel;
DROP TRIGGER IF EXISTS request_logs_billing_audit ON request_logs;
DO $$
BEGIN
    IF to_regclass('request_logs_hot') IS NOT NULL THEN
        DROP TRIGGER IF EXISTS request_logs_hot_billing_audit ON request_logs_hot;
    END IF;
END $$;
DROP FUNCTION IF EXISTS set_billed_despite_cancellation();
DO $$
BEGIN
    IF to_regclass('request_logs_hot') IS NOT NULL THEN
        ALTER TABLE request_logs_hot DROP COLUMN IF EXISTS billed_despite_cancellation;
    END IF;
END $$;
ALTER TABLE request_logs DROP COLUMN IF EXISTS billed_despite_cancellation;

COMMIT;
