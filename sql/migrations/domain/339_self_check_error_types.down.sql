-- Migration 339 (down): restore the original self-check error constraint.
BEGIN;

ALTER TABLE self_check_runs
    DROP CONSTRAINT IF EXISTS self_check_runs_error_type_check;

ALTER TABLE self_check_runs
    ADD CONSTRAINT self_check_runs_error_type_check CHECK (
        error_type IS NULL OR error_type IN (
            'http_000', 'http_502', 'http_503', 'http_504',
            'timeout', 'upstream_fail', 'none'
        )
    );

COMMIT;
