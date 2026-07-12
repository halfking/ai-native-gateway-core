-- 388_billing_cancellation_audit.sql
-- Make the audited billing boundary visible in every request log row.
-- A row is marked when usage was charged after a client cancellation.

BEGIN;

ALTER TABLE request_logs
    ADD COLUMN IF NOT EXISTS billed_despite_cancellation boolean
    NOT NULL DEFAULT false;

DO $$
BEGIN
    IF to_regclass('request_logs_hot') IS NOT NULL THEN
        ALTER TABLE request_logs_hot
            ADD COLUMN IF NOT EXISTS billed_despite_cancellation boolean
            NOT NULL DEFAULT false;
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('request_logs_hot') IS NOT NULL THEN
        UPDATE request_logs_hot
        SET billed_despite_cancellation =
            COALESCE(credits_charged, 0) > 0
            AND COALESCE(stream_interrupted, false)
            AND lower(COALESCE(failure_detail_code, error_kind, ''))
                IN ('client_cancel', 'client_disconnected')
        WHERE billed_despite_cancellation = false;
    END IF;
END $$;

CREATE OR REPLACE FUNCTION set_billed_despite_cancellation()
RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.billed_despite_cancellation :=
        COALESCE(NEW.credits_charged, 0) > 0
        AND COALESCE(NEW.stream_interrupted, false)
        AND lower(COALESCE(NEW.failure_detail_code, NEW.error_kind, ''))
            IN ('client_cancel', 'client_disconnected');
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS request_logs_billing_audit ON request_logs;
CREATE TRIGGER request_logs_billing_audit
BEFORE INSERT OR UPDATE OF credits_charged, stream_interrupted, failure_detail_code, error_kind
ON request_logs
FOR EACH ROW EXECUTE FUNCTION set_billed_despite_cancellation();

DO $$
BEGIN
    IF to_regclass('request_logs_hot') IS NOT NULL THEN
        DROP TRIGGER IF EXISTS request_logs_hot_billing_audit ON request_logs_hot;
        CREATE TRIGGER request_logs_hot_billing_audit
        BEFORE INSERT OR UPDATE OF credits_charged, stream_interrupted, failure_detail_code, error_kind
        ON request_logs_hot
        FOR EACH ROW EXECUTE FUNCTION set_billed_despite_cancellation();
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_request_logs_billed_cancel
    ON request_logs (tenant_id, ts DESC)
    WHERE billed_despite_cancellation = TRUE;

DO $$
BEGIN
    IF to_regclass('request_logs_hot') IS NOT NULL THEN
        CREATE INDEX IF NOT EXISTS idx_request_logs_hot_billed_cancel
            ON request_logs_hot (tenant_id, ts DESC)
            WHERE billed_despite_cancellation = TRUE;
    END IF;
END $$;

COMMENT ON COLUMN request_logs.billed_despite_cancellation IS
    'TRUE when client cancellation followed upstream usage and a credit charge';

COMMIT;
