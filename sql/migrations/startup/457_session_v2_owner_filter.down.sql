-- Migration 457 down: drop the owner-user RLS filter policies.
BEGIN;

DROP POLICY IF EXISTS session_bodies_owner_filter ON gateway.session_bodies;
DROP POLICY IF EXISTS sessions_owner_filter ON gateway.sessions;
DROP POLICY IF EXISTS session_turns_owner_filter ON gateway.session_turns;

COMMIT;