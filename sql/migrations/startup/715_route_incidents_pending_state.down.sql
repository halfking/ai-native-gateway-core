-- 715_route_incidents_pending_state.down.sql
-- Revert addition of 'pending' state.
--
-- WARNING: This down migration will fail if any incidents exist in
-- 'pending' state. Operator must manually update or delete those rows
-- before rolling back.

BEGIN;

-- Drop the new index
DROP INDEX IF EXISTS uq_route_incidents_active_route;

-- Recreate the old index (without 'pending')
CREATE UNIQUE INDEX uq_route_incidents_active_route
    ON route_incidents (
        tenant_id, endpoint_protocol, model, COALESCE(provider_id, 0), COALESCE(credential_id, 0)
    )
    WHERE state IN ('active', 'recovering');

-- Drop the new constraint
ALTER TABLE route_incidents
    DROP CONSTRAINT IF EXISTS route_incidents_state_check;

-- Restore the old constraint (without 'pending')
ALTER TABLE route_incidents
    ADD CONSTRAINT route_incidents_state_check
    CHECK (state IN ('active', 'recovering', 'recovered'));

COMMIT;
