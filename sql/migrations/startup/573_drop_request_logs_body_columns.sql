-- Migration 573: Drop request body columns from request_logs_hot / request_logs
-- (parent + all monthly partitions via PG 11+ propagation), and rebuild
-- request_logs_with_current_month view so it no longer projects the dropped
-- columns. Body content remains exclusively in request_logs_bodies_hot /
-- request_logs_bodies (and the bodies UNION view), to which all readers
-- must join when they need body payloads.
--
-- Date: 2026-08-24
--
-- Background
-- ──────────
-- 1. The historical "everything in request_logs_hot" schema carries three
--    jsonb body columns (request_body / response_body / outbound_body) on
--    every row, even though writers always set them to NULL after the
--    request_logs_bodies_hot decoupling (migration 341 + 328a).
-- 2. The bodies live in request_logs_bodies_hot / request_logs_bodies.
--    Every reader that needs bodies already LEFT JOINs the bodies view,
--    so the duplicate columns on the request_logs side are pure
--    storage overhead (12-18 KB/row × ~5M rows ≈ 60-90 GB heap).
-- 3. PostgreSQL 11+ propagates ALTER TABLE ... DROP COLUMN to partitioned
--    parent tables automatically; all existing monthly partitions
--    (request_logs_2026_07 / 2026_08 / ...) inherit the change. No per-
--    partition DDL required.
-- 4. The view request_logs_with_current_month used to SELECT
--    request_body / response_body / outbound_body from both
--    request_logs_hot and request_logs in its projection list. After DROP
--    those references become SQLSTATE 42703. We must rebuild the view
--    FIRST (CREATE OR REPLACE) and then drop the columns.
-- 5. The view request_logs_bodies_with_current_month is the new SSOT for
--    body content; readers must LEFT JOIN this view to access bodies.
-- 6. LP5 audit script (scripts/check-body-storage-schema.sh) gates the
--    rollout: it exits non-zero while any of the 3 columns still exists
--    on request_logs_hot (or, after LP1, on the live DB).
--
-- Scope (hard constraints from plan)
-- ──────────────────────────────────
-- - DROP COLUMN only on body columns; no other schema change.
-- - Both request_logs_hot and request_logs (parent + partitions).
-- - view reconstruction: DROP the two views whose definitions reference
--   the body columns (request_logs_bodies_progress, the backfill
--   progress view added by 328a, and request_logs_with_current_month),
--   then CREATE request_logs_with_current_month back without the body
--   projection. All inside one BEGIN/COMMIT. Rule 49 §9.1: PG's CREATE
--   OR REPLACE VIEW may only APPEND trailing columns; it cannot REMOVE
--   existing ones. We must DROP+CREATE to drop the body projection.
-- - DOWN migration re-adds the columns as NULLABLE jsonb and restores the
--   view projection. Existing rows that have NULL bodies remain NULL;
--   this is a structural revert, not a data backfill (we never persisted
--   full bodies here after migration 341 anyway).
--
-- Breaking change
-- ───────────────
-- Readers that used to do `SELECT rl.request_body FROM
-- request_logs_with_current_month rl` will break with SQLSTATE 42703.
-- All such readers must be migrated to LEFT JOIN
-- request_logs_bodies_with_current_month rb in the same release. The Go
-- reader migration lands in the same PR (admin/{body_resolver,
-- logs_summary, no_topic_session, session_title}.go +
-- admin/telemetry_test.go).

BEGIN;

-- ─── 1. Rebuild request_logs_with_current_month view (drop body projection) ───
-- Rule 49 §9.1: PG's CREATE OR REPLACE VIEW cannot remove columns. We must
-- DROP + CREATE. We also DROP request_logs_bodies_progress (a 328a
-- backfill-progress view whose definition reads request_logs.body) —
-- after 573 its definition becomes a SQLSTATE 42703 at parse time.
-- Both DROPs + the CREATE below live inside this single BEGIN/COMMIT, so
-- the views are never absent for concurrent readers (a reader starting
-- before this txn sees the old views; a reader starting after COMMIT
-- sees the new view without the body projection). pg_depend + view
-- definition scan (2026-08-25 audit) confirmed no other view/function
-- depends on these two.
DROP VIEW IF EXISTS public.request_logs_bodies_progress;
DROP VIEW IF EXISTS public.request_logs_with_current_month;

