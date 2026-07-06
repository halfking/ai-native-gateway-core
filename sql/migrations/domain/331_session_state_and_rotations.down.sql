-- 331_session_state_and_rotations.down.sql
-- Rollback session state management feature

DROP TABLE IF EXISTS public.session_credential_rotations;
DROP TABLE IF EXISTS public.session_state_snapshots;

-- Note: Redis keys (session:{id}, session:{id}:cred_rotations, session:stopped:{tenant})
-- will naturally expire based on their TTL. No manual cleanup needed for rollback.
