-- Migration 659: atomize the seven remaining legacy hot-table promote functions.
--
-- 2026-09-05 audit D-2#6 (.audit-workspace/2026-09-05-round2/axis-D-partition.md):
-- promote_usage_ledger_hot_to_partition (344, reinstalled by 399),
-- promote_request_wal_hot_to_partition (345), promote_routing_decision_log_hot_to_partition
-- (346, reinstalled by 399), promote_credential_model_index_hot_to_partition (347,
-- reinstalled by 399), promote_tool_usage_stats_hot_to_partition (348),
-- promote_credit_ledger_hot_to_partition (349) and
-- promote_request_logs_bodies_hot_to_partition (353/455, reinstalled by 528) still
-- used the pre-602 "temp table + delete-then-insert wrapped in an error-swallowing
-- handler" pattern. A plpgsql handler sub-block only rolls back the INSERT; the DELETE that
-- ran before it stays committed, so every INSERT failure (column drift, missing
-- partition, columnar limits) silently dropped the whole batch while a warning log
-- claimed "rows preserved in hot table". That is the exact 2026-08-25 request_logs
-- loss mechanism fixed by 602. It is not theoretical here: usage_ledger_hot gained
-- five multimodal token columns the parent lacks (2026-07-13-multimodal-token-fields),
-- so its star-select temp table has 24 columns against the parent's 19 and every batch
-- fails 42601 — DELETE committed, batch gone, function returns 0.
--
-- Fix: reinstall all seven bodies as a single data-modifying CTE following the
-- 656 template (promote_auto_route_selections_hot_to_partition): parameter guards,
-- a monthly pre-ensure loop over the same predicate as the batch CTE, WITH batch
-- (... locking the batch with SKIP LOCKED) → moved_rows AS (DELETE ... RETURNING explicit
-- columns) → inserted AS (INSERT INTO parent (explicit columns) ... RETURNING).
-- Delete and insert are one statement: an INSERT failure aborts the statement and
-- the hot rows stay in place. No error-swallowing handler, no temp table, no star
-- projection; errors propagate to the Go caller (bg recordPromoteFailure / admin
-- promote job).
--
-- No conflict-arbiter clause anywhere (diverging from 656 on purpose): five of the
-- seven parents own columnar leaf partitions and Citus columnar rejects speculative
-- insertion (migration 399 removed the conflict clause for exactly this reason), and
-- routing_decision_log / credential_model_index / request_logs_bodies have no
-- parent unique constraint at all. A concurrent promote cannot double-fire a row
-- because batch row locks are held to commit and SKIP LOCKED lets the loser pick
-- other rows. A genuine duplicate against a constrained parent now surfaces as an
-- error with the hot rows preserved, instead of a silently swallowed batch.
--
-- request_wal note: the 345 body was a placeholder ("no timestamp column") but
-- request_wal has carried created_at as its partition key and PK half since 032;
-- the placeholder left request_wal_hot unbounded while the scheduler kept calling
-- it hourly. This migration activates real created_at-based promotion. Signature
-- and DEFAULTs ('7 days', 5000) are unchanged for all seven functions.
--
-- Known fidelity limits (pre-existing, unchanged here, need a separate parent-schema
-- migration): usage_ledger_hot's reasoning/image/audio/video/provider_tokens and
-- request_logs_bodies_hot's tenant_id have no parent counterpart; those values are
-- not carried into the parent (the *_with_current_month views never exposed them
-- either). Under the old bodies the same rows were lost in full.
--
-- Idempotent: yes (CREATE OR REPLACE, deterministic bodies).
-- Down: 659_legacy_promote_atomic_cte.down.sql restores the pre-659 bodies —
-- it reintroduces the delete-before-insert loss window, emergency rollback only.

\set ON_ERROR_STOP on
BEGIN;

