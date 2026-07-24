-- Migration 457: Session V2 owner-user RLS filter
--
-- Purpose: add an owner-user predicate on top of the existing tenant isolation
-- policies (430) for the V2 tables (sessions / session_turns / session_bodies).
-- The tenant isolation policy remains as the FOR ALL RESTRICTIVE *baseline*;
-- this migration adds a second RESTRICTIVE layer so both must pass.
--
-- Up-stream:
--   - 430_sessions_v2_schema.sql  (V2 tables + tenant + super_admin policies)
--   - 456_session_v2_display_columns.sql  (no DDL changes to policy)
--
-- Schema reality:
--   - gateway.sessions / session_turns / session_bodies each have an
--     `tenant_id` column and a `session_id` column.
--   - session_dim lives in the PUBLIC schema (created by 350) and its PK is
--     `gw_session_id`. It does NOT currently carry an `owner_user` column —
--     owner_user is a request_logs concept.
--   - To resolve owner_user we join to public.request_logs picking the
--     earliest (first-seen) request per gw_session_id. This is a stable
--     association because a session is owned by whoever created it.
--
-- Notes:
--   - RESTRICTIVE means BOTH the tenant policy (430) AND this owner policy
--     must pass for a row to be visible. Existing tenant isolation is
--     preserved.
--   - The policy allows super_admin / bypass_rls to see everything, matching
--     the convention from 430.
--
-- Author: llm-gateway-ops
-- Date: 2026-07-24

BEGIN;

-- session_turns ─────────────────────────────────────────────────────────
DROP POLICY IF EXISTS session_turns_owner_filter ON gateway.session_turns;
CREATE POLICY session_turns_owner_filter ON gateway.session_turns
    AS RESTRICTIVE
    FOR ALL
    TO PUBLIC
    USING (
        EXISTS (
            SELECT 1
            FROM (
                SELECT DISTINCT ON (gw_session_id) gw_session_id, owner_user
                FROM public.request_logs
                WHERE gw_session_id IS NOT NULL
                ORDER BY gw_session_id, ts ASC
            ) first_rl
            WHERE first_rl.gw_session_id = gateway.session_turns.session_id
              AND (
                  first_rl.owner_user = current_setting('app.current_user', true)
                  OR current_setting('app.current_role', true) = 'super_admin'
                  OR current_setting('app.bypass_rls', true) = 'true'
              )
        )
    );

-- sessions ──────────────────────────────────────────────────────────────
DROP POLICY IF EXISTS sessions_owner_filter ON gateway.sessions;
CREATE POLICY sessions_owner_filter ON gateway.sessions
    AS RESTRICTIVE
    FOR ALL
    TO PUBLIC
    USING (
        EXISTS (
            SELECT 1
            FROM (
                SELECT DISTINCT ON (gw_session_id) gw_session_id, owner_user
                FROM public.request_logs
                WHERE gw_session_id IS NOT NULL
                ORDER BY gw_session_id, ts ASC
            ) first_rl
            WHERE first_rl.gw_session_id = gateway.sessions.session_id
              AND (
                  first_rl.owner_user = current_setting('app.current_user', true)
                  OR current_setting('app.current_role', true) = 'super_admin'
                  OR current_setting('app.bypass_rls', true) = 'true'
              )
        )
    );

-- session_bodies ────────────────────────────────────────────────────────
DROP POLICY IF EXISTS session_bodies_owner_filter ON gateway.session_bodies;
CREATE POLICY session_bodies_owner_filter ON gateway.session_bodies
    AS RESTRICTIVE
    FOR ALL
    TO PUBLIC
    USING (
        EXISTS (
            SELECT 1
            FROM (
                SELECT DISTINCT ON (gw_session_id) gw_session_id, owner_user
                FROM public.request_logs
                WHERE gw_session_id IS NOT NULL
                ORDER BY gw_session_id, ts ASC
            ) first_rl
            WHERE first_rl.gw_session_id = gateway.session_bodies.session_id
              AND (
                  first_rl.owner_user = current_setting('app.current_user', true)
                  OR current_setting('app.current_role', true) = 'super_admin'
                  OR current_setting('app.bypass_rls', true) = 'true'
              )
        )
    );

COMMIT;