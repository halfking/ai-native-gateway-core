-- ============================================================
-- Phase 23 / 99 — Final verification report
--
-- Run after applying 00..03. Reports:
--   - All public partitions and their storage
--   - Non-compliant partitions
--   - Function / trigger presence
-- ============================================================

\echo ''
\echo '=== Phase 23 verification ==='

\echo ''
\echo '--- columnar_healthcheck() ---'
SELECT parent_name, partition_name, storage, expected, compliant,
       pg_size_pretty(total_size_bytes) AS size,
       n_live_tup AS rows
FROM columnar_healthcheck()
WHERE compliant = false
ORDER BY parent_name, partition_name;

\echo ''
\echo '--- columnar_drift_report() ---'
SELECT parent_name, compliant_count, noncompliant_count,
       pg_size_pretty(total_size_bytes) AS total,
       pg_size_pretty(heap_size_bytes)     AS heap_bytes,
       pg_size_pretty(columnar_size_bytes) AS columnar_bytes
FROM columnar_drift_report();

\echo ''
\echo '--- ensure functions ---'
SELECT proname, pronargs
FROM pg_proc
WHERE proname LIKE 'ensure_%_partition'
ORDER BY proname;

\echo ''
\echo '--- event trigger ---'
SELECT evtname, evtenabled, evtevent
FROM pg_event_trigger
WHERE evtname = 'enforce_columnar_trigger';

\echo ''
\echo '--- columnar_heal result (idempotent — should be empty on a healthy DB) ---'
SELECT *
FROM columnar_heal();

\echo ''
\echo '=== Phase 23 verification complete ==='
