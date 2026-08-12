-- Migration 999 (TEST): POST_CONDITION pass-path regression
--
-- 一次性测试迁移，验证 db-changelog.sh 的 POST_CONDITION assert 链路
-- 端到端工作 (PASS 分支)。
--
-- 验证步骤:
--   1. commit + push
--   2. bash scripts/deploy-245.sh → deploy log 应显示
--        [db]     | BEGIN
--        [db]     | DO
--        [db]     | COMMIT
--        [db]     [assert] SELECT 1 FROM pg_class WHERE relname = 'pg_class'
--        [db]     [assert] ✓
--   3. 验证后删除本文件 + commit + push 清理
--
-- 设计:
--   - DO block 仅 RAISE NOTICE 一条 marker
--   - POST_CONDITION 总是返回 1 (pg_class 永远存在)
--   - 这是 PASS 分支测试 — fail 分支通过手动制造条件验证
--     (见 999_test_postcondition_fail.sql 临时性测试, 验证后删除)

BEGIN;

DO $$
BEGIN
  RAISE NOTICE 'postcondition_pass verification marker — POST_CONDITION pass path works';
END $$;

COMMIT;

-- POST_CONDITION: SELECT 1 FROM pg_class WHERE relname = 'pg_class'
