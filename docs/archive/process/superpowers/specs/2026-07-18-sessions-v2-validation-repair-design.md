# Sessions V2 Validation and Repair Tool Design

## Goal

Provide a production-ready tool to validate data consistency between V1 (request_logs) and V2 (sessions tables) during the dual-write phase, and safely repair discrepancies by rebuilding V2 from V1 as the source of truth.

## Scope

### In Scope
- Per-session validation comparing V1 and V2 data across all dimensions
- Batch validation with configurable date ranges and tenant filtering
- Detailed discrepancy reporting with field-level differences
- Safe repair mode that rebuilds V2 sessions from V1 source
- Dry-run by default with explicit `--apply` flag for writes
- Settle window to exclude recently-written sessions from batch validation
- JSON and text output formats for automation and human review

### Out of Scope
- Modifying V1 data (V1 is always the source of truth)
- Repairing V2 data without deleting and rebuilding (partial updates are error-prone)
- Real-time validation during request processing (this is an offline diagnostic tool)
- Automatic repair without user confirmation (requires explicit `--apply`)

## Background

The Sessions V2 architecture (Migration 430) introduced parallel storage:
- **V1**: `gateway.request_logs` (legacy, monolithic JSONB)
- **V2**: `gateway.sessions`, `gateway.session_turns`, `gateway.session_bodies`, `gateway.session_turn_logs`

During dual-write phase, the DualWriter writes to both systems. This tool validates consistency and repairs divergence by treating V1 as the source of truth.

## Terminology

- **Session**: A conversation identified by `session_id`, containing multiple turns
- **Turn**: A single request-response pair within a session, identified by `request_id`
- **Settle Window**: Time buffer (default 10 minutes) to exclude sessions that may still be receiving async aggregation updates
- **Repair**: Delete all V2 records for a session and rebuild from V1 using backfill logic
- **Dry Run**: Validation and reporting without any writes (default mode)

## Data Flow

```
┌─────────────────────────────────────────────────────────────┐
│ 1. Load Sessions                                            │
│    - Batch: SELECT DISTINCT session_id WHERE ...           │
│    - Single: Validate specified session_id                 │
│    - Apply settle window filter (updated_at < NOW() - 10m) │
└──────────────────┬──────────────────────────────────────────┘
                   │
                   ▼
┌─────────────────────────────────────────────────────────────┐
│ 2. Per-Session Validation                                   │
│    - Load V1 turns: SELECT * FROM request_logs              │
│    - Load V2 turns: SELECT * FROM session_turns             │
│    - Load V2 bodies: SELECT * FROM session_bodies           │
│    - Load V2 session: SELECT * FROM sessions                │
└──────────────────┬──────────────────────────────────────────┘
                   │
                   ▼
┌─────────────────────────────────────────────────────────────┐
│ 3. Run Validation Checks                                    │
│    ├─ Request ID parity (V1 vs V2 turn mapping)            │
│    ├─ Timestamp consistency                                 │
│    ├─ Token sum (V1.usage vs V2.prompt+completion)         │
│    ├─ Cost sum (V1.cost_usd vs V2.cost_usd)                │
│    ├─ Metadata (model, provider, credential, verdicts)     │
│    ├─ Session snapshot (sessions table vs aggregated turns)│
│    └─ Bodies integrity (JSON parseable, delta chain valid) │
└──────────────────┬──────────────────────────────────────────┘
                   │
                   ▼
┌─────────────────────────────────────────────────────────────┐
│ 4. Generate Report                                          │
│    - Status: ok | warning | error                          │
│    - Summary: V1/V2 counts, tokens, costs                  │
│    - Differences: field-level discrepancies with severity  │
│    - Incremental validation: delta reconstruction results  │
└──────────────────┬──────────────────────────────────────────┘
                   │
                   ▼
┌─────────────────────────────────────────────────────────────┐
│ 5. Repair (if --repair and --apply)                        │
│    - DELETE FROM session_bodies WHERE session_id = $1      │
│    - DELETE FROM session_turns WHERE session_id = $1       │
│    - DELETE FROM sessions WHERE session_id = $1            │
│    - Rebuild from V1 using backfill logic                  │
│    - Verify rebuild: re-run validation                     │
└─────────────────────────────────────────────────────────────┘
```

