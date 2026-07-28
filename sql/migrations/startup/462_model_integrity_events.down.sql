-- ===========================================================================
-- File:          sql/migrations/startup/462_model_integrity_events.down.sql
-- Database:      llm_gateway
-- Object Type:   TABLE (rollback)
-- Object Name:   model_integrity_events
-- Purpose:       Rollback migration 462.
-- ===========================================================================

BEGIN;

DROP POLICY IF EXISTS model_integrity_events_tenant_isolation ON public.model_integrity_events;
DROP POLICY IF EXISTS model_integrity_events_super_admin ON public.model_integrity_events;
DROP TABLE IF EXISTS model_integrity_events;

COMMIT;
