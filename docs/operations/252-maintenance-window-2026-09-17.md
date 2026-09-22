# 252 Maintenance Window Runbook (2026-09-17 Post-R38)

## Context

R38 audit identified two issues with 252 database state:
1. **717 false registration**: schema_migrations has 717 entry (09:29) but effects not applied — agent_name/agent_type still `text` instead of `varchar`
2. **719 pending**: Standard migration ready for next deployment
3. **713 pending**: session_turns 220K rows across 5 partitions, low-traffic window recommended

## Pre-flight Checks

```bash
# 1. Verify 252 is podman (not docker)
ssh <252-host>
podman ps  # or sudo podman ps

# 2. Check disk space (need 78G+ available)
df -h

# 3. Check memory (need 6G+ available)
free -h

# 4. Access database via tunnel or container-internal psql
podman exec -it <container-name> psql -U llm_gateway -d llm_gateway
```

## Task 1: Fix 717 False Registration

**Timing**: Low-traffic window, DDL takes seconds (hot table has 8373 rows per R38)

**Execution**: Run the **current hardened version** of 717 file body directly via psql:

```sql
-- Copy the entire DO block from installer/cmd/llm-gw-installer/embeddata/startup/717_request_logs_hot_column_alignment.sql
-- Lines 39-167 (the DO $$ ... END $$ block)
-- Execute in a single psql session
```

**Verification**:

```sql
-- Verify all 10 columns align (zero drift expected)
SELECT column_name, data_type, character_maximum_length
FROM information_schema.columns
WHERE table_schema = 'public' 
  AND table_name = 'request_logs_hot'
  AND column_name IN (
    'agent_name',        -- expect: character varying(255)
    'agent_type',        -- expect: character varying(50)
    'api_key_fingerprint', -- expect: character varying(16)
    'task_id',           -- expect: character varying(255)
    'customer_id',       -- expect: bigint
    'content_safety_score', -- expect: jsonb
    'dlp_violations',    -- expect: jsonb
    'protocol_conversion', -- expect: boolean
    'ir_extensions',     -- expect: jsonb
    'sanitizer_mutations' -- expect: jsonb
  )
ORDER BY column_name;

-- Count rows affected (should be 8373 per R38 audit)
SELECT COUNT(*) FROM request_logs_hot;
```

**Post-execution**: No schema_migrations surgery needed (row already exists, we're just applying the missing effects)

## Task 2: Execute 719 (Redundant Index Cleanup)

**Timing**: Next deployment via revision-sequence channel (CONCURRENTLY safe, non-partitioned)

**Contents**: 
- Drop 9 constraint-shadowed indexes
- Drop idx_request_logs_parent_request_id partition tree (5 attached leaves)
- Drop 3 ASC indexes on tool_usage_stats_hot (keep DESC variants)

**Verification Post-Deployment**:

```sql
-- Verify 13 drops completed
-- (Check against specific index names in 719 migration file)

-- Verify parent_ts index tree intact (5 leaves)
SELECT inhrelid::regclass AS leaf_index
FROM pg_inherits
WHERE inhparent = 'idx_request_logs_parent_ts'::regclass;
-- Expect 5 rows

-- Verify parent_request_id tree cleared
SELECT inhrelid::regclass AS leaf_index
FROM pg_inherits
WHERE inhparent = 'idx_request_logs_parent_request_id'::regclass;
-- Expect 0 rows (or regclass lookup fails)
```

## Task 3: Execute 713 (session_turns Column Type Change)

**Timing**: Low-traffic window during deployment (220K rows, ACCESS EXCLUSIVE seconds to tens of seconds)

**Pre-check**:

```sql
-- Verify current row count and partition structure
SELECT 
  schemaname, tablename, 
  pg_size_pretty(pg_total_relation_size(schemaname||'.'||tablename)) AS size
FROM pg_tables 
WHERE tablename LIKE 'session_turns%' 
  AND schemaname = 'public'
ORDER BY tablename;

-- Should show ~220K rows across 5 partitions per R38
SELECT COUNT(*) FROM session_turns;
```

**Note**: 713 migration details not yet reviewed in this runbook. Refer to migration file for exact ALTER statements.

## Recovery Plan

If 717 execution fails mid-DO block:
- The DO block is atomic (single psql autocommit statement)
- Partial failure = full rollback
- Re-run the entire DO block

If 719/713 fail during deployment:
- Standard rollback via revision-sequence mechanism
- No data loss (719 is DROP only, 713 is type alignment)

## Post-Window Verification

```bash
# 1. Gateway health check
curl http://252-endpoint/healthz

# 2. Check for 42501 or empty-result errors in logs
# (These would indicate RLS or type mismatches)

# 3. Admin UI spot-check: credential list, provider list

# 4. Worker lag check
# (supplier_error_stats_aggregator, session reaper, etc.)
```

## Notes

- 252 uses podman, not docker
- Current disk: 78G available, memory: 6G available (per R38 audit)
- 717 timing discrepancy: R38 audit found 717@09:29 in schema_migrations but effects missing, likely ran R36 original version that hit 42883 and aborted mid-execution
- 718 verified fully applied (3 indexes added, 3 dropped)
- No other false registrations found in R38 audit
