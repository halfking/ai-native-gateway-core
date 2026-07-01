# Bandit Integration Fixes - 2026-06-28

## Summary

Fixed all field naming inconsistencies between migration, flusher, LoadFromDB, and integration test. All components now use consistent `bandit_` prefixed field names as per the original design.

## Issues Fixed

### 1. ✅ Migration Field Naming (P0)
**Problem**: Migration 033 used inconsistent field names (some with `bandit_` prefix, some without)

**Files Changed**: `deploy/sql/migrations/033_bandit_scoring.sql`

**Changes**:
- `total_requests` → ❌ Removed (not needed, calculated from success+failure)
- `success_requests` → `bandit_success_count`
- `failure_requests` → `bandit_failure_count`
- `total_latency_ms` → `bandit_total_latency_ms`
- (added) `bandit_avg_latency_ms` (computed during flush)
- `rate_limit_hits` → `bandit_429_count`
- `last_rate_limit_hit` → `penalty_429_last_at`
- `rate_limit_penalty` → `penalty_429_accumulated`

**Result**: All bandit-related fields now use consistent `bandit_` or `penalty_` prefix.

---

### 2. ✅ Flusher Type Conversion (P0)
**Problem**: `bg/bandit_flusher.go` used `database/sql.DB` instead of `pgxpool.Pool`

**Files Changed**: `bg/bandit_flusher.go`

**Changes**:
- Import: `database/sql` → `github.com/jackc/pgx/v5/pgxpool`
- Type: `DB *sql.DB` → `DB *pgxpool.Pool`
- Function signature: `NewBanditFlusher(scorer, db *sql.DB, ...)` → `NewBanditFlusher(scorer, db *pgxpool.Pool, ...)`
- Transaction API: `BeginTx(ctx, nil)` → `Begin(ctx)`, `Rollback()` → `Rollback(ctx)`, `Commit()` → `Commit(ctx)`
- Exec API: `stmt.ExecContext(ctx, ...)` → `tx.Exec(ctx, "UPDATE ...", ...)`

**Result**: Flusher now matches the pgxpool.Pool type used by LoadFromDB and main.go

---

### 3. ✅ Active Flusher Table Name (P0)
**Problem**: `domains/credential/flusher.go` (the active flusher used by main.go) was writing to `api_keys` table instead of `credentials`

**Files Changed**: `domains/credential/flusher.go`

**Changes**:
- Line 135: `UPDATE api_keys` → `UPDATE credentials`

**Result**: Flusher now writes to the correct table that matches the migration.

---

### 4. ✅ Integration Test Connection String
**Problem**: Integration test was using wrong credentials for local database

**Files Changed**: `domains/credential/integration_test.go`

**Changes**:
- Connection string updated to match `local-main-pg` container credentials
- Database name: `llm_gateway` → `postgres`
- User/password: Updated to match actual container

**Result**: Test can now connect to local database (when credentials table exists)

---

## Field Mapping Reference

| Concept | Final Field Name | Type | Notes |
|---------|------------------|------|-------|
| Alpha parameter | `bandit_alpha` | REAL | Beta distribution α (success + 1) |
| Beta parameter | `bandit_beta` | REAL | Beta distribution β (failure + 1) |
| Success count | `bandit_success_count` | BIGINT | 2xx responses |
| Failure count | `bandit_failure_count` | BIGINT | 4xx/5xx responses |
| Total latency | `bandit_total_latency_ms` | BIGINT | Cumulative |
| Average latency | `bandit_avg_latency_ms` | REAL | Computed during flush |
| 429 count | `bandit_429_count` | INT | Rate limit hits |
| Last 429 time | `penalty_429_last_at` | TIMESTAMPTZ | Timestamp |
| 429 penalty | `penalty_429_accumulated` | REAL | 0-10 score |
| Quota remaining | `quota_remaining` | BIGINT | From headers |
| Quota total | `quota_total` | BIGINT | From headers |
| Last quota update | `last_quota_update` | TIMESTAMPTZ | Timestamp |
| Intelligence rank | `intelligence_rank` | INT | 1-100 (lower = smarter) |
| Last scored | `last_scored_at` | TIMESTAMPTZ | Timestamp |

