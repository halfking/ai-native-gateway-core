-- =============================================================================
-- Migration 403: Reclassify high-perf glm-5.2 credentials token → token_plan
-- Created: 2026-07-15
-- Author:  gateway maintainers (glm-5.2 routing skew incident)
--
-- Root cause (see fix commit "routing: unify 2-seg COALESCE + lift glm-5.2
-- per_token candidates to rank 1"):
--   provider/client.go's candidate ORDER BY ranks `billing_mode` first:
--     free / token_plan / code_plan / monthly   → rank 1 (preferred)
--     per_token                                  → rank 2 (last resort)
--   Several stable, high-concurrency credentials for glm-5.2 were seeded with
--   plan_type='token' (→ billing_mode='per_token'), so the router only tried
--   them AFTER every free/token_plan candidate — including the 28.5%-success
--   NVIDIA `endless` (free) — had failed or saturated. As a result
--   glm-xianyu (100% success, concurrency 100) received only 11 requests in
--   48h while bleeding traffic through flaky rank-1 candidates.
--
-- What this migration does:
--   Promotes four well-performing credentials from plan_type='token' to
--   'token_plan' and aligns their credential_model_bindings.billing_mode so
--   they participate in rank 1 alongside zhipu/sensenova-prod. These were
--   chosen because all four show 100% recent success (where they have
--   traffic) and non-trivial concurrency budgets.
--
--   | cred_id | provider        | label      | concurrency | ok_pct(48h) |
--   |---------|-----------------|------------|-------------|-------------|
--   |      16 | glm-xianyu      | 2026-06-20 |         100 |     100.0 % |
--   |       4 | sensenova       | dd         |          20 |     100.0 % |
--   |       3 | sensenova       | default    |          20 |     100.0 % |
--   |      20 | glm-5.2-oneday  | default    |           2 |      (cold) |
--
--   NOTE: credential_id values are matched by (provider_code, label) so the
--   migration is portable across environments whose sequences differ.
--
-- Idempotency:
--   Both UPDATEs are guarded by plan_type='token' / billing_mode='per_token'
--   predicates, so re-running is a no-op. Safe as a startup migration.
--
-- What this migration does NOT do:
--   - It does not touch free / token_plan credentials (NIM endless, zhipu,
--     scnet, sensenova-prod, nvidia-latest keep their existing ranks).
--   - It does not disable any credential. Flaky rank-1 candidates (e.g. NIM
--     endless at 28.5%) stay routable; lifting the per_token group simply
--     lets the router reach glm-xianyu BEFORE exhausting the rank-1 pool.
-- =============================================================================

\set ON_ERROR_STOP on

\echo '=== 403 glm-5.2 per_token → token_plan promotion ==='

-- ---------------------------------------------------------------------------
-- 0. Pre-flight: show the credentials we are about to promote.
-- ---------------------------------------------------------------------------
\echo '--- 0. pre-flight: candidates currently on rank 2 (per_token) ---'
SELECT
    c.id          AS cred_id,
    p.code        AS provider,
    c.label,
    c.plan_type,
    c.concurrency_limit
FROM credentials c
JOIN providers p ON p.id = c.provider_id
WHERE (p.code, c.label) IN (
          ('glm-xianyu',     '2026-06-20'),
          ('sensenova',      'dd'),
          ('sensenova',      'default'),
          ('glm-5.2-oneday', 'default')
      )
  AND c.plan_type = 'token'
ORDER BY p.code, c.label;

-- ---------------------------------------------------------------------------
-- 1. Promote credentials.plan_type: token → token_plan.
--    DeriveBillingMode() maps 'token_plan' through unchanged, so discovery
--    will keep writing billing_mode='token_plan' for these on future upserts.
-- ---------------------------------------------------------------------------
UPDATE credentials c
SET plan_type = 'token_plan',
    updated_at = NOW()
FROM providers p
WHERE p.id = c.provider_id
  AND (p.code, c.label) IN (
          ('glm-xianyu',     '2026-06-20'),
          ('sensenova',      'dd'),
          ('sensenova',      'default'),
          ('glm-5.2-oneday', 'default')
      )
  AND c.plan_type = 'token';

-- ---------------------------------------------------------------------------
-- 2. Align cmb.billing_mode for existing bindings so the change takes effect
--    immediately (model_offers is a VIEW over cmb; the router reads it on
--    every request). The discovery upsert's ON CONFLICT branch does NOT
--    rewrite billing_mode, so without this UPDATE the existing rows would
--    keep billing_mode='per_token' until manually rebound.
-- ---------------------------------------------------------------------------
UPDATE credential_model_bindings cmb
SET billing_mode = 'token_plan',
    updated_at = NOW()
FROM credentials c
JOIN providers p ON p.id = c.provider_id
WHERE c.id = cmb.credential_id
  AND (p.code, c.label) IN (
          ('glm-xianyu',     '2026-06-20'),
          ('sensenova',      'dd'),
          ('sensenova',      'default'),
          ('glm-5.2-oneday', 'default')
      )
  AND cmb.billing_mode = 'per_token';

-- ---------------------------------------------------------------------------
-- 3. Post-flight: confirm the four credentials are now token_plan and their
--    glm-5.2 bindings are rank 1. Expect 4 rows.
-- ---------------------------------------------------------------------------
\echo '--- 3. post-flight: promoted credentials and their glm-5.2 billing_mode ---'
SELECT
    p.code        AS provider,
    c.label,
    c.plan_type,
    pm.raw_model_name,
    cmb.billing_mode
FROM credentials c
JOIN providers p              ON p.id = c.provider_id
JOIN credential_model_bindings cmb ON cmb.credential_id = c.id
JOIN provider_models pm       ON pm.id = cmb.provider_model_id
WHERE (p.code, c.label) IN (
          ('glm-xianyu',     '2026-06-20'),
          ('sensenova',      'dd'),
          ('sensenova',      'default'),
          ('glm-5.2-oneday', 'default')
      )
  AND pm.canonical_raw_name = 'glm-5.2'
ORDER BY p.code, c.label;

\echo '=== 403 glm-5.2 per_token → token_plan promotion: done ==='
