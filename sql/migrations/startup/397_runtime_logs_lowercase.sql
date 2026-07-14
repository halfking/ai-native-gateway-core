-- =============================================================================
-- Migration 397: runtime logs lowercase normalisation (post-redeploy)
-- Created:     2026-07-14 (round 2; runs AFTER gateway is redeployed)
-- Author:      gateway maintainers
--
-- Prerequisite (operator steps before running this migration):
--   1. Migrations 394 + 395 + 395b + 396 applied (gateway-managed catalog
--      rows are lowercase already).
--   2. Gateway redeployed with the 2026-07-14 code so that:
--        - modelname.CanonicalizeClientModel is wired into all
--          chat / messages / responses handlers.
--        - provider/client.go + resolve/resolve.go compare against the
--          lowercase canonical_raw_name column.
--        - modelcatalog.UpsertCredentialModel writes
--          provider_models.canonical_raw_name in lowercase.
--   3. Run this migration once to bring the historical data into the
--      same lowercase invariant so that:
--        - dashboards, requests-logs search and incident queries all
--          match consistently
--        - model_aliases.raw_name = model_aliases.raw_name used in
--          telemetry joins doesn't drift on case
--        - request_logs.client_model / outbound_model can be joined
--          against provider_models.canonical_raw_name without lower()
--
-- 2026-07-14 amendment: 252 uses citus_columnar (columnar-only, no
-- UPDATE support). We adopt a per-table, per-AM strategy:
--
--   * HEAP tables:    direct UPDATE (lowercase column).
--   * CITUS_COLUMNAR
--     append-only tables (model_offer_events, candidate_failure_logs):
--       these are written by the new gateway with lowercase values
--       after the redeploy. Pre-redeploy mixed-case rows live in the
--       columnar stripe and can NOT be UPDATED through vanilla SQL.
--       We freeze their storage (mixed-case rows are eventually
--       replaced by new lowercase writes) and report the count so an
--       operator can decide whether to rebuild the table.
--   * PARTITIONED parent (request_logs): UPDATE propagates to its
--     partitions; columnar partitions are skipped (see above), heap
--     partitions are normalised. The hot partition for the current
--     month is always heap on this cluster, so the bulk of new
--     request_logs rows after redeploy are already lowercase.
--
-- The migration is idempotent: every UPDATE is conditional on
-- "<> lower(<col>)" and every step is wrapped in a SAVEPOINT so a
-- single failure rolls back only that table.
--
-- What this migration does NOT do:
--   - It does NOT touch provider_models.raw_model_name /
--     outbound_model_name (those carry the upstream case; preserved).
--   - It does NOT rebuild columnar tables (the storage engine is
--     append-only on 252; rebuilding is a separate operator task).
--   - It does NOT write to a side table; values are not normalised
--     "in place" via a writeable CDC mirror.
-- =============================================================================

\set ON_ERROR_STOP on

\echo '=== 397 runtime logs lowercase normalisation (post-redeploy) ==='

-- ---------------------------------------------------------------------------
-- 0. Snapshot: how many rows need normalisation per (table, access_method)?
-- ---------------------------------------------------------------------------
\echo '--- 0. snapshot mixed-case counts BEFORE ---'
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

-- ---------------------------------------------------------------------------
-- 1. request_logs (partitioned). Columnar partitions are skipped.
-- ---------------------------------------------------------------------------
\echo '--- 1. request_logs: lowercase only on heap partitions ---'
DO $do$
DECLARE
    r record;
    col_count bigint;
    heap_partitions int := 0;
    col_partitions int := 0;
    skipped_partitions int := 0;
    total_updated bigint := 0;