## Command Interface

```bash
# Validate single session (detailed output)
go run ./cmd/tools/validate_sessions_v2 \
  -dsn "postgres://..." \
  -tenant-id "tenant_xxx" \
  -session-id "gw_abc123" \
  -verbose

# Validate date range (batch mode)
go run ./cmd/tools/validate_sessions_v2 \
  -dsn "postgres://..." \
  -tenant-id "tenant_xxx" \
  -start-date "2026-07-01" \
  -end-date "2026-07-17" \
  -max-sessions 1000 \
  -settle-window 10m

# Repair single session (dry-run first, then apply)
go run ./cmd/tools/validate_sessions_v2 \
  -dsn "postgres://..." \
  -tenant-id "tenant_xxx" \
  -session-id "gw_abc123" \
  -repair

go run ./cmd/tools/validate_sessions_v2 \
  -dsn "postgres://..." \
  -tenant-id "tenant_xxx" \
  -session-id "gw_abc123" \
  -repair \
  -apply

# Output formats
go run ./cmd/tools/validate_sessions_v2 \
  -dsn "postgres://..." \
  -tenant-id "tenant_xxx" \
  -session-id "gw_abc123" \
  -format json > report.json

go run ./cmd/tools/validate_sessions_v2 \
  -dsn "postgres://..." \
  -tenant-id "tenant_xxx" \
  -start-date "2026-07-01" \
  -end-date "2026-07-17" \
  -format text
```

### Flags

| Flag | Type | Required | Default | Description |
|------|------|----------|---------|-------------|
| `-dsn` | string | Yes | - | PostgreSQL connection string |
| `-tenant-id` | string | Yes | - | Tenant ID to validate |
| `-session-id` | string | No | - | Specific session to validate (single-session mode) |
| `-start-date` | string | No | - | Start date YYYY-MM-DD (batch mode) |
| `-end-date` | string | No | - | End date YYYY-MM-DD (batch mode) |
| `-max-sessions` | int | No | 1000 | Maximum sessions to validate in batch mode |
| `-settle-window` | duration | No | 10m | Exclude sessions updated within this window |
| `-verbose` | bool | No | false | Show detailed per-session output during validation |
| `-format` | string | No | json | Output format: `json` or `text` |
| `-repair` | bool | No | false | Enable repair mode (rebuild V2 from V1) |
| `-apply` | bool | No | false | Apply changes (without this, dry-run only) |

### Constraints

- `--repair` requires `--session-id` (single-session only)
- `--repair` without `--apply` shows what would be deleted/rebuilt
- `--settle-window` only applies to batch mode (ignored for single-session)
- Exit code 0 if all sessions are `ok` or `warning`, 1 if any `error`

## Validation Checks

Each session undergoes the following checks, categorized by severity:

### 1. Request ID Parity (ERROR)

**Purpose**: Verify that V1 and V2 have the same set of turns.

**Logic**:
```sql
-- V1 turns
SELECT request_id, ts FROM gateway.request_logs
WHERE tenant_id = $1 AND session_id = $2
ORDER BY ts ASC

-- V2 turns
SELECT request_id, turn_no, ts FROM gateway.session_turns
WHERE tenant_id = $1 AND session_id = $2
ORDER BY turn_no ASC
```

**Checks**:
- Every V1 `request_id` exists in V2
- Every V2 `request_id` exists in V1
- Turn count matches: `COUNT(V1) = COUNT(V2)`

**Failures**:
- `missing_in_v2`: request_id in V1 but not V2 → ERROR
- `missing_in_v1`: request_id in V2 but not V1 → ERROR (should never happen)
- `count_mismatch`: different row counts → ERROR

### 2. Timestamp Consistency (ERROR)

**Purpose**: Verify timestamps match between V1 and V2.

**Logic**: For each matched `request_id`, compare `ts` fields.

**Tolerance**: Exact match (no tolerance for timestamp drift)

**Failures**:
- `timestamp_mismatch`: V1.ts ≠ V2.ts → ERROR

### 3. Token Sum (WARNING with tolerance)

**Purpose**: Verify token counts are consistent.

