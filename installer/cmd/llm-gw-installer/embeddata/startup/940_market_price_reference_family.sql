-- ===========================================================================
-- File:          sql/migrations/startup/940_market_price_reference_family.sql
-- Migration:     940
-- Purpose:       「运营管理与价格计费体系」P1 数据地基（总体设计 §2.3/§2.7；
--                《价格档》§5.1 三表 + AF-8 修订）：
--                ① market_price_reference —— 行情层（salesdecorate）单向下行
--                  同步的网关侧副本；P1 原厂刊例 + P2 市场最低（排除我方平台）
--                  + fx 快照；按 dimension 分行（standard/batch/…，条件价
--                  不压平红线）。
--                  ★ AF-8 修订：加 canonical_id bigint NULL 解析列 + 部分唯一
--                    索引 —— 网关侧消费（评分/路由投影）优先走 id；
--                    同步器解析失败进 conflicts（拒绝族 canonical_unresolved）。
--                ② supplier_market_position —— C1/C2/C3 每日重算落库；
--                  PK(credential_id, provider_model_id) 引用的是
--                  credential_model_bindings 的 UNIQUE 约束（AF-7：主键是
--                  代理键 id，组合键是 UNIQUE）。
--                ③ market_price_reference_sync —— 同步台账（派生台账可对账：
--                  源条数 = 落地 + 拒绝）。
--                全部平台表（行情面，非租户对象）⇒ 不加 RLS。
--
-- Down: 940_market_price_reference_family.down.sql
-- ===========================================================================

BEGIN;

-- ★ 锁守卫照 933 形态；★ 实测（2026-11-04 本地 PG17 空库）：三表 + 4 索引
--   全部建在**本文件刚 CREATE 的空表**上（无存量可扫），墙钟 <0.15s，
--   形态同 912/913/928 先例。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

-- ── ① 市场参考价（P1/P2 快照，per (canonical_name, dimension)） ──────────
CREATE TABLE IF NOT EXISTS public.market_price_reference (
    canonical_name            text NOT NULL,
    dimension                 text NOT NULL DEFAULT 'standard',
    currency                  text NOT NULL DEFAULT 'USD',
    canonical_id              bigint,              -- AF-8 解析列；NULL = 名字空间未解析
    -- P1 原厂刊例价
    vendor_list_in_per_1m     numeric(14,6),
    vendor_list_out_per_1m    numeric(14,6),
    vendor_platform_id        text NOT NULL DEFAULT '',
    vendor_vendor             text NOT NULL DEFAULT '',
    -- P2 市场最低价（排除我方平台，A-5）
    market_min_in_per_1m      numeric(14,6),
    market_min_out_per_1m     numeric(14,6),
    market_min_platform_id    text NOT NULL DEFAULT '',
    market_min_platform_count int  NOT NULL DEFAULT 0,
    market_min_cny_in_per_1m  numeric(14,6),
    market_min_cny_out_per_1m numeric(14,6),
    fx_rate                   numeric(12,6),
    -- 出处（照抄 standard_iq 范式）
    source_run_date           date NOT NULL,
    source_confidence         char(1) NOT NULL DEFAULT 'A',
    source_url                text NOT NULL DEFAULT '',
    updated_at                timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (canonical_name, dimension)
);

CREATE INDEX IF NOT EXISTS idx_mpr_vendor ON public.market_price_reference (vendor_vendor, source_run_date DESC);
CREATE INDEX IF NOT EXISTS idx_mpr_market ON public.market_price_reference (market_min_platform_id, source_run_date DESC);
-- AF-8：解析成功者在 (canonical_id, dimension) 上唯一；未解析行（NULL）不受约束。
CREATE UNIQUE INDEX IF NOT EXISTS uq_mpr_canonical_id
    ON public.market_price_reference (canonical_id, dimension)
    WHERE canonical_id IS NOT NULL;

