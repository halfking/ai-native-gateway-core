-- Rollback for migration 625: drop the explicit-column session_bodies_unified
-- view. Migration 614's original SELECT * view is left in place because it
-- remains valid; callers must not rely on it being dropped.

DROP VIEW IF EXISTS public.session_bodies_unified;
