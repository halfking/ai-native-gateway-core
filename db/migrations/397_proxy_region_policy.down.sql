-- Migration 397 down: drop proxy region avoidance columns and policy table.
ALTER TABLE proxy_subscriptions DROP COLUMN IF EXISTS banned_regions;
ALTER TABLE proxy_nodes         DROP COLUMN IF EXISTS banned_regions;
DROP TABLE IF EXISTS proxy_selection_policy;
