-- Rollback for migration 838.
--
-- Restores the pre-838 behavior: every partition of the last N months is
-- analyzed on every pass, with no "never analyzed" guard.
--
-- ⚠ This restores a known waste. The previous-month group measured 25.7% of
-- the pass's per-column statistics work on production (runbook §10.92), and a
-- rolled-over partition stops receiving writes, so its statistics cannot go
-- stale. Roll back only to unblock a stuck migration, not as a fix.
--
-- Status: active
-- Idempotent: YES (CREATE OR REPLACE FUNCTION)

BEGIN;

CREATE OR REPLACE FUNCTION analyze_llm_gateway_table_stats(p_recent_months integer DEFAULT 2)
RETURNS integer
LANGUAGE plpgsql
AS $function$
DECLARE
    r record;
    suffix text;
    m integer;
    analyzed integer := 0;
BEGIN
    IF p_recent_months < 1 THEN
        p_recent_months := 1;
    ELSIF p_recent_months > 12 THEN
        p_recent_months := 12;
    END IF;

    -- heap 热表
    FOR r IN
        SELECT c.relname
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        JOIN pg_am am ON am.oid = c.relam
        WHERE n.nspname = 'public'
          AND c.relkind = 'r'
          AND c.relname LIKE '%\_hot' ESCAPE '\'
          AND am.amname = 'heap'
    LOOP
        EXECUTE format('ANALYZE %I', r.relname);
        analyzed := analyzed + 1;
    END LOOP;

    -- 近 N 个月分区（columnar + heap）
    FOR m IN 0..(p_recent_months - 1) LOOP
        suffix := to_char(date_trunc('month', now()) - (m || ' months')::interval, 'YYYY_MM');
        FOR r IN
            SELECT c.relname
            FROM pg_class c
            JOIN pg_namespace n ON n.oid = c.relnamespace
            WHERE n.nspname = 'public'
              AND c.relkind = 'r'
              AND c.relname ~ ('^(' ||
                  'credential_model_index|model_probe_runs|request_logs|routing_decision_log|' ||
                  'request_wal|usage_ledger|credit_ledger|tool_usage_stats|' ||
                  'candidate_failure_logs|handoff_logs|request_logs_bodies' ||
                  ')_' || suffix || '$')
        LOOP
            EXECUTE format('ANALYZE %I', r.relname);
            analyzed := analyzed + 1;
        END LOOP;
    END LOOP;

    RETURN analyzed;
END;
$function$;

COMMENT ON FUNCTION analyze_llm_gateway_table_stats(integer) IS
'ANALYZE hot heap tables + recent monthly partitions (default 2 months).
Columnar partitions do not always get autovacuum analyze after bulk INSERT.
Rolled back from migration 838: this restores unconditional re-analysis of
every month in range.';

COMMIT;