-- Persist extended provider profile metric signals for existing installations.
-- Safe to run more than once.
BEGIN;

ALTER TABLE provider_profile_metrics
    ADD COLUMN IF NOT EXISTS rate_limit_hits INTEGER,
    ADD COLUMN IF NOT EXISTS rate_limit_total_requests INTEGER,
    ADD COLUMN IF NOT EXISTS concurrency_limit INTEGER,
    ADD COLUMN IF NOT EXISTS concurrency_limit_auto INTEGER,
    ADD COLUMN IF NOT EXISTS concurrency_eff_limit INTEGER,
    ADD COLUMN IF NOT EXISTS concurrency_is_capped BOOLEAN,
    ADD COLUMN IF NOT EXISTS downtime_buckets INTEGER,
    ADD COLUMN IF NOT EXISTS downtime_total_buckets INTEGER,
    ADD COLUMN IF NOT EXISTS longest_downtime_run INTEGER,
    ADD COLUMN IF NOT EXISTS quality_stability_mean DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS quality_stability_stddev DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS quality_stability_cv DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS quality_stability_is_volatile BOOLEAN,
    ADD COLUMN IF NOT EXISTS quality_stability_sample_n INTEGER;

COMMIT;
