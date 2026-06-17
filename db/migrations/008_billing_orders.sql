-- Migration 008: Three-pool wallets, billing orders, payment placeholders
-- Idempotent: safe to run multiple times.

-- Split wallet into granted (credit) + purchased (paid) pools.
-- balance_credits kept in sync as granted + purchased for backward compat.
ALTER TABLE tenant_credit_wallets
    ADD COLUMN IF NOT EXISTS granted_balance BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS purchased_balance BIGINT NOT NULL DEFAULT 0;

-- One-time migration: move legacy balance into purchased pool.
UPDATE tenant_credit_wallets
   SET purchased_balance = balance_credits - granted_balance
 WHERE purchased_balance = 0
   AND balance_credits > 0
   AND (granted_balance = 0 OR balance_credits > granted_balance);

UPDATE tenant_credit_wallets
   SET balance_credits = granted_balance + purchased_balance
 WHERE balance_credits != granted_balance + purchased_balance;

-- Ledger pool tracking (subscription_quota | granted | purchased)
ALTER TABLE credit_ledger
    ADD COLUMN IF NOT EXISTS pool VARCHAR(32);

-- Payment placeholder settings
ALTER TABLE maas_settings
    ADD COLUMN IF NOT EXISTS alipay_account VARCHAR(128) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS wechat_mch_id VARCHAR(128) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS stub_alipay_qr_url TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS stub_wechat_qr_url TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS billing_orders (
    id BIGSERIAL PRIMARY KEY,
    order_no VARCHAR(64) NOT NULL UNIQUE,
    tenant_id VARCHAR(64) NOT NULL REFERENCES tenants(code) ON DELETE CASCADE,
    order_type VARCHAR(16) NOT NULL
        CHECK (order_type IN ('subscribe', 'topup')),
    status VARCHAR(16) NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'paid', 'cancelled', 'expired')),
    amount_cents INT NOT NULL,
    credits BIGINT NOT NULL,
    plan_id INT REFERENCES subscription_plans(id),
    package_id INT REFERENCES topup_packages(id),
    payment_channel VARCHAR(16) NOT NULL DEFAULT 'alipay'
        CHECK (payment_channel IN ('alipay', 'wechat', 'manual')),
    qr_payload TEXT NOT NULL DEFAULT '',
    qr_url TEXT NOT NULL DEFAULT '',
    paid_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ NOT NULL,
    note TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_billing_orders_tenant
    ON billing_orders (tenant_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_billing_orders_status
    ON billing_orders (status, created_at DESC);

--
-- Round 33 (2026-06-16) — Pattern A RLS for billing_orders.
-- Every order belongs to a tenant; orders should only be visible to
-- that tenant.
--
ALTER TABLE public.billing_orders ENABLE ROW LEVEL SECURITY;
CREATE OR REPLACE POLICY tenant_isolation_billing_orders ON public.billing_orders
  USING ((tenant_id)::text = (public.get_current_tenant())::text);
