-- Down for 928: drop the userscript capture family (P0-3 M1).
-- ⚠ 删表即删捕获历史（bodies 是正文唯一持有方）——先导出再执行。
BEGIN;
DROP POLICY IF EXISTS capture_tenant_isolation ON public.capture_bodies;
DROP POLICY IF EXISTS capture_tenant_isolation ON public.capture_messages;
DROP POLICY IF EXISTS capture_tenant_isolation ON public.capture_batches;
DROP TABLE IF EXISTS public.capture_bodies;
DROP TABLE IF EXISTS public.capture_messages;
DROP TABLE IF EXISTS public.capture_batches;
DELETE FROM public.schema_migrations WHERE version = '928';
COMMIT;