**Logic**:
```go
v1Tokens := SUM((usage->>'prompt_tokens')::int + (usage->>'completion_tokens')::int)
v2Tokens := SUM(prompt_tokens + completion_tokens)
```

**Tolerance**: 1% or 10 tokens (whichever is larger)

**Failures**:
- `token_sum_mismatch`: |v1Tokens - v2Tokens| > tolerance → WARNING
- Explanation: Rounding in JSON extraction and cache token handling may cause minor diffs

### 4. Cost Sum (WARNING with tolerance)

**Purpose**: Verify cost calculations are consistent.

**Logic**:
```go
v1Cost := SUM(cost_usd) FROM request_logs
v2Cost := SUM(cost_usd) FROM session_turns
```

**Tolerance**: $0.01 or 1% (whichever is larger)

**Failures**:
- `cost_sum_mismatch`: |v1Cost - v2Cost| > tolerance → WARNING

### 5. Metadata Consistency (ERROR)

**Purpose**: Verify critical metadata fields match.

**Fields**:
- `model`: V1.client_model vs V2.model
- `provider`: V1.provider_id vs V2.provider
- `credential_id`: V1.credential_id vs V2.credential_id
- `injection_verdict`: Extract from V1.compression_meta vs V2.injection_verdict
- `output_verdict`: Extract from V1.compression_meta vs V2.output_verdict

**Tolerance**: None (exact match required)

**Failures**:
- `metadata_mismatch`: Any field mismatch → ERROR
- Report field name, V1 value, V2 value, turn_no

### 6. Session Snapshot Accuracy (ERROR)

**Purpose**: Verify `gateway.sessions` aggregates match `gateway.session_turns`.

**Logic**:
```sql
-- Expected from session_turns
SELECT
  COUNT(*) as turn_count,
  SUM(prompt_tokens + completion_tokens) as token_sum,
  SUM(cost_usd) as cost_sum,
  MAX(turn_no) as last_turn_no
FROM gateway.session_turns
WHERE tenant_id = $1 AND session_id = $2

-- Actual from sessions
SELECT total_turns, total_tokens, total_cost_usd, last_turn_no
FROM gateway.sessions
WHERE tenant_id = $1 AND session_id = $2
```

**Tolerance**: Same as token/cost checks

**Failures**:
- `snapshot_turn_count_mismatch`: total_turns ≠ COUNT(*) → ERROR
- `snapshot_token_mismatch`: |total_tokens - SUM(tokens)| > tolerance → WARNING
- `snapshot_cost_mismatch`: |total_cost_usd - SUM(cost)| > tolerance → WARNING
- `snapshot_last_turn_mismatch`: last_turn_no ≠ MAX(turn_no) → ERROR

### 7. Bodies Integrity (WARNING for compressed modes)

**Purpose**: Verify `gateway.session_bodies` deltas are valid JSON and reconstructable.

**Checks**:

a) **JSON Validity** (ERROR):
```go
for each turn:
  - json.Unmarshal(request_delta) must succeed
  - json.Unmarshal(response_delta) must succeed
```

b) **Delta Reconstruction** (WARNING for non-full modes):
```go
// Only strict check for submit_mode = 'full'
// For 'delta', 'snapshot', 'inferred_compressed': report WARNING if mismatch

if submit_mode == 'full':
  reconstructed := accumulateDeltas(session_bodies)
  for each turn:
    if !jsonEqual(reconstructed[turn], V1.request_body.messages):
      → ERROR: "Bodies reconstruction failed for turn X"
else:
  // Compressed modes: can't guarantee exact reconstruction
  → WARNING: "Submit mode X prevents strict body comparison"
```

**Failures**:
- `invalid_json`: Delta JSON parsing failed → ERROR
- `reconstruction_mismatch_full`: Full mode body mismatch → ERROR
- `reconstruction_warning_compressed`: Compressed mode body differences → WARNING

## Status Classification

Each session gets one of three statuses:

| Status | Condition | Exit Code Contribution |
|--------|-----------|----------------------|
| `ok` | All checks pass | 0 (success) |
| `warning` | Only WARNING-level issues (token/cost tolerance, compressed body) | 0 (success) |
| `error` | Any ERROR-level issue (missing turns, metadata mismatch, etc.) | 1 (failure) |

