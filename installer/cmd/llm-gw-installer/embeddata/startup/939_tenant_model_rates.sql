-- ===========================================================================
-- File:          sql/migrations/startup/939_tenant_model_rates.sql
-- Migration:     939
-- Purpose:       「运营管理与价格计费体系」P0（总体设计 §2.3；《运营档》P0-1）：
--                tenant_model_rates —— 租户级分层零售价（积分计价，币种无关）。
--                PK(tenant_id, canonical_id, effective_from)；生效窗用
--                effective_to（NULL = 现行）；回退链（maas/rates.go）：
--                tenant_model_rates → model_credit_rates(manual) → 全局默认。
--                租户对象 ⇒ RLS（红线）。
--
-- Down: 939_tenant_model_rates.down.sql
-- ===========================================================================

BEGIN;

-- ★ 锁守卫照 933 形态；★ 实测（2026-11-04 本地 PG17 空库）：
--   新建空表 + PK <0.05s；无独立 CREATE INDEX（PK 即索引）。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

CREATE TABLE IF NOT EXISTS public.tenant_model_rates (
    tenant_id               varchar      NOT NULL,
    canonical_id            bigint       NOT NULL,
    effective_from          timestamptz  NOT NULL DEFAULT now(),
    effective_to            timestamptz,
    credits_per_1m_in       bigint       NOT NULL DEFAULT 0,
    credits_per_1m_out      bigint       NOT NULL DEFAULT 0,
    credits_per_1m_cache_in  bigint      NOT NULL DEFAULT 0,
    credits_per_1m_cache_out bigint      NOT NULL DEFAULT 0,
    discount_pct            numeric(5,2) NOT NULL DEFAULT 0,
    notes                   text         NOT NULL DEFAULT '',
    created_at              timestamptz  NOT NULL DEFAULT now(),
    updated_at              timestamptz  NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, canonical_id, effective_from),
    CONSTRAINT tenant_model_rates_discount_range_check
        CHECK (discount_pct >= 0 AND discount_pct <= 100)
);

ALTER TABLE public.tenant_model_rates ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation_tenant_model_rates ON public.tenant_model_rates;
CREATE POLICY tenant_isolation_tenant_model_rates ON public.tenant_model_rates
    USING (((tenant_id)::text = public.get_current_tenant()));

COMMENT ON TABLE public.tenant_model_rates IS
    'Tenant-tiered retail prices in credits (currency-agnostic, 939). Price fallback chain: tenant_model_rates (effective window) -> model_credit_rates (manual) -> maas_settings global defaults (maas/rates.go effectiveModelRates).';
COMMENT ON COLUMN public.tenant_model_rates.discount_pct IS '整体折扣百分比 0..100；0 = 不折扣。与单项 credits 列是乘法关系（先单价后折扣）。';

INSERT INTO public.schema_migrations (version, description)
VALUES ('939', 'tenant_model_rates: tenant-tiered per-model credit prices with effective window and discount_pct (tenant-scoped with RLS)')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