-- ============================================================
-- 1) promote_usage_ledger_hot_to_partition (344 → 399)
--    ts column: ts; hot keys: (request_id, ts); parent: UNIQUE (request_id, ts)
-- ============================================================

CREATE OR REPLACE FUNCTION public.promote_usage_ledger_hot_to_partition(
  p_retention interval DEFAULT '7 days'::interval,
  p_batch_size integer DEFAULT 5000
)
RETURNS bigint LANGUAGE plpgsql AS $$
DECLARE moved bigint := 0; month_rec record;
BEGIN
  IF p_retention IS NULL OR p_retention <= interval '0 seconds' THEN RAISE EXCEPTION 'p_retention must be positive'; END IF;
  IF p_batch_size IS NULL OR p_batch_size < 1 OR p_batch_size > 50000 THEN RAISE EXCEPTION 'p_batch_size must be between 1 and 50000'; END IF;
  -- The month pre-ensure loop must use the same predicate as the batch CTE.
  FOR month_rec IN
    SELECT DISTINCT date_trunc('month', ts) AS month_start
    FROM public.usage_ledger_hot
    WHERE ts < now() - p_retention
    ORDER BY 1 LIMIT 12
  LOOP
    PERFORM public.ensure_usage_ledger_partition(month_rec.month_start);
  END LOOP;
  WITH batch AS (
    SELECT request_id, ts FROM public.usage_ledger_hot
    WHERE ts < now() - p_retention
    ORDER BY ts, request_id LIMIT p_batch_size FOR UPDATE SKIP LOCKED
  ), moved_rows AS (
    DELETE FROM public.usage_ledger_hot h USING batch b
    WHERE h.request_id = b.request_id AND h.ts = b.ts
    RETURNING h.request_id, h.ts, h.tenant_id, h.application_id, h.api_key_id,
      h.end_user_id, h.credential_id, h.provider_id, h.canonical_id, h.raw_model_name,
      h.prompt_tokens, h.completion_tokens, h.cache_read_tokens, h.cache_write_tokens,
      h.total_tokens, h.cost_usd, h.latency_ms, h.success, h.error_kind
  ), inserted AS (
    INSERT INTO public.usage_ledger (
      request_id, ts, tenant_id, application_id, api_key_id,
      end_user_id, credential_id, provider_id, canonical_id, raw_model_name,
      prompt_tokens, completion_tokens, cache_read_tokens, cache_write_tokens,
      total_tokens, cost_usd, latency_ms, success, error_kind)
    SELECT request_id, ts, tenant_id, application_id, api_key_id,
      end_user_id, credential_id, provider_id, canonical_id, raw_model_name,
      prompt_tokens, completion_tokens, cache_read_tokens, cache_write_tokens,
      total_tokens, cost_usd, latency_ms, success, error_kind
    FROM moved_rows
    RETURNING request_id
  ) SELECT count(*) INTO moved FROM inserted;
  RETURN moved;
END;
$$;

-- ============================================================
-- 2) promote_request_wal_hot_to_partition (345 placeholder → real promote)
--    ts column: created_at; hot keys: request_id (PK); parent: PK (request_id, created_at)
-- ============================================================

