-- Migration 329 down: Drop approval workflow tables

-- Drop triggers first
DROP TRIGGER IF EXISTS approval_rules_updated_at ON approval_rules;
DROP TRIGGER IF EXISTS approval_approvers_updated_at ON approval_approvers;
DROP TRIGGER IF EXISTS approval_configs_updated_at ON approval_configs;

-- Drop the update function
DROP FUNCTION IF EXISTS update_approval_updated_at();

-- Drop foreign key constraints
ALTER TABLE approval_rules DROP CONSTRAINT IF EXISTS fk_approval_rules_tenant;
ALTER TABLE approval_approvers DROP CONSTRAINT IF EXISTS fk_approval_approvers_tenant;
ALTER TABLE approval_configs DROP CONSTRAINT IF EXISTS fk_approval_configs_tenant;
ALTER TABLE approval_requests DROP CONSTRAINT IF EXISTS fk_approval_requests_tenant;

-- Drop tables in reverse dependency order
DROP TABLE IF EXISTS approval_rules;
DROP TABLE IF EXISTS approval_approvers;
DROP TABLE IF EXISTS approval_requests;
DROP TABLE IF EXISTS approval_configs;
