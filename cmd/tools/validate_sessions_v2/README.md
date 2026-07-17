# Sessions V2 Tools

This directory contains operational tools for Sessions V2 storage architecture.

## Tools

### 1. validate_sessions_v2

Data validation tool that compares V1 (request_logs) and V2 (sessions tables) to verify dual-write integrity.

**Purpose:**
- Validate row count parity between request_logs and session_turns
- Verify token sum consistency
- Verify cost sum consistency
- Check session snapshot accuracy (sessions table vs session_turns aggregates)
- Validate bodies integrity (session_bodies deltas)

**Usage:**
```bash
go run ./cmd/tools/validate_sessions_v2 \
  -dsn "postgres://user:pass@host:5432/gateway" \
  -tenant-id "tenant_xxx" \
  -start-date "2026-07-01" \
  -end-date "2026-07-17" \
  [-session-id "gw_xxxxx"] \
  [-verbose]
```

**Parameters:**
- `-dsn`: PostgreSQL connection string (required)
- `-tenant-id`: Tenant ID to validate (required)
- `-start-date`: Start date YYYY-MM-DD (optional)
- `-end-date`: End date YYYY-MM-DD (optional)
- `-session-id`: Specific session to validate (optional)
- `-verbose`: Show detailed discrepancies (optional)

**Output:**
- Summary report with pass/fail for each check
- Exit code 0 if all checks pass, 1 otherwise

**Example:**
```bash
# Validate all data for a tenant in a date range
go run ./cmd/tools/validate_sessions_v2 \
  -dsn "postgres://localhost:5432/gateway?sslmode=disable" \
  -tenant-id "tenant_123" \
  -start-date "2026-07-01" \
  -end-date "2026-07-17" \
  -verbose

# Validate a specific session
go run ./cmd/tools/validate_sessions_v2 \
  -dsn "postgres://localhost:5432/gateway?sslmode=disable" \
  -tenant-id "tenant_123" \
  -session-id "gw_abc123" \
  -verbose
```

## Related Files

### backfill_sessions_v2.sql

SQL script for backfilling historical data from request_logs to Sessions V2 tables.

**Location:** `sql/scripts/backfill_sessions_v2.sql`

**Usage:**
```bash
psql -h <host> -U <user> -d gateway -f sql/scripts/backfill_sessions_v2.sql \
  -v tenant_id='tenant_xxx' \
  -v start_date='2026-07-01' \
  -v end_date='2026-07-17' \
  -v batch_size=1000 \
  -v dry_run=false
```

**Features:**
- Idempotent (uses ON CONFLICT DO NOTHING)
- Incremental batch processing
- Progress logging
- Can be interrupted and resumed
- Dry-run mode for testing

**Parameters:**
- `tenant_id`: Tenant to backfill (required)
- `start_date`: Start date YYYY-MM-DD (required)
- `end_date`: End date YYYY-MM-DD (required)
- `batch_size`: Rows per batch (default: 1000)
- `dry_run`: If true, only show counts (default: false)

## Testing

Run unit tests:
```bash
cd cmd/tools/validate_sessions_v2
go test -v
```

## Architecture

Both tools work with the Sessions V2 architecture:

**Tables:**
- `gateway.sessions` - Session snapshots (one per session)
- `gateway.session_turns` - Turn metadata (no bodies)
- `gateway.session_bodies` - Incremental message deltas (columnar)
- `gateway.session_turn_logs` - Processing stage logs (24h TTL)

**Migration:** `sql/migrations/startup/430_sessions_v2_schema.sql`

## Workflow

1. **Deploy V2 schema** via migration 430
2. **Enable dual-write** via feature flag (writes to both V1 and V2)
3. **Backfill historical data** using `backfill_sessions_v2.sql`
4. **Validate data** using `validate_sessions_v2`
5. **Monitor** for discrepancies
6. **Cutover** to V2-only when validated

## Monitoring

Use validation tool in cron job for ongoing monitoring:
```bash
# Daily validation check
0 2 * * * go run /path/to/validate_sessions_v2 \
  -dsn "$DB_DSN" \
  -tenant-id "tenant_prod" \
  -start-date "$(date -d '1 day ago' +%Y-%m-%d)" \
  -end-date "$(date +%Y-%m-%d)" \
  || alert-on-failure
```

## Troubleshooting

### Validation failures

If validation reports discrepancies:

1. Check if dual-write is enabled and working
2. Review application logs for write errors
3. Check database constraints and RLS policies
4. Run validation with `-verbose` for details
5. Use `session-id` flag to inspect specific sessions

### Backfill issues

If backfill fails:

1. Check partition existence (script auto-creates)
2. Verify database has sufficient resources
3. Reduce `batch_size` if timeouts occur
4. Use `dry_run=true` to preview without writing
5. Backfill is idempotent - safe to retry

## See Also

- [SESSION_V2_IMPLEMENTATION_SUMMARY.md](../../../docs/SESSION_V2_IMPLEMENTATION_SUMMARY.md)
- [SESSION_V2_DATA_MAPPING.md](../../../docs/SESSION_V2_DATA_MAPPING.md)
- [domains/session/v2/README.md](../../../domains/session/v2/README.md)