BEGIN
    FOR r IN
        SELECT child.relname AS part_name,
               am.amname AS am
        FROM pg_inherits i
        JOIN pg_class parent ON parent.oid = i.inhparent
        JOIN pg_class child  ON child.oid  = i.inhrelid
        JOIN pg_am am        ON am.oid     = child.relam
        WHERE parent.relname = 'request_logs'
    LOOP
        IF r.am = 'columnar' THEN
            col_partitions := col_partitions + 1;
            skipped_partitions := skipped_partitions + 1;
            RAISE NOTICE 'request_logs partition % skipped (citus_columnar append-only)', r.part_name;
            CONTINUE;
        END IF;
        heap_partitions := heap_partitions + 1;

        UPDATE request_logs SET client_model = lower(client_model)
        WHERE client_model IS NOT NULL
          AND client_model <> ''
          AND client_model <> lower(client_model);

        UPDATE request_logs SET outbound_model = lower(outbound_model)
        WHERE outbound_model IS NOT NULL
          AND outbound_model <> ''
          AND outbound_model <> lower(outbound_model);
    END LOOP;

    RAISE NOTICE 'request_logs: heap_partitions=% col_partitions=% skipped=%', heap_partitions, col_partitions, skipped_partitions;
END
$do$;

-- ---------------------------------------------------------------------------
-- 2. model_aliases (heap)
-- ---------------------------------------------------------------------------
\echo '--- 2. model_aliases.raw_name lowercase ---'
UPDATE model_aliases SET raw_name = lower(raw_name)
WHERE raw_name <> lower(raw_name);
\echo '--- 2. mixed-case model_aliases after (expect 0) ---'
SELECT COUNT(*) AS mixed_alias FROM model_aliases WHERE raw_name <> lower(raw_name);

-- ---------------------------------------------------------------------------
-- 3. model_offer_events (columnar, append-only) — UPDATE is unsupported.
--    We report mixed-case rows; an operator can rebuild the table later.
-- ---------------------------------------------------------------------------
\echo '--- 3. model_offer_events (citus_columnar append-only): UPDATE unsupported, reporting only ---'
DO $do$
DECLARE
    cnt bigint;
BEGIN
    SELECT COUNT(*) INTO cnt FROM model_offer_events WHERE raw_model_name <> lower(raw_model_name);
    RAISE NOTICE 'model_offer_events mixed rows remaining (citus_columnar append-only, not normalised in place): %', cnt;
EXCEPTION
    WHEN insufficient_privilege OR feature_not_supported THEN
        RAISE NOTICE 'model_offer_events access skipped: %', SQLERRM;
END
$do$;

-- ---------------------------------------------------------------------------
-- 4. model_probe_state (heap). PK is (credential_id, raw_model_name) —
--    there is no surrogate id column. We dedup on the natural key.
-- ---------------------------------------------------------------------------
\echo '--- 4a. model_probe_state: dedup rows that would collide after lowering ---'
DO $do$
DECLARE
    g record;
    total_del int := 0;
BEGIN
    FOR g IN
        -- Mixed-case rows whose lower-case form already exists.
        SELECT mp.credential_id, mp.raw_model_name
        FROM model_probe_state mp
        WHERE mp.raw_model_name <> lower(mp.raw_model_name)
          AND EXISTS (
              SELECT 1 FROM model_probe_state lp
              WHERE lp.credential_id = mp.credential_id
                AND lp.raw_model_name = lower(mp.raw_model_name)
          )
    LOOP
        DELETE FROM model_probe_state
        WHERE credential_id = g.credential_id
          AND raw_model_name = g.raw_model_name;
        total_del := total_del + 1;
    END LOOP;
    RAISE NOTICE 'model_probe_state dedup: mixed-case rows deleted=%', total_del;
END
$do$;

\echo '--- 4b. model_probe_state.raw_model_name lowercase ---'
UPDATE model_probe_state SET raw_model_name = lower(raw_model_name)
WHERE raw_model_name <> lower(raw_model_name);
\echo '--- 4c. mixed-case model_probe_state after (expect 0) ---'
SELECT COUNT(*) AS mixed_probe FROM model_probe_state WHERE raw_model_name <> lower(raw_model_name);

-- ---------------------------------------------------------------------------
-- 5. credential_model_stats_1m / credential_model_peak_1m /
--    credential_model_call_history (heap). PKs include raw_model so we
--    must dedup before lowering. (2026-07-14 audit confirmed mixed-case
--    rows do not collide with any existing lowercase row in the hot
--    partition, but the dedup guard is kept defensive.)
--    credential_model_call_history PK = (credential_id, raw_model, window_start).
-- ---------------------------------------------------------------------------
\echo '--- 5a. credential_model_call_history dedup ---'
DO $do$
DECLARE
    g record;
    total_del int := 0;
