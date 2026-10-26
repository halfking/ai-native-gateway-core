-- Destructive schema rollback; use the rule API for policy version rollback.

DROP TABLE IF EXISTS sanitize_rule_instances,sanitize_rule_events,sanitize_rule_head,sanitize_rule_versions CASCADE;
DROP FUNCTION IF EXISTS sanitize_rule_immutable_version();
