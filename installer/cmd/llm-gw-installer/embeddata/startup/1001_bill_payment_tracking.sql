-- ============================================================
-- 迁移 1001: 账单付款追踪字段
-- 目的：修复 P0-2（账单无法标记已付款）
-- 日期：2026-11-26
-- 影响：maas_monthly_bills
-- ============================================================

-- 锁守卫（installer 门 startup_ddl_lock_guard_gate_test.go：版本 > 870 且含强锁 DDL
--   ⇒ 必须自带 lock_timeout）：本件含 ALTER TABLE ＋ CREATE INDEX ⇒ 声明
--   5s lock_timeout / 10min statement_timeout（同 999a/999d 口径）。
SET lock_timeout = '5s';
SET statement_timeout = '10min';

BEGIN;
-- 添加付款相关字段
ALTER TABLE maas_monthly_bills 
  ADD COLUMN IF NOT EXISTS payment_date timestamptz,
  ADD COLUMN IF NOT EXISTS payment_method text,
  ADD COLUMN IF NOT EXISTS payment_voucher text,
  ADD COLUMN IF NOT EXISTS payment_amount_cents bigint,
  ADD COLUMN IF NOT EXISTS payment_notes text,
  ADD COLUMN IF NOT EXISTS voided_at timestamptz,
  ADD COLUMN IF NOT EXISTS void_reason text,
  ADD COLUMN IF NOT EXISTS void_by text;

COMMENT ON COLUMN maas_monthly_bills.payment_date IS 
  '实际收款日期';

COMMENT ON COLUMN maas_monthly_bills.payment_method IS 
  '付款方式：wire_transfer / alipay / wechat / credit_card / other';

COMMENT ON COLUMN maas_monthly_bills.payment_voucher IS 
  '付款凭证号（银行流水号、交易号等）';

COMMENT ON COLUMN maas_monthly_bills.payment_amount_cents IS 
  '实际收款金额（分）。可能与账单金额不同（部分付款、折扣等）';

COMMENT ON COLUMN maas_monthly_bills.payment_notes IS 
  '付款备注（如：部分付款、延期付款等）';

COMMENT ON COLUMN maas_monthly_bills.voided_at IS 
  '作废时间';

COMMENT ON COLUMN maas_monthly_bills.void_reason IS 
  '作废原因：dispute / adjustment / duplicate / error / other';

COMMENT ON COLUMN maas_monthly_bills.void_by IS 
  '作废操作人（admin用户名或邮箱）';

-- 索引：快速查询已付款账单
CREATE INDEX IF NOT EXISTS idx_maas_monthly_bills_paid 
  ON maas_monthly_bills(payment_date) 
  WHERE status = 'paid';

-- 索引：快速查询作废账单
CREATE INDEX IF NOT EXISTS idx_maas_monthly_bills_voided 
  ON maas_monthly_bills(voided_at) 
  WHERE status = 'void';

-- 索引：快速查询逾期未付款账单（issued超过30天）
CREATE INDEX IF NOT EXISTS idx_maas_monthly_bills_overdue 
  ON maas_monthly_bills(period_end, status) 
  WHERE status = 'issued';

-- 视图：逾期账单监控
CREATE OR REPLACE VIEW v_overdue_bills AS
SELECT 
    b.id,
    b.tenant_id,
    b.period_ym,
    b.amount_cents,
    b.currency,
    b.issued_at,
    b.period_end,
    EXTRACT(days FROM NOW() - (b.period_end + INTERVAL '30 days')) AS overdue_days,
    CASE 
        WHEN EXTRACT(days FROM NOW() - (b.period_end + INTERVAL '30 days')) > 60 THEN 'critical'
        WHEN EXTRACT(days FROM NOW() - (b.period_end + INTERVAL '30 days')) > 30 THEN 'severe'
        ELSE 'moderate'
    END AS overdue_severity
FROM maas_monthly_bills b
WHERE status = 'issued'
  AND period_end < NOW() - INTERVAL '30 days'
ORDER BY overdue_days DESC;

COMMENT ON VIEW v_overdue_bills IS 
  '逾期账单监控：显示所有issued状态且超过付款期限（账期结束后30天）的账单';

