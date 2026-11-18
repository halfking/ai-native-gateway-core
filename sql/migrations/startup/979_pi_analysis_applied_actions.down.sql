-- Down migration for 979_pi_analysis_applied_actions
-- ★ 只撤本件加的列；已应用的动作明细会随列一起消失（down 只用于
--   干净回退，生产回滚走 releases/ 目录整树回退，不跑本件）。
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

DO $$
BEGIN
    IF to_regclass('public.error_analysis_results') IS NULL THEN
        RAISE NOTICE '979 down: public.error_analysis_results 不在场 ⇒ 无列可撤';
        RETURN;
    END IF;

    ALTER TABLE public.error_analysis_results
        DROP COLUMN IF EXISTS applied_actions;
END;
$$;

DELETE FROM public.schema_migrations WHERE version = '979';

COMMIT;
