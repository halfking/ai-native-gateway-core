-- ============================================================
-- Phase 22 — Columnar Storage Conversion (matches 184)
-- Generated: 2026-06-26
--
-- Convert append-only / time-series tables to columnar storage
-- to match 184 production. After conversion, these tables will
-- use Citus columnar (15-40x compression on append-only data).
--
-- Note: Requires citus_columnar extension (already loaded in 01-schema.sql).
-- Run AFTER all tables are created (run after Phase 21 sync scripts).
-- ============================================================

\connect llm_gateway

-- 9 columnar tables (matches 184)
ALTER TABLE IF EXISTS public.candidate_failure_logs SET ACCESS METHOD columnar;
ALTER TABLE IF EXISTS public.credential_probe_model_log SET ACCESS METHOD columnar;
ALTER TABLE IF EXISTS public.model_offer_events SET ACCESS METHOD columnar;
ALTER TABLE IF EXISTS public.model_probe_runs SET ACCESS METHOD columnar;
ALTER TABLE IF EXISTS public.price_change_events SET ACCESS METHOD columnar;
ALTER TABLE IF EXISTS public.provider_events SET ACCESS METHOD columnar;
ALTER TABLE IF EXISTS public.tool_call_events SET ACCESS METHOD columnar;
ALTER TABLE IF EXISTS public.usage_ledger SET ACCESS METHOD columnar;
ALTER TABLE IF EXISTS public.test_columnar_new SET ACCESS METHOD columnar;

-- Other append-only time-series tables that benefit from columnar
-- (per partition — converting partitioned parents is complex,
-- convert the individual partitions instead)

-- credit_ledger partitions (already created by Phase 21 sync)
DO $$
DECLARE
    part_name text;
BEGIN
    FOR part_name IN
        SELECT c.relname
        FROM pg_class c
        JOIN pg_inherits i ON c.oid = i.inhrelid
        JOIN pg_class p ON i.inhparent = p.oid
        WHERE p.relname = 'credit_ledger'
    LOOP
        EXECUTE format('ALTER TABLE public.%I SET ACCESS METHOD columnar', part_name);
    END LOOP;
END
$$;

-- tool_usage_stats partitions
DO $$
DECLARE
    part_name text;
BEGIN
    FOR part_name IN
        SELECT c.relname
        FROM pg_class c
        JOIN pg_inherits i ON c.oid = i.inhrelid
        JOIN pg_class p ON i.inhparent = p.oid
        WHERE p.relname = 'tool_usage_stats'
    LOOP
        EXECUTE format('ALTER TABLE public.%I SET ACCESS METHOD columnar', part_name);
    END LOOP;
END
$$;

-- usage_ledger partitions
DO $$
DECLARE
    part_name text;
BEGIN
    FOR part_name IN
        SELECT c.relname
        FROM pg_class c
        JOIN pg_inherits i ON c.oid = i.inhrelid
        JOIN pg_class p ON i.inhparent = p.oid
        WHERE p.relname = 'usage_ledger'
    LOOP
        EXECUTE format('ALTER TABLE public.%I SET ACCESS METHOD columnar', part_name);
    END LOOP;
END
$$;

-- Verify conversion
SELECT
    c.relname AS table_name,
    CASE WHEN c.relam = (SELECT oid FROM pg_am WHERE amname = 'columnar')
         THEN 'columnar' ELSE 'heap' END AS storage
FROM pg_class c
JOIN pg_namespace n ON c.relnamespace = n.oid
WHERE n.nspname = 'public'
  AND c.relam = (SELECT oid FROM pg_am WHERE amname = 'columnar')
ORDER BY c.relname;
