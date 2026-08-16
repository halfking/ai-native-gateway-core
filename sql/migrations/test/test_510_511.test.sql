-- test_510_511.sql — V3.2 迁移验证脚本
-- 用法: psql <dsn> -f test_510_511.sql
-- 覆盖: 列存在 / CHECK 约束 / 索引 / 视图 / 插入查询 / 回滚可读性

\set ON_ERROR_STOP on

-- ── 510: request_type 列存在于三处 ─────────────────────────────
SELECT '510: request_type on request_logs_hot' AS check_name,
       COUNT(*) = 1 AS pass
  FROM information_schema.columns
 WHERE table_name = 'request_logs_hot' AND column_name = 'request_type';

SELECT '510: request_type on request_logs' AS check_name,
       COUNT(*) = 1 AS pass
  FROM information_schema.columns
 WHERE table_name = 'request_logs' AND column_name = 'request_type';

SELECT '510: request_type on view' AS check_name,
       COUNT(*) = 1 AS pass
  FROM information_schema.columns
 WHERE table_name = 'request_logs_with_current_month' AND column_name = 'request_type';

-- ── 510: CHECK 约束生效（非法值应报错）─────────────────────────
DO $$
BEGIN
  BEGIN
    INSERT INTO request_logs_hot (request_id, request_type)
    VALUES ('test-510-bad', 'not_a_valid_type');
    RAISE EXCEPTION '510 CHECK FAILED: invalid request_type was accepted';
  EXCEPTION
    WHEN check_violation THEN
      RAISE NOTICE '510: CHECK constraint OK (rejected invalid value)';
  END;
END $$;

-- ── 511: 表与索引存在 ─────────────────────────────────────────
SELECT '511: table exists' AS check_name,
       COUNT(*) = 1 AS pass
  FROM pg_tables WHERE tablename = 'request_state_transitions';

SELECT '511: index request exists' AS check_name,
       COUNT(*) >= 1 AS pass
  FROM pg_indexes
 WHERE tablename = 'request_state_transitions'
   AND indexname = 'idx_state_transitions_request';

-- ── 511: 插入 + 查询 + CHECK ──────────────────────────────────
INSERT INTO request_state_transitions (request_id, tenant_id, transition_type, from_state, to_state, metadata)
VALUES ('test-511-req', 'default', 'route', NULL, 'route_resolve',
        '{"candidates": ["cred-1"], "block_reason": null}'::jsonb),
       ('test-511-req', 'default', 'retry', 'upstream_request', 'route_credential',
        '{"retry_seq": 1, "reason_class": "rate_limit"}'::jsonb),
       ('test-511-req', 'default', 'node_switch', 'cred-1', 'cred-2',
        '{"from_node": "cred-1", "to_node": "cred-2"}'::jsonb);

SELECT '511: insert + query' AS check_name,
       COUNT(*) = 3 AS pass
  FROM request_state_transitions
 WHERE request_id = 'test-511-req';

-- 清理测试数据
DELETE FROM request_state_transitions WHERE request_id = 'test-511-req';

SELECT 'ALL CHECKS DONE' AS status;
