-- 671_routing_opt_active_fix.sql
-- P2.2 fix: enforce at most ONE active row in routing_optimization_state.
--
-- Migration 670 created a partial unique index on activated_at
-- (WHERE deactivated_at IS NULL), which does NOT express a single-active-row
-- constraint: two rows with different activated_at can both be active and
-- concurrent OptimizationStateDAO.Create calls can race between the
-- deactivate-UPDATE and the INSERT.
--
-- Fix: unique index on a constant expression over the same partial predicate
-- — PostgreSQL enforces "at most one row matches WHERE deactivated_at IS NULL".
-- Concurrent Create() that violates this now fails on INSERT instead of
-- leaving two active states; the DAO transaction rolls back cleanly.
--
-- 2026-09-07

DROP INDEX IF EXISTS idx_opt_state_active;

CREATE UNIQUE INDEX IF NOT EXISTS idx_opt_state_single_active
    ON routing_optimization_state ((1))
    WHERE deactivated_at IS NULL;

COMMENT ON INDEX idx_opt_state_single_active IS
  'P2.2: at most one active optimization state (constant-expression unique over the partial predicate)';
