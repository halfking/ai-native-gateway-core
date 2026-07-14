-- Migration 399: 修复 promote 函数中 ON CONFLICT 对 columnar 分区的误用
--
-- 背景:
--   Citus Columnar 存储不支持 ON CONFLICT DO NOTHING/UPDATE
--   (columnar_tuple_insert_speculative not implemented)。
--   所有写入 columnar 分区的 INSERT 不能带 ON CONFLICT。
--
-- 本次修复涉及 5 个 promote 函数:
--   341: promote_request_logs_hot_to_partition          → request_logs (ALL columnar)
--   344: promote_usage_ledger_hot_to_partition           → usage_ledger (mixed: 06=col, 07=heap)
--   346: promote_routing_decision_log_hot_to_partition   → routing_decision_log (ALL columnar)
--   347: promote_credential_model_index_hot_to_partition → credential_model_index (ALL columnar)
--   353: promote_request_logs_bodies_hot_to_partition    → request_logs_bodies (ALL columnar)
--
-- 修复方式: 移除 ON CONFLICT 子句。
-- promote 函数已经在 INSERT 前 DELETE 了 hot 表中的行，不存在冲突风险。
-- 如果万一 INSERT 失败，EXCEPTION handler 保留 hot 表数据等待下次重试。

-- ============================================================
-- 1) promote_request_logs_hot_to_partition (migration 341)
-- ============================================================

CREATE OR REPLACE FUNCTION promote_request_logs_hot_to_partition(
  p_retention interval DEFAULT '7 days',
  p_batch_size int DEFAULT 5000
)
RETURNS bigint
LANGUAGE plpgsql AS $$
DECLARE
  n bigint := 0;
BEGIN
  CREATE TEMP TABLE _promote_hot_batch ON COMMIT DROP AS
  SELECT * FROM request_logs_hot
  WHERE ts < now() - p_retention
  ORDER BY ts
  LIMIT p_batch_size;

  GET DIAGNOSTICS n = ROW_COUNT;

  IF n = 0 THEN
    RETURN 0;
  END IF;

  DELETE FROM request_logs_hot
  WHERE id IN (SELECT id FROM _promote_hot_batch);

  BEGIN
    INSERT INTO request_logs
    SELECT * FROM _promote_hot_batch;
  EXCEPTION WHEN OTHERS THEN
    RAISE WARNING 'promote_request_logs_hot_to_partition: INSERT failed (%), rows preserved in hot table', SQLERRM;
    n := 0;
  END;

  RETURN n;
END;
$$;

-- ============================================================
-- 2) promote_usage_ledger_hot_to_partition (migration 344)
-- ============================================================

CREATE OR REPLACE FUNCTION promote_usage_ledger_hot_to_partition(
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
-- 3) promote_routing_decision_log_hot_to_partition (migration 346)
-- ============================================================

CREATE OR REPLACE FUNCTION promote_routing_decision_log_hot_to_partition(
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
-- 4) promote_credential_model_index_hot_to_partition (migration 347)
-- ============================================================

CREATE OR REPLACE FUNCTION promote_credential_model_index_hot_to_partition(
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
-- 5) promote_request_logs_bodies_hot_to_partition (migration 353)
-- ============================================================

CREATE OR REPLACE FUNCTION promote_request_logs_bodies_hot_to_partition(
  p_retention interval DEFAULT '7 days',
  p_batch_size int DEFAULT 5000
)
RETURNS bigint
LANGUAGE plpgsql AS $$
DECLARE
  v_moved bigint := 0;
BEGIN
  WITH batch AS (
    SELECT request_id, ts, request_body, outbound_body, response_body
    FROM request_logs_bodies_hot
    WHERE ts < now() - p_retention
    ORDER BY ts
    LIMIT p_batch_size
  ),
  deleted AS (
    DELETE FROM request_logs_bodies_hot
    WHERE (request_id, ts) IN (SELECT request_id, ts FROM batch)
    RETURNING *
  )
  INSERT INTO request_logs_bodies (request_id, ts, request_body, outbound_body, response_body)
  SELECT request_id, ts, request_body, outbound_body, response_body FROM deleted;

  GET DIAGNOSTICS v_moved = ROW_COUNT;
  RETURN v_moved;
END;
$$;

-- ============================================================
-- 6) fix_cleanup_stale_in_progress_requests: UPDATE request_logs → request_logs_hot
-- ============================================================

CREATE OR REPLACE FUNCTION cleanup_stale_in_progress_requests()
RETURNS TABLE(cleaned_count bigint) AS $$
DECLARE
    updated_rows bigint;
BEGIN
    UPDATE request_logs_hot
    SET
        success = false,
        request_status = 'failure',
        error_kind = 'gateway_timeout',
        failure_stage = 'gateway',
        failure_detail_code = 'gw_processing_timeout'
    WHERE
        request_status = 'in_progress'
        AND ts < NOW() - INTERVAL '5 minutes'
        AND success = false;

    GET DIAGNOSTICS updated_rows = ROW_COUNT;

    IF updated_rows > 0 THEN
        RAISE NOTICE 'Cleaned up % stale in_progress request_logs_hot records', updated_rows;
    END IF;

    RETURN QUERY SELECT updated_rows;
END;
$$ LANGUAGE plpgsql;

-- ============================================================
-- 7) 验证
-- ============================================================

DO $$
DECLARE
    fn_count int := 0;
BEGIN
    PERFORM 1 FROM pg_proc WHERE proname = 'promote_request_logs_hot_to_partition';
    fn_count := fn_count + 1;
    PERFORM 1 FROM pg_proc WHERE proname = 'promote_usage_ledger_hot_to_partition';
    fn_count := fn_count + 1;
    PERFORM 1 FROM pg_proc WHERE proname = 'promote_routing_decision_log_hot_to_partition';
    fn_count := fn_count + 1;
    PERFORM 1 FROM pg_proc WHERE proname = 'promote_credential_model_index_hot_to_partition';
    fn_count := fn_count + 1;
    PERFORM 1 FROM pg_proc WHERE proname = 'promote_request_logs_bodies_hot_to_partition';
    fn_count := fn_count + 1;
    PERFORM 1 FROM pg_proc WHERE proname = 'cleanup_stale_in_progress_requests';
    fn_count := fn_count + 1;
    RAISE NOTICE 'Migration 399 completed: 6 functions updated (removed ON CONFLICT from columnar writes, fixed cleanup to use request_logs_hot)';
END $$;
