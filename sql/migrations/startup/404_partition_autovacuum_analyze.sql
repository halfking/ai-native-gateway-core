-- 404_partition_autovacuum_analyze.sql
-- 2026-07-15: llm_gateway 热表 + 模型/请求分区表 autovacuum 显式开启，
-- 并补充 analyze 函数（columnar 分区 bulk promote 后 autovacuum analyze 常不触发）。
--
-- 背景：PG17 集群 autovacuum=on，但 credential_model_index_* columnar 分区
-- 在 80 万行时 last_autoanalyze 仍为空，导致规划器统计陈旧。

BEGIN;

-- ── 1. 热表 + 分区父表：显式 autovacuum + 更积极的 analyze ─────────────
CREATE OR REPLACE FUNCTION apply_llm_gateway_autovacuum_settings()
RETURNS integer
LANGUAGE plpgsql
AS $function$
DECLARE
    opts_sql constant text := '
        autovacuum_enabled=true,
        autovacuum_vacuum_scale_factor=0.05,
        autovacuum_vacuum_threshold=10,
        autovacuum_analyze_scale_factor=0.02,
        autovacuum_analyze_threshold=50';
    r record;
    applied integer := 0;
BEGIN
    FOR r IN
        SELECT c.relname
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE n.nspname = 'public'
          AND c.relkind = 'r'
          AND (
              c.relname LIKE '%\_hot' ESCAPE '\'
              OR c.relname = ANY (ARRAY[
                  'credential_probe_model_log'
              ])
          )
    LOOP
        BEGIN
            EXECUTE format('ALTER TABLE %I SET (%s)', r.relname, opts_sql);
            applied := applied + 1;
        EXCEPTION
            WHEN undefined_table THEN NULL;
            WHEN others THEN
                RAISE NOTICE 'apply_llm_gateway_autovacuum_settings: skip % (%).', r.relname, SQLERRM;
        END;
    END LOOP;

    -- 已有月度子分区（含 columnar）
    FOR r IN
        SELECT c.relname
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        JOIN pg_inherits i ON i.inhrelid = c.oid
        JOIN pg_class p ON p.oid = i.inhparent
        WHERE n.nspname = 'public'
          AND p.relname = ANY (ARRAY[
              'credential_model_index', 'model_probe_runs', 'request_logs',
              'routing_decision_log', 'request_wal', 'usage_ledger',
              'credit_ledger', 'tool_usage_stats', 'candidate_failure_logs',
              'handoff_logs', 'request_logs_bodies'
          ])
    LOOP
        BEGIN
            EXECUTE format('ALTER TABLE %I SET (%s)', r.relname, opts_sql);
            applied := applied + 1;
        EXCEPTION
            WHEN others THEN
                RAISE NOTICE 'apply_llm_gateway_autovacuum_settings: skip partition % (%).', r.relname, SQLERRM;
        END;
    END LOOP;

    RETURN applied;
END;
$function$;

COMMENT ON FUNCTION apply_llm_gateway_autovacuum_settings() IS
'Enable aggressive autovacuum/analyze reloptions on llm_gateway hot + partition tables.
Idempotent. Migration 404 / partition_manager startup.';

-- ── 2. 主动 ANALYZE（columnar 分区 promote 后补统计）────────────────────
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
Columnar partitions do not always get autovacuum analyze after bulk INSERT.';

SELECT apply_llm_gateway_autovacuum_settings();
SELECT analyze_llm_gateway_table_stats(3);

COMMIT;
