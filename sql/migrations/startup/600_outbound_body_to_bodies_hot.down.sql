-- Migration 600 down: revert outbound_body consolidation in request_logs_hot
--
-- Phase 1 stops new outbound_body writes to request_logs_hot while retaining
-- the legacy column for compatibility. Reverting the application is enough to
-- restore legacy writes; tenant_id on request_logs_bodies_hot is additive.

\set ON_ERROR_STOP on

-- No destructive down action is required.
