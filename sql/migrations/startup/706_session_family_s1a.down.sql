-- Rollback Migration 706: session family S1a tables + sessions access columns.
--
-- drops the three new table families (parent + hot + promote/ensure functions)
-- and the nullable sessions columns added in 706. The schema_migrations row is
-- kept (append-only ledger convention, same as 703/704 down).
--
-- Session tables carry RLS policies; CASCADE is not needed (no dependents
-- beyond indexes/policies internal to each table).

DROP TABLE IF EXISTS public.session_tools_hot;
DROP TABLE IF EXISTS public.session_censors_hot;
DROP TABLE IF EXISTS public.session_memora_hot;

DROP TABLE IF EXISTS public.session_tools CASCADE;
DROP TABLE IF EXISTS public.session_censors CASCADE;
DROP TABLE IF EXISTS public.session_memora CASCADE;

DROP FUNCTION IF EXISTS public.promote_session_tools_hot_to_partition(interval, integer);
DROP FUNCTION IF EXISTS public.promote_session_censors_hot_to_partition(interval, integer);
DROP FUNCTION IF EXISTS public.promote_session_memora_hot_to_partition(interval, integer);
DROP FUNCTION IF EXISTS public.ensure_session_family_partitions(DATE);

ALTER TABLE public.sessions
    DROP COLUMN IF EXISTS project_id,
    DROP COLUMN IF EXISTS api_key_id,
    DROP COLUMN IF EXISTS application_id,
    DROP COLUMN IF EXISTS end_user_id,
    DROP COLUMN IF EXISTS owner_user,
    DROP COLUMN IF EXISTS client_ip,
    DROP COLUMN IF EXISTS agent_name,
    DROP COLUMN IF EXISTS duration_ms;
