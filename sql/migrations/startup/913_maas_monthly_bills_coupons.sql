-- Migration 913: MaaS monthly bills + coupons (+ invoice placeholder).
-- Idempotent: IF NOT EXISTS / ON CONFLICT DO NOTHING.
-- Does NOT alter credit_ledger entry_type CHECK (coupon credits use entry_type='adjust', ref_type='coupon').

-- 锁守卫（startup_ddl_lock_guard_gate_test.go）：本文件含 CREATE INDEX（非 CONCURRENTLY，
-- 拿 ShareLock）与 ALTER TABLE ... ENABLE ROW LEVEL SECURITY ⇒ 必须自带 lock_timeout，
-- 避免等锁期间堵住全库（参见 §44）。幂等 DDL 本身在锁等待前就会跳过已存在对象。
SET LOCAL lock_timeout = '2s';
SET LOCAL statement_timeout = '30s';

CREATE TABLE IF NOT EXISTS maas_monthly_bills (
    id              BIGSERIAL PRIMARY KEY,
    tenant_id       VARCHAR(64) NOT NULL REFERENCES tenants(code) ON DELETE CASCADE,
    period_ym       CHAR(7) NOT NULL,
    period_start    TIMESTAMPTZ NOT NULL,
    period_end      TIMESTAMPTZ NOT NULL,
    total_credits   BIGINT NOT NULL DEFAULT 0,
    total_requests  BIGINT NOT NULL DEFAULT 0,
    amount_cents    BIGINT NOT NULL DEFAULT 0,
    currency        VARCHAR(8) NOT NULL DEFAULT 'CNY',
    status          VARCHAR(32) NOT NULL DEFAULT 'issued'
        CHECK (status IN ('open', 'issued', 'paid', 'void')),
    issued_at       TIMESTAMPTZ,
    note            TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT maas_monthly_bills_tenant_period_uq UNIQUE (tenant_id, period_ym),
    CONSTRAINT maas_monthly_bills_period_ym_fmt CHECK (period_ym ~ '^[0-9]{4}-[0-9]{2}$')
);

CREATE INDEX IF NOT EXISTS idx_maas_monthly_bills_tenant_issued
    ON maas_monthly_bills (tenant_id, period_ym DESC);

CREATE TABLE IF NOT EXISTS maas_coupons (
    id                  BIGSERIAL PRIMARY KEY,
    code                VARCHAR(64) NOT NULL,
    credits_amount      BIGINT NOT NULL CHECK (credits_amount > 0),
    max_redemptions     INT NOT NULL DEFAULT 1 CHECK (max_redemptions > 0),
    redemption_count    INT NOT NULL DEFAULT 0 CHECK (redemption_count >= 0),
    expires_at          TIMESTAMPTZ,
    status              VARCHAR(32) NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'exhausted', 'disabled', 'expired')),
    note                TEXT NOT NULL DEFAULT '',
    created_by          VARCHAR(128) NOT NULL DEFAULT '',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT maas_coupons_code_uq UNIQUE (code)
);

CREATE INDEX IF NOT EXISTS idx_maas_coupons_status
    ON maas_coupons (status, expires_at);

CREATE TABLE IF NOT EXISTS maas_coupon_redemptions (
    id               BIGSERIAL PRIMARY KEY,
    coupon_id        BIGINT NOT NULL REFERENCES maas_coupons(id) ON DELETE CASCADE,
    tenant_id        VARCHAR(64) NOT NULL REFERENCES tenants(code) ON DELETE CASCADE,
    credits_granted  BIGINT NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT maas_coupon_redemptions_once_per_tenant UNIQUE (coupon_id, tenant_id)
);

CREATE INDEX IF NOT EXISTS idx_maas_coupon_redemptions_tenant
    ON maas_coupon_redemptions (tenant_id, created_at DESC);

-- Invoice API placeholder only (Q1'27). Rows may be created; fulfillment is stubbed.
CREATE TABLE IF NOT EXISTS maas_invoice_requests (
    id           BIGSERIAL PRIMARY KEY,
    tenant_id    VARCHAR(64) NOT NULL REFERENCES tenants(code) ON DELETE CASCADE,
    bill_id      BIGINT REFERENCES maas_monthly_bills(id) ON DELETE SET NULL,
    status       VARCHAR(32) NOT NULL DEFAULT 'placeholder'
        CHECK (status IN ('placeholder', 'pending', 'issued', 'rejected')),
    note         TEXT NOT NULL DEFAULT 'invoice API placeholder — not implemented',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_maas_invoice_requests_tenant
    ON maas_invoice_requests (tenant_id, created_at DESC);

ALTER TABLE maas_monthly_bills ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation_maas_monthly_bills ON maas_monthly_bills;
CREATE POLICY tenant_isolation_maas_monthly_bills ON maas_monthly_bills
  USING ((tenant_id)::text = (public.get_current_tenant())::text);

ALTER TABLE maas_coupon_redemptions ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation_maas_coupon_redemptions ON maas_coupon_redemptions;
CREATE POLICY tenant_isolation_maas_coupon_redemptions ON maas_coupon_redemptions
  USING ((tenant_id)::text = (public.get_current_tenant())::text);

ALTER TABLE maas_invoice_requests ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation_maas_invoice_requests ON maas_invoice_requests;
CREATE POLICY tenant_isolation_maas_invoice_requests ON maas_invoice_requests
  USING ((tenant_id)::text = (public.get_current_tenant())::text);
