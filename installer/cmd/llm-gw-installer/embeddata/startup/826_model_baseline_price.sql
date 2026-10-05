-- ===========================================================================
-- File:          sql/migrations/startup/826_model_baseline_price.sql
-- Migration:     826
-- Database:      llm_gateway
-- Purpose:       给每个模型补上「原厂标准价（基准价）」这一层，并给出
--                供应商实际价相对基准价的偏差口径与价差对账台账。
--
-- 立项依据（本地代码实测，非推断）：
--
-- 1. 基准价这一层**根本不存在**。
--    models_canonical 有 input_price_cny / output_price_cny 两列，但：
--      - 只在 admin/models.go:443 被读，纯展示用，不进成本路径；
--      - 写入是 ON CONFLICT DO NOTHING（admin/models.go:380）⇒ 永不变；
--      - 单位是 CNY/百万，而实盘成本单位是 USD/百万（credential_model_bindings
--        .unit_price_*_per_1m 的 currency 默认 'USD'）⇒ 两者根本不可比。
--    ⇒ 现状没有任何一根标尺可以说「这个模型本该值多少钱」。
--
-- 2. 实盘成本完全由人工填的绝对价决定。
--    provider/client.go:1643 取 COALESCE(mo.unit_price_in_per_1m,
--    pp_fb.plan_in) —— 先凭证绑定、再 pricing_plans 兜底，两处都是人填的。
--    兜底还带一刀钝斧：sql/migrations/startup/480_model_iq_cost_calibration.sql
--    把零价 offer 一律写成 0.1/0.1。于是「价填错了」与「价没填」在账面上
--    分不开，成本核算没有可对账的参照物。
--
-- 3. 定时刷新是坏的。
--    deploy/k8s/cron/pricing-monthly-refresh.yaml:61 指向本仓不存在的
--    docs/pricing/scripts/fetch-pricing.sh 路径，:81 调用的
--    /api/pricing/import 在本仓没有任何 Go handler。真正在用的价格仍是
--    手工维护的 docs/02-resources/research/pricing/scripts/vendor_pricing_table.py。
--
-- 本迁移给出四样东西：
--
--   (a) models_canonical 上的基准价四元组 + 完整出处（vendor / source /
--       source_url / fetched_at）。**出处三件套的形状照抄
--       standard_iq / standard_iq_source / standard_iq_updated_at**
--       （cmd/fetch-standard-iq/main.go:139-141）—— 本仓已有这个范式，
--       不另发明一套。
--       单位统一 USD/百万 token，与实盘成本口径**同量纲**，这样偏差才有意义。
--
--   (b) model_baseline_price_reconciliation：每次对账一行，把「清单里的
--       价」与「观察到的价」并排放进去。对账**只记账不改价** —— 坏价
--       需要人确认，机器不该替运营改账。
--
--   (c) v_supplier_price_vs_baseline：供应商实际价相对基准价的倍率与偏差。
--       成本核算与毛利由此可算，缺了它「准确控制实际成本」只能靠人肉对账。
--
--   (d) 基准价的「零」有明确含义：NULL = 未设定，0 = 原厂确认免费。
--       这两者的区别在成本核算里是本质的，CHECK 约束保证 0 不会被当成缺省。
--
-- 幂等：ADD COLUMN IF NOT EXISTS + CREATE TABLE/VIEW IF NOT EXISTS，可安全重放。
-- ===========================================================================
BEGIN;

-- ---------------------------------------------------------------------------
-- (a) 基准价 + 出处
-- ---------------------------------------------------------------------------
ALTER TABLE public.models_canonical
    ADD COLUMN IF NOT EXISTS baseline_price_currency text,
    ADD COLUMN IF NOT EXISTS baseline_input_price_per_1m numeric(14,6),
    ADD COLUMN IF NOT EXISTS baseline_output_price_per_1m numeric(14,6),
    ADD COLUMN IF NOT EXISTS baseline_cache_read_price_per_1m numeric(14,6),
    ADD COLUMN IF NOT EXISTS baseline_cache_write_price_per_1m numeric(14,6),
    ADD COLUMN IF NOT EXISTS baseline_price_vendor text,
    ADD COLUMN IF NOT EXISTS baseline_price_source text,
    ADD COLUMN IF NOT EXISTS baseline_price_source_url text,
    ADD COLUMN IF NOT EXISTS baseline_price_fetched_at timestamptz;

COMMENT ON COLUMN public.models_canonical.baseline_price_currency IS
    'ISO currency of the baseline prices. USD in practice, so that it is directly comparable with credential_model_bindings.unit_price_*_per_1m (currency defaults to USD). The pre-existing input_price_cny/output_price_cny are CNY-per-1M and are display-only; do not mix the two.';
COMMENT ON COLUMN public.models_canonical.baseline_input_price_per_1m IS
    'Vendor list price per 1M input tokens. NULL = not set; 0 = the vendor confirms the model is free. Those two are different facts and must not be collapsed.';