CREATE OR REPLACE FUNCTION public.promote_request_wal_hot_to_partition(
  p_retention interval DEFAULT '7 days'::interval,
  p_batch_size integer DEFAULT 5000
)
RETURNS bigint LANGUAGE plpgsql AS $$
DECLARE moved bigint := 0; month_rec record;
BEGIN
  IF p_retention IS NULL OR p_retention <= interval '0 seconds' THEN RAISE EXCEPTION 'p_retention must be positive'; END IF;
  IF p_batch_size IS NULL OR p_batch_size < 1 OR p_batch_size > 50000 THEN RAISE EXCEPTION 'p_batch_size must be between 1 and 50000'; END IF;
  FOR month_rec IN
    SELECT DISTINCT date_trunc('month', created_at) AS month_start
    FROM public.request_wal_hot
    WHERE created_at < now() - p_retention
    ORDER BY 1 LIMIT 12
  LOOP
    PERFORM public.ensure_request_wal_partition(month_rec.month_start);
  END LOOP;
  WITH batch AS (
    SELECT request_id, created_at FROM public.request_wal_hot
    WHERE created_at < now() - p_retention
    ORDER BY created_at, request_id LIMIT p_batch_size FOR UPDATE SKIP LOCKED
  ), moved_rows AS (
    DELETE FROM public.request_wal_hot h USING batch b
    WHERE h.request_id = b.request_id AND h.created_at = b.created_at
    RETURNING h.request_id, h.tenant_id, h.gw_session_id, h.status, h.stage,
      h.client_model, h.upstream_provider_id, h.upstream_credential_id,
      h.completion_tokens, h.prompt_tokens, h.created_at, h.completed_at,
      h.upstream_request_at, h.upstream_response_at, h.error,
      h.compression_strategy, h.compression_meta
  ), inserted AS (
    INSERT INTO public.request_wal (
      request_id, tenant_id, gw_session_id, status, stage,
      client_model, upstream_provider_id, upstream_credential_id,
      completion_tokens, prompt_tokens, created_at, completed_at,
      upstream_request_at, upstream_response_at, error,
      compression_strategy, compression_meta)
    SELECT request_id, tenant_id, gw_session_id, status, stage,
      client_model, upstream_provider_id, upstream_credential_id,
      completion_tokens, prompt_tokens, created_at, completed_at,
      upstream_request_at, upstream_response_at, error,
      compression_strategy, compression_meta
    FROM moved_rows
    RETURNING request_id
  ) SELECT count(*) INTO moved FROM inserted;
  RETURN moved;
END;
$$;

-- ============================================================
-- 3) promote_routing_decision_log_hot_to_partition (346 → 399)
--    ts column: ts; hot keys: (request_id, ts); parent: no unique constraint
-- ============================================================

CREATE OR REPLACE FUNCTION public.promote_routing_decision_log_hot_to_partition(
  p_retention interval DEFAULT '7 days'::interval,
  p_batch_size integer DEFAULT 5000
)
RETURNS bigint LANGUAGE plpgsql AS $$
DECLARE moved bigint := 0; month_rec record;
BEGIN
  IF p_retention IS NULL OR p_retention <= interval '0 seconds' THEN RAISE EXCEPTION 'p_retention must be positive'; END IF;
  IF p_batch_size IS NULL OR p_batch_size < 1 OR p_batch_size > 50000 THEN RAISE EXCEPTION 'p_batch_size must be between 1 and 50000'; END IF;
  FOR month_rec IN
    SELECT DISTINCT date_trunc('month', ts) AS month_start
    FROM public.routing_decision_log_hot
    WHERE ts < now() - p_retention
    ORDER BY 1 LIMIT 12
  LOOP
    PERFORM public.ensure_routing_decision_log_partition(month_rec.month_start);
  END LOOP;
  WITH batch AS (
    SELECT request_id, ts FROM public.routing_decision_log_hot
    WHERE ts < now() - p_retention
    ORDER BY ts, request_id LIMIT p_batch_size FOR UPDATE SKIP LOCKED
  ), moved_rows AS (
    DELETE FROM public.routing_decision_log_hot h USING batch b
    WHERE h.request_id = b.request_id AND h.ts = b.ts
    RETURNING h.ts, h.request_id, h.idempotency_key, h.tenant_id, h.api_key_id,
      h.model, h.chosen_credential_id, h.chosen_provider_id, h.tier,
      h.candidates_tried, h.latency_ms, h.success, h.error_class,
      h.prompt_tokens, h.completion_tokens, h.cost_usd, h.request_bytes,
      h.response_bytes, h.client_model, h.resolved_raw_model, h.sticky_hit,
      h.client_profile, h.outbound_model, h.request_mode, h.identity_hash,
      h.transform_rule_id, h.egress_protocol, h.failure_stage,
      h.failure_detail_code, h.virtual_client_id, h.virtual_ip, h.virtual_mac,
      h.resolution_path, h.canonical_model, h.resolution_raw_models, h.decision_trace
  ), inserted AS (
    INSERT INTO public.routing_decision_log (
      ts, request_id, idempotency_key, tenant_id, api_key_id,
      model, chosen_credential_id, chosen_provider_id, tier,
      candidates_tried, latency_ms, success, error_class,
      prompt_tokens, completion_tokens, cost_usd, request_bytes,
      response_bytes, client_model, resolved_raw_model, sticky_hit,
      client_profile, outbound_model, request_mode, identity_hash,
      transform_rule_id, egress_protocol, failure_stage,
      failure_detail_code, virtual_client_id, virtual_ip, virtual_mac,
      resolution_path, canonical_model, resolution_raw_models, decision_trace)
    SELECT ts, request_id, idempotency_key, tenant_id, api_key_id,
      model, chosen_credential_id, chosen_provider_id, tier,
      candidates_tried, latency_ms, success, error_class,
      prompt_tokens, completion_tokens, cost_usd, request_bytes,
      response_bytes, client_model, resolved_raw_model, sticky_hit,
      client_profile, outbound_model, request_mode, identity_hash,
      transform_rule_id, egress_protocol, failure_stage,
      failure_detail_code, virtual_client_id, virtual_ip, virtual_mac,
      resolution_path, canonical_model, resolution_raw_models, decision_trace
    FROM moved_rows
    RETURNING request_id
  ) SELECT count(*) INTO moved FROM inserted;
  RETURN moved;
