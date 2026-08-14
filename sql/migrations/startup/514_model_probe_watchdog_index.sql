-- Migration 514: optimize the non-featured healthy watchdog update.
-- The watchdog scans only healthy_confirmed rows ordered by next_retry_at.
-- Keep this migration idempotent for startup and deploy re-runs.

CREATE INDEX IF NOT EXISTS idx_mps_healthy_confirmed_next_retry
    ON model_probe_state (next_retry_at)
    WHERE state = 'healthy_confirmed';
