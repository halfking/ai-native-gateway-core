-- 400_distribution.down.sql

DROP TABLE IF EXISTS release_artifacts;
DROP TABLE IF EXISTS donations;
DROP TABLE IF EXISTS download_events;
ALTER TABLE licenses DROP COLUMN IF EXISTS holder_id;
DROP TABLE IF EXISTS license_holders;
