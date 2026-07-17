# Sessions V2 Validation Tool

Data validation tool for Sessions V2 storage architecture. Compares V1 (request_logs) and V2 (sessions tables) to verify dual-write integrity.

## Features

- **7 Validation Checks**: Request ID parity, timestamps, tokens, costs, metadata, snapshot accuracy, bodies integrity
- **Single Session Mode**: Detailed validation for one session
- **Batch Mode**: Validate multiple sessions with date range and settle window
- **Multiple Formats**: JSON (machine-readable) and text (human-readable)
- **Exit Codes**: 0 for success/warnings, 1 for errors

## Quick Start

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

## Output Examples

### JSON Format

```json
{
  "session_id": "gw_abc123",
  "status": "error",
  "summary": {
    "v1_turns": 10,
    "v2_turns": 9
  },
  "differences": [
    {
      "name": "Request ID Parity",
      "severity": "error",
      "description": "Missing in V2: 1 request(s)"
    }
  ]
}
```

### Text Format

```
================================================================================
SESSION VALIDATION REPORT
================================================================================
Session:    gw_abc123
Status:     ERROR

Summary:
  V1: 10 turns, 15000 tokens, $0.150000
  V2: 9 turns, 15000 tokens, $0.150000

Differences (1):
  ✗ [1] Request ID Parity (ERROR)
      Missing in V2: 1 request(s)
================================================================================
```

## Exit Codes

- **0**: All checks passed or warnings only
- **1**: At least one ERROR-level issue found
- **2**: Invalid command-line arguments

## Architecture

```
cmd/tools/validate_sessions_v2/
├── main.go              # CLI entry, orchestration
├── loader.go            # Load V1/V2 data
├── validator.go         # 7 validation checks
├── reconstruct.go       # Delta chain reconstruction
├── report.go            # JSON/text formatting
├── helpers.go           # Utility functions
└── *_test.go           # Unit tests (34 tests)
```

## See Also

- [Design Spec](../../../docs/superpowers/specs/2026-07-18-sessions-v2-validation-repair-design.md)
- [Data Mapping](../../../docs/SESSION_V2_DATA_MAPPING.md)
- [Backfill Script](../../../sql/scripts/backfill_sessions_v2.sql)
