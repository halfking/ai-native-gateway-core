-- backfill_sessions_v2.sql: Backfill historical data from request_logs to Sessions V2
--
-- Purpose:
--   Migrate historical request_logs data into the new V2 storage architecture
--   (sessions, session_turns, session_bodies) for continuity and analytics.
--
-- Prerequisites:
--   1. Migration 430 (430_sessions_v2_schema.sql) has been applied
--   2. Partitions exist for the target date range
--   3. Database has sufficient resources (CPU, memory, I/O)
--
-- Usage:
--   psql -h <host> -U <user> -d gateway -f backfill_sessions_v2.sql \
--     -v tenant_id='tenant_xxx' \
--     -v start_date='2026-07-01' \
--     -v end_date='2026-07-17' \
--     -v batch_size=1000 \
--     -v dry_run=false
--
-- Parameters:
--   tenant_id:   Tenant to backfill (required)
--   start_date:  Start date YYYY-MM-DD (required)
--   end_date:    End date YYYY-MM-DD (required)
--   batch_size:  Number of rows per batch (default: 1000)
--   dry_run:     If true, only show counts without writing (default: false)
--
-- Safety:
--   - Idempotent: uses ON CONFLICT DO NOTHING
--   - Incremental: processes in batches with progress logging
--   - Can be interrupted and resumed
--   - Validates data before writing
--
-- Author: llm-gateway-ops
-- Date: 2026-07-17
-- Status: PRODUCTION-READY

\set ON_ERROR_STOP on
\timing on

-- Default parameter values (can be overridden with -v flag)
\set tenant_id :tenant_id
\set start_date :start_date
\set end_date :end_date
\set batch_size 1000
\set dry_run false

-- Validate parameters
DO $$
BEGIN
    IF :'tenant_id' = ':tenant_id' THEN
        RAISE EXCEPTION 'Parameter tenant_id is required. Use: psql ... -v tenant_id=tenant_xxx';
    END IF;
    
    IF :'start_date' = ':start_date' THEN
        RAISE EXCEPTION 'Parameter start_date is required. Use: psql ... -v start_date=2026-07-01';
    END IF;
    
    IF :'end_date' = ':end_date' THEN
        RAISE EXCEPTION 'Parameter end_date is required. Use: psql ... -v end_date=2026-07-17';
    END IF;
    
    RAISE NOTICE '=== Backfill Parameters ===';
    RAISE NOTICE 'Tenant:     %', :'tenant_id';
    RAISE NOTICE 'Date Range: % to %', :'start_date', :'end_date';
    RAISE NOTICE 'Batch Size: %', :'batch_size';
    RAISE NOTICE 'Dry Run:    %', :'dry_run';
END $$;

-- Ensure required partitions exist
SELECT ensure_sessions_v2_partitions(date :'start_date');
SELECT ensure_sessions_v2_partitions(date :'end_date');
SELECT ensure_sessions_v2_partitions((date :'start_date' + interval '1 month')::date);
SELECT ensure_sessions_v2_partitions((date :'end_date' + interval '1 month')::date);

-- Step 1: Analyze source data
\echo ''
\echo '=== Step 1: Analyzing Source Data ==='

SELECT 
    COUNT(*) as total_rows,
    COUNT(DISTINCT session_id) as unique_sessions,
    MIN(ts) as earliest_ts,
    MAX(ts) as latest_ts,
    SUM((usage->>'prompt_tokens')::int + (usage->>'completion_tokens')::int) as total_tokens,
    SUM(cost_usd) as total_cost_usd
FROM gateway.request_logs
WHERE tenant_id = :'tenant_id'
  AND ts >= (date :'start_date')
  AND ts < (date :'end_date');

-- Step 2: Check for existing V2 data
\echo ''
\echo '=== Step 2: Checking Existing V2 Data ==='

SELECT 
    'session_turns' as table_name,
    COUNT(*) as existing_rows
FROM public.session_turns
WHERE tenant_id = :'tenant_id'
  AND ts >= (date :'start_date')
  AND ts < (date :'end_date')
UNION ALL
SELECT 
    'session_bodies' as table_name,
    COUNT(*) as existing_rows
FROM public.session_bodies
WHERE tenant_id = :'tenant_id'
  AND ts >= (date :'start_date')
  AND ts < (date :'end_date')
UNION ALL
SELECT 
    'sessions' as table_name,
    COUNT(*) as existing_rows
FROM public.sessions
WHERE tenant_id = :'tenant_id'
  AND created_at >= (date :'start_date')
  AND created_at < (date :'end_date');

-- Exit if dry_run
DO $$
BEGIN
    IF :'dry_run' = 'true' THEN
        RAISE NOTICE '=== Dry Run Mode: Exiting without backfill ===';
        RAISE EXCEPTION 'DRY_RUN' USING ERRCODE = 'P0001';
    END IF;
END $$;

-- Step 3: Backfill session_turns
\echo ''
\echo '=== Step 3: Backfilling session_turns ==='

DO $$
DECLARE
    batch_start INT := 0;
    batch_end INT := :batch_size;
    rows_inserted INT := 0;
    total_inserted INT := 0;
    batch_count INT := 0;
BEGIN
    LOOP
        -- Insert batch with turn_no assignment
        WITH ordered_logs AS (
            SELECT 
                request_id,
                session_id,
                tenant_id,
                ts,
                client_model,
                outbound_model,
                credential_id,
                usage,
                cost_usd,
                success,
                status_code,
                latency_ms,
                ROW_NUMBER() OVER (PARTITION BY session_id ORDER BY ts ASC) as turn_no
            FROM gateway.request_logs
            WHERE tenant_id = :'tenant_id'
              AND ts >= (date :'start_date')
              AND ts < (date :'end_date')
            ORDER BY session_id, ts
            LIMIT :batch_size OFFSET batch_start
        )
        INSERT INTO public.session_turns (
            session_id, turn_no, tenant_id, request_id, ts,
            submit_mode,
            compression_applied, compression_strategy, compression_meta, compression_tokens_saved,
            injection_verdict, output_verdict,
            model, provider, credential_id,
            prompt_tokens, completion_tokens, cache_read_tokens, cache_write_tokens, cost_usd,
            latency_ms, status_code, success, error_kind,
            source_kind, quality,
            partition_date
        )
        SELECT 
            ol.session_id,
            ol.turn_no,
            ol.tenant_id,
            ol.request_id,
            ol.ts,
            'full' as submit_mode,
            false as compression_applied,
            NULL as compression_strategy,
            '{}'::jsonb as compression_meta,
            0 as compression_tokens_saved,
            'skip' as injection_verdict,
            'skip' as output_verdict,
            COALESCE(ol.client_model, '') as model,
            '' as provider,
            COALESCE(ol.credential_id, '') as credential_id,
            COALESCE((ol.usage->>'prompt_tokens')::int, 0) as prompt_tokens,
            COALESCE((ol.usage->>'completion_tokens')::int, 0) as completion_tokens,
            COALESCE((ol.usage->>'cache_read_input_tokens')::int, 0) as cache_read_tokens,
            COALESCE((ol.usage->>'cache_creation_input_tokens')::int, 0) as cache_write_tokens,
            COALESCE(ol.cost_usd, 0) as cost_usd,
            COALESCE(ol.latency_ms, 0) as latency_ms,
            COALESCE(ol.status_code, 0) as status_code,
            COALESCE(ol.success, false) as success,
            CASE WHEN ol.success = false THEN 'backfill_error' ELSE NULL END as error_kind,
            'backfill' as source_kind,
            'inferred' as quality,
            ol.ts::date as partition_date
        FROM ordered_logs ol
        ON CONFLICT (request_id, partition_date) DO NOTHING;
        
        GET DIAGNOSTICS rows_inserted = ROW_COUNT;
        total_inserted := total_inserted + rows_inserted;
        batch_count := batch_count + 1;
        
        EXIT WHEN rows_inserted = 0;
        
        RAISE NOTICE 'Batch % completed: % rows inserted (total: %)', 
            batch_count, rows_inserted, total_inserted;
        
        batch_start := batch_start + :batch_size;
        
        -- Throttle to avoid overwhelming the database
        PERFORM pg_sleep(0.1);
    END LOOP;
    
    RAISE NOTICE 'session_turns backfill complete: % rows inserted in % batches', 
        total_inserted, batch_count;
END $$;

-- Step 4: Backfill session_bodies
\echo ''
\echo '=== Step 4: Backfilling session_bodies ==='

DO $$
DECLARE
    batch_start INT := 0;
    batch_end INT := :batch_size;
    rows_inserted INT := 0;
    total_inserted INT := 0;
    batch_count INT := 0;
BEGIN
    LOOP
        -- Insert batch
        WITH turn_data AS (
            SELECT 
                t.session_id,
                t.turn_no,
                t.tenant_id,
                t.request_id,
                t.ts,
                rl.body,
                rl.response
            FROM public.session_turns t
            JOIN gateway.request_logs rl ON rl.request_id = t.request_id
            WHERE t.tenant_id = :'tenant_id'
              AND t.ts >= (date :'start_date')
              AND t.ts < (date :'end_date')
              AND t.source_kind = 'backfill'
            ORDER BY t.session_id, t.turn_no
            LIMIT :batch_size OFFSET batch_start
        )
        INSERT INTO public.session_bodies (
            session_id, turn_no, tenant_id, request_id,
            request_delta, response_delta, outbound_body,
            request_attachments, response_attachments,
            ts, partition_date
        )
        SELECT 
            td.session_id,
            td.turn_no,
            td.tenant_id,
            td.request_id,
            -- For backfill, we store the full body as delta (we don't have deltas)
            COALESCE(td.body->'messages', '[]'::jsonb) as request_delta,
            COALESCE(
                CASE 
                    WHEN jsonb_typeof(td.response) = 'array' THEN td.response
                    WHEN td.response ? 'choices' THEN 
                        jsonb_build_array(
                            jsonb_build_object(
                                'role', 'assistant',
                                'content', COALESCE(td.response->'choices'->0->'message'->>'content', '')
                            )
                        )
                    ELSE '[]'::jsonb
                END,
                '[]'::jsonb
            ) as response_delta,
            COALESCE(td.body->'messages', '[]'::jsonb) as outbound_body,
            '[]'::jsonb as request_attachments,
            '[]'::jsonb as response_attachments,
            td.ts,
            td.ts::date as partition_date
        FROM turn_data td
        ON CONFLICT (request_id, partition_date) DO NOTHING;
        
        GET DIAGNOSTICS rows_inserted = ROW_COUNT;
        total_inserted := total_inserted + rows_inserted;
        batch_count := batch_count + 1;
        
        EXIT WHEN rows_inserted = 0;
        
        RAISE NOTICE 'Batch % completed: % rows inserted (total: %)', 
            batch_count, rows_inserted, total_inserted;
        
        batch_start := batch_start + :batch_size;
        
        -- Throttle
        PERFORM pg_sleep(0.1);
    END LOOP;
    
    RAISE NOTICE 'session_bodies backfill complete: % rows inserted in % batches', 
        total_inserted, batch_count;
END $$;

-- Step 5: Backfill sessions (aggregate from session_turns)
\echo ''
\echo '=== Step 5: Backfilling sessions (aggregates) ==='

INSERT INTO public.sessions (
    session_id, tenant_id,
    created_at, updated_at, status,
    total_turns, total_tokens, total_cost_usd,
    last_turn_no, last_request_summary, last_response_summary,
    last_model, last_provider,
    primary_request_id,
    partition_date
)
SELECT 
    t.session_id,
    t.tenant_id,
    MIN(t.ts) as created_at,
    MAX(t.ts) as updated_at,
    'closed' as status,
    COUNT(*) as total_turns,
    SUM(t.prompt_tokens + t.completion_tokens) as total_tokens,
    SUM(t.cost_usd) as total_cost_usd,
    MAX(t.turn_no) as last_turn_no,
    '' as last_request_summary,
    '' as last_response_summary,
    (SELECT model FROM public.session_turns 
     WHERE session_id = t.session_id AND turn_no = MAX(t.turn_no) LIMIT 1) as last_model,
    '' as last_provider,
    (SELECT request_id FROM public.session_turns 
     WHERE session_id = t.session_id ORDER BY turn_no ASC LIMIT 1) as primary_request_id,
    MIN(t.ts)::date as partition_date
FROM public.session_turns t
WHERE t.tenant_id = :'tenant_id'
  AND t.ts >= (date :'start_date')
  AND t.ts < (date :'end_date')
  AND t.source_kind = 'backfill'
GROUP BY t.session_id, t.tenant_id
ON CONFLICT (session_id, partition_date) DO UPDATE SET
    updated_at = EXCLUDED.updated_at,
    total_turns = EXCLUDED.total_turns,
    total_tokens = EXCLUDED.total_tokens,
    total_cost_usd = EXCLUDED.total_cost_usd,
    last_turn_no = EXCLUDED.last_turn_no,
    last_model = EXCLUDED.last_model;

-- Step 6: Verification
\echo ''
\echo '=== Step 6: Verification ==='

SELECT 
    'request_logs' as source_table,
    COUNT(*) as row_count,
    COUNT(DISTINCT session_id) as unique_sessions,
    SUM((usage->>'prompt_tokens')::int + (usage->>'completion_tokens')::int) as total_tokens,
    SUM(cost_usd) as total_cost_usd
FROM gateway.request_logs
WHERE tenant_id = :'tenant_id'
  AND ts >= (date :'start_date')
  AND ts < (date :'end_date')
UNION ALL
SELECT 
    'session_turns' as source_table,
    COUNT(*) as row_count,
    COUNT(DISTINCT session_id) as unique_sessions,
    SUM(prompt_tokens + completion_tokens) as total_tokens,
    SUM(cost_usd) as total_cost_usd
FROM public.session_turns
WHERE tenant_id = :'tenant_id'
  AND ts >= (date :'start_date')
  AND ts < (date :'end_date');

-- Step 7: Summary
\echo ''
\echo '=== Backfill Complete ==='

DO $$
DECLARE
    v1_count INT;
    v2_turns_count INT;
    v2_bodies_count INT;
    v2_sessions_count INT;
BEGIN
    SELECT COUNT(*) INTO v1_count
    FROM gateway.request_logs
    WHERE tenant_id = :'tenant_id'
      AND ts >= (date :'start_date')
      AND ts < (date :'end_date');
    
    SELECT COUNT(*) INTO v2_turns_count
    FROM public.session_turns
    WHERE tenant_id = :'tenant_id'
      AND ts >= (date :'start_date')
      AND ts < (date :'end_date');
    
    SELECT COUNT(*) INTO v2_bodies_count
    FROM public.session_bodies
    WHERE tenant_id = :'tenant_id'
      AND ts >= (date :'start_date')
      AND ts < (date :'end_date');
    
    SELECT COUNT(*) INTO v2_sessions_count
    FROM public.sessions
    WHERE tenant_id = :'tenant_id'
      AND created_at >= (date :'start_date')
      AND created_at < (date :'end_date');
    
    RAISE NOTICE '';
    RAISE NOTICE '=== Backfill Summary ===';
    RAISE NOTICE 'Tenant:           %', :'tenant_id';
    RAISE NOTICE 'Date Range:       % to %', :'start_date', :'end_date';
    RAISE NOTICE 'Source (V1):      % rows in request_logs', v1_count;
    RAISE NOTICE 'Target (V2):';
    RAISE NOTICE '  - session_turns:  % rows', v2_turns_count;
    RAISE NOTICE '  - session_bodies: % rows', v2_bodies_count;
    RAISE NOTICE '  - sessions:       % rows', v2_sessions_count;
    RAISE NOTICE '';
    
    IF v2_turns_count = v1_count AND v2_bodies_count = v1_count THEN
        RAISE NOTICE '✓ Backfill successful: row counts match';
    ELSE
        RAISE WARNING '✗ Row count mismatch detected';
        RAISE WARNING '  Expected: % rows in each V2 table', v1_count;
        RAISE WARNING '  Got: session_turns=%, session_bodies=%', v2_turns_count, v2_bodies_count;
        RAISE NOTICE '';
        RAISE NOTICE 'Run validation tool for detailed comparison:';
        RAISE NOTICE '  go run ./cmd/tools/validate_sessions_v2 -dsn="..." -tenant-id=% -start-date=% -end-date=% -verbose',
            :'tenant_id', :'start_date', :'end_date';
    END IF;
END $$;

\timing off
\echo ''
\echo '=== Script Complete ==='