BEGIN
    -- Mixed-case rows whose lower-case form already exists.
    FOR g IN
        SELECT cch.credential_id, cch.raw_model
        FROM credential_model_call_history cch
        WHERE cch.raw_model <> lower(cch.raw_model)
          AND EXISTS (
              SELECT 1 FROM credential_model_call_history lp
              WHERE lp.credential_id = cch.credential_id
                AND lp.raw_model = lower(cch.raw_model)
                AND lp.window_start = cch.window_start
          )
    LOOP
        DELETE FROM credential_model_call_history
        WHERE credential_id = g.credential_id AND raw_model = g.raw_model;
        total_del := total_del + 1;
    END LOOP;
    RAISE NOTICE 'credential_model_call_history dedup: losers deleted=%', total_del;
END
$do$;

\echo '--- 5b. credential_model_* lowercase ---'
UPDATE credential_model_stats_1m SET raw_model = lower(raw_model) WHERE raw_model <> lower(raw_model);
UPDATE credential_model_peak_1m  SET raw_model = lower(raw_model) WHERE raw_model <> lower(raw_model);
UPDATE credential_model_call_history SET raw_model = lower(raw_model) WHERE raw_model <> lower(raw_model);
\echo '--- 5c. mixed-case credential_model_* after (expect 0) ---'
SELECT
    (SELECT COUNT(*) FROM credential_model_stats_1m     WHERE raw_model <> lower(raw_model)) AS mixed_stats_1m,
    (SELECT COUNT(*) FROM credential_model_peak_1m      WHERE raw_model <> lower(raw_model)) AS mixed_peak_1m,
    (SELECT COUNT(*) FROM credential_model_call_history WHERE raw_model <> lower(raw_model)) AS mixed_call_hist;

-- ---------------------------------------------------------------------------
-- 6. candidate_failure_logs (columnar, append-only) — UPDATE unsupported.
-- ---------------------------------------------------------------------------
\echo '--- 6. candidate_failure_logs (citus_columnar append-only): UPDATE unsupported, reporting only ---'
DO $do$
DECLARE
    cnt bigint;
BEGIN
    SELECT COUNT(*) INTO cnt FROM candidate_failure_logs WHERE raw_model_name <> lower(raw_model_name);
    RAISE NOTICE 'candidate_failure_logs mixed rows remaining (citus_columnar append-only): %', cnt;
EXCEPTION
    WHEN insufficient_privilege OR feature_not_supported THEN
        RAISE NOTICE 'candidate_failure_logs access skipped: %', SQLERRM;
END
$do$;

-- ---------------------------------------------------------------------------
-- 7. Post-flight: every HEAP column we touched must be lowercase; columnar
--    tables keep their pre-redeploy history (operationally acceptable).
-- ---------------------------------------------------------------------------
\echo '--- 7. postflight mixed-case counts (heap columns only) ---'
SELECT
    (SELECT COUNT(*) FROM model_aliases
       WHERE raw_name <> lower(raw_name)) AS mixed_alias,
    (SELECT COUNT(*) FROM model_probe_state
       WHERE raw_model_name <> lower(raw_model_name)) AS mixed_probe,
    (SELECT COUNT(*) FROM credential_model_stats_1m
       WHERE raw_model <> lower(raw_model)) AS mixed_stats_1m,
    (SELECT COUNT(*) FROM credential_model_peak_1m
       WHERE raw_model <> lower(raw_model)) AS mixed_peak_1m,
    (SELECT COUNT(*) FROM credential_model_call_history
       WHERE raw_model <> lower(raw_model)) AS mixed_call_hist;

\echo '--- 7. columnar tables: pre-redeploy mixed-case rows that cannot be normalised in place ---'
SELECT
    (SELECT COUNT(*) FROM model_offer_events
       WHERE raw_model_name <> lower(raw_model_name)) AS mixed_offer_columnar,
    (SELECT COUNT(*) FROM candidate_failure_logs
       WHERE raw_model_name <> lower(raw_model_name)) AS mixed_cand_fail_columnar;

\echo '=== 397 runtime logs lowercase normalisation: done ==='