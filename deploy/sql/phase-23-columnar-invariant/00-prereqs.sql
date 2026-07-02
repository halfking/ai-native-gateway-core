-- ============================================================
-- Phase 23 — Columnar Storage Invariant (durable enforcement)
--
-- Generated: 2026-07-02
-- Purpose: Make the columnar access method a permanent property of
--          append-only / time-series tables so the system stays
--          compliant without manual one-off conversions.
--
-- Files in this phase (run in order):
--
--   00-prereqs.sql                — extension checks, safety wrapper
--   01-rewrite-ensure-functions.sql — patch ensure_<t>_partition() to
--                                    emit USING columnar for INSERT-only
--                                    parents
--   02-event-trigger.sql          — runtime safety net that converts any
--                                    newly-attached heap partition of an
--                                    INSERT-only parent into columnar
--   03-healthcheck-and-heal.sql   — columnar_healthcheck() +
--                                    columnar_heal() functions used by
--                                    boot checks and the daily cron
--   99-verify.sql                 — final report
--
-- Audit basis (2026-07-02):
--   - go source for request_logs: domains/hooks/observability/telemetry/client.go
--     (lines 836, 782, 810 contain UPDATE statements). → STAYS HEAP.
--   - go source for request_wal: domains/hooks/observability/telemetry/
--     request_logger.go:243 contains UPDATE statements. → STAYS HEAP.
--   - go source for usage_ledger: same file as request_logs (lines 782, 810).
--     → STAYS HEAP.
--   - go source for routing_decision_log: INSERT-only (admin/telemetry.go,
--     routing_resolve_probe.go). → COLUMNAR.
--   - go source for credential_model_index: INSERT-only
--     (admin/credential_monitor.go and credential_success_rate.go DELETE
--      the entire table on user-recount, not row-level UPDATE).
--     → COLUMNAR.
--   - go source for request_logs: response/outbound/request_body JSONB
--     overflow Citus columnar's 1 GB serialization buffer (see
--     db/migrations/318b_request_logs_archive_heap.sql). Once body columns
--     are split into request_logs_bodies (separate migration 328), the
--     metadata-only partition can be re-classified as columnar-eligible.
--
-- Idempotency: every file uses CREATE OR REPLACE / DROP IF EXISTS guards.
-- Running this phase twice produces no diffs.
--
-- ============================================================

\echo 'Phase 23: Columnar Storage Invariant'
\echo 'Generated: 2026-07-02'
\echo ''

-- Pre-flight: extensions must exist
SELECT
    CASE
        WHEN EXISTS (SELECT 1 FROM pg_extension WHERE extname='citus_columnar')
        THEN 'OK: citus_columnar extension present'
        ELSE 'FATAL: citus_columnar extension missing — run phase-22 first'
    END AS prerequisite_check;