END;
$$;

-- ============================================================
-- 4) promote_credential_model_index_hot_to_partition (347 → 399)
--    retention column: updated_at; partition key: bucket;
--    hot keys: (bucket, credential_id, raw_model); parent: no unique constraint
-- ============================================================

CREATE OR REPLACE FUNCTION public.promote_credential_model_index_hot_to_partition(
  p_retention interval DEFAULT '7 days'::interval,
  p_batch_size integer DEFAULT 5000
)
RETURNS bigint LANGUAGE plpgsql AS $$
DECLARE moved bigint := 0; month_rec record;
BEGIN
  IF p_retention IS NULL OR p_retention <= interval '0 seconds' THEN RAISE EXCEPTION 'p_retention must be positive'; END IF;
  IF p_batch_size IS NULL OR p_batch_size < 1 OR p_batch_size > 50000 THEN RAISE EXCEPTION 'p_batch_size must be between 1 and 50000'; END IF;
  -- Rows route by bucket, so pre-ensure the months the moved rows will land in.
  FOR month_rec IN
    SELECT DISTINCT date_trunc('month', bucket) AS month_start
    FROM public.credential_model_index_hot
    WHERE updated_at < now() - p_retention
    ORDER BY 1 LIMIT 12
  LOOP
    PERFORM public.ensure_credential_model_index_partition(month_rec.month_start);
  END LOOP;
  WITH batch AS (
    SELECT bucket, credential_id, raw_model FROM public.credential_model_index_hot
    WHERE updated_at < now() - p_retention
    ORDER BY updated_at, bucket, credential_id, raw_model LIMIT p_batch_size FOR UPDATE SKIP LOCKED
  ), moved_rows AS (
    DELETE FROM public.credential_model_index_hot h USING batch b
    WHERE h.bucket = b.bucket AND h.credential_id = b.credential_id AND h.raw_model = b.raw_model
    RETURNING h.bucket, h.credential_id, h.raw_model, h.canonical_id, h.billing_mode,
      h.unit_price_in_per_1m, h.unit_price_out_per_1m, h.context_window,
      h.success_rate, h.p95_latency_ms, h.active_sessions, h.concurrency_limit,
      h.pressure_ratio, h.score_smart, h.score_speed_first, h.score_cost_first, h.updated_at
  ), inserted AS (
    INSERT INTO public.credential_model_index (
      bucket, credential_id, raw_model, canonical_id, billing_mode,
      unit_price_in_per_1m, unit_price_out_per_1m, context_window,
      success_rate, p95_latency_ms, active_sessions, concurrency_limit,
      pressure_ratio, score_smart, score_speed_first, score_cost_first, updated_at)
    SELECT bucket, credential_id, raw_model, canonical_id, billing_mode,
      unit_price_in_per_1m, unit_price_out_per_1m, context_window,
      success_rate, p95_latency_ms, active_sessions, concurrency_limit,
      pressure_ratio, score_smart, score_speed_first, score_cost_first, updated_at
    FROM moved_rows
    RETURNING credential_id
  ) SELECT count(*) INTO moved FROM inserted;
  RETURN moved;
END;
$$;

-- ============================================================
-- 5) promote_tool_usage_stats_hot_to_partition (348)
--    retention column: usage_date; partition key: created_at;
--    hot keys: (tool_id, tenant_id, usage_date)
-- ============================================================

CREATE OR REPLACE FUNCTION public.promote_tool_usage_stats_hot_to_partition(
  p_retention interval DEFAULT '7 days'::interval,
  p_batch_size integer DEFAULT 5000
)
RETURNS bigint LANGUAGE plpgsql AS $$
DECLARE moved bigint := 0; month_rec record;
BEGIN
  IF p_retention IS NULL OR p_retention <= interval '0 seconds' THEN RAISE EXCEPTION 'p_retention must be positive'; END IF;
  IF p_batch_size IS NULL OR p_batch_size < 1 OR p_batch_size > 50000 THEN RAISE EXCEPTION 'p_batch_size must be between 1 and 50000'; END IF;
  -- Rows route by created_at, so pre-ensure those months; the retention
  -- predicate itself stays on usage_date exactly as in migration 348.
  FOR month_rec IN
    SELECT DISTINCT date_trunc('month', created_at) AS month_start
    FROM public.tool_usage_stats_hot
    WHERE usage_date < CURRENT_DATE - p_retention
    ORDER BY 1 LIMIT 12
  LOOP
    PERFORM public.ensure_tool_usage_stats_partition(month_rec.month_start);
  END LOOP;
  WITH batch AS (
    SELECT tool_id, tenant_id, usage_date FROM public.tool_usage_stats_hot
    WHERE usage_date < CURRENT_DATE - p_retention
    ORDER BY usage_date, tool_id, tenant_id LIMIT p_batch_size FOR UPDATE SKIP LOCKED
  ), moved_rows AS (
    DELETE FROM public.tool_usage_stats_hot h USING batch b
    WHERE h.tool_id = b.tool_id AND h.tenant_id = b.tenant_id AND h.usage_date = b.usage_date
    RETURNING h.id, h.tool_id, h.tenant_id, h.usage_date, h.call_count,
      h.success_count, h.error_count, h.avg_latency_ms, h.last_called_at,
      h.created_at, h.updated_at
  ), inserted AS (
    INSERT INTO public.tool_usage_stats (
      id, tool_id, tenant_id, usage_date, call_count,
      success_count, error_count, avg_latency_ms, last_called_at,
      created_at, updated_at)
    SELECT id, tool_id, tenant_id, usage_date, call_count,
      success_count, error_count, avg_latency_ms, last_called_at,
      created_at, updated_at
    FROM moved_rows
    RETURNING id
  ) SELECT count(*) INTO moved FROM inserted;
  RETURN moved;
END;
$$;

-- ============================================================
-- 6) promote_credit_ledger_hot_to_partition (349)
--    ts column: created_at; hot keys: id; parent: PK (id, created_at)
-- ============================================================

CREATE OR REPLACE FUNCTION public.promote_credit_ledger_hot_to_partition(
  p_retention interval DEFAULT '7 days'::interval,
  p_batch_size integer DEFAULT 5000
)
RETURNS bigint LANGUAGE plpgsql AS $$
DECLARE moved bigint := 0; month_rec record;
BEGIN
  IF p_retention IS NULL OR p_retention <= interval '0 seconds' THEN RAISE EXCEPTION 'p_retention must be positive'; END IF;
  IF p_batch_size IS NULL OR p_batch_size < 1 OR p_batch_size > 50000 THEN RAISE EXCEPTION 'p_batch_size must be between 1 and 50000'; END IF;
  FOR month_rec IN
    SELECT DISTINCT date_trunc('month', created_at) AS month_start
    FROM public.credit_ledger_hot
    WHERE created_at < now() - p_retention
    ORDER BY 1 LIMIT 12
  LOOP
    PERFORM public.ensure_credit_ledger_partition(month_rec.month_start);
  END LOOP;
  WITH batch AS (
    SELECT id FROM public.credit_ledger_hot
    WHERE created_at < now() - p_retention
    ORDER BY created_at, id LIMIT p_batch_size FOR UPDATE SKIP LOCKED
  ), moved_rows AS (
    DELETE FROM public.credit_ledger_hot h USING batch b
    WHERE h.id = b.id
    RETURNING h.id, h.tenant_id, h.entry_type, h.amount, h.balance_after,
      h.ref_type, h.ref_id, h.note, h.created_at, h.pool
  ), inserted AS (
    INSERT INTO public.credit_ledger (
      id, tenant_id, entry_type, amount, balance_after,
      ref_type, ref_id, note, created_at, pool)
    SELECT id, tenant_id, entry_type, amount, balance_after,
      ref_type, ref_id, note, created_at, pool
    FROM moved_rows
    RETURNING id
  ) SELECT count(*) INTO moved FROM inserted;
  RETURN moved;
END;
$$;

-- ============================================================
-- 7) promote_request_logs_bodies_hot_to_partition (353/455 → 528)
--    ts column: ts; hot keys: request_id (unique); parent: no unique constraint.
--    Keeps migration 528's two-phase contract: expired rows past the configured
--    body TTL are deleted first (that phase is intentionally destructive and
--    single-statement); the promotion phase is now the 656-style atomic CTE.
-- ============================================================

CREATE OR REPLACE FUNCTION public.promote_request_logs_bodies_hot_to_partition(
  p_retention interval DEFAULT '7 days'::interval,
  p_batch_size integer DEFAULT 5000
)
RETURNS bigint LANGUAGE plpgsql AS $$
DECLARE
  v_processed bigint := 0;
  v_ttl_days int := 7;
  month_rec record;