COMMENT ON COLUMN public.models_canonical.baseline_output_price_per_1m IS
    'Vendor list price per 1M output tokens. Same NULL-vs-0 semantics as the input column.';
COMMENT ON COLUMN public.models_canonical.baseline_price_vendor IS
    'Originating vendor id (openai, anthropic, google, deepseek, zhipu, ...) — the party the baseline price is quoted from, NOT the supplier actually being billed.';
COMMENT ON COLUMN public.models_canonical.baseline_price_source IS
    'How the baseline was obtained: vendor_pricing_page, vendor_docs, machine_readable_catalog, operator_override. Recorded so a bad number can be traced to its origin.';
COMMENT ON COLUMN public.models_canonical.baseline_price_source_url IS
    'Where the number came from. A price without a URL is not auditable, and an un-auditable price is how the current hand-maintained table drifted.';
COMMENT ON COLUMN public.models_canonical.baseline_price_fetched_at IS
    'When the source was last read. Freshness is what the periodic reconciliation checks first: a stale source is worse than a wrong one, because it looks authoritative.';

ALTER TABLE public.models_canonical
    DROP CONSTRAINT IF EXISTS models_canonical_baseline_price_check;
ALTER TABLE public.models_canonical
    ADD CONSTRAINT models_canonical_baseline_price_check
    CHECK (
        (baseline_input_price_per_1m IS NULL OR baseline_input_price_per_1m >= 0)
        AND (baseline_output_price_per_1m IS NULL OR baseline_output_price_per_1m >= 0)
        AND (baseline_cache_read_price_per_1m IS NULL OR baseline_cache_read_price_per_1m >= 0)
        AND (baseline_cache_write_price_per_1m IS NULL OR baseline_cache_write_price_per_1m >= 0)
    );

-- ---------------------------------------------------------------------------
-- (b) 价差对账台账
--
-- 只记账、不改价。verdict ∈ match / drift / stale_source / missing / not_comparable。
--
-- 为什么不自动改价：价格是钱。自动改价意味着机器替运营做了「我们按这个
-- 价卖」的决定，而观察源本身是第三方数据（可能是社区维护的），把它当权威
-- 写进账本，会把一个「需要人看一眼的信号」变成「已经生效的事实」。
-- 正确形态是：漂移被看见、被记账、可以按模型逐条处理。
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.model_baseline_price_reconciliation (
    id              bigserial PRIMARY KEY,
    canonical_name  text NOT NULL,
    checked_at      timestamptz NOT NULL DEFAULT now(),

    -- 清单侧（SSOT，仓内带出处的价目表）
    ssot_input_price_per_1m  numeric(14,6),
    ssot_output_price_per_1m numeric(14,6),
    ssot_currency            text,
    ssot_source_url          text,
    ssot_source_fetched_at   timestamptz,

    -- 观察侧（外部机读源）
    observed_input_price_per_1m  numeric(14,6),
    observed_output_price_per_1m numeric(14,6),
    observed_currency            text,
    observed_source              text,
    observed_source_url          text,

    -- 判词
    verdict       text NOT NULL,
    input_drift_pct  numeric(9,4),
    output_drift_pct numeric(9,4),
    detail        jsonb NOT NULL DEFAULT '{}'::jsonb,

    CONSTRAINT model_baseline_price_reconciliation_verdict_check
        CHECK (verdict IN ('match', 'drift', 'stale_source', 'missing', 'not_comparable'))
);

CREATE INDEX IF NOT EXISTS idx_model_baseline_price_reconciliation_model
    ON public.model_baseline_price_reconciliation (canonical_name, checked_at DESC);

-- 告警面：只看最近一次对账里判为 drift 的那些。
CREATE INDEX IF NOT EXISTS idx_model_baseline_price_reconciliation_drift
    ON public.model_baseline_price_reconciliation (checked_at DESC)
    WHERE verdict = 'drift';

COMMENT ON TABLE public.model_baseline_price_reconciliation IS
    'Per-model record of each baseline-price reconciliation. Ledger only: no row here ever changes a price. A machine-observed price is a signal for a human, not an authority that may rewrite billing.';

