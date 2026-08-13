-- test_513.sql — V3.2 schema unification + dual-write 验证脚本
-- 用法: psql <dsn> -f test_513.sql
-- 覆盖: gateway schema 已不存在 / public.session_turns 10 列到位 / 原 cleanup/partition 函数还能跑

\set ON_ERROR_STOP on

-- ── 1. gateway schema 已不存在 ────────────────────────────────────────────
SELECT '513: gateway schema dropped' AS check_name,
       COUNT(*) = 0 AS pass
  FROM information_schema.schemata
 WHERE schema_name = 'gateway';

-- ── 2. gateway schema 下 4 张 v2 表已不存在 ─────────────────────────────────
SELECT '513: gateway.sessions dropped' AS check_name,
       COUNT(*) = 0 AS pass
  FROM information_schema.tables
 WHERE table_schema = 'gateway' AND table_name = 'sessions';

SELECT '513: gateway.session_turns dropped' AS check_name,
       COUNT(*) = 0 AS pass
  FROM information_schema.tables
 WHERE table_schema = 'gateway' AND table_name = 'session_turns';

SELECT '513: gateway.session_bodies dropped' AS check_name,
       COUNT(*) = 0 AS pass
  FROM information_schema.tables
 WHERE table_schema = 'gateway' AND table_name = 'session_bodies';

SELECT '513: gateway.session_turn_logs dropped' AS check_name,
       COUNT(*) = 0 AS pass
  FROM information_schema.tables
 WHERE table_schema = 'gateway' AND table_name = 'session_turn_logs';

-- ── 3. public.session_turns 10 列到位 ─────────────────────────────────────
CREATE OR REPLACE FUNCTION test_513_col_exists(col_name text)
RETURNS boolean AS $$
  SELECT EXISTS (
    SELECT 1 FROM information_schema.columns
     WHERE table_schema = 'public'
       AND table_name = 'session_turns'
       AND column_name = col_name
  );
$$ LANGUAGE SQL;

SELECT '513: public.session_turns.t0_arrived_at' AS check_name, test_513_col_exists('t0_arrived_at') AS pass
UNION ALL SELECT '513: public.session_turns.t1_total_enqueued_at', test_513_col_exists('t1_total_enqueued_at')
UNION ALL SELECT '513: public.session_turns.t2_total_dequeued_at', test_513_col_exists('t2_total_dequeued_at')
UNION ALL SELECT '513: public.session_turns.t3_model_enqueued_at', test_513_col_exists('t3_model_enqueued_at')
UNION ALL SELECT '513: public.session_turns.t4_model_dequeued_at', test_513_col_exists('t4_model_dequeued_at')
UNION ALL SELECT '513: public.session_turns.t5_cred_enqueued_at',  test_513_col_exists('t5_cred_enqueued_at')
UNION ALL SELECT '513: public.session_turns.t6_cred_dequeued_at',  test_513_col_exists('t6_cred_dequeued_at')
UNION ALL SELECT '513: public.session_turns.t7_forward_start_at',  test_513_col_exists('t7_forward_start_at')
UNION ALL SELECT '513: public.session_turns.t8_response_start_at', test_513_col_exists('t8_response_start_at')
UNION ALL SELECT '513: public.session_turns.t9_response_end_at',   test_513_col_exists('t9_response_end_at');

-- ── 4. 索引到位 ─────────────────────────────────────────────────────────────
SELECT '513: idx_session_turns_t0_arrived' AS check_name,
       COUNT(*) = 1 AS pass
  FROM pg_indexes
 WHERE schemaname = 'public'
   AND tablename = 'session_turns'
   AND indexname = 'idx_session_turns_t0_arrived';

-- ── 5. cleanup_expired_session_turn_logs() 在 public schema 还能跑 ─────────
SELECT '513: cleanup_expired_session_turn_logs() callable' AS check_name,
       public.cleanup_expired_session_turn_logs() IS NULL AS pass;

-- ── 6. ensure_sessions_v2_partitions() 还能跑（用下月日期，无副作用）────
SELECT '513: ensure_sessions_v2_partitions() callable' AS check_name,
       public.ensure_sessions_v2_partitions((CURRENT_DATE + INTERVAL '1 month')::date) IS NULL AS pass;

-- ── 7. INSERT + 读回 10 列一致 ────────────────────────────────────────────
INSERT INTO public.session_turns (
    session_id, turn_no, tenant_id, request_id, ts,
    submit_mode, source_kind, quality, partition_date,
    t0_arrived_at, t1_total_enqueued_at, t2_total_dequeued_at,
    t3_model_enqueued_at, t4_model_dequeued_at, t5_cred_enqueued_at,
    t6_cred_dequeued_at, t7_forward_start_at, t8_response_start_at,
    t9_response_end_at
) VALUES (
    'test-513-sess', 999, 'default', 'test-513-req', NOW(),
    'full', 'live', 'verified', CURRENT_DATE,
    NOW(), NOW() + INTERVAL '1 ms', NOW() + INTERVAL '2 ms',
    NOW() + INTERVAL '3 ms', NOW() + INTERVAL '4 ms', NOW() + INTERVAL '5 ms',
    NOW() + INTERVAL '6 ms', NOW() + INTERVAL '7 ms', NOW() + INTERVAL '8 ms',
    NOW() + INTERVAL '9 ms'
)
ON CONFLICT (tenant_id, request_id, partition_date) DO UPDATE SET
    t0_arrived_at = EXCLUDED.t0_arrived_at,
    t9_response_end_at = EXCLUDED.t9_response_end_at;

SELECT '513: insert+read back' AS check_name,
       t0_arrived_at < t9_response_end_at AS pass
  FROM public.session_turns
 WHERE tenant_id = 'default' AND request_id = 'test-513-req';

-- 清理
DELETE FROM public.session_turns WHERE request_id = 'test-513-req';

DROP FUNCTION test_513_col_exists(text);
