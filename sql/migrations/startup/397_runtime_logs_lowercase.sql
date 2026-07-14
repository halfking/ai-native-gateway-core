-- =============================================================================
-- Migration 397: runtime logs / aliases lowercase normalisation
-- Created:     2026-07-14 (round 2; runs AFTER gateway is redeployed)
-- Author:      gateway maintainers
--
-- Prerequisite (operator steps before running this migration):
--   1. Migrations 394 + 395 + 396 applied (gateway-managed catalog
--      rows are lowercase already).
--   2. Gateway redeployed with the 2026-07-14 code so that:
--        - modelname.CanonicalizeClientModel is wired into all
--          chat / messages / responses handlers.
--        - provider/client.go + resolve/resolve.go compare against the
--          lowercase canonical_raw_name column.
--        - modelcatalog.UpsertCredentialModel writes
--          provider_models.canonical_raw_name in lowercase.
--      Once the redeploy has been live for at least 24h (so existing
--      in-flight sessions drain), the only remaining mixed-case data
--      is the historical request_logs + model_aliases + model_offer_events
--      + model_probe_state written by the OLD code.
--   3. Run this migration once to bring the historical data into the
--      same lowercase invariant so that:
--        - dashboards, requests-logs search and incident queries all
--          match consistently
--        - model_aliases.raw_name = model_aliases.raw_name used in
--          telemetry joins doesn't drift on case
--        - request_logs.client_model / outbound_model can be joined
--          against provider_models.canonical_raw_name without lower()
--
-- What this migration does:
--   1. request_logs.client_model → lower(client_model)
--      request_logs.outbound_model → lower(outbound_model)
--      (only when the value is mixed-case; NULL/empty untouched).
--   2. model_aliases.raw_name     → lower(raw_name) (already covered by
--      migration 396, but we re-run idempotently for any rows that the
--      AliasSyncService inserted between 396 and this migration).
--   3. model_offer_events.raw_model_name → lower(raw_model_name)
--   4. model_probe_state.raw_model_name   → lower(raw_model_name)
--   5. credential_model_stats_1m.raw_model / credential_model_peak_1m.raw_model
--      → lower(raw_model) (so /metrics aggregates stay consistent with
--      the new request_logs schema).
--   6. Re-index check: report any mixed-case rows left (expect 0).
--
-- What this migration does NOT do:
--   - It does NOT touch provider_models.raw_model_name /
--     outbound_model_name (those carry the upstream case; preserved).
--   - It does NOT touch request_logs.client_request_id / ids etc.
-- =============================================================================

\set ON_ERROR_STOP on

\echo '=== 397 runtime logs lowercase normalisation (post-redeploy) ==='

-- ---------------------------------------------------------------------------
-- 0. Snapshot: how many rows need normalisation?
-- ---------------------------------------------------------------------------
\echo '--- 0. snapshot before ---'
SELECT
    (SELECT COUNT(*) FROM request_logs_with_current_month rl
       WHERE (rl.client_model IS NOT NULL AND rl.client_model <> lower(rl.client_model))
          OR (rl.outbound_model IS NOT NULL AND rl.outbound_model <> lower(rl.outbound_model))) AS mixed_rl,
    (SELECT COUNT(*) FROM model_aliases WHERE raw_name <> lower(raw_name)) AS mixed_alias,
    (SELECT COUNT(*) FROM model_offer_events WHERE raw_model_name <> lower(raw_model_name)) AS mixed_offer,
    (SELECT COUNT(*) FROM model_probe_state WHERE raw_model_name <> lower(raw_model_name)) AS mixed_probe,
    (SELECT COUNT(*) FROM credential_model_stats_1m WHERE raw_model <> lower(raw_model)) AS mixed_stats_1m,
    (SELECT COUNT(*) FROM credential_model_peak_1m  WHERE raw_model <> lower(raw_model)) AS mixed_peak_1m;

-- ---------------------------------------------------------------------------
-- 1. request_logs_with_current_month (the view that unions hot + cold).
--    We update BOTH the underlying base table partitions and the view.
-- ---------------------------------------------------------------------------
\echo '--- 1. request_logs.client_model / outbound_model lowercase ---'
UPDATE request_logs
SET client_model = lower(client_model)
WHERE client_model IS NOT NULL
  AND client_model <> ''
  AND client_model <> lower(client_model);