CREATE VIEW public.request_logs_with_current_month AS
 SELECT request_logs_hot.id,
    request_logs_hot.request_id,
    request_logs_hot.ts,
    request_logs_hot.tenant_id,
    request_logs_hot.application_id,
    request_logs_hot.api_key_id,
    request_logs_hot.end_user_id,
    request_logs_hot.client_model,
    request_logs_hot.outbound_model,
    request_logs_hot.credential_id,
    request_logs_hot.provider_id,
    request_logs_hot.canonical_id,
    request_logs_hot.client_profile,
    request_logs_hot.request_mode,
    request_logs_hot.prompt_tokens,
    request_logs_hot.completion_tokens,
    request_logs_hot.total_tokens,
    request_logs_hot.cost_usd,
    request_logs_hot.latency_ms,
    request_logs_hot.success,
    request_logs_hot.error_kind,
    request_logs_hot.search_text,
    request_logs_hot.cache_read_tokens,
    request_logs_hot.cache_write_tokens,
    request_logs_hot.identity_hash,
    request_logs_hot.virtual_client_id,
    request_logs_hot.virtual_ip,
    request_logs_hot.virtual_mac,
    request_logs_hot.affinity_hit,
    request_logs_hot.stream_first_chunk_ms,
    request_logs_hot.stream_chunk_count,
    request_logs_hot.stream_interrupted,
    request_logs_hot.stream_done_sent,
    request_logs_hot.request_checksum,
    request_logs_hot.response_checksum,
    request_logs_hot.transform_rule_id,
    request_logs_hot.egress_protocol,
    request_logs_hot.failure_stage,
    request_logs_hot.failure_detail_code,
    request_logs_hot.request_preview,
    request_logs_hot.transform_summary,
    request_logs_hot.response_preview,
    request_logs_hot.stream_done_received,
    request_logs_hot.cost_display,
    request_logs_hot.cost_currency,
    request_logs_hot.usage_source,
    request_logs_hot.gw_session_id,
    request_logs_hot.gw_task_id,
    request_logs_hot.request_status,
    request_logs_hot.api_key_prefix,
    request_logs_hot.owner_user,
    request_logs_hot.application_code,
    request_logs_hot.key_alias,
    request_logs_hot.api_key_owner_user,
    request_logs_hot.is_auto_request,
    request_logs_hot.task_type,
    request_logs_hot.auto_profile,
    request_logs_hot.auto_decision,
    request_logs_hot.auto_confidence,
    request_logs_hot.work_type,
    request_logs_hot.task_type_chosen,
    request_logs_hot.confidence_num,
    request_logs_hot.model_chosen,
    request_logs_hot.strategy_used,
    request_logs_hot.credits_charged,
    request_logs_hot.parent_request_id,
    request_logs_hot.compression_reason,
    request_logs_hot.compression_strategy,
    request_logs_hot.compression_meta,
    request_logs_hot.outbound_msg_count,
    request_logs_hot.outbound_token_est,
    request_logs_hot.outbound_msg_hashes,
    request_logs_hot.quality_flags,
    request_logs_hot.quality_fix_actions,
    request_logs_hot.quality_score,
    request_logs_hot.upstream_finish_reason,
    request_logs_hot.tool_calls,
    request_logs_hot.client_endpoint,
    request_logs_hot.client_timeout,
    request_logs_hot.stream_chunk_errors,
    request_logs_hot.stream_chunks_sent,
    request_logs_hot.client_request_id,
    request_logs_hot.upstream_status_code,
    request_logs_hot.test_col,
    request_logs_hot.test_tab_indent,
    request_logs_hot.provider_model,
    request_logs_hot.attachments,
    request_logs_hot.has_attachments,
    request_logs_hot.attachment_count,
    request_logs_hot.routing_attempts,
    request_logs_hot.routing_summary,
    request_logs_hot.agent_name,
    request_logs_hot.agent_type,
    request_logs_hot.client_protocol,
    request_logs_hot.canonical_model,
    request_logs_hot.t0_arrived_at,
    request_logs_hot.t1_total_enqueued_at,
    request_logs_hot.t2_total_dequeued_at,
    request_logs_hot.t3_model_enqueued_at,
    request_logs_hot.t4_model_dequeued_at,
    request_logs_hot.t5_cred_enqueued_at,
    request_logs_hot.t6_cred_dequeued_at,
    request_logs_hot.t7_forward_start_at,
    request_logs_hot.t8_response_start_at,
    request_logs_hot.t9_response_end_at,
    request_logs_hot.request_type,
    request_logs_hot.is_final_success,
    request_logs_hot.origin_actor
   FROM public.request_logs_hot
