-- ===========================================================================
-- File:          sql/migrations/startup/942_sales_supply_annotations.sql
-- Migration:     942
-- Purpose:       报价系统 MVP（实施规划 W1-T1，docs/audit/2026-11-04）：
--                sales_supply_annotations —— 供给池登记面（人工维护）。
--                「能卖什么」的可信清单由 v_supplier_price_vs_baseline
--                WHERE price_confidence='trusted' 活读（941 视图，零新增
--                代码面）；本表只登记**视图之外**的运营字段：
--                容量 / SLA 档 / 有效期 / 供应商联系人 / 状态 / 维护人。
--                SLA/容量是人工登记字段，不自动测（风暴档 §4-1）。
--
--                定位：自营内部运营表（非租户可见数据）⇒ 不挂
--                tenant_isolation_* RLS（风暴档 §5 预登记，同 940 市场三表
--                的平台级先例）；角色闸在应用层（ops/super）。
--
--                引用面：PK 引用 credential_model_bindings 的 UNIQUE
--                (credential_id, provider_model_id)（AF-7：主键是代理键
--                id，组合键才是 UNIQUE）——照 940 supplier_market_position
--                先例，不加 DB 级 FK，以索引 + 注释承载引用关系。
--
-- Down: 942_sales_supply_annotations.down.sql
-- ===========================================================================

BEGIN;

-- ★ 锁守卫照 939/940 形态；★ 实测（2026-11-05 本地 PG17 空库）：
--   新建空表 + UNIQUE 约束 + 2 索引 <0.05s（无存量可扫）。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

CREATE TABLE IF NOT EXISTS public.sales_supply_annotations (
    id               bigserial PRIMARY KEY,
    credential_id    bigint NOT NULL,
    provider_model_id bigint NOT NULL,
    -- 运营登记字段（全部人工维护）
    capacity_notes   text NOT NULL DEFAULT '',
    sla_tier         text NOT NULL DEFAULT '',
    valid_from       date,
    valid_until      date,
    vendor_contact   text NOT NULL DEFAULT '',
    -- active = 可进供给清单；suspended = 暂停售卖（保留登记不删行）
    status           text NOT NULL DEFAULT 'active',
    maintainer       text NOT NULL DEFAULT '',
    notes            text NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    -- 一个 binding 一行登记（对账口径：登记数 ≤ trusted binding 数）
    CONSTRAINT sales_supply_annotations_binding_unique
        UNIQUE (credential_id, provider_model_id),
    CONSTRAINT sales_supply_annotations_status_check
        CHECK (status IN ('active', 'suspended'))
);

CREATE INDEX IF NOT EXISTS idx_ssa_credential
    ON public.sales_supply_annotations (credential_id);
CREATE INDEX IF NOT EXISTS idx_ssa_status
    ON public.sales_supply_annotations (status);

COMMENT ON TABLE public.sales_supply_annotations IS
    'Supply-pool manual annotations for the sales quotation MVP (942). One row per credential_model_bindings (credential_id, provider_model_id) UNIQUE pair — referenced by index, no DB-level FK (AF-7 precedent, same as 940 supplier_market_position). The sellable list itself is read live from v_supplier_price_vs_baseline WHERE price_confidence = ''trusted'' (941); this table only carries operations fields (capacity / SLA tier / validity window / vendor contact / status / maintainer) that the view cannot know. Internal ops table: no tenant RLS (pre-registered in the 2026-11-04 brainstorm doc §5), role gate at the application layer.';
COMMENT ON COLUMN public.sales_supply_annotations.status IS
    'active | suspended. Suspended keeps the row (annotation retained) but excludes the binding from the sellable list; the C3 trusted gate still applies independently.';
COMMENT ON COLUMN public.sales_supply_annotations.sla_tier IS
    'Manually registered SLA tier label (free text by design — SLA/capacity are not auto-probed in MVP).';

INSERT INTO public.schema_migrations (version, description)
VALUES ('942', 'sales_supply_annotations: supply-pool manual annotation table (binding-pair unique, status active/suspended, capacity/SLA/validity/vendor-contact; internal ops table, no tenant RLS)')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
