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

-- Test 6: passive boost —— 真断言版（2026-10-13 重写）
--
-- ⚠️ 旧版的问题（三条，都实测过）：
--  1. 注释写「verify next_retry_at is pulled forward」，函数体里**没有任何 verify**；
--  2. 从未插入 model_probe_state 行 ⇒ 函数里的 UPDATE 匹配 0 行，
--     阈值 / LEAST / 状态豁免三段逻辑一条都测不到；
--  3. 用的是 raw_model_id / http_status / failure_stage 等**已不存在的列**，
--     实跑直接 ERROR 退出 ⇒ 这份判据在当前 schema 上根本跑不完。
--
-- 现在用 RAISE EXCEPTION 断言，失败会让 psql 非零退出，可直接当门。
-- 全程包在事务里并 ROLLBACK，用伪造的 credential_id=99999，不留任何状态。
BEGIN;

CREATE OR REPLACE FUNCTION pg_temp.pb_assert(p_case text, p_ok boolean, p_detail text)
RETURNS void LANGUAGE plpgsql AS $$
BEGIN
  IF NOT p_ok THEN
    RAISE EXCEPTION '判据失败 [%]: %', p_case, p_detail;
  END IF;
  RAISE NOTICE 'PASS  %', p_case;
END; $$;

CREATE OR REPLACE FUNCTION pg_temp.pb_case(p_name text, p_n int, p_state text, p_not_before timestamptz)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE v_got timestamptz; v_boost boolean;
BEGIN
  INSERT INTO model_probe_state (credential_id, raw_model_name, state, next_retry_at)
  VALUES (99999::bigint, 'pb-fake-model', p_state, p_not_before);

  INSERT INTO candidate_failure_logs
    (request_id, credential_id, provider_id, raw_model_name, error_kind, ts)
  SELECT 'pb-req-'||g, 99999, 1, 'pb-fake-model', 'transient', NOW() - INTERVAL '1 minute'
  FROM generate_series(1, p_n) g;

  PERFORM model_probe_passive_boost(99999::bigint, 'pb-fake-model', NOW());

  SELECT next_retry_at INTO v_got FROM model_probe_state
   WHERE credential_id = 99999::bigint AND raw_model_name = 'pb-fake-model';

  v_boost := p_state NOT IN ('healthy_confirmed','broken_confirmed') AND p_n >= 2;

  IF v_boost THEN
    -- 阈值：3 次→30s 内，2 次→1m 内
    PERFORM pg_temp.pb_assert(p_name,
      v_got <= NOW() + (CASE WHEN p_n >= 3 THEN INTERVAL '31 seconds' ELSE INTERVAL '61 seconds' END),
      format('next_retry_at=%s，期望被推到 now+%s 内', v_got,
             CASE WHEN p_n >= 3 THEN '30s' ELSE '1m' END));
  ELSE
    -- 未达阈值或状态被豁免 ⇒ 原样不动
    PERFORM pg_temp.pb_assert(p_name, v_got = p_not_before,
      format('next_retry_at=%s 期望保持 %s 不变', v_got, p_not_before));
  END IF;

  -- ⭐ 单调性：已排得很近的（10s）绝不能被推到更远
  IF p_n >= 3 AND p_state NOT IN ('healthy_confirmed','broken_confirmed') THEN
    PERFORM pg_temp.pb_assert('LEAST 单调性：已排 10s 不得被推到 30s 之后',
      v_got <= NOW() + INTERVAL '30 seconds', format('next_retry_at=%s', v_got));
  END IF;

  DELETE FROM model_probe_state WHERE credential_id = 99999::bigint;
  DELETE FROM candidate_failure_logs WHERE credential_id = 99999;
END; $$;

SELECT pg_temp.pb_case('3 failures → 推到现在+30s 内', 3, 'default',           NOW() + INTERVAL '2 hours');
SELECT pg_temp.pb_case('2 failures → 推到现在+1m 内',  2, 'default',           NOW() + INTERVAL '2 hours');
SELECT pg_temp.pb_case('1 failure  → 不动',            1, 'default',           NOW() + INTERVAL '2 hours');
SELECT pg_temp.pb_case('healthy_confirmed → 豁免',    3, 'healthy_confirmed', NOW() + INTERVAL '2 hours');
SELECT pg_temp.pb_case('broken_confirmed  → 豁免',    3, 'broken_confirmed',  NOW() + INTERVAL '2 hours');
SELECT pg_temp.pb_case('已排 10s → 不得被推后',        3, 'default',           NOW() + INTERVAL '10 seconds');

\echo '=== passive boost 全部判据通过 ==='
ROLLBACK;

-- Test 5b: recovery ladder 真断言（2026-10-13 新增）
-- 旧版只 SELECT 打印让人目视；且 bg/model_probe_adaptive_test.go 里那张
-- 「failures → interval」表**7 行错了 4 行**，因为它漏了 age 这一维。
-- 下面的期望值是 2026-10-13 在 245 活库上**实测**出来的，不是从文档抄的。
BEGIN;
CREATE OR REPLACE FUNCTION pg_temp.ladder_assert(p_f int, p_age_min int, p_want_s int)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE v_got int;
BEGIN
  v_got := EXTRACT(EPOCH FROM model_probe_backoff_v2(
            p_f, NOW() - p_age_min * INTERVAL '1 minute'))::int;
  IF v_got <> p_want_s THEN
    RAISE EXCEPTION '阶梯判据失败 [%f/ %min age]: 实得 %s, 期望 %s',
      p_f, p_age_min, v_got, p_want_s;
  END IF;
  RAISE NOTICE 'PASS  %f 次失败 / %min age => %s', p_f, p_age_min, v_got;
END; $$;

-- 0 次失败：恒定 2h 看门狗
SELECT pg_temp.ladder_assert(0,   1, 7200);
SELECT pg_temp.ladder_assert(0, 1440, 7200);
-- 1 次失败：120s（**不是** 10s）
SELECT pg_temp.ladder_assert(1,   1,  120);
-- 2 次失败：随 age 爬升 300 → 600 → 900 → 2700（**不是** 恒定 30s）
SELECT pg_temp.ladder_assert(2,   1,  300);
SELECT pg_temp.ladder_assert(2,  20,  600);
SELECT pg_temp.ladder_assert(2,  45,  900);
SELECT pg_temp.ladder_assert(2, 120, 2700);
-- 3 次及以上：恒定 3600s
SELECT pg_temp.ladder_assert(3,   1, 3600);
SELECT pg_temp.ladder_assert(4,   1, 3600);
SELECT pg_temp.ladder_assert(5,  60, 3600);
SELECT pg_temp.ladder_assert(7,   1, 3600);

\echo '=== recovery ladder 全部判据通过 ==='
ROLLBACK;
