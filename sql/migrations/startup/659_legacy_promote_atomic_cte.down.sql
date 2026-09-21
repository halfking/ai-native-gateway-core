-- Down for 659: restore the pre-fix promote bodies, verbatim from the migration
-- chain (344/345/346/347/348/349 as reinstalled by 399, and 528 for
-- request_logs_bodies).
--
-- WARNING: the restored bodies reintroduce the delete-before-insert loss window
-- fixed by 659 (audit D-2#6): a plpgsql EXCEPTION sub-block only rolls back the
-- INSERT while the earlier DELETE stays committed, so any INSERT failure (column
-- drift, missing partition, columnar limits) silently drops the whole batch. For
-- usage_ledger this is not hypothetical: its hot table has carried five multimodal
-- token columns the parent lacks since 2026-07-13, so the restored SELECT * body
-- fails every batch. This file exists for the mandatory down-migration convention
-- and emergency rollback only — do NOT apply it to the shared production PG.

\set ON_ERROR_STOP on

BEGIN;

-- ============================================================
-- 1) promote_usage_ledger_hot_to_partition (pre-659 body, from 399)
-- ============================================================

CREATE OR REPLACE FUNCTION public.promote_usage_ledger_hot_to_partition(
  p_retention interval DEFAULT '7 days',
  p_batch_size int DEFAULT 5000
)
RETURNS bigint
LANGUAGE plpgsql AS $$
DECLARE
  n bigint := 0;
BEGIN
  CREATE TEMP TABLE _promote_hot_batch ON COMMIT DROP AS
  SELECT * FROM usage_ledger_hot
  WHERE ts < now() - p_retention
  ORDER BY ts
  LIMIT p_batch_size;

  GET DIAGNOSTICS n = ROW_COUNT;

  IF n = 0 THEN
    RETURN 0;
  END IF;

  DELETE FROM usage_ledger_hot
  WHERE (request_id, ts) IN (SELECT request_id, ts FROM _promote_hot_batch);

  BEGIN
    INSERT INTO usage_ledger
    SELECT * FROM _promote_hot_batch;
  EXCEPTION WHEN OTHERS THEN
    RAISE WARNING 'promote_usage_ledger_hot_to_partition: INSERT failed (%), rows preserved in hot table', SQLERRM;
    n := 0;
  END;

  RETURN n;
END;
$$;

-- ============================================================
-- 2) promote_request_wal_hot_to_partition (pre-659 body, from 345)
-- ============================================================

CREATE OR REPLACE FUNCTION public.promote_request_wal_hot_to_partition(
  p_retention interval DEFAULT '7 days',
  p_batch_size int DEFAULT 5000
)
RETURNS bigint
LANGUAGE plpgsql AS $$
DECLARE
  n bigint := 0;
BEGIN
  -- request_wal 没有 ts 字段，无法按时间 promote
  -- 这里保留函数签名，但返回 0（暂不实现）
  RAISE NOTICE 'request_wal_hot_to_partition: no timestamp column, skip promote';
  RETURN 0;
END;
$$;

-- ============================================================
-- 3) promote_routing_decision_log_hot_to_partition (pre-659 body, from 399)
-- ============================================================

CREATE OR REPLACE FUNCTION public.promote_routing_decision_log_hot_to_partition(
  p_retention interval DEFAULT '7 days',
  p_batch_size int DEFAULT 5000
)
RETURNS bigint
LANGUAGE plpgsql AS $$
DECLARE
  n bigint := 0;
BEGIN
  CREATE TEMP TABLE _promote_hot_batch ON COMMIT DROP AS
  SELECT * FROM routing_decision_log_hot
  WHERE ts < now() - p_retention
  ORDER BY ts
  LIMIT p_batch_size;

  GET DIAGNOSTICS n = ROW_COUNT;

  IF n = 0 THEN
    RETURN 0;
  END IF;

  DELETE FROM routing_decision_log_hot
  WHERE (request_id, ts) IN (SELECT request_id, ts FROM _promote_hot_batch);

  BEGIN
    INSERT INTO routing_decision_log
    SELECT * FROM _promote_hot_batch;
  EXCEPTION WHEN OTHERS THEN
    RAISE WARNING 'promote_routing_decision_log_hot_to_partition: INSERT failed (%), rows preserved in hot table', SQLERRM;
    n := 0;
  END;

  RETURN n;
END;
$$;

-- ============================================================
-- 4) promote_credential_model_index_hot_to_partition (pre-659 body, from 399)
-- ============================================================

CREATE OR REPLACE FUNCTION public.promote_credential_model_index_hot_to_partition(
  p_retention interval DEFAULT '7 days',
  p_batch_size int DEFAULT 5000
)
RETURNS bigint
LANGUAGE plpgsql AS $$
DECLARE
  n bigint := 0;
