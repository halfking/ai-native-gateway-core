-- Rollback Migration 356: Drop Session Health Columns

ALTER TABLE session_summaries
    DROP CONSTRAINT IF EXISTS chk_health_score_range,
    DROP CONSTRAINT IF EXISTS chk_health_grade_enum,
    DROP CONSTRAINT IF EXISTS chk_outcome_enum,
    DROP COLUMN IF EXISTS health_score,
    DROP COLUMN IF EXISTS health_grade,
    DROP COLUMN IF EXISTS outcome,
    DROP COLUMN IF EXISTS last_health_at;
