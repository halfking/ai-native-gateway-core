-- Migration 838: stop re-analyzing last month's frozen partitions every hour.
--
-- Why this exists
-- --------------
-- bg.PartitionManager.analyzePartitionStats calls
-- analyze_llm_gateway_table_stats(2) once an hour on every gateway instance.
-- That call is currently the #1 consumer of database time in the gateway
-- database: 81.0% of all measured DB time (runbook §10.90), ~56 minutes/day.
--
-- The function ANALYZEs two groups:
--   * every heap table matching '%\_hot'                (22 relations, 2026-10-07)
--   * <parent>_<YYYY_MM> partitions for the last N months (22 relations)
--
-- Runbook §10.92 measured the split on production, per-relation:
--
--   group                      relations   heap     work_units   share
--   %_hot heap tables               22    64 MB       3,585,388   23.0%
--   current + previous month        22   734 MB      12,038,122   77.0%
--     of which current month       11             8,022,804
--     of which previous month      11             4,015,318   25.7% of total
--
-- work_units = min(est_rows, 30000) * n_columns, i.e. ANALYZE's actual CPU
-- driver. It is not a proxy for table size: a full heap scan of all 22 hot
-- tables takes ~84 ms in total, while one pass of this function takes 28-130
-- seconds, so >99% of the cost is per-column statistics computation on the
-- sampled rows, not page I/O.
--
-- The previous month's partitions are the interesting half. Measured
-- autovacuum coverage of the 7 non-empty 2026_09 partitions:
--
--   candidate_failure_logs_2026_09  119,214 rows  autovacuum analyzed 10-01
--   request_logs_2026_09              4,307 rows  autovacuum analyzed 10-02
--   request_wal_2026_09             446,777 rows  autovacuum analyzed 10-01
--   usage_ledger_2026_09           1,416,558 rows  autovacuum analyzed 10-01
--   credential_model_index_2026_09  301,255 rows  never autoanalyzed (columnar)
--   request_logs_bodies_2026_09       4,408 rows  never autoanalyzed (columnar)
--   routing_decision_log_2026_09   440,138 rows  never autoanalyzed (columnar)
--
-- The columnar three are the known case already documented in the function's
-- own comment: bulk INSERT leaves n_mod_since_analyze = 0 so autovacuum never
-- fires. They genuinely need this pass.
--
-- But every partition is analyzed while it is the CURRENT month, and after
-- the month rolls over it stops receiving writes. Its statistics cannot go
-- stale, so re-analyzing it every hour is repeated work. Steady-state cost
-- of the previous-month group is 25.7% of the pass, spent refreshing
-- statistics that are already correct.
--
-- Fix
-- ---
-- The current month is still analyzed unconditionally. For months older
-- than the current one, a partition is analyzed ONLY when pg_statistic holds
-- no rows for it — i.e. it has never been analyzed at all. That preserves the
-- first-time coverage the columnar partitions depend on (a partition whose
-- first analysis was skipped while the service was down still gets picked up),
-- while steady state drops the redundant previous-month work.
--
-- Note: an empty table produces no pg_statistic rows even after a successful
-- ANALYZE (there are no rows to sample), so empty partitions are still visited
-- every hour. That is intentional and costs nothing — measured work_units for
-- the empty relations is exactly 0.
--
-- Expected effect: ~25.7% off one pass, i.e. ~56 min/day -> ~41 min/day before
-- the advisory mutex in bg/partition_manager.go is deployed, and ~20 min/day
-- after. The mutex (halving) is the larger and already-coded lever; this one
-- is additional and independent.
--
-- Status: active
-- Idempotent: YES (CREATE OR REPLACE FUNCTION)

BEGIN;

-- ── analyze_llm_gateway_table_stats: skip stale-proof re-analysis ───────────
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

    -- heap 热表：每次都分析（行一直在变，autovacuum 也覆盖这里）
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
    --   m = 0（当月）：无条件分析，正在写入
    --   m >= 1（往月）：只在「从未被分析过」时补一次。跨月后不再写入，
    --                  统计量不会失效，重复分析是纯开销（迁移 838 / §10.92）。
    --                  空表即使 ANALYZE 成功也没有 pg_statistic 行，会被反复
    --                  访问；实测其 work_units 为 0，代价可忽略。
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
              AND (m = 0 OR NOT EXISTS (SELECT 1 FROM pg_statistic s WHERE s.starelid = c.oid))
        LOOP
            EXECUTE format('ANALYZE %I', r.relname);
            analyzed := analyzed + 1;
        END LOOP;
    END LOOP;

    RETURN analyzed;
END;
$function$;

COMMENT ON FUNCTION analyze_llm_gateway_table_stats(integer) IS
'ANALYZE hot heap tables unconditionally, plus monthly partitions for the last N
months. Partitions older than the current month are only analyzed when they have
never been analyzed (no pg_statistic rows), because a rolled-over partition stops
receiving writes and its statistics cannot go stale. Measured 2026-10-07: the
previous-month group was 25.7% of the pass''s per-column statistics work.
Migration 838 / runbook §10.92.';

-- Mirror of the installed function body.
-- sql/objects/functions/analyze_llm_gateway_table_stats_integer.sql
-- deploy/sql/schemas/baseline/01-schema.sql
-- sql/schema/01-schema.sql
-- installer/cmd/llm-gw-installer/embeddata/01-schema.sql

COMMIT;