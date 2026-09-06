-- Migration 665: Fix llm_hourly_stats timestamp format issue
-- Purpose: Handle truncated timestamp format from external redclaw services
-- Created: 2026-09-06
--
-- Problem: External services send truncated timestamps like "2026-09-05T23"
-- Solution: Keep base table with TIMESTAMPTZ, create a helper function to normalize input
--
-- Strategy: Don't change the table structure (keep it compatible).
-- Instead, document the proper way to insert and provide a helper function.

-- Step 1: Create normalization function that external services should use
-- Step 1: Create normalization function that external services should use
CREATE OR REPLACE FUNCTION normalize_hour_timestamp(hour_input TEXT)
RETURNS TIMESTAMPTZ AS $$
DECLARE
  normalized_ts TIMESTAMPTZ;
BEGIN
  -- Try to parse as-is first (full format)
  BEGIN
    normalized_ts := hour_input::TIMESTAMPTZ;
    RETURN date_trunc('hour', normalized_ts);
  EXCEPTION WHEN OTHERS THEN
    -- If fails, try to handle truncated formats
    NULL;
  END;

  -- Handle "YYYY-MM-DDTHH" format (no minutes/seconds/timezone)
  -- Example: "2026-09-05T23" -> "2026-09-05T23:00:00+00"
  IF hour_input ~ '^\d{4}-\d{2}-\d{2}T\d{2}$' THEN
    normalized_ts := (hour_input || ':00:00+00')::TIMESTAMPTZ;
    RETURN date_trunc('hour', normalized_ts);
  END IF;

  -- Handle "YYYY-MM-DD HH" format (space-separated)
  IF hour_input ~ '^\d{4}-\d{2}-\d{2}\s+\d{2}$' THEN
    normalized_ts := (hour_input || ':00:00+00')::TIMESTAMPTZ;
    RETURN date_trunc('hour', normalized_ts);
  END IF;

  -- Handle "YYYY-MM-DDTHH:MM" format (no seconds/timezone)
  IF hour_input ~ '^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$' THEN
    normalized_ts := (hour_input || ':00+00')::TIMESTAMPTZ;
    RETURN date_trunc('hour', normalized_ts);
  END IF;

  -- If nothing works, raise error with helpful message
  RAISE EXCEPTION 'Invalid hour format: "%". Expected formats: "YYYY-MM-DDTHH" (e.g. "2026-09-05T23"), "YYYY-MM-DDTHH:MM:SS+TZ", or full ISO8601', hour_input
    USING HINT = 'Use normalize_hour_timestamp() function or format as "YYYY-MM-DDTHH:MM:SS+00"';
END;
$$ LANGUAGE plpgsql IMMUTABLE;

COMMENT ON FUNCTION normalize_hour_timestamp(TEXT) IS
  'Normalize flexible hour timestamp formats to standard TIMESTAMPTZ truncated to hour. Accepts: "2026-09-05T23", "2026-09-05 23", "2026-09-05T23:00:00+00", etc.';

-- Step 2: Create a helper stored procedure for safe insertion
CREATE OR REPLACE FUNCTION upsert_llm_hourly_stats(
  hour_input TEXT,
  p_success_count INTEGER,
  p_failure_count INTEGER,
  p_total_count INTEGER,
  p_total_cost NUMERIC
) RETURNS VOID AS $$
DECLARE
  normalized_hour TIMESTAMPTZ;
BEGIN
  normalized_hour := normalize_hour_timestamp(hour_input);

  INSERT INTO llm_hourly_stats (
    hour, success_count, failure_count, total_count, total_cost
  ) VALUES (
    normalized_hour, p_success_count, p_failure_count, p_total_count, p_total_cost
  )
  ON CONFLICT (hour) DO UPDATE SET
    success_count = EXCLUDED.success_count,
    failure_count = EXCLUDED.failure_count,
    total_count = EXCLUDED.total_count,
    total_cost = EXCLUDED.total_cost,
    updated_at = NOW();
END;
$$ LANGUAGE plpgsql;

COMMENT ON FUNCTION upsert_llm_hourly_stats(TEXT, INTEGER, INTEGER, INTEGER, NUMERIC) IS
  'Safe upsert for llm_hourly_stats that accepts flexible hour formats. Call as: SELECT upsert_llm_hourly_stats(''2026-09-05T23'', 100, 5, 105, 1.23);';

-- Step 3: Create an INSTEAD OF trigger on a writable view
-- This allows external services to use their existing INSERT statements without modification
CREATE OR REPLACE VIEW llm_hourly_stats_flexible AS
SELECT
  hour::TEXT as hour,  -- Cast to TEXT for flexible input
  success_count,
  failure_count,
  total_count,
  total_cost,
  created_at,
  updated_at