**Batch validation exit code**:
- 0 if all sessions are `ok` or `warning`
- 1 if any session has `error` status

## Output Format

### JSON Output (default)

**Single session**:
```json
{
  "session_id": "gw_abc123",
  "tenant_id": "tenant_xxx",
  "validation_time": "2026-07-18T15:00:00Z",
  "status": "error",
  "summary": {
    "v1_turns": 10,
    "v2_turns": 9,
    "v1_tokens": 15000,
    "v2_tokens": 15000,
    "v1_cost": 0.15,
    "v2_cost": 0.15
  },
  "differences": [
    {
      "check": "Request ID Parity",
      "severity": "error",
      "field": "request_count",
      "v1_value": 10,
      "v2_value": 9,
      "description": "Missing in V2: req_xyz789"
    },
    {
      "check": "Token Sum",
      "severity": "warning",
      "field": "total_tokens",
      "v1_value": 15000,
      "v2_value": 15010,
      "description": "Difference within 1% tolerance"
    }
  ],
  "incremental_validation": {
    "status": "warning",
    "turns_validated": 10,
    "mismatches": [
      {
        "turn_no": 3,
        "reason": "submit_mode=inferred_compressed prevents strict comparison"
      }
    ]
  }
}
```

**Batch validation**:
```json
{
  "tenant_id": "tenant_xxx",
  "start_date": "2026-07-01",
  "end_date": "2026-07-17",
  "validation_time": "2026-07-18T15:00:00Z",
  "settle_window": "10m",
  "summary": {
    "sessions_checked": 847,
    "sessions_ok": 820,
    "sessions_warning": 25,
    "sessions_error": 2
  },
  "sessions": [
    { /* session report 1 */ },
    { /* session report 2 */ },
    // ...
  ]
}
```

### Text Output (human-readable)

```
================================================================================
SESSIONS V2 VALIDATION REPORT
================================================================================
Tenant:          tenant_xxx
Date Range:      2026-07-01 to 2026-07-17
Settle Window:   10m
Validation Time: 2026-07-18 15:00:00
--------------------------------------------------------------------------------

Summary: 2/847 sessions have errors, 25 warnings

Session: gw_abc123 [ERROR]
  V1: 10 turns, 15000 tokens, $0.15
  V2: 9 turns, 15000 tokens, $0.15

  [ERROR] Request ID Parity
    Missing in V2: req_xyz789

  [WARNING] Token Sum
    V1: 15000, V2: 15010 (diff: 10, within tolerance)

Session: gw_def456 [OK]
  V1: 5 turns, 8000 tokens, $0.08
  V2: 5 turns, 8000 tokens, $0.08
  All checks passed.

================================================================================
EXIT CODE: 1 (errors found)
================================================================================
```

## Repair Mode

### Safety Constraints

- **Single-session only**: `--repair` requires `--session-id`
- **Dry-run by default**: Without `--apply`, only shows what would happen
- **Transactional**: All deletes and rebuilds in one transaction
- **Verification**: After rebuild, re-run validation to confirm

### Repair Transaction

```sql
BEGIN;

-- 1. Acquire advisory lock (same as TurnWriter)
SELECT pg_advisory_xact_lock(hashSessionKey(tenant_id, session_id));

-- 2. Delete V2 data (reverse FK order)
DELETE FROM gateway.session_turn_logs
WHERE tenant_id = $1 AND session_id = $2;

DELETE FROM gateway.session_bodies
WHERE tenant_id = $1 AND session_id = $2;

DELETE FROM gateway.session_turns
WHERE tenant_id = $1 AND session_id = $2;

DELETE FROM gateway.sessions
WHERE tenant_id = $1 AND session_id = $2;

-- 3. Rebuild from V1 (using backfill logic)
WITH ordered_logs AS (
  SELECT *, ROW_NUMBER() OVER (ORDER BY ts ASC) as turn_no
  FROM gateway.request_logs
  WHERE tenant_id = $1 AND session_id = $2
)
INSERT INTO gateway.session_turns (...)
SELECT ... FROM ordered_logs;

INSERT INTO gateway.session_bodies (...)
SELECT ... FROM ordered_logs JOIN request_logs_bodies ...;

INSERT INTO gateway.sessions (...)
SELECT ... FROM gateway.session_turns
WHERE session_id = $2
GROUP BY session_id;

COMMIT;
```

### Repair Output

**Dry-run** (`--repair` without `--apply`):
```
[DRY RUN] Repair plan for session gw_abc123:

  Will DELETE:
    - 10 rows from session_turn_logs
    - 10 rows from session_bodies
    - 10 rows from session_turns
    - 1 row from sessions

  Will REBUILD from V1:
    - 10 turns
    - 10 bodies
    - 1 session snapshot

  Source: 10 rows in request_logs

Run with --apply to execute this repair.
```

**Applied** (`--repair --apply`):
```
[REPAIR] Rebuilding session gw_abc123 from V1...

  Deleted:
    ✓ 10 rows from session_turn_logs
    ✓ 10 rows from session_bodies
    ✓ 10 rows from session_turns
    ✓ 1 row from sessions

  Rebuilt:
    ✓ 10 turns inserted
    ✓ 10 bodies inserted
    ✓ 1 session snapshot created

  Verification:
    ✓ Re-validation passed (status: ok)

Repair completed successfully.
```

## Code Structure

Extend existing `cmd/tools/validate_sessions_v2/` directory:

```
cmd/tools/validate_sessions_v2/
├── main.go              # CLI entry, flag parsing, output formatting
├── main_test.go         # Existing helper tests
├── loader.go            # NEW: Load V1/V2 data per session
├── loader_test.go       # NEW: Loader unit tests
├── validator.go         # NEW: Run validation checks
├── validator_test.go    # NEW: Validator unit tests (no DB)
├── reconstruct.go       # NEW: Delta chain reconstruction
├── reconstruct_test.go  # NEW: Reconstruction unit tests
├── repair.go            # NEW: Rebuild V2 from V1
├── repair_test.go       # NEW: Repair integration tests
├── report.go            # NEW: JSON/text report generation
└── README.md            # Updated usage documentation
```

### Module Responsibilities

**`main.go`**:
- Parse CLI flags
- Validate flag combinations
- Invoke batch or single-session validation
- Format and print reports
- Exit with appropriate code

**`loader.go`**:
```go
type V1Turn struct {
  RequestID   string
  Ts          time.Time
  SessionID   string
  TenantID    string
  Usage       json.RawMessage
  CostUSD     float64
  ClientModel string
  ProviderID  string
  // ... other fields
}

type V2Turn struct {
  RequestID        string
  TurnNo           int
  Ts               time.Time
  SubmitMode       string
  PromptTokens     int
  CompletionTokens int
  CostUSD          float64
  Model            string
  Provider         string
  // ... other fields
}

func LoadV1Turns(ctx, db, tenantID, sessionID) ([]V1Turn, error)
func LoadV2Turns(ctx, db, tenantID, sessionID) ([]V2Turn, error)
func LoadV2Bodies(ctx, db, tenantID, sessionID) ([]V2Body, error)
func LoadV2Session(ctx, db, tenantID, sessionID) (*V2Session, error)
```

**`validator.go`**:
```go
type ValidationCheck struct {
  Name        string
  Severity    string // "error" | "warning"
  Passed      bool
  V1Value     interface{}
  V2Value     interface{}
  Description string
  Details     []string
}

func CheckRequestIDParity(v1, v2 []V1Turn) ValidationCheck
func CheckTokenSum(v1, v2) ValidationCheck
func CheckCostSum(v1, v2) ValidationCheck
func CheckMetadata(v1, v2) ValidationCheck
func CheckSnapshotAccuracy(turns, session) ValidationCheck
```

**`reconstruct.go`**:
```go
type ReconstructionResult struct {
  TurnNo      int
  Reconstructed []Message
  Expected    []Message
  Match       bool
  Reason      string
}

func ReconstructFromDeltas(bodies []V2Body) ([][]Message, error)
func ValidateReconstruction(v1, v2Reconstructed, submitModes) []ReconstructionResult
```

