-- 544_session_project_attribution.down.sql
-- Both tables are new in 544 and nothing else references them, so a plain
-- DROP is safe. session_dim.project_id (migration 407) is untouched by
-- 544 and must survive this rollback.

DROP TABLE IF EXISTS public.session_project_attribution CASCADE;
DROP TABLE IF EXISTS public.project_dim CASCADE;