COMMENT ON COLUMN public.market_price_reference.vendor_list_in_per_1m IS 'P1 原厂刊例价；NULL = 无原厂价（未知），0 = 原厂确认免费 —— 两态不可合并（NULL≠0 红线）。';
COMMENT ON COLUMN public.market_price_reference.market_min_platform_count IS 'P2 独立平台数；<3 时该分降级为参考（≥3 硬指标）。';
COMMENT ON COLUMN public.market_price_reference.canonical_id IS 'AF-8：解析到 models_canonical.id 的网关侧 id；NULL = 未解析（同步器拒绝族 canonical_unresolved 进 conflicts）。';

-- ── ② 供应商竞争力评分（C1/C2/C3 每日重算；可重放） ─────────────────────
CREATE TABLE IF NOT EXISTS public.supplier_market_position (
    credential_id             bigint NOT NULL,
    provider_model_id         bigint NOT NULL,
    canonical_name            text NOT NULL,
    currency                  text NOT NULL DEFAULT 'USD',
    supplier_in_per_1m        numeric(14,6),
    supplier_out_per_1m       numeric(14,6),
    -- C1 采购折扣率（NULL = 无原厂价（未知）≠ 0 = 正好等于原厂价）
    discount_vs_vendor        numeric(10,4),
    vendor_ref_in_per_1m      numeric(14,6),
    vendor_ref_out_per_1m     numeric(14,6),
    -- C2 市场位势（市场最低价/供应商价，>1 = 我们更便宜）
    market_position           numeric(10,4),
    market_ref_in_per_1m      numeric(14,6),
    market_ref_out_per_1m     numeric(14,6),
    market_ref_platform_id    text NOT NULL DEFAULT '',
    market_ref_platform_count int  NOT NULL DEFAULT 0,
    -- C3 价格置信度（是门不是分）：trusted|suspect_symmetric|empty|unknown_currency
    price_confidence          text NOT NULL DEFAULT 'unknown',
    market_advantage_score    numeric(6,2),
    computed_at               timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (credential_id, provider_model_id)
);

CREATE INDEX IF NOT EXISTS idx_smp_canon ON public.supplier_market_position (canonical_name);
CREATE INDEX IF NOT EXISTS idx_smp_conf  ON public.supplier_market_position (price_confidence);

COMMENT ON TABLE public.supplier_market_position IS
    'Daily-recomputed supplier competitiveness (940). PK references credential_model_bindings UNIQUE (credential_id, provider_model_id) — the table PK is surrogate id (AF-7). discount_vs_vendor is C1 (NULL = no vendor price), market_position is C2 (>1 = cheaper than market min), price_confidence is C3 gate (not a score); market_advantage_score NULL = no signal (consumers treat as neutral 0, never as free/cheapest).';

-- ── ③ 同步台账（源条数 = 落地 + 拒绝，可对账） ──────────────────────────
CREATE TABLE IF NOT EXISTS public.market_price_reference_sync (
    id               bigserial PRIMARY KEY,
    run_date         date NOT NULL,
    source_platforms int  NOT NULL DEFAULT 0,
    source_rows      int  NOT NULL DEFAULT 0,
    landed_rows      int  NOT NULL DEFAULT 0,
    rejected_rows    int  NOT NULL DEFAULT 0,
    conflicts        jsonb NOT NULL DEFAULT '[]'::jsonb,
    detail           jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at       timestamptz NOT NULL DEFAULT now()
);

COMMENT ON COLUMN public.market_price_reference_sync.detail IS '同步明细：拒绝族计数（含 canonical_unresolved，AF-8）、我方平台排除清单（A-5）、fx 快照等。';

INSERT INTO public.schema_migrations (version, description)
VALUES ('940', 'market price intel tables: market_price_reference (P1/P2 snapshot + AF-8 canonical_id resolution), supplier_market_position (C1/C2/C3), market_price_reference_sync ledger')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
