-- ===========================================================================
-- File:          sql/migrations/startup/970_market_price_platform_quotes.sql
-- Migration:     970
-- Purpose:       运营定价参考 · 按模型跨平台现价明细（对齐 salesdecorate
--                Compare 现价模式）。salesdecorate.price_observation 经
--                market-price-sync.py 单向落本表；只服务运营读面，禁止回流
--                路由/计费（与 940 同红线）。
--                PK(dimension, canonical_name, platform_id)；NULL 价 = 未知
--                （NULL≠0）。平台表 ⇒ 不加 RLS。
--
-- Down: 970_market_price_platform_quotes.down.sql
-- ===========================================================================

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

CREATE TABLE IF NOT EXISTS public.market_price_platform_quotes (
    dimension       text NOT NULL DEFAULT 'standard',
    canonical_name  text NOT NULL,
    platform_id     text NOT NULL,
    platform_name   text NOT NULL DEFAULT '',
    region          text NOT NULL DEFAULT '',
    native_currency text NOT NULL DEFAULT '',
    is_vendor       boolean NOT NULL DEFAULT false,
    vendor          text NOT NULL DEFAULT '',
    currency        text NOT NULL DEFAULT '',
    input_per_1m    numeric(14,6),
    output_per_1m   numeric(14,6),
    cny_in_per_1m   numeric(14,6),
    cny_out_per_1m  numeric(14,6),
    run_date        date NOT NULL,
    source_url      text NOT NULL DEFAULT '',
    updated_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (dimension, canonical_name, platform_id)
);

CREATE INDEX IF NOT EXISTS idx_mppq_canon
    ON public.market_price_platform_quotes (canonical_name, dimension);
CREATE INDEX IF NOT EXISTS idx_mppq_platform
    ON public.market_price_platform_quotes (platform_id, run_date DESC);

COMMENT ON TABLE public.market_price_platform_quotes IS
    'Per-platform latest standard quotes mirrored from salesdecorate (970). Read-only ops surface; never feeds routing/billing.';
COMMENT ON COLUMN public.market_price_platform_quotes.input_per_1m IS
    'Native-currency input price per 1M tokens; NULL = unknown (NULL≠0).';

INSERT INTO public.schema_migrations (version, description)
VALUES ('970', 'market_price_platform_quotes: per-platform latest quotes for ops pricing reference (salesdecorate compare parity)')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
