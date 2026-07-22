-- Rollback: 454_response_format_anomalies
BEGIN;
DROP VIEW IF EXISTS v_format_anomaly_summary;
DROP TABLE IF EXISTS response_format_anomalies CASCADE;
COMMIT;
