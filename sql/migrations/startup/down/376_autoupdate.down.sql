-- 376_autoupdate.down.sql
-- Rollback autoupdate module tables

DROP TABLE IF EXISTS instance_release_status CASCADE;
DROP TABLE IF EXISTS upgrade_logs CASCADE;
DROP TABLE IF EXISTS gray_release_rules CASCADE;
DROP TABLE IF EXISTS releases CASCADE;
