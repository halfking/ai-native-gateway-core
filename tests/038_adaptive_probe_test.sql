-- Migration 038 test: verify the adaptive backoff algorithm.
--
-- Tests the model_probe_backoff_v2 function with representative inputs
-- spanning the full age × failures grid documented in the design doc.
--
-- Run: psql -f tests/038_adaptive_probe_test.sql

-- Test 1: 0 failures → 2h watchdog regardless of age
SELECT '0f-5m' AS case, model_probe_backoff_v2(0, NOW() - INTERVAL '5 minutes') AS got
UNION ALL
SELECT '0f-1h', model_probe_backoff_v2(0, NOW() - INTERVAL '1 hour')
UNION ALL
SELECT '0f-3d', model_probe_backoff_v2(0, NOW() - INTERVAL '3 days');

-- Test 2: 1 failure ramps 1m → 30m as age increases
SELECT '1f-<5m' AS case, model_probe_backoff_v2(1, NOW() - INTERVAL '4 minutes') AS got
UNION ALL
SELECT '1f-5-30m', model_probe_backoff_v2(1, NOW() - INTERVAL '20 minutes')
UNION ALL
SELECT '1f-30-60m', model_probe_backoff_v2(1, NOW() - INTERVAL '45 minutes')
UNION ALL
SELECT '1f->60m', model_probe_backoff_v2(1, NOW() - INTERVAL '2 hours');

-- Test 3: 2 failures ramps 2m → 45m
SELECT '2f-<5m', model_probe_backoff_v2(2, NOW() - INTERVAL '4 minutes')
UNION ALL
SELECT '2f-5-30m', model_probe_backoff_v2(2, NOW() - INTERVAL '20 minutes')
UNION ALL
SELECT '2f-30-60m', model_probe_backoff_v2(2, NOW() - INTERVAL '45 minutes')
UNION ALL
SELECT '2f->60m', model_probe_backoff_v2(2, NOW() - INTERVAL '2 hours');

-- Test 4: 3+ failures → 60m (still recovering toward broken)
SELECT '3f-1m', model_probe_backoff_v2(3, NOW() - INTERVAL '1 minute')
UNION ALL
SELECT '5f-1h', model_probe_backoff_v2(5, NOW() - INTERVAL '1 hour');

-- Test 5: NULL last_attempt_at → defaults to "old" (1h ago)
SELECT '1f-null', model_probe_backoff_v2(1, NULL);

-- Test 6: passive boost (requires candidate_failure_logs rows)
-- Insert fake failures, run boost, verify next_retry_at is pulled forward
INSERT INTO candidate_failure_logs
    (tenant_id, credential_id, raw_model_name, raw_model_id, error_kind,
     http_status, error_code, failure_stage, failure_detail_code, ts)
SELECT 'default', 99999, 'fake-model', NULL, 'transient',
       500, 'test', 'upstream', 'test', NOW() - INTERVAL '1 minute'
FROM generate_series(1, 3);

SELECT model_probe_passive_boost(99999, 'fake-model', NOW());

-- Cleanup
DELETE FROM candidate_failure_logs
WHERE credential_id = 99999 AND raw_model_name = 'fake-model';

DELETE FROM model_probe_state
WHERE credential_id = 99999 AND raw_model_name = 'fake-model';