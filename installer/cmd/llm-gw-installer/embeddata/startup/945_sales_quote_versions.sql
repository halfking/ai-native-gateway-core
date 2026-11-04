-- ===========================================================================
-- File:          sql/migrations/startup/945_sales_quote_versions.sql
-- Migration:     945
-- Purpose:       报价系统 MVP（实施规划 W1-T1，决议 A 快照物化）：
--                sales_quote_versions —— **不可变**版本行：
--                五价坐标系 jsonb 快照 + FX 快照 + C3 置信档 + 毛利
--                预计算落库（Odoo store=True 范式：之后主数据/汇率变了
--                报表不漂）+ valid_until（Expired 读取时派生）+
--                superseded_by（Kill Bill 版本序列：发出后修订 = 新版本
--                行 + 旧版本回填 superseded_by）。
--
--                ★ 不可变性（W1-T2 验收门「UPDATE 被拒」的 DB 侧载体）：
--                  触发器 BEFORE UPDATE OR DELETE ——
--                  · UPDATE 仅允许 superseded_by 一列变化（新版本创建时
--                    回填旧版本），其余任何列被改 ⇒ 异常回滚；
--                  · 直接 DELETE 拒绝；仅头表 ON DELETE CASCADE 连带清理
--                    放行（pg_trigger_depth() > 1 区分 —— 深度 1 是用户
--                    直删，>1 是级联触发）。
--
--                ★ 944.current_version_id 的 FK 在本迁移回补（版本表
--                  后建；down 顺序按 948→942 逆序应用，无悬挂引用）。
--
--                FX 单源红线：fx_rate 快照值一律取 Go 侧
--                settings.CNYToUSDFX（7.2），禁第二把汇率（风暴档 §6）。
--
-- Down: 945_sales_quote_versions.down.sql
-- ===========================================================================

BEGIN;

-- ★ 锁守卫照 942–944 形态；★ 实测（2026-11-05 本地 PG17 空库）：
--   新建空表 + UNIQUE + 触发器 + ALTER ADD CONSTRAINT（空表）<0.1s。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

CREATE TABLE IF NOT EXISTS public.sales_quote_versions (
    id               bigserial PRIMARY KEY,
    -- 头级联（引用 944 已建成的头表；**缺这条 = 头清理时版本行孤儿残留**
    -- —— 2026-11-05 本地实测抓到后补）。
    quote_id         bigint NOT NULL,
    version_no       int    NOT NULL,
    -- 五价坐标系快照（P1 原厂刊例 / P2 市场最低 / P3 供应商实付 /
    -- P4 基准价锚定 / P5 客户零售价）——引用来源与读数一起固化。
    price_axes       jsonb  NOT NULL DEFAULT '{}'::jsonb,
    -- C3 置信档（门不是分）：工作台出正式单的准入 = 'trusted'。
    price_confidence text   NOT NULL DEFAULT 'unknown',
    -- 三档报价系数档位（aggressive/standard/conservative，待裁决默认标准）
    tier             text   NOT NULL DEFAULT 'standard',
    -- FX 快照（Odoo exchange_rate 同位；单源 settings.CNYToUSDFX）
    fx_rate          numeric(12,6) NOT NULL DEFAULT 7.2,
    -- 毛利预计算落库（store=True 范式）：报表永远读快照列，不做实时 join。
    cost_total_usd   numeric(14,4) NOT NULL DEFAULT 0,
    sale_total_usd   numeric(14,4) NOT NULL DEFAULT 0,
    margin_usd       numeric(14,4) NOT NULL DEFAULT 0,
    margin_pct       numeric(8,4),
    -- Expired 派生态的日历（读取时 valid_until < now() ⇒ expired）
    valid_until      timestamptz,
    -- Kill Bill 版本序列：被哪个新版本替代（新版本创建时回填旧行，
    -- 这是全表唯一可 UPDATE 的列）
    superseded_by    bigint,
    created_by       text   NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT sales_quote_versions_quote_version_unique
        UNIQUE (quote_id, version_no),
    CONSTRAINT sales_quote_versions_quote_fk
        FOREIGN KEY (quote_id)
        REFERENCES public.sales_quotes (id)
        ON DELETE CASCADE,
    CONSTRAINT sales_quote_versions_tier_check
        CHECK (tier IN ('aggressive', 'standard', 'conservative')),
    CONSTRAINT sales_quote_versions_confidence_check
        CHECK (price_confidence IN ('trusted', 'suspect_symmetric',
                                    'empty', 'unknown_currency', 'unknown'))
);

