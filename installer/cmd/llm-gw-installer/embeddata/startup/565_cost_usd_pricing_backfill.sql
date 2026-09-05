-- 565_cost_usd_pricing_backfill.sql
-- Backfill missing offer prices and recompute request_logs_hot.cost_usd so
-- session_summaries KPI (563 trigger + 564 GREATEST) can aggregate non-zero cost.
--
-- Idempotent: YES (COALESCE / NULL guards / only NULL cost rows updated)
-- Do NOT re-run 563 REPLACE; run 564 after this migration for summary lift.

BEGIN;

-- 1) pricing_plans → credential_model_bindings
UPDATE credential_model_bindings cmb
SET
    unit_price_in_per_1m = COALESCE(cmb.unit_price_in_per_1m, sub.plan_in),
    unit_price_out_per_1m = COALESCE(cmb.unit_price_out_per_1m, sub.plan_out),
    currency = COALESCE(NULLIF(cmb.currency, ''), sub.plan_currency, 'USD'),
    pricing_source = COALESCE(cmb.pricing_source, 'pricing_plans'),
    pricing_updated_at = NOW()
FROM (
    SELECT DISTINCT ON (cmb.id)
        cmb.id AS binding_id,
        NULLIF(pp.plan_json->>'input_per_1m', '')::numeric AS plan_in,
        NULLIF(pp.plan_json->>'output_per_1m', '')::numeric AS plan_out,
        pp.currency AS plan_currency
    FROM credential_model_bindings cmb
    JOIN provider_models pm ON pm.id = cmb.provider_model_id
    JOIN pricing_plans pp ON pp.model_canonical_id = pm.canonical_id
        AND pp.effective_to IS NULL
        AND (pp.credential_id = cmb.credential_id OR pp.credential_id IS NULL)
    WHERE (cmb.unit_price_in_per_1m IS NULL OR cmb.unit_price_out_per_1m IS NULL)
      AND (
          NULLIF(pp.plan_json->>'input_per_1m', '') IS NOT NULL
          OR NULLIF(pp.plan_json->>'output_per_1m', '') IS NOT NULL
      )
    ORDER BY cmb.id,
        CASE WHEN pp.credential_id = cmb.credential_id THEN 0 ELSE 1 END,
        pp.effective_from DESC NULLS LAST
) sub
WHERE cmb.id = sub.binding_id;

-- 2) inherit priced offer within same provider + raw model
UPDATE credential_model_bindings tgt
SET
    unit_price_in_per_1m = src.unit_price_in_per_1m,
    unit_price_out_per_1m = src.unit_price_out_per_1m,
    cache_read_price_per_1m = COALESCE(tgt.cache_read_price_per_1m, src.cache_read_price_per_1m),
    cache_write_price_per_1m = COALESCE(tgt.cache_write_price_per_1m, src.cache_write_price_per_1m),
    currency = COALESCE(NULLIF(tgt.currency, ''), src.currency, 'USD'),
    billing_mode = COALESCE(NULLIF(tgt.billing_mode, ''), src.billing_mode),
    pricing_source = 'inherited',
    pricing_updated_at = NOW()
FROM credential_model_bindings tgt_row
JOIN provider_models pm_t ON pm_t.id = tgt_row.provider_model_id
JOIN credentials ct ON ct.id = tgt_row.credential_id
JOIN credential_model_bindings src ON src.credential_id <> tgt_row.credential_id
JOIN provider_models pm_s ON pm_s.id = src.provider_model_id
    AND pm_s.raw_model_name = pm_t.raw_model_name
JOIN credentials cs ON cs.id = src.credential_id AND cs.provider_id = ct.provider_id
WHERE tgt.id = tgt_row.id
  AND tgt_row.unit_price_in_per_1m IS NULL
  AND tgt_row.unit_price_out_per_1m IS NULL
  AND COALESCE(tgt_row.billing_mode, '') <> 'free'
  AND (
      COALESCE(src.unit_price_in_per_1m, 0) > 0
      OR COALESCE(src.unit_price_out_per_1m, 0) > 0
  );

-- 3) catalog_estimate for dominant gpt-5.6 aliases (KPI estimate, not billing truth)
UPDATE credential_model_bindings cmb
SET
    unit_price_in_per_1m = 2.5,
    unit_price_out_per_1m = 15.0,
    currency = 'USD',
    pricing_source = 'catalog_estimate',
    pricing_updated_at = NOW()
FROM provider_models pm
WHERE pm.id = cmb.provider_model_id
  AND pm.raw_model_name IN ('gpt-5.6-terra', 'gpt-5.6-sol', 'gpt-5.6-luna')
  AND cmb.unit_price_in_per_1m IS NULL
  AND cmb.unit_price_out_per_1m IS NULL;

-- 4) recompute hot rows with priced offers + tokens
WITH priced AS (
    SELECT
        h.request_id,
        h.ts,
        COALESCE(h.prompt_tokens, 0)::float8 AS pt,
        COALESCE(h.completion_tokens, 0)::float8 AS ct,
        COALESCE(mo.unit_price_in_per_1m, 0)::float8 AS pin,
        COALESCE(mo.unit_price_out_per_1m, 0)::float8 AS pout,
        COALESCE(NULLIF(mo.currency, ''), 'USD') AS currency
    FROM request_logs_hot h
    JOIN LATERAL (
        SELECT mo.*
        FROM model_offers mo
        WHERE mo.credential_id = h.credential_id
          AND (
              mo.raw_model_name = h.outbound_model
              OR mo.raw_model_name = h.provider_model
          )
        ORDER BY mo.id DESC
        LIMIT 1
    ) mo ON TRUE
    WHERE h.cost_usd IS NULL
      AND (h.prompt_tokens IS NOT NULL OR h.completion_tokens IS NOT NULL)
      AND (
          COALESCE(mo.unit_price_in_per_1m, 0) > 0
          OR COALESCE(mo.unit_price_out_per_1m, 0) > 0
      )
),
computed AS (
    SELECT
        request_id,
        ts,
        currency,
        ROUND(
            ((pt * pin + ct * pout) / 1000000.0)::numeric,
            8
        ) AS native_cost
    FROM priced
    WHERE (pt * pin + ct * pout) > 0
)
UPDATE request_logs_hot h
SET
    cost_usd = CASE
        WHEN UPPER(c.currency) IN ('', 'USD') THEN c.native_cost
        ELSE ROUND((c.native_cost / 7.2)::numeric, 8)
    END,
    cost_display = CASE
        WHEN UPPER(c.currency) NOT IN ('', 'USD') THEN c.native_cost
        ELSE NULL
    END,
    cost_currency = CASE
        WHEN UPPER(c.currency) NOT IN ('', 'USD') THEN c.currency
        ELSE NULL
    END
FROM computed c
WHERE h.request_id = c.request_id
  AND h.ts = c.ts;

COMMIT;
