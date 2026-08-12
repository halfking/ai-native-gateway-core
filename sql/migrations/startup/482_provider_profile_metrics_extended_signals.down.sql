-- Migration 482 (down): drop extended metric signal columns from provider_profile_metrics
--
-- Used by `bash scripts/sql-rollback.sh 482` (rule 38 §3).
-- All 14 columns are nullable + no DEFAULT, so DROP COLUMN is safe.
-- Order matters: not really (no constraints reference these columns),
-- but list newest first to match the up migration's reverse order.

BEGIN;

ALTER TABLE provider_profile_metrics
    DROP COLUMN IF EXISTS quality_stability_sample_n,
    DROP COLUMN IF EXISTS quality_stability_is_volatile,
    DROP COLUMN IF EXISTS quality_stability_cv,
    DROP COLUMN IF EXISTS quality_stability_stddev,
    DROP COLUMN IF EXISTS quality_stability_mean,
    DROP COLUMN IF EXISTS longest_downtime_run,
    DROP COLUMN IF EXISTS downtime_total_buckets,
    DROP COLUMN IF EXISTS downtime_buckets,
    DROP COLUMN IF EXISTS concurrency_is_capped,
    DROP COLUMN IF EXISTS concurrency_eff_limit,
    DROP COLUMN IF EXISTS concurrency_limit_auto,
    DROP COLUMN IF EXISTS concurrency_limit,
    DROP COLUMN IF EXISTS rate_limit_total_requests,
    DROP COLUMN IF EXISTS rate_limit_hits;

COMMIT;
