# 2026-08-23 cost_usd / SessionStats KPI fix (migration 565)

## Problem
`request_logs_hot.cost_usd` was NULL for ~100% of 7d traffic on 154 despite token usage, so `session_summaries.total_cost_usd` stayed near zero (563 trigger sums `COALESCE(cost_usd,0)`).

## Root cause
- Most `credential_model_bindings` rows had NULL unit prices; candidate SQL coerced NULL→0 so `CalcCost` returned nil.
- CNY→`cost_display` branch in handler was dead code; non-USD native cost never landed in KPI columns.

## Fix
- `AssignRequestCost` in streaming: USD→`cost_usd`; non-USD→`cost_display` + FX 7.2→`cost_usd`.
- Candidate query: stop `COALESCE(price,0)`; fallback to `pricing_plans.plan_json`.
- Migration 565: plans backfill, inherit, `catalog_estimate` for gpt-5.6-* ($2.5/$15 KPI estimate), hot cost recompute.
- Re-run **564 only** (GREATEST) after 565 — do **not** re-run 563 REPLACE.

## Note
`catalog_estimate` prices are for KPI visibility, not billing settlement truth.
