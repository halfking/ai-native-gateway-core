-- 715_route_incidents_pending_state.sql
-- Add 'pending' state to route_incidents to support threshold-based visibility.
--
-- Background: incidents were becoming visible at streak 1 instead of waiting
-- for the FailureToActive threshold (default 3). The 'pending' state tracks
-- failures below the threshold without making them visible on the dashboard.
--
-- Changes:
--   1. Extend state CHECK constraint to include 'pending'.
--   2. Extend partial unique index to include 'pending' (only one
--      pending/active/recovering incident per route).
--
-- Migration safety:
--   - Backward compatible: existing 'active'/'recovering'/'recovered' rows
--     remain valid.
--   - Ordering: the CONSTRAINT must allow 'pending' BEFORE the new code can
--     write it — otherwise DecideState's first sub-threshold write fails with
--     23514 and the observer silently drops all incident tracking. This file
--     is mirrored by db.ensureRouteIncidentPendingState, applied by
--     ApplyMigrations at binary startup before any traffic is served, so the
--     constraint always lands before the first 'pending' write. Manual psql
--     application is only needed if the operator skips the startup chain.
--   - No data migration needed: existing incidents are already active
--     (they crossed the threshold in the old logic).

BEGIN;

-- Drop the old constraint
ALTER TABLE route_incidents
    DROP CONSTRAINT IF EXISTS route_incidents_state_check;

-- Add the new constraint with 'pending'
ALTER TABLE route_incidents
    ADD CONSTRAINT route_incidents_state_check
    CHECK (state IN ('pending', 'active', 'recovering', 'recovered'));

-- Drop the old partial unique index
DROP INDEX IF EXISTS uq_route_incidents_active_route;

-- Recreate with 'pending' included in the WHERE clause
CREATE UNIQUE INDEX uq_route_incidents_active_route
    ON route_incidents (
        tenant_id, endpoint_protocol, model, COALESCE(provider_id, 0), COALESCE(credential_id, 0)
    )
    WHERE state IN ('pending', 'active', 'recovering');

COMMIT;