BEGIN
  IF p_retention IS NULL OR p_retention <= interval '0 seconds' THEN RAISE EXCEPTION 'p_retention must be positive'; END IF;
  IF p_batch_size IS NULL OR p_batch_size < 1 OR p_batch_size > 50000 THEN RAISE EXCEPTION 'p_batch_size must be between 1 and 50000'; END IF;

  -- Phase 1 (unchanged from 528): delete rows older than the configured body
  -- TTL so a stale backlog cannot block promote on dropped partitions (23514).
  SELECT CASE jsonb_typeof(value)
           WHEN 'number' THEN value::text::int
           WHEN 'string' THEN trim(both '"' from value::text)::int
           ELSE 7
         END
    INTO v_ttl_days
    FROM settings_kv
   WHERE key = 'lifecycle.request_logs_bodies_ttl_days'
     AND scope = 'platform'
   LIMIT 1;

  v_ttl_days := GREATEST(COALESCE(v_ttl_days, 7), 1);

  WITH expired_batch AS (
    SELECT request_id
      FROM public.request_logs_bodies_hot
     WHERE ts < now() - make_interval(days => v_ttl_days)
       AND ts < now() - p_retention
     ORDER BY ts
     LIMIT p_batch_size
  ),
  expired_deleted AS (
    DELETE FROM public.request_logs_bodies_hot
     WHERE request_id IN (SELECT request_id FROM expired_batch)
    RETURNING request_id
  )
  SELECT count(*) INTO v_processed FROM expired_deleted;

  IF v_processed > 0 THEN
    RETURN v_processed;
  END IF;

  -- Phase 2: atomic promote (528 was already one statement but had no guards,
  -- no pre-ensure, no SKIP LOCKED and used RETURNING *).
  FOR month_rec IN
    SELECT DISTINCT date_trunc('month', ts) AS month_start
    FROM public.request_logs_bodies_hot
    WHERE ts < now() - p_retention
    ORDER BY 1 LIMIT 12
  LOOP
    PERFORM public.ensure_request_logs_bodies_partition(month_rec.month_start);
  END LOOP;

  WITH batch AS (
    SELECT request_id FROM public.request_logs_bodies_hot
    WHERE ts < now() - p_retention
    ORDER BY ts, request_id LIMIT p_batch_size FOR UPDATE SKIP LOCKED
  ), moved_rows AS (
    DELETE FROM public.request_logs_bodies_hot h USING batch b
    WHERE h.request_id = b.request_id
    RETURNING h.request_id, h.ts, h.request_body, h.outbound_body, h.response_body
  ), inserted AS (
    INSERT INTO public.request_logs_bodies (
      request_id, ts, request_body, outbound_body, response_body)
    SELECT request_id, ts, request_body, outbound_body, response_body
    FROM moved_rows
    RETURNING request_id
  ) SELECT count(*) INTO v_processed FROM inserted;
  RETURN v_processed;
END;
$$;

-- POST_CONDITION: each pg_get_functiondef above contains the SKIP LOCKED batch
-- scan, contains no swallowed-error handler, no temp table and no star projection
-- — asserted by the verification block below and by migration_659_test.go.

DO $verify_659$
DECLARE
  fn text;
  bodies text[] := ARRAY[
    'promote_usage_ledger_hot_to_partition(interval,integer)',
    'promote_request_wal_hot_to_partition(interval,integer)',
    'promote_routing_decision_log_hot_to_partition(interval,integer)',
    'promote_credential_model_index_hot_to_partition(interval,integer)',
    'promote_tool_usage_stats_hot_to_partition(interval,integer)',
    'promote_credit_ledger_hot_to_partition(interval,integer)',
    'promote_request_logs_bodies_hot_to_partition(interval,integer)'
  ];
BEGIN
  FOREACH fn IN ARRAY bodies LOOP
    IF NOT EXISTS (SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
                   WHERE n.nspname = 'public' AND p.oid = to_regprocedure('public.' || fn)) THEN
      RAISE EXCEPTION 'migration 659 verification: function % missing', fn;
    END IF;
    IF pg_get_functiondef(to_regprocedure('public.' || fn)) NOT LIKE '%FOR UPDATE SKIP LOCKED%' THEN
      RAISE EXCEPTION 'migration 659 verification: % lost FOR UPDATE SKIP LOCKED', fn;
    END IF;
    -- Guards legitimately RAISE EXCEPTION; only the legacy WHEN OTHERS handler
    -- (the pre-602 swallow pattern) is forbidden.
    IF position('WHEN OTHERS' in upper(pg_get_functiondef(to_regprocedure('public.' || fn)))) > 0 THEN
      RAISE EXCEPTION 'migration 659 verification: % still swallows errors with a legacy handler', fn;
    END IF;
  END LOOP;
  RAISE NOTICE 'migration 659 verified: 7 promote functions are atomic CTE bodies';
END
$verify_659$;

INSERT INTO public.schema_migrations (version, description)
VALUES ('659', 'atomize seven legacy hot-table promote functions (audit D-2#6)')
ON CONFLICT (version) DO NOTHING;

COMMIT;
