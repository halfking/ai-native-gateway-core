-- Down for 933: drop task_events.
BEGIN;
-- ★ 锁守卫（与 up 迁移同款）：DROP TABLE 拿 AccessExclusiveLock ⇒ 等锁期间
--   会堵住全库 ⇒ 拿不到锁就快速失败。SET LOCAL 必须落在事务块内。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';
DROP TABLE IF EXISTS public.task_events;
DELETE FROM public.schema_migrations WHERE version = '933';
COMMIT;
