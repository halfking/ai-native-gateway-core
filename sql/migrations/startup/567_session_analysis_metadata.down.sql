-- 567_session_analysis_metadata.down.sql
-- Roll back session_analysis_metadata table.

BEGIN;

DROP INDEX IF EXISTS public.idx_session_analysis_metadata_tenant_updated;
DROP TABLE IF EXISTS public.session_analysis_metadata;

COMMIT;