**`repair.go`**:
```go
type RepairPlan struct {
  SessionID     string
  DeleteCounts  map[string]int // table -> row count
  RebuildCounts map[string]int // table -> row count
  SourceCount   int            // V1 rows
}

func PlanRepair(ctx, db, tenantID, sessionID) (*RepairPlan, error)
func ExecuteRepair(ctx, db, tenantID, sessionID) error
func VerifyRepair(ctx, db, tenantID, sessionID) (*SessionReport, error)
```

**`report.go`**:
```go
type SessionReport struct {
  SessionID  string
  TenantID   string
  Status     string // "ok" | "warning" | "error"
  Summary    Summary
  Differences []ValidationCheck
  Incremental IncrementalValidation
}

func GenerateSessionReport(v1, v2, checks) *SessionReport
func FormatJSON(report) ([]byte, error)
func FormatText(report) string
```

## Testing Strategy

### Unit Tests (no database)

**Loader**: Mock pgx.Rows, test SQL parsing and struct mapping
**Validator**: Pure functions with sample V1/V2 data
**Reconstruct**: Test delta accumulation logic with various message sequences
**Report**: Test JSON/text formatting with known inputs

### Integration Tests (with database)

**Repair**:
- Create test session in V1
- Write mismatched V2 data
- Run repair, verify rebuild matches V1
- Test rollback on failure

**End-to-end**:
- Populate test tenant with known V1/V2 data
- Run validation, verify reports
- Test batch mode with settle window
- Test single-session mode with repair

## Rollout Plan

### Phase 1: Validation Enhancement (Current PR)
- Extend `loader.go` with V1/V2 loading
- Implement `validator.go` with all checks
- Add `reconstruct.go` for delta chain validation
- Enhance `report.go` with JSON/text formats
- Add comprehensive unit tests
- Update README with new flags and examples

### Phase 2: Repair Mode (Next PR)
- Implement `repair.go` with transaction safety
- Add repair integration tests
- Add dry-run output formatting
- Document repair workflow and safety constraints

### Phase 3: Production Usage
- Run validation in CI/CD pipeline
- Set up daily cron job for production validation
- Create runbook for investigating and repairing discrepancies
- Monitor validation metrics (error rate, repair success rate)

## Security Considerations

- **Tenant Isolation**: All queries filter by `tenant_id`
- **RLS Bypass**: Tool sets `app.bypass_rls=true` for admin access
- **Connection Security**: DSN from environment or secure config only
- **Audit Logging**: Log all repair operations with timestamp and actor
- **No PII in Logs**: Never log request bodies or session content

## Performance Considerations

- **Batch Size**: Default 1000 sessions, configurable with `--max-sessions`
- **Parallel Loading**: Load V1/V2 data concurrently per session
- **Connection Pooling**: Reuse single pgxpool.Pool across validations
- **Memory**: Stream session reports instead of buffering all in memory
- **Indexes**: Rely on existing indexes on `(tenant_id, session_id, ts)`

## Acceptance Criteria

- [ ] Single-session validation produces detailed report with all checks
- [ ] Batch validation respects settle window and max-sessions limit
- [ ] All ERROR conditions produce exit code 1
- [ ] WARNING-only sessions produce exit code 0
- [ ] JSON output is parseable and contains all required fields
- [ ] Text output is human-readable with color/formatting
- [ ] Repair dry-run shows delete/rebuild counts without writing
- [ ] Repair with --apply rebuilds V2 from V1 and verifies
- [ ] Repair transaction rolls back on any failure
- [ ] Unit tests cover all validation logic without database
- [ ] Integration tests verify repair with real database
- [ ] README documents all flags and usage examples
- [ ] Tool runs successfully against staging environment

## Open Questions

None at this time. Design is ready for implementation.

## References

- Migration 430: `sql/migrations/startup/430_sessions_v2_schema.sql`
- Data Mapping: `docs/SESSION_V2_DATA_MAPPING.md`
- Backfill Script: `sql/scripts/backfill_sessions_v2.sql`
- Existing Validator: `cmd/tools/validate_sessions_v2/main.go` (Phase 2.2)
- V2 Writers: `domains/session/v2/*.go`
- DualWriter: `domains/session/dual_writer.go`
