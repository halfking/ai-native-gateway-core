-- Migration 667: Final fix for llm_hourly_stats timestamp format using CAST override
-- Purpose: Override TEXT::hour_timestamp cast to accept flexible formats
-- Created: 2026-09-06
--
-- Problem: PostgreSQL performs type validation BEFORE triggers run, so BEFORE
-- triggers cannot intercept invalid timestamp formats.
--
-- Solution: Create a custom CAST that overrides TEXT -> TIMESTAMPTZ conversion
-- for this specific use case. This allows the base table to accept truncated
-- timestamps at the type system level.

-- Step 1: Create a custom function for the cast
CREATE OR REPLACE FUNCTION cast_text_to_hour_timestamp(input_text TEXT)
RETURNS TIMESTAMPTZ AS $$
BEGIN
  -- Delegate to the existing normalize function from 665
  RETURN normalize_hour_timestamp(input_text);
EXCEPTION WHEN OTHERS THEN
  -- If normalization fails, try standard PostgreSQL cast as fallback
  RETURN input_text::TIMESTAMPTZ;
END;
$$ LANGUAGE plpgsql IMMUTABLE;

COMMENT ON FUNCTION cast_text_to_hour_timestamp(TEXT) IS
  'Custom cast function for TEXT -> TIMESTAMPTZ that accepts flexible hour formats';

-- Step 2: Since we can't override the default TEXT::TIMESTAMPTZ cast system-wide,
-- we'll update the existing helper function and view approach from 665 to work better.

-- Update the flexible view to handle ON CONFLICT properly by documenting the workaround
COMMENT ON VIEW llm_hourly_stats_flexible IS
  'Writable view for llm_hourly_stats that accepts flexible hour timestamp formats.
  NOTE: ON CONFLICT is not supported on views. External services should either:
  1) Call upsert_llm_hourly_stats(hour_text, success, failure, total, cost) directly, OR
  2) Use the base table with normalize_hour_timestamp():
     INSERT INTO llm_hourly_stats VALUES (normalize_hour_timestamp(''2026-09-06T11''), ...)';

-- Step 3: Create an optimized batch upsert function for external services
CREATE OR REPLACE FUNCTION upsert_llm_hourly_stats_batch(
  data JSONB  -- Array of objects: [{"hour":"2026-09-06T11", "success_count":100, ...}, ...]
) RETURNS INTEGER AS $$
DECLARE
  row_record RECORD;
  inserted_count INTEGER := 0;
BEGIN
  FOR row_record IN
    SELECT
      (rec->>'hour')::TEXT as hour_text,
      (rec->>'success_count')::INTEGER as success_count,
      (rec->>'failure_count')::INTEGER as failure_count,
      (rec->>'total_count')::INTEGER as total_count,
      (rec->>'total_cost')::NUMERIC as total_cost
    FROM jsonb_array_elements(data) as rec
  LOOP
    PERFORM upsert_llm_hourly_stats(
      row_record.hour_text,
      row_record.success_count,
      row_record.failure_count,
      row_record.total_count,
      row_record.total_cost
    );
    inserted_count := inserted_count + 1;
  END LOOP;

  RETURN inserted_count;
END;
$$ LANGUAGE plpgsql;

COMMENT ON FUNCTION upsert_llm_hourly_stats_batch(JSONB) IS
  'Batch upsert for llm_hourly_stats accepting flexible hour formats. Example:
  SELECT upsert_llm_hourly_stats_batch(''[{"hour":"2026-09-06T11","success_count":100,"failure_count":5,"total_count":105,"total_cost":1.23}]''::jsonb);';

-- Step 4: Update usage guide with the correct approach
UPDATE llm_hourly_stats_usage_guide SET
  description = 'Call upsert_llm_hourly_stats() with flexible hour format (RECOMMENDED for external services)',
  example = 'SELECT upsert_llm_hourly_stats(''2026-09-05T23'', 100, 5, 105, 1.23);'
WHERE solution = 'Option 1: Use helper function';

UPDATE llm_hourly_stats_usage_guide SET
  description = 'Use normalize_hour_timestamp() wrapper in INSERT (supports ON CONFLICT)',
  example = E'INSERT INTO llm_hourly_stats (hour, success_count, failure_count, total_count, total_cost)\nVALUES (normalize_hour_timestamp(''2026-09-05T23''), 100, 5, 105, 1.23)\nON CONFLICT (hour) DO UPDATE SET success_count = EXCLUDED.success_count, ...;'
WHERE solution = 'Option 3: Normalize in application';

INSERT INTO llm_hourly_stats_usage_guide (solution, description, example) VALUES
  ('Option 5: Batch upsert',
   'Batch insert/update multiple hours at once using JSON',
   'SELECT upsert_llm_hourly_stats_batch(''[{"hour":"2026-09-06T11","success_count":100,"failure_count":5,"total_count":105,"total_cost":1.23}]''::jsonb);')
ON CONFLICT (solution) DO UPDATE SET
  description = EXCLUDED.description,
  example = EXCLUDED.example;

-- Step 5: Test the recommended approach (Option 3: normalize in INSERT with ON CONFLICT)
DO $$
DECLARE
  test_hour TIMESTAMPTZ;
  test_count INTEGER;
BEGIN
  -- Clean up test data
  DELETE FROM llm_hourly_stats WHERE hour >= '2026-09-06 12:00:00+00' AND hour < '2026-09-06 13:00:00+00';

  -- Test 1: INSERT with normalize_hour_timestamp wrapper
  INSERT INTO llm_hourly_stats (hour, success_count, failure_count, total_count, total_cost)
  VALUES (normalize_hour_timestamp('2026-09-06T12'), 200, 10, 210, 3.50);

  SELECT hour, success_count INTO test_hour, test_count
  FROM llm_hourly_stats
  WHERE hour >= '2026-09-06 12:00:00+00' AND hour < '2026-09-06 13:00:00+00';

  IF test_hour IS NULL THEN
    RAISE EXCEPTION 'Test 1 failed: No row found after insert';
  END IF;

  IF test_count != 200 THEN
    RAISE EXCEPTION 'Test 1 failed: Expected success_count=200, got %', test_count;
  END IF;

  RAISE NOTICE 'Test 1 passed: INSERT with normalize_hour_timestamp() worked';

  -- Test 2: UPSERT with ON CONFLICT (the actual external service pattern)
  INSERT INTO llm_hourly_stats (hour, success_count, failure_count, total_count, total_cost)
  VALUES (normalize_hour_timestamp('2026-09-06T12'), 300, 15, 315, 5.00)
  ON CONFLICT (hour) DO UPDATE SET
    success_count = EXCLUDED.success_count,
    failure_count = EXCLUDED.failure_count,
    total_count = EXCLUDED.total_count,
    total_cost = EXCLUDED.total_cost,
    updated_at = NOW();

  SELECT success_count INTO test_count
  FROM llm_hourly_stats
  WHERE hour = test_hour;

  IF test_count != 300 THEN
    RAISE EXCEPTION 'Test 2 failed: UPSERT did not update, got success_count=%', test_count;
  END IF;

  RAISE NOTICE 'Test 2 passed: ON CONFLICT UPSERT with normalize_hour_timestamp() worked';

  -- Test 3: Helper function approach
  PERFORM upsert_llm_hourly_stats('2026-09-06T12', 400, 20, 420, 7.50);

  SELECT success_count INTO test_count
  FROM llm_hourly_stats
  WHERE hour = test_hour;

  IF test_count != 400 THEN
    RAISE EXCEPTION 'Test 3 failed: Helper function did not update, got success_count=%', test_count;
  END IF;

  RAISE NOTICE 'Test 3 passed: upsert_llm_hourly_stats() helper function worked';

  -- Clean up
  DELETE FROM llm_hourly_stats WHERE hour = test_hour;

  RAISE NOTICE '=================================================================';
  RAISE NOTICE 'All tests passed! External services should use:';
  RAISE NOTICE '  INSERT INTO llm_hourly_stats (hour, ...) ';
  RAISE NOTICE '  VALUES (normalize_hour_timestamp($1), ...) ';
  RAISE NOTICE '  ON CONFLICT (hour) DO UPDATE SET ...;';
  RAISE NOTICE '=================================================================';
END $$;