CREATE INDEX IF NOT EXISTS idx_sqv_quote
    ON public.sales_quote_versions (quote_id, version_no DESC);

COMMENT ON TABLE public.sales_quote_versions IS
    'Immutable quotation versions (945). Materialized snapshot per Decision A: five-price axes jsonb + FX snapshot (single source settings.CNYToUSDFX) + C3 price_confidence gate + precomputed margin (Odoo store=True — reports read snapshot columns, never live joins). Rows are write-once: a BEFORE trigger allows UPDATE of superseded_by only and forbids direct DELETE (head-table cascade cleanup is exempt via pg_trigger_depth).';
COMMENT ON COLUMN public.sales_quote_versions.price_axes IS
    'Five-price coordinate snapshot: p1 vendor list / p2 market min / p3 supplier paid / p4 baseline anchor (display only — never enters money math, red line) / p5 retail. Source row ids stored alongside readings.';
COMMENT ON COLUMN public.sales_quote_versions.margin_pct IS
    'Precomputed (sale-cost)/sale ratio in USD terms; NULL = sale total is zero (avoid div-by-zero artifact).';
COMMENT ON COLUMN public.sales_quote_versions.valid_until IS
    'Quote validity end. Expired is derived at read time (valid_until < now()), never stored as a status (Invoice Ninja form).';

-- ── 不可变守卫（★ W1-T2 验收门的 DB 侧载体） ─────────────────────────────
CREATE OR REPLACE FUNCTION public.sales_quote_versions_immutable_guard()
RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        -- 头表 ON DELETE CASCADE 连带清理放行；用户直删拒绝。
        IF pg_trigger_depth() > 1 THEN
            RETURN OLD;
        END IF;
        RAISE EXCEPTION
            'sales_quote_versions rows are immutable: direct DELETE forbidden (delete via quote head cascade)';
    END IF;
    -- UPDATE：仅 superseded_by 允许变化；其余任一列被改即拒绝。
    IF NEW.quote_id         IS DISTINCT FROM OLD.quote_id
       OR NEW.version_no    IS DISTINCT FROM OLD.version_no
       OR NEW.price_axes    IS DISTINCT FROM OLD.price_axes
       OR NEW.price_confidence IS DISTINCT FROM OLD.price_confidence
       OR NEW.tier          IS DISTINCT FROM OLD.tier
       OR NEW.fx_rate       IS DISTINCT FROM OLD.fx_rate
       OR NEW.cost_total_usd IS DISTINCT FROM OLD.cost_total_usd
       OR NEW.sale_total_usd IS DISTINCT FROM OLD.sale_total_usd
       OR NEW.margin_usd    IS DISTINCT FROM OLD.margin_usd
       OR NEW.margin_pct    IS DISTINCT FROM OLD.margin_pct
       OR NEW.valid_until   IS DISTINCT FROM OLD.valid_until
       OR NEW.created_by    IS DISTINCT FROM OLD.created_by
       OR NEW.created_at    IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION
            'sales_quote_versions rows are immutable: only superseded_by may be updated';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS sales_quote_versions_immutable ON public.sales_quote_versions;
CREATE TRIGGER sales_quote_versions_immutable
    BEFORE UPDATE OR DELETE ON public.sales_quote_versions
    FOR EACH ROW EXECUTE FUNCTION sales_quote_versions_immutable_guard();

COMMENT ON FUNCTION public.sales_quote_versions_immutable_guard IS
    'Immutability guard (945): UPDATE may only change superseded_by; direct DELETE is rejected (cascade from sales_quotes is exempt — pg_trigger_depth() > 1).';

-- 回补 944 头表指针的 FK（版本表已在本迁移建成）。
-- ★ 幂等（DO 块，照 936 先例）：fresh-install 的 01-schema 折叠基线已带
--   同名约束 ⇒ 裸 ALTER ADD CONSTRAINT 会 42710 重跑炸链。
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
         WHERE conname = 'sales_quotes_current_version_fk'
           AND conrelid = 'public.sales_quotes'::regclass
    ) THEN
        ALTER TABLE public.sales_quotes
            ADD CONSTRAINT sales_quotes_current_version_fk
            FOREIGN KEY (current_version_id)
            REFERENCES public.sales_quote_versions (id);
    END IF;
END $$;

INSERT INTO public.schema_migrations (version, description)
VALUES ('945', 'sales_quote_versions immutable version rows (five-price axes jsonb + FX snapshot + C3 gate + precomputed margin + valid_until + superseded_by; write-once trigger allowing superseded_by updates only, direct DELETE rejected with cascade exemption) + backfill sales_quotes.current_version_id FK')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
