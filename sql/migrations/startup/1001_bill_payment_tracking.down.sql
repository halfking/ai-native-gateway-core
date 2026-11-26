-- ============================================================
-- 迁移 1001 回滚: 删除账单付款追踪字段
-- 日期：2026-11-26
-- 警告：此操作会删除所有付款记录
-- ============================================================

-- 删除触发器
DROP TRIGGER IF EXISTS trg_log_bill_status_change ON maas_monthly_bills;
DROP FUNCTION IF EXISTS log_bill_status_change();

-- 删除审计日志表
DROP TABLE IF EXISTS maas_bill_status_audit;

-- 删除视图
DROP VIEW IF EXISTS v_overdue_bills;

-- 删除索引
DROP INDEX IF EXISTS idx_maas_monthly_bills_paid;
DROP INDEX IF EXISTS idx_maas_monthly_bills_voided;
DROP INDEX IF EXISTS idx_maas_monthly_bills_overdue;

-- 删除字段
ALTER TABLE maas_monthly_bills 
  DROP COLUMN IF EXISTS payment_date,
  DROP COLUMN IF EXISTS payment_method,
  DROP COLUMN IF EXISTS payment_voucher,
  DROP COLUMN IF EXISTS payment_amount_cents,
  DROP COLUMN IF EXISTS payment_notes,
  DROP COLUMN IF EXISTS voided_at,
  DROP COLUMN IF EXISTS void_reason,
  DROP COLUMN IF EXISTS void_by;

-- 验证回滚完成
DO $$
BEGIN
    RAISE NOTICE 'Migration 1001 rollback completed';
END $$;
