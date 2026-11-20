-- Down migration for 995_error_playbook
-- ★ 只撤本件建的两表/函数；down 只用于干净回退，生产回滚走 releases/
--   目录整树回退，不跑本件。
BEGIN;
SET lock_timeout = '5s';
SET statement_timeout = '10min';

DROP TABLE IF EXISTS public.error_playbook_executions;
DROP TABLE IF EXISTS public.error_playbook_entries;
DROP FUNCTION IF EXISTS public.fn_error_playbook_entries_updated_at();

DELETE FROM public.schema_migrations WHERE version = '995';

COMMIT;
