-- Migration 007: MaaS billing — credits, plans, wallets, ledger
-- Idempotent: uses IF NOT EXISTS and CREATE OR REPLACE POLICY, safe to run multiple times.

ALTER TABLE request_logs
    ADD COLUMN IF NOT EXISTS credits_charged BIGINT;

CREATE INDEX IF NOT EXISTS idx_request_logs_credits_charged
    ON request_logs (tenant_id, ts DESC)
    WHERE credits_charged IS NOT NULL AND credits_charged > 0;

CREATE TABLE IF NOT EXISTS maas_settings (
    id INT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    cents_per_credit NUMERIC(10, 4) NOT NULL DEFAULT 0.1,
    base_credits_per_1m BIGINT NOT NULL DEFAULT 10000,
    currency_display VARCHAR(8) NOT NULL DEFAULT 'CNY',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO maas_settings (id) VALUES (1) ON CONFLICT (id) DO NOTHING;

CREATE TABLE IF NOT EXISTS subscription_plans (
    id SERIAL PRIMARY KEY,
    code VARCHAR(32) NOT NULL UNIQUE,
    tier VARCHAR(16) NOT NULL CHECK (tier IN ('basic', 'pro', 'max')),
    name VARCHAR(128) NOT NULL,
    price_cents INT NOT NULL,
    monthly_credits BIGINT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    sort_order INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS topup_packages (
    id SERIAL PRIMARY KEY,
    code VARCHAR(32) NOT NULL UNIQUE,
    tier VARCHAR(16) NOT NULL CHECK (tier IN ('small', 'medium', 'large')),
    name VARCHAR(128) NOT NULL,
    price_cents INT NOT NULL,
    credits_amount BIGINT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    sort_order INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS tenant_credit_wallets (
    tenant_id VARCHAR(64) PRIMARY KEY REFERENCES tenants(code) ON DELETE CASCADE,
    balance_credits BIGINT NOT NULL DEFAULT 0,
    locked_credits BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS tenant_subscriptions (
    id SERIAL PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL REFERENCES tenants(code) ON DELETE CASCADE,
    plan_id INT NOT NULL REFERENCES subscription_plans(id),
    status VARCHAR(32) NOT NULL DEFAULT 'active'
        CHECK (status IN ('pending', 'active', 'expired', 'cancelled')),
    period_start TIMESTAMPTZ NOT NULL,
    period_end TIMESTAMPTZ NOT NULL,
    quota_remaining BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_tenant_subscriptions_tenant
    ON tenant_subscriptions (tenant_id, status);

CREATE TABLE IF NOT EXISTS model_credit_rates (
    canonical_id INT PRIMARY KEY REFERENCES models_canonical(id) ON DELETE CASCADE,
    credits_per_1m_in BIGINT,
    credits_per_1m_out BIGINT,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS credit_ledger (
    id BIGSERIAL PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL REFERENCES tenants(code),
    entry_type VARCHAR(32) NOT NULL
        CHECK (entry_type IN ('consume', 'topup', 'subscribe', 'adjust', 'refund')),
    amount BIGINT NOT NULL,
    balance_after BIGINT NOT NULL,
    ref_type VARCHAR(32),
    ref_id VARCHAR(128),
    note TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_credit_ledger_tenant_ts
    ON credit_ledger (tenant_id, created_at DESC);

-- Seed subscription plans (idempotent by code)
INSERT INTO subscription_plans (code, tier, name, price_cents, monthly_credits, sort_order)
VALUES
    ('basic-monthly', 'basic', '基础版', 2900, 100000, 1),
    ('pro-monthly', 'pro', '高级版', 9900, 500000, 2),
    ('max-monthly', 'max', '最大版', 29900, 2000000, 3)
ON CONFLICT (code) DO NOTHING;

INSERT INTO topup_packages (code, tier, name, price_cents, credits_amount, sort_order)
VALUES
    ('topup-small', 'small', '加油包 · 小', 1000, 10000, 1),
    ('topup-medium', 'medium', '加油包 · 中', 5000, 55000, 2),
    ('topup-large', 'large', '加油包 · 大', 10000, 120000, 3)
ON CONFLICT (code) DO NOTHING;

--
-- Round 33 (2026-06-16) — Pattern A RLS for billing tables.
-- Note: request_logs is the hottest write path (every LLM call writes
-- one row). RLS USING clause is evaluated on every query; for INSERT
-- the GUC must be set on the connection BEFORE the INSERT. R33 assumes
-- the application already sets app.current_tenant per connection.
-- If write performance degrades, consider adding a service-role
-- bypass policy (CREATE POLICY ... AS PERMISSIVE FOR ALL TO service_role).
--

-- request_logs: every LLM request is tenant-scoped (ALTER TABLE earlier
-- in this migration added tenant_id; RLS keeps SELECTs filtered)
-- Uses CREATE OR REPLACE POLICY to be idempotent across deployments.
ALTER TABLE public.request_logs ENABLE ROW LEVEL SECURITY;
CREATE OR REPLACE POLICY tenant_isolation_request_logs ON public.request_logs
  USING ((tenant_id)::text = (public.get_current_tenant())::text);

-- tenant_credit_wallets: per-tenant balance, only own tenant visible
ALTER TABLE public.tenant_credit_wallets ENABLE ROW LEVEL SECURITY;
CREATE OR REPLACE POLICY tenant_isolation_tenant_credit_wallets ON public.tenant_credit_wallets
  USING ((tenant_id)::text = (public.get_current_tenant())::text);

-- tenant_subscriptions: subscription history per tenant
ALTER TABLE public.tenant_subscriptions ENABLE ROW LEVEL SECURITY;
CREATE OR REPLACE POLICY tenant_isolation_tenant_subscriptions ON public.tenant_subscriptions
  USING ((tenant_id)::text = (public.get_current_tenant())::text);

-- credit_ledger: every credit movement is tenant-scoped
ALTER TABLE public.credit_ledger ENABLE ROW LEVEL SECURITY;
CREATE OR REPLACE POLICY tenant_isolation_credit_ledger ON public.credit_ledger
  USING ((tenant_id)::text = (public.get_current_tenant())::text);
