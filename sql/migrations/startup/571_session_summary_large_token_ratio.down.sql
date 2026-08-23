-- 571_session_summary_large_token_ratio.down.sql
-- Rollback is intentionally refused: the prior function body casts token
-- counts to DECIMAL(10,6), which rolls back telemetry writes above 9,999
-- tokens. Restoring that body is not a safe rollback.

BEGIN;

DO $$
BEGIN
    RAISE EXCEPTION
        'migration 571 cannot be rolled back safely: the prior token ratio casts reject long-context request logs';
END;
$$;

COMMIT;