BEGIN
  CREATE TEMP TABLE _promote_hot_batch ON COMMIT DROP AS
  SELECT * FROM credential_model_index_hot
  WHERE updated_at < now() - p_retention
  ORDER BY updated_at
  LIMIT p_batch_size;

  GET DIAGNOSTICS n = ROW_COUNT;

  IF n = 0 THEN
    RETURN 0;
  END IF;

  DELETE FROM credential_model_index_hot
  WHERE (bucket, credential_id, raw_model) IN (
    SELECT bucket, credential_id, raw_model FROM _promote_hot_batch
  );

  BEGIN
    INSERT INTO credential_model_index
    SELECT * FROM _promote_hot_batch;
  EXCEPTION WHEN OTHERS THEN
    RAISE WARNING 'promote_credential_model_index_hot_to_partition: INSERT failed (%), rows preserved in hot table', SQLERRM;
    n := 0;
  END;

  RETURN n;
END;
$$;

-- ============================================================
-- 5) promote_tool_usage_stats_hot_to_partition (pre-659 body, from 348)
-- ============================================================

CREATE OR REPLACE FUNCTION public.promote_tool_usage_stats_hot_to_partition(
  p_retention interval DEFAULT '7 days',
  p_batch_size int DEFAULT 5000
)
RETURNS bigint
LANGUAGE plpgsql AS $$
DECLARE
  n bigint := 0;
BEGIN
  CREATE TEMP TABLE _promote_hot_batch ON COMMIT DROP AS
  SELECT * FROM tool_usage_stats_hot
  WHERE usage_date < CURRENT_DATE - p_retention::interval
  ORDER BY usage_date
  LIMIT p_batch_size;

  GET DIAGNOSTICS n = ROW_COUNT;

  IF n = 0 THEN
    RETURN 0;
  END IF;

  DELETE FROM tool_usage_stats_hot
  WHERE (tool_id, tenant_id, usage_date) IN (
    SELECT tool_id, tenant_id, usage_date FROM _promote_hot_batch
  );

  BEGIN
    INSERT INTO tool_usage_stats
    SELECT * FROM _promote_hot_batch
    ON CONFLICT (tool_id, tenant_id, usage_date) DO NOTHING;
  EXCEPTION WHEN OTHERS THEN
    RAISE WARNING 'promote_tool_usage_stats_hot_to_partition: INSERT failed (%), rows preserved in hot table', SQLERRM;
    n := 0;
  END;

  RETURN n;
END;
$$;

-- ============================================================
-- 6) promote_credit_ledger_hot_to_partition (pre-659 body, from 349)
-- ============================================================

CREATE OR REPLACE FUNCTION public.promote_credit_ledger_hot_to_partition(
  p_retention interval DEFAULT '7 days',
  p_batch_size int DEFAULT 5000
)
RETURNS bigint
LANGUAGE plpgsql AS $$
DECLARE
  n bigint := 0;
BEGIN
  CREATE TEMP TABLE _promote_hot_batch ON COMMIT DROP AS
  SELECT * FROM credit_ledger_hot
  WHERE created_at < now() - p_retention
  ORDER BY created_at
  LIMIT p_batch_size;

  GET DIAGNOSTICS n = ROW_COUNT;

  IF n = 0 THEN
    RETURN 0;
  END IF;

  DELETE FROM credit_ledger_hot
  WHERE id IN (SELECT id FROM _promote_hot_batch);

  BEGIN
    INSERT INTO credit_ledger
    SELECT * FROM _promote_hot_batch
    ON CONFLICT (id, created_at) DO NOTHING;
  EXCEPTION WHEN OTHERS THEN
    RAISE WARNING 'promote_credit_ledger_hot_to_partition: INSERT failed (%), rows preserved in hot table', SQLERRM;
    n := 0;
  END;

  RETURN n;
END;
$$;

-- ============================================================
-- 7) promote_request_logs_bodies_hot_to_partition (pre-659 body, from 528)
-- ============================================================

CREATE OR REPLACE FUNCTION public.promote_request_logs_bodies_hot_to_partition(
  p_retention interval DEFAULT '7 days',
  p_batch_size int DEFAULT 5000
)
RETURNS bigint
LANGUAGE plpgsql AS $$
DECLARE
  v_processed bigint := 0;
  v_ttl_days int := 7;
BEGIN
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
      FROM request_logs_bodies_hot
     WHERE ts < now() - make_interval(days => v_ttl_days)
       AND ts < now() - p_retention
     ORDER BY ts
     LIMIT p_batch_size
  ),
  expired_deleted AS (
    DELETE FROM request_logs_bodies_hot
     WHERE request_id IN (SELECT request_id FROM expired_batch)
    RETURNING request_id
  )
  SELECT count(*) INTO v_processed FROM expired_deleted;

  IF v_processed > 0 THEN
    RETURN v_processed;
  END IF;

  WITH batch AS (
    SELECT request_id, ts, request_body, outbound_body, response_body
      FROM request_logs_bodies_hot
     WHERE ts < now() - p_retention
     ORDER BY ts
     LIMIT p_batch_size
  ),
  deleted AS (
    DELETE FROM request_logs_bodies_hot
     WHERE request_id IN (SELECT request_id FROM batch)
    RETURNING *
  )
  INSERT INTO request_logs_bodies
    (request_id, ts, request_body, outbound_body, response_body)
  SELECT request_id, ts, request_body, outbound_body, response_body
    FROM deleted;

  GET DIAGNOSTICS v_processed = ROW_COUNT;
  RETURN v_processed;
END;
$$;

DELETE FROM public.schema_migrations WHERE version = '659';

COMMIT;