-- 审计日志表：记录账单状态变更
CREATE TABLE IF NOT EXISTS maas_bill_status_audit (
    id bigserial PRIMARY KEY,
    bill_id bigint NOT NULL REFERENCES maas_monthly_bills(id) ON DELETE CASCADE,
    old_status text NOT NULL,
    new_status text NOT NULL,
    action text NOT NULL, -- mark_paid / void / reopen / adjust
    action_by text NOT NULL, -- 操作人
    action_reason text,
    payment_details jsonb, -- 付款详情（金额、方式、凭证等）
    created_at timestamptz DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_maas_bill_status_audit_bill 
  ON maas_bill_status_audit(bill_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_maas_bill_status_audit_time 
  ON maas_bill_status_audit(created_at DESC);

COMMENT ON TABLE maas_bill_status_audit IS 
  '账单状态变更审计日志：记录所有标记付款、作废等操作，确保可追溯';

-- 触发器：自动记录状态变更
CREATE OR REPLACE FUNCTION log_bill_status_change() 
RETURNS TRIGGER AS $$
BEGIN
    IF OLD.status IS DISTINCT FROM NEW.status THEN
        INSERT INTO maas_bill_status_audit 
        (bill_id, old_status, new_status, action, action_by, payment_details)
        VALUES (
            NEW.id,
            OLD.status,
            NEW.status,
            CASE 
                WHEN NEW.status = 'paid' THEN 'mark_paid'
                WHEN NEW.status = 'void' THEN 'void'
                WHEN NEW.status = 'issued' AND OLD.status = 'void' THEN 'reopen'
                ELSE 'adjust'
            END,
            COALESCE(NEW.void_by, current_user),
            jsonb_build_object(
                'payment_date', NEW.payment_date,
                'payment_method', NEW.payment_method,
                'payment_voucher', NEW.payment_voucher,
                'payment_amount_cents', NEW.payment_amount_cents,
                'void_reason', NEW.void_reason
            )
        );
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_log_bill_status_change ON maas_monthly_bills;

CREATE TRIGGER trg_log_bill_status_change
AFTER UPDATE OF status ON maas_monthly_bills
FOR EACH ROW
WHEN (OLD.status IS DISTINCT FROM NEW.status)
EXECUTE FUNCTION log_bill_status_change();

COMMENT ON TRIGGER trg_log_bill_status_change ON maas_monthly_bills IS 
  '触发器：账单状态变更时自动记录到审计日志';

-- 验证迁移成功
DO $$
DECLARE
    missing_columns text[];
BEGIN
    SELECT ARRAY_AGG(col) INTO missing_columns
    FROM (VALUES 
        ('payment_date'),
        ('payment_method'),
        ('payment_voucher'),
        ('payment_amount_cents'),
        ('voided_at'),
        ('void_reason')
    ) AS expected(col)
    WHERE NOT EXISTS (
        SELECT 1 FROM information_schema.columns 
        WHERE table_name = 'maas_monthly_bills' 
          AND column_name = expected.col
    );
    
    IF array_length(missing_columns, 1) > 0 THEN
        RAISE EXCEPTION 'Migration 1001 incomplete: missing columns: %', 
                        array_to_string(missing_columns, ', ');
    END IF;
    
    RAISE NOTICE 'Migration 1001 completed successfully';
END $$;

-- 生成统计报告
SELECT 
    '=== Migration 1001 Statistics ===' AS report
UNION ALL
SELECT format('Total bills: %s', COUNT(*))
FROM maas_monthly_bills
UNION ALL
SELECT format('Issued bills: %s', COUNT(*))
FROM maas_monthly_bills WHERE status = 'issued'
UNION ALL
SELECT format('Paid bills: %s', COUNT(*))
FROM maas_monthly_bills WHERE status = 'paid'
UNION ALL
SELECT format('Void bills: %s', COUNT(*))
FROM maas_monthly_bills WHERE status = 'void'
UNION ALL
SELECT format('Overdue bills: %s', COUNT(*))
FROM v_overdue_bills;

COMMIT;