UNION ALL
 SELECT request_logs.id,
    request_logs.request_id,
    request_logs.ts,
    request_logs.tenant_id,
    request_logs.application_id,
    request_logs.api_key_id,
    request_logs.end_user_id,
    request_logs.client_model,
    request_logs.outbound_model,
    request_logs.credential_id,
    request_logs.provider_id,
    request_logs.canonical_id,
    request_logs.client_profile,
    request_logs.request_mode,
    request_logs.prompt_tokens,
    request_logs.completion_tokens,
    request_logs.total_tokens,
    request_logs.cost_usd,
    request_logs.latency_ms,
    request_logs.success,
    request_logs.error_kind,
    request_logs.search_text,
    request_logs.cache_read_tokens,
    request_logs.cache_write_tokens,
    request_logs.identity_hash,
    request_logs.virtual_client_id,
    request_logs.virtual_ip,
    request_logs.virtual_mac,
    request_logs.affinity_hit,
    request_logs.stream_first_chunk_ms,
    request_logs.stream_chunk_count,
    request_logs.stream_interrupted,
    request_logs.stream_done_sent,
    request_logs.request_checksum,
    request_logs.response_checksum,
    request_logs.transform_rule_id,
    request_logs.egress_protocol,
    request_logs.failure_stage,
    request_logs.failure_detail_code,
    request_logs.request_preview,
    request_logs.transform_summary,
    request_logs.response_preview,
    request_logs.stream_done_received,
    request_logs.cost_display,
    request_logs.cost_currency,
    request_logs.usage_source,
    request_logs.gw_session_id,
    request_logs.gw_task_id,
    request_logs.request_status,
    request_logs.api_key_prefix,
    request_logs.owner_user,
    request_logs.application_code,
    request_logs.key_alias,
    request_logs.api_key_owner_user,
    request_logs.is_auto_request,
    request_logs.task_type,
    request_logs.auto_profile,
    request_logs.auto_decision,
    request_logs.auto_confidence,
    request_logs.work_type,
    request_logs.task_type_chosen,
    request_logs.confidence_num,
    request_logs.model_chosen,
    request_logs.strategy_used,
    request_logs.credits_charged,
    request_logs.parent_request_id,
    request_logs.compression_reason,
    request_logs.compression_strategy,
    request_logs.compression_meta,
    request_logs.outbound_msg_count,
    request_logs.outbound_token_est,
    request_logs.outbound_msg_hashes,
    request_logs.quality_flags,
    request_logs.quality_fix_actions,
    request_logs.quality_score,
    request_logs.upstream_finish_reason,
    request_logs.tool_calls,
    request_logs.client_endpoint,
    request_logs.client_timeout,
    request_logs.stream_chunk_errors,
    request_logs.stream_chunks_sent,
    request_logs.client_request_id,
    request_logs.upstream_status_code,
    request_logs.test_col,
    request_logs.test_tab_indent,
    request_logs.provider_model,
    request_logs.attachments,
    request_logs.has_attachments,
    request_logs.attachment_count,
    request_logs.routing_attempts,
    request_logs.routing_summary,
    request_logs.agent_name,
    request_logs.agent_type,
    request_logs.client_protocol,
    request_logs.canonical_model,
    request_logs.t0_arrived_at,
    request_logs.t1_total_enqueued_at,
    request_logs.t2_total_dequeued_at,
    request_logs.t3_model_enqueued_at,
    request_logs.t4_model_dequeued_at,
    request_logs.t5_cred_enqueued_at,
    request_logs.t6_cred_dequeued_at,
    request_logs.t7_forward_start_at,
    request_logs.t8_response_start_at,
    request_logs.t9_response_end_at,
    request_logs.request_type,
    request_logs.is_final_success,
    request_logs.origin_actor
   FROM public.request_logs;

-- ─── 2. Drop body columns from request_logs_hot ───
ALTER TABLE public.request_logs_hot DROP COLUMN IF EXISTS request_body;
ALTER TABLE public.request_logs_hot DROP COLUMN IF EXISTS response_body;
ALTER TABLE public.request_logs_hot DROP COLUMN IF EXISTS outbound_body;

-- ─── 3. Drop body columns from request_logs (parent + propagates to all partitions) ───
ALTER TABLE public.request_logs DROP COLUMN IF EXISTS request_body;
ALTER TABLE public.request_logs DROP COLUMN IF EXISTS response_body;
ALTER TABLE public.request_logs DROP COLUMN IF EXISTS outbound_body;

COMMIT;

-- Post-deploy verification (rule 38 §6.5 + LP5 audit):
--   ./scripts/check-body-storage-schema.sh
--   expected: exit 0
-- ============================================================================
-- 2026-08-24: DEFERRED via .skip rename — blocks every deploy, never applied.
--
-- Root cause of failure on the shared prod DB (172.16.2.210 llm_gateway):
--   psql:573: ERROR: cannot drop columns from view
--   (error line 279 = end of the CREATE OR REPLACE VIEW statement)
--
-- PostgreSQL's CREATE OR REPLACE VIEW may only APPEND trailing columns; it
-- can never REMOVE or reorder existing ones (rule 49 §9.1 view freeze).
-- This migration rebuilds request_logs_with_current_month WITHOUT the
-- request_body/response_body/outbound_body projections via CREATE OR
-- REPLACE, which PG rejects with "cannot drop columns from view".
--
-- TO UN-SKIP (LP owner):
--   1. Replace both CREATE OR REPLACE VIEW statements with
--      DROP VIEW ... ; CREATE VIEW ... (inside the same txn — atomic).
--      Check pg_depend for dependent views first; use plain DROP VIEW
--      (no CASCADE) to surface dependents loudly.
--   2. Dry-run on prod:  psql --single-transaction -v ON_ERROR_STOP=1 -f <file>
--      (error before COMMIT → auto rollback, safe).
--   3. Rename this file back to .sql and deploy; deploy gate applies it.
-- ============================================================================
