-- ===========================================================================
-- File:          sql/migrations/startup/946_sales_quote_items.sql
-- Migration:     946
-- Purpose:       报价系统 MVP（实施规划 W1-T1，决议 A 快照物化 + 决议 C
--                「基础价 × 折扣正交」）：
--                sales_quote_items —— 行级快照（Invoice Ninja line_items +
--                Odoo purchase_price 双源）：创建时点的成本 / 币种 /
--                基础零售价 / 折扣 / 量 / 毛利全部固化，之后 940 表与
--                汇率怎么变，已存行不动。
--
--                ★ 决议 C：折扣永不写回基础价——
--                  rate_in/rate_out = 基础零售价（积分/1M，引用
--                  effectiveModelRates 兜底链读数），discount_pct = 商谈
--                  折扣（0..100），折后价在读取/预计算时相乘，两轴分列。
--                ★ 毛利分解与第 45 增量流水 detail 五因子同构：
--                  {rate_in, rate_out, tokens, discount, confidence}
--                  ⇒ W3 对账时有逐列对账面。
--                ★ fx_rate：CNY 成本折 USD 用（Odoo exchange_rate 同位，
--                  单源 settings.CNYToUSDFX）。
--                ★ 毛利预计算落库（store=True）：revenue/cost/margin USD。
--                ★ 2026-11-05 审计 A7 补：version_id 补 FK ON DELETE CASCADE
--                  —— 此前头级联删版本会留下永久孤儿 items（本迁移
--                  prepared 未应用 ⇒ 改字节合法，936 先例；同步两份基线
--                  副本与 embeddata 逐字节重拷）。
--
-- Down: 946_sales_quote_items.down.sql
-- ===========================================================================

BEGIN;

-- ★ 锁守卫照 942–945 形态；★ 实测（2026-11-05 本地 PG17 空库）：
--   新建空表 + 1 索引 <0.05s（无存量可扫）。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

CREATE TABLE IF NOT EXISTS public.sales_quote_items (
    id            bigserial PRIMARY KEY,
    -- A7：级联链 = 头 CASCADE → 版本 → 行快照（无 FK 会在删头时留下
    -- 永久孤儿 items；945 down 的「逆序假设」只管迁移回放，不管生产路径）。
    version_id    bigint NOT NULL REFERENCES public.sales_quote_versions (id) ON DELETE CASCADE,
    -- 模型（行级主体）：canon 名 + 展示名
    canonical_name text NOT NULL,
    display_name  text NOT NULL DEFAULT '',
    -- 量（预估 tokens；tokens_est 0 = 计费按别的口径，见 volume_note）
    est_tokens_in  bigint NOT NULL DEFAULT 0,
    est_tokens_out bigint NOT NULL DEFAULT 0,
    volume_note   text NOT NULL DEFAULT '',
    -- P3 成本快照（创建时点；币种随供应商）
    cost_in_per_1m  numeric(14,6),
    cost_out_per_1m numeric(14,6),
    cost_currency   text NOT NULL DEFAULT 'USD',
    -- P5 基础零售价（积分/1M，决议 C 基础价轴）——引用
    -- effectiveModelRates 兜底链（租户价→manual→全局）的读数
    rate_in       numeric(14,4),
    rate_out      numeric(14,4),
    -- 商谈折扣（决议 C 折扣轴）：0..100；折后价 = ceil(rate × (1-d/100))
    discount_pct  numeric(5,2) NOT NULL DEFAULT 0,
    -- C3 置信档（行级快照，镜像 941 视图口径）
    confidence    text NOT NULL DEFAULT 'unknown',
    -- FX 快照（CNY 成本折 USD；单源 settings.CNYToUSDFX）
    fx_rate       numeric(12,6) NOT NULL DEFAULT 7.2,
    -- 毛利预计算落库（store=True 范式，USD 口径；写侧 Go 计算）
    revenue_usd   numeric(14,4) NOT NULL DEFAULT 0,
    cost_usd      numeric(14,4) NOT NULL DEFAULT 0,
    margin_usd    numeric(14,4) NOT NULL DEFAULT 0,
    margin_pct    numeric(8,4),
    created_at    timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT sales_quote_items_confidence_check
        CHECK (confidence IN ('trusted', 'suspect_symmetric',
                              'empty', 'unknown_currency', 'unknown')),
    CONSTRAINT sales_quote_items_discount_range_check
        CHECK (discount_pct >= 0 AND discount_pct <= 100)
);

CREATE INDEX IF NOT EXISTS idx_sqi_version
    ON public.sales_quote_items (version_id);
CREATE INDEX IF NOT EXISTS idx_sqi_canonical
    ON public.sales_quote_items (canonical_name);

COMMENT ON TABLE public.sales_quote_items IS
    'Per-line immutable snapshot of a quote version (946): cost (P3, with currency), base retail rate (credits/1M, Decision C base-price axis), negotiated discount (orthogonal axis — never folded back into the base rate), estimated volume, C3 confidence, FX snapshot, and precomputed USD revenue/cost/margin (store=True). Mirrors the ledger consumption detail five-factor shape for W3 reconciliation.';
COMMENT ON COLUMN public.sales_quote_items.rate_in IS
    'Base retail price for input, credits per 1M (Decision C). NULL = no rate available (never 0-by-default: NULL≠0 red line). Discount applies multiplicatively at read/precompute time.';
COMMENT ON COLUMN public.sales_quote_items.discount_pct IS
    'Negotiated discount 0..100 (Decision C discount axis). Never persisted back into rate_in/rate_out.';
COMMENT ON COLUMN public.sales_quote_items.confidence IS
    'C3 gate snapshot per line (mirrors 941 price_confidence): trusted | suspect_symmetric | empty | unknown_currency | unknown. trusted is the workbench admission gate for a formal quote.';

INSERT INTO public.schema_migrations (version, description)
VALUES ('946', 'sales_quote_items per-line snapshot: P3 cost + currency, base retail rate (credits/1M) with orthogonal discount_pct, volume estimate, C3 confidence, FX snapshot, precomputed USD revenue/cost/margin (store=True)')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
