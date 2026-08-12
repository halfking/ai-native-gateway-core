-- Migration 999 (TEST): verify deploy-seamless captures RAISE NOTICE
--
-- 一次性测试迁移，验证 db-changelog.sh 的 NOTICE capture 修复
-- (scripts/deploy-lib/db-changelog.sh:295 附近) 端到端工作。
--
-- 验证步骤:
--   1. commit + push
--   2. bash scripts/deploy-245.sh → deploy log 应显示
--        [db]   | NOTICE:  test_capture_migration verification marker
--      表示 stdout capture 链路正常
--   3. 删除本文件 + commit + push 清理
--
-- 设计:
--   - DO block RAISE NOTICE 一条 marker 字符串
--   - 不修改任何 schema (幂等, no schema changes)
--   - 故意用 NOTICE 而不是 RAISE EXCEPTION, 让 psql 退出 0,
--     让 _db_log "    |" 前缀能进入 deploy log

BEGIN;

DO $$
BEGIN
  RAISE NOTICE 'test_capture_migration verification marker — db-changelog.sh stdout capture works';
END $$;

COMMIT;
