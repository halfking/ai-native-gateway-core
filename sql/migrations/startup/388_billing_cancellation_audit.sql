-- 388_billing_cancellation_audit.sql
-- Make the audited billing boundary visible in every request log row.
-- A row is marked when usage was charged after a client cancellation.

BEGIN;

-- ★ 2026-11-11（48h 审计）：★ 表存在性守卫——低版本投递清单（F48）会把本迁移
--   ★ 投给已过 890（v1 族退役）的存量库 ⇒ request_logs 已不存在 ⇒ 裸 ALTER 42P01
--   ★ 中止部署（245 实测）；本文件其余 request_logs 触碰点同款守卫。
--   ★ 表不存在 = 计费审计列随 v1 族一起退役 ⇒ 跳过是正确终态。
DO $do$
BEGIN
IF to_regclass('request_logs') IS NOT NULL THEN
    ALTER TABLE request_logs
        ADD COLUMN IF NOT EXISTS billed_despite_cancellation boolean
        NOT NULL DEFAULT false;
END IF;
END
$do$;

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

DO $do$
BEGIN
IF to_regclass('request_logs') IS NOT NULL THEN
    DROP TRIGGER IF EXISTS request_logs_billing_audit ON request_logs;
    CREATE TRIGGER request_logs_billing_audit
    BEFORE INSERT OR UPDATE OF credits_charged, stream_interrupted, failure_detail_code, error_kind
    ON request_logs
    FOR EACH ROW EXECUTE FUNCTION set_billed_despite_cancellation();
END IF;
END
$do$;

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

DO $do$
BEGIN
IF to_regclass('request_logs') IS NOT NULL THEN
    CREATE INDEX IF NOT EXISTS idx_request_logs_billed_cancel
        ON request_logs (tenant_id, ts DESC)
        WHERE billed_despite_cancellation = TRUE;
END IF;
END
$do$;

DO $$
BEGIN
    IF to_regclass('request_logs_hot') IS NOT NULL THEN
        CREATE INDEX IF NOT EXISTS idx_request_logs_hot_billed_cancel
            ON request_logs_hot (tenant_id, ts DESC)
            WHERE billed_despite_cancellation = TRUE;
    END IF;
END $$;

DO $do$
BEGIN
IF to_regclass('request_logs') IS NOT NULL THEN
    EXECUTE $$COMMENT ON COLUMN request_logs.billed_despite_cancellation IS
        'TRUE when client cancellation followed upstream usage and a credit charge'$$;
END IF;
END
$do$;

COMMIT;
