-- Down for 931: drop the userscript capture family (P0-3 M1).
-- ⚠ 删表即删捕获历史（bodies 是正文唯一持有方）——先导出再执行。
BEGIN;
-- ★ 锁守卫（与 up 迁移同款）：DROP TABLE 拿 AccessExclusiveLock ⇒ 等锁期间
--   会堵住全库 ⇒ 拿不到锁就快速失败。SET LOCAL 必须落在事务块内。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';
DROP POLICY IF EXISTS capture_tenant_isolation ON public.capture_bodies;
DROP POLICY IF EXISTS capture_tenant_isolation ON public.capture_messages;
DROP POLICY IF EXISTS capture_tenant_isolation ON public.capture_batches;
DROP TABLE IF EXISTS public.capture_bodies;
DROP TABLE IF EXISTS public.capture_messages;
DROP TABLE IF EXISTS public.capture_batches;
DELETE FROM public.schema_migrations WHERE version = '931';
COMMIT;