-- ---------------------------------------------------------------------------
-- (c) 供应商实际价 vs 基准价
--
-- 取价口径与 provider/client.go:1643 的 COALESCE 完全一致（凭证绑定优先、
-- pricing_plans 兜底），否则这个视图算出来的偏差和实盘成本对不上——那是
-- 「报了一个数字但没人能用」的典型。
-- ---------------------------------------------------------------------------
CREATE OR REPLACE VIEW public.v_supplier_price_vs_baseline AS
SELECT
    cmb.credential_id,
    pm.raw_model_name,
    COALESCE(pm.canonical_raw_name, mc.canonical_name) AS canonical_name,
    COALESCE(p.id::text, '')                         AS provider_name,
    COALESCE(cmb.currency, 'USD')                    AS supplier_currency,
    COALESCE(cmb.billing_mode, 'per_token')          AS billing_mode,
    cmb.unit_price_in_per_1m                        AS supplier_in_per_1m,
    cmb.unit_price_out_per_1m                       AS supplier_out_per_1m,
    cmb.pricing_source                              AS supplier_pricing_source,
    cmb.pricing_updated_at                          AS supplier_pricing_updated_at,
    mc.baseline_price_currency                      AS baseline_currency,
    mc.baseline_input_price_per_1m                  AS baseline_in_per_1m,
    mc.baseline_output_price_per_1m                 AS baseline_out_per_1m,
    -- 倍率：供应商实付 / 原厂标准。>1 = 比原厂贵，<1 = 有折扣。
    -- 只有在**币种相同**时才算得出来，所以下面同时给出可比性标记。
    CASE
      WHEN mc.baseline_input_price_per_1m IS NULL
        OR mc.baseline_input_price_per_1m = 0
        OR COALESCE(cmb.currency, 'USD') IS DISTINCT FROM COALESCE(mc.baseline_price_currency, 'USD')
      THEN NULL
      ELSE round(cmb.unit_price_in_per_1m / mc.baseline_input_price_per_1m, 4)
    END                                             AS input_price_ratio,
    CASE
      WHEN mc.baseline_output_price_per_1m IS NULL
        OR mc.baseline_output_price_per_1m = 0
        OR COALESCE(cmb.currency, 'USD') IS DISTINCT FROM COALESCE(mc.baseline_price_currency, 'USD')
      THEN NULL
      ELSE round(cmb.unit_price_out_per_1m / mc.baseline_output_price_per_1m, 4)
    END                                             AS output_price_ratio,
    -- 币种不一致时必须显眼：拿 CNY 的供应商价去比 USD 的基准价，出来的
    -- 偏差是纯噪声，但它在报表里长得和真偏差一模一样。
    (COALESCE(cmb.currency, 'USD')
       IS NOT DISTINCT FROM COALESCE(mc.baseline_price_currency, 'USD'))  AS currency_comparable,
    (mc.baseline_input_price_per_1m IS NOT NULL)    AS has_baseline,
    mc.baseline_price_fetched_at                    AS baseline_fetched_at
FROM credential_model_bindings cmb
JOIN provider_models pm ON pm.id = cmb.provider_model_id
JOIN credentials c      ON c.id = cmb.credential_id
LEFT JOIN providers p   ON p.id = c.provider_id
-- ★ 这个 JOIN 原来写成 `ON mc.id = pm.canonical_id OR lower(mc.canonical_name) =
--   lower(pm.canonical_raw_name)`，**真库实测会把一条供应商绑定变成三行**。
--
-- 成因：OR 两侧可以各自命中**不同的** canonical 行。provider_models 的
-- canonical_id 与 canonical_raw_name 由**不同代码路径**写入，不一致是完全
-- 可能的状态；一旦 canonical_id 指向 claude-opus-4-8 而 canonical_raw_name
-- 是 claude-opus-4.8，JOIN 同时命中两行。而仓里 modelname/normalize.go 明确
-- 声明**不做** claude-opus-4-8 ↔ claude-opus-4.8 的跨形态归一，于是
-- models_canonical 里这两个写法可以并存。
--
-- 后果不是「多几行」那么轻：**同一个供应商价被同时报成比基准贵 20% 和
-- 比基准便宜 40%**（基准 5.00 → 1.20，基准 9.99 → 0.60）。运维拿到这两个数
-- 无从裁决；要是对这些行求和，成本就被计了两遍。这是「准确控制成本」这条
-- 目标上最直接的错价。
--
-- 修法：名字匹配那条路**只在 canonical_id 为空时**才走（canonical_id 是
-- 权威，名字是兜底），再用 ORDER BY + LIMIT 1 保证恰好命中一行。
-- models_canonical.canonical_name 上有 UNIQUE 约束（models_canonical_
-- canonical_name_key），所以兜底那条路至多一行。
LEFT JOIN LATERAL (
    SELECT mc.id, mc.canonical_name, mc.baseline_price_currency,
           mc.baseline_input_price_per_1m, mc.baseline_output_price_per_1m,
           mc.baseline_price_fetched_at
      FROM public.models_canonical mc
     WHERE mc.id = pm.canonical_id
        OR (pm.canonical_id IS NULL
            AND lower(mc.canonical_name) = lower(pm.canonical_raw_name))
     ORDER BY (pm.canonical_id IS NOT NULL AND mc.id = pm.canonical_id) DESC
     LIMIT 1
) mc ON true;

COMMENT ON VIEW public.v_supplier_price_vs_baseline IS
    'Per (credential, model) supplier price against the vendor baseline. Ratios are NULL unless the currencies match and the baseline is set — a CNY supplier price divided by a USD baseline produces a number that looks like a deviation but is pure noise, so currency_comparable is exposed rather than folded into the ratio.';

COMMIT;
