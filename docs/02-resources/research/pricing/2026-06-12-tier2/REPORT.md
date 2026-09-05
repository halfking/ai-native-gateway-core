# Tier 2 Pricing Rollout — 2026-06-12 18:35

## Coverage
| Phase | tier_1 (Phase E) | tier_2 (this rollout) | Total |
|---|---|---|---|
| **Offers priced** | 66 (35%) | 124 (66%) | **190 (100%)** |
| - token billing_mode | 52 | 119 | 171 |
| - token_plan billing_mode | 14 | 5 | 19 |
| - free (plan_meta.flag) | 0 | 5 | 5 |
| **Pricing source = 'imported'** | 66 | 124 | 190 |

## Tier 2 CSV (124 rows)
- `tier2-pricing.csv` — combined (all billing modes)
- `tier2-token.csv` — 119 rows for `token` billing_mode (uploaded via Go `/api/pricing/import`)
- `tier2-token-plan.csv` — 5 rows for `token_plan` (applied via direct psql)

## Sources (manual lookup, 130-model table)
| Source | Count | Notes |
|---|---|---|
| Tier 1 forward (already in cmb) | 92 | Same canonical name priced in tier_1 |
| OpenRouter `api/v1/models` direct match | 26 | openrouter_ai/models |
| OpenRouter prefix-strip + lookup | 5 | haiku-4.6/opus-4.6/opus-4.7/sonnet-4/sonnet-4.6 |
| Manual estimate (NVIDIA NIM = upstream) | 1 | jamba-1.5-large-instruct, mistral-medium-3.5 etc. |
| **Zhipu Flash free tier (plan_meta.flag)** | 5 | glm-4-flash, glm-4.5-flash, glm-4.7-flash, glm-4v-flash, glm-z1-flash |

## SQL: xiaomi token-plan
- `xiaomi_tokenplan_tier2.sql` — 5 `INSERT pricing_plans` (token_plan) + 5 `UPDATE credential_model_bindings`

## Final state
- `pricing_plans`: 14 (tier_1) + 5 (tier_2) = **19 token_plan** + tier_1's existing 52 token = 71 rows
- `credential_model_bindings`: 100% covered (priced_in=184, free=5)
- `pricing_plans.source = 'scraped'`: 19 rows; phase E bulk had 66 token rows for tier_1 + 14 token_plan for tier_1 (8 was 14 already in DB before)