UPDATE request_logs
SET outbound_model = lower(outbound_model)
WHERE outbound_model IS NOT NULL
  AND outbound_model <> ''
  AND outbound_model <> lower(outbound_model);

-- ---------------------------------------------------------------------------
-- 2. model_aliases (idempotent re-run of 396's normalisation).
-- ---------------------------------------------------------------------------
\echo '--- 2. model_aliases.raw_name lowercase ---'
UPDATE model_aliases
SET raw_name = lower(raw_name)
WHERE raw_name <> lower(raw_name);

-- ---------------------------------------------------------------------------
-- 3. model_offer_events.
-- ---------------------------------------------------------------------------
\echo '--- 3. model_offer_events.raw_model_name lowercase ---'
UPDATE model_offer_events
SET raw_model_name = lower(raw_model_name)
WHERE raw_model_name <> lower(raw_model_name);

-- ---------------------------------------------------------------------------
-- 4. model_probe_state.
-- ---------------------------------------------------------------------------
\echo '--- 4. model_probe_state.raw_model_name lowercase ---'
UPDATE model_probe_state
SET raw_model_name = lower(raw_model_name)
WHERE raw_model_name <> lower(raw_model_name);

-- ---------------------------------------------------------------------------
-- 5. credential_model_stats_1m + credential_model_peak_1m (the time-series
--    bucket tables used by /metrics and route-incidents dashboards).
-- ---------------------------------------------------------------------------
\echo '--- 5. credential_model_stats_1m / credential_model_peak_1m lowercase ---'
UPDATE credential_model_stats_1m
SET raw_model = lower(raw_model)
WHERE raw_model <> lower(raw_model);

UPDATE credential_model_peak_1m
SET raw_model = lower(raw_model)
WHERE raw_model <> lower(raw_model);

-- ---------------------------------------------------------------------------
-- 6. credential_model_call_history.raw_model.
-- ---------------------------------------------------------------------------
\echo '--- 6. credential_model_call_history.raw_model lowercase ---'
UPDATE credential_model_call_history
SET raw_model = lower(raw_model)
WHERE raw_model <> lower(raw_model);

-- ---------------------------------------------------------------------------
-- 7. candidate_failure_logs.raw_model_name.
-- ---------------------------------------------------------------------------
\echo '--- 7. candidate_failure_logs.raw_model_name lowercase ---'
UPDATE candidate_failure_logs
SET raw_model_name = lower(raw_model_name)
WHERE raw_model_name <> lower(raw_model_name);

-- ---------------------------------------------------------------------------
-- 8. Post-flight: every operational column we touched must be lowercase.
-- ---------------------------------------------------------------------------
\echo '--- 8. postflight mixed-case counts (expect all 0) ---'
SELECT
    (SELECT COUNT(*) FROM request_logs
       WHERE (client_model IS NOT NULL AND client_model <> lower(client_model))
          OR (outbound_model IS NOT NULL AND outbound_model <> lower(outbound_model))) AS mixed_rl,
    (SELECT COUNT(*) FROM model_aliases WHERE raw_name <> lower(raw_name)) AS mixed_alias,
    (SELECT COUNT(*) FROM model_offer_events WHERE raw_model_name <> lower(raw_model_name)) AS mixed_offer,
    (SELECT COUNT(*) FROM model_probe_state WHERE raw_model_name <> lower(raw_model_name)) AS mixed_probe,
    (SELECT COUNT(*) FROM credential_model_stats_1m WHERE raw_model <> lower(raw_model)) AS mixed_stats_1m,
    (SELECT COUNT(*) FROM credential_model_peak_1m  WHERE raw_model <> lower(raw_model)) AS mixed_peak_1m,
    (SELECT COUNT(*) FROM credential_model_call_history WHERE raw_model <> lower(raw_model)) AS mixed_call_hist,
    (SELECT COUNT(*) FROM candidate_failure_logs WHERE raw_model_name <> lower(raw_model_name)) AS mixed_cand_fail;

\echo '=== 397 runtime logs lowercase normalisation: done ==='