FROM llm_hourly_stats;

COMMENT ON VIEW llm_hourly_stats_flexible IS
  'Writable view for llm_hourly_stats that accepts flexible hour timestamp formats via INSTEAD OF trigger';

CREATE OR REPLACE FUNCTION llm_hourly_stats_flexible_insert()
RETURNS TRIGGER AS $$
DECLARE
  normalized_hour TIMESTAMPTZ;
BEGIN
  normalized_hour := normalize_hour_timestamp(NEW.hour);

  INSERT INTO llm_hourly_stats (
    hour, success_count, failure_count, total_count, total_cost
  ) VALUES (
    normalized_hour,
    COALESCE(NEW.success_count, 0),
    COALESCE(NEW.failure_count, 0),
    COALESCE(NEW.total_count, 0),
    COALESCE(NEW.total_cost, 0)
  )
  ON CONFLICT (hour) DO UPDATE SET
    success_count = EXCLUDED.success_count,
    failure_count = EXCLUDED.failure_count,
    total_count = EXCLUDED.total_count,
    total_cost = EXCLUDED.total_cost,
    updated_at = NOW();

  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_llm_hourly_stats_flexible_insert ON llm_hourly_stats_flexible;

CREATE TRIGGER trg_llm_hourly_stats_flexible_insert
  INSTEAD OF INSERT ON llm_hourly_stats_flexible
  FOR EACH ROW
  EXECUTE FUNCTION llm_hourly_stats_flexible_insert();

-- Step 4: Create documentation table
CREATE TABLE IF NOT EXISTS llm_hourly_stats_usage_guide (
  solution TEXT PRIMARY KEY,
  description TEXT,
  example TEXT
);

INSERT INTO llm_hourly_stats_usage_guide (solution, description, example) VALUES
  ('Option 1: Use helper function',
   'Call upsert_llm_hourly_stats() with flexible hour format',
   'SELECT upsert_llm_hourly_stats(''2026-09-05T23'', 100, 5, 105, 1.23);'),
  ('Option 2: Use flexible view',
   'INSERT into llm_hourly_stats_flexible view (accepts TEXT hour)',
   'INSERT INTO llm_hourly_stats_flexible (hour, success_count, ...) VALUES (''2026-09-05T23'', 100, ...);'),
  ('Option 3: Normalize in application',
   'Use normalize_hour_timestamp() in your INSERT statement',
   'INSERT INTO llm_hourly_stats (hour, ...) VALUES (normalize_hour_timestamp(''2026-09-05T23''), ...);'),
  ('Option 4: Fix timestamp format',
   'Update external service to send full ISO8601 timestamps',
   'Format as: "2026-09-05T23:00:00+00:00" or use date_trunc(''hour'', NOW())')
ON CONFLICT (solution) DO UPDATE SET
  description = EXCLUDED.description,
  example = EXCLUDED.example;

COMMENT ON TABLE llm_hourly_stats_usage_guide IS
  'Usage guide for llm_hourly_stats with flexible timestamp format support. Query this table for integration examples.';

-- Step 5: Test the normalization function
DO $$
DECLARE
  test_result TIMESTAMPTZ;
BEGIN
  -- Test 1: Truncated format "YYYY-MM-DDTHH"
  test_result := normalize_hour_timestamp('2026-09-05T23');
  IF test_result != '2026-09-05 23:00:00+00'::TIMESTAMPTZ THEN
    RAISE EXCEPTION 'Test 1 failed: expected 2026-09-05 23:00:00+00, got %', test_result;
  END IF;
  RAISE NOTICE 'Test 1 passed: "2026-09-05T23" -> %', test_result;

  -- Test 2: Full ISO8601 format
  test_result := normalize_hour_timestamp('2026-09-05T23:00:00+00:00');
  IF test_result != '2026-09-05 23:00:00+00'::TIMESTAMPTZ THEN
    RAISE EXCEPTION 'Test 2 failed: expected 2026-09-05 23:00:00+00, got %', test_result;
  END IF;
  RAISE NOTICE 'Test 2 passed: "2026-09-05T23:00:00+00:00" -> %', test_result;

  -- Test 3: Space-separated format
  test_result := normalize_hour_timestamp('2026-09-05 23');
  IF test_result != '2026-09-05 23:00:00+00'::TIMESTAMPTZ THEN
    RAISE EXCEPTION 'Test 3 failed: expected 2026-09-05 23:00:00+00, got %', test_result;
  END IF;
  RAISE NOTICE 'Test 3 passed: "2026-09-05 23" -> %', test_result;

  RAISE NOTICE 'All normalization tests passed!';
END $$;