---

## Two BanditFlusher Implementations

**Important Discovery**: There are TWO flusher implementations in the codebase:

1. **`domains/credential/flusher.go`** (ACTIVE)
   - Used by `cmd/gateway/main.go`
   - Has `MarkDirty()` method and batch size support
   - Signature: `NewBanditFlusher(db, bandit, flushInterval, batchSize)`
   - ✅ Fixed to use `credentials` table

2. **`bg/bandit_flusher.go`** (UNUSED)
   - Not referenced by main.go
   - Simpler implementation without dirty tracking
   - Signature: `NewBanditFlusher(scorer, db, flushInterval)`
   - ✅ Fixed to use pgxpool.Pool

**Recommendation**: Consider consolidating to a single implementation in a future refactor.

---

## Verification Status

### ✅ Code Compilation
```bash
cd services/llm-gateway-go
go build ./...
# Result: SUCCESS - No compilation errors
```

### ⚠️ Integration Test
```bash
go test -v ./domains/credential -run TestBanditIntegration_EndToEnd
# Result: SKIPPED - credentials table doesn't exist in local DB
```

**To run the integration test**:
1. Apply all migrations to local database (including 033_bandit_scoring.sql)
2. Ensure `credentials` table exists with all `bandit_*` fields
3. Run: `go test -v ./domains/credential -run TestBanditIntegration_EndToEnd`

---

## Files Modified

1. ✅ `deploy/sql/migrations/033_bandit_scoring.sql` - Consistent field naming
2. ✅ `bg/bandit_flusher.go` - Type conversion to pgxpool.Pool
3. ✅ `domains/credential/flusher.go` - Table name fix (api_keys → credentials)
4. ✅ `domains/credential/integration_test.go` - Connection string update

**Files NOT Modified** (already correct):
- `domains/credential/bandit.go` - LoadFromDB() already uses correct field names
- `cmd/gateway/main.go` - Already uses correct flusher

---

## Next Steps

### High Priority
1. **Apply migration 033 to local/staging database**
   ```bash
   psql -h localhost -p 5434 -U kxuser -d postgres -f deploy/sql/migrations/033_bandit_scoring.sql
   ```

2. **Run integration test to validate end-to-end flow**
   ```bash
   go test -v ./domains/credential -run TestBanditIntegration_EndToEnd
   ```

3. **Deploy to staging with feature flag disabled**
   ```bash
   LLM_GATEWAY_ENABLE_BANDIT_SCORING=false
   ```

### Medium Priority
4. **Add Prometheus metrics** (P2)
   - `bandit_scores_computed_total`
   - `bandit_flushes_total{status="success|failure"}`
   - `bandit_flush_duration_seconds`
   - `bandit_load_errors_total`

5. **Update deployment documentation**
   - Reflect actual file locations
   - Document two flusher implementations
   - Add migration checklist

### Future Considerations
6. **Consolidate flusher implementations**
   - Choose one canonical flusher
   - Remove or clearly mark the other as deprecated

7. **Add more observability**
   - Log Thompson Sampling selection decisions
   - Track bandit score distribution per credential

---

## Deployment Checklist

Before enabling `LLM_GATEWAY_ENABLE_BANDIT_SCORING=true`:

- [ ] Migration 033 applied to target database
- [ ] Verify `credentials` table has all `bandit_*` fields
- [ ] Integration test passes locally
- [ ] Prometheus metrics added (optional but recommended)
- [ ] Deploy with feature flag OFF
- [ ] Monitor startup logs for "bandit: loaded N credentials"
- [ ] Enable feature flag for single instance (canary)
- [ ] Monitor for 24 hours
- [ ] Gradually roll out to remaining instances

---

**Fixed By**: AI Assistant  
**Date**: 2026-06-28  
**Status**: ✅ All code fixes complete, awaiting database migration + integration test validation