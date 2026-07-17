# Sessions V2 Validation & Repair Tool

Data validation and repair tool for Sessions V2 storage architecture. Compares V1 (request_logs) and V2 (sessions tables) to verify dual-write integrity, and rebuilds V2 data when discrepancies are found.

## Features

- **7 Validation Checks**: Request ID parity, timestamps, tokens, costs, metadata, snapshot accuracy, bodies integrity
- **Single Session Mode**: Detailed validation for one session
- **Batch Mode**: Validate multiple sessions with date range and settle window
- **Repair Mode**: Transactional rebuild of V2 from V1 (single-session only)
- **Multiple Formats**: JSON (machine-readable) and text (human-readable)
- **Exit Codes**: 0 for success/warnings, 1 for errors

## Quick Start

### Validation

```bash
# Single session
go run ./cmd/tools/validate_sessions_v2 \
  -dsn "postgres://..." \
  -tenant-id "tenant_xxx" \
  -session-id "gw_abc123" \
  -format text

# Batch validation
go run ./cmd/tools/validate_sessions_v2 \
  -dsn "postgres://..." \
  -tenant-id "tenant_xxx" \
  -start-date "2026-07-01" \
  -end-date "2026-07-17" \
  -format json
```

### Repair

```bash
# Dry-run (preview only)
go run ./cmd/tools/validate_sessions_v2 \
  -dsn "postgres://..." \
  -tenant-id "tenant_xxx" \
  -session-id "gw_abc123" \
  -repair \
  -format text

# Execute repair
go run ./cmd/tools/validate_sessions_v2 \
  -dsn "postgres://..." \
  -tenant-id "tenant_xxx" \
  -session-id "gw_abc123" \
  -repair \
  -apply
```

## Command-Line Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `-dsn` | string | - | PostgreSQL connection string (required) |
| `-tenant-id` | string | - | Tenant ID to validate (required) |
| `-session-id` | string | - | Specific session ID (single-session mode) |
| `-start-date` | string | - | Start date YYYY-MM-DD (batch mode) |
| `-end-date` | string | - | End date YYYY-MM-DD (batch mode) |
| `-max-sessions` | int | 1000 | Max sessions in batch mode |
| `-settle-window` | duration | 10m | Exclude recently updated sessions (batch mode) |
| `-format` | string | json | Output format: `json` or `text` |
| `-verbose` | bool | false | Show per-session details during batch |
| `-repair` | bool | false | Enable repair mode (single-session only) |
| `-apply` | bool | false | Apply changes (without this, dry-run only) |

## Repair Mode

### How It Works

1. **Plan**: Analyzes V1 source and V2 target, generates repair plan
2. **Execute** (with `--apply`):
   - Acquire advisory lock on session
   - DELETE all V2 data (4 tables: session_turn_logs, session_bodies, session_turns, sessions)
   - REBUILD from V1 (insert session_turns, session_bodies, sessions)
   - Commit transaction
3. **Verify**: Re-run validation on rebuilt data

### Safety Features

- **Dry-run by default**: Preview changes without `--apply`
- **Advisory lock**: Prevents concurrent modifications
- **Transactional**: All-or-nothing, automatic rollback on error
- **Post-verification**: Ensures rebuilt data passes validation
- **Single-session only**: Batch repair requires explicit confirmation for each session

### Example Output (Dry-run)

```
================================================================================
[DRY RUN] Repair plan for session gw_abc123
================================================================================
Tenant:     tenant_xxx
Session:    gw_abc123
V1 Source:  10 rows

Will DELETE:
  - sessions:              1 rows
  - session_turns:         9 rows
  - session_bodies:        9 rows
  - session_turn_logs:     0 rows

Will REBUILD:
  - session_turns:         10 rows
  - session_bodies:        10 rows
  - sessions:              1 rows

Run with --apply to execute this repair.
================================================================================
```

### Example Output (Applied)

```
================================================================================
REPAIR COMPLETED
================================================================================
Session:    gw_abc123
Tenant:     tenant_xxx

Deleted:
  ✓ sessions:              1 rows
  ✓ session_turns:         9 rows
  ✓ session_bodies:        9 rows
  ✓ session_turn_logs:     0 rows

Rebuilt:
  ✓ session_turns:         10 rows
  ✓ session_bodies:        10 rows
  ✓ sessions:              1 rows

Verification:
  ✓ Re-validation passed (status: ok)
================================================================================
```

## Validation Checks

### 1. Request ID Parity (ERROR)
Verifies V1 and V2 have the same set of request IDs.

### 2. Timestamp Consistency (ERROR)
Ensures timestamps match between V1 and V2 (1s tolerance).

### 3. Token Sum (WARNING)
Validates token counts with 1% or 10 token tolerance.

### 4. Cost Sum (WARNING)
Validates costs with $0.01 or 1% tolerance.

### 5. Metadata Consistency (ERROR)
Checks model, provider, credential, and verdict fields.

### 6. Session Snapshot Accuracy (ERROR/WARNING)
Verifies `sessions` table aggregates match `session_turns`.

### 7. Bodies Integrity (WARNING for compressed)
Validates JSON and delta reconstruction. Strict for `full` mode, warnings for compressed modes.

## Exit Codes

- **0**: All checks passed or warnings only
- **1**: At least one ERROR-level issue found (or repair verification failed)
- **2**: Invalid command-line arguments

## Architecture

```
cmd/tools/validate_sessions_v2/
├── main.go              # CLI entry, orchestration, repair workflow
├── loader.go            # Load V1/V2 data
├── validator.go         # 7 validation checks
├── reconstruct.go       # Delta chain reconstruction
├── repair.go            # Transactional repair engine
├── report.go            # JSON/text formatting
├── helpers.go           # Utility functions
└── *_test.go           # Unit tests (34 tests)
```

## Troubleshooting

### Repair Fails with "no V1 turns found"
V1 data (request_logs) is required as source of truth. Cannot repair sessions that don't exist in V1.

### Repair Verification Fails
If post-repair validation fails, the transaction was already committed. Investigate V1 data quality or re-run repair.

### Advisory Lock Timeout
Another process is modifying this session. Wait and retry, or check for stuck locks:
```sql
SELECT * FROM pg_locks WHERE locktype = 'advisory';
```

## See Also

- [Design Spec](../../../docs/superpowers/specs/2026-07-18-sessions-v2-validation-repair-design.md)
- [Data Mapping](../../../docs/SESSION_V2_DATA_MAPPING.md)
- [Backfill Script](../../../sql/scripts/backfill_sessions_v2.sql)
