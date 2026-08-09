# Modality Sticky-Upsert Fix + Backfill Tool

## Problem Summary

**Root cause**: `discovery.Service` upsert used dead-code COALESCE that prevented modality upgrades.

```sql
-- BEFORE (dead code):
modality = COALESCE(models_canonical.modality, $4)
-- models_canonical.modality is NOT NULL DEFAULT 'text'
-- → COALESCE always returns existing value → sticky forever
```

**Impact**: Models seeded before inference-rule fixes stayed at stale modality values:
- `glm-4.5v` stuck at `text` → vision requests 503 (dropped from candidate set)
- `qwen2.5-vl-72b` stuck at `text` → same
- 50+ models affected by commit 4639d4ae5 rules but not repaired in production DB

## Fixes Applied (2026-08-09)

### 1. Discovery Upsert Logic (discovery/discovery.go)

**Changed**:
```sql
modality = CASE
  WHEN models_canonical.modality = 'text' AND $4 <> 'text'
  THEN $4
  ELSE models_canonical.modality
END
```

**Semantics**:
- **Upgrade-only**: adopts fresh inference when stored='text' and inferred≠'text'
- **Never downgrades**: if stored is already vision/audio/multimodal, keeps it
- **Respects manual overrides**: super_admin PATCH always sets non-'text' value
  (see admin/model_modality.go), so those rows are never touched

**Validation**:
- ✅ Parses with pglast (real PostgreSQL grammar)
- ✅ Builds: `go build ./discovery/`
- ✅ SQL comment guard: `go test ./internal/sqlguard/`

### 2. Backfill Tool (cmd/tools/backfill-modality)

**Purpose**: Repair existing production rows stuck at modality='text'.

**Algorithm**:
1. SELECT all rows where `modality='text' AND status!='disabled'`
2. Re-run `modelname.InferModality(canonical_name)` for each
3. Upgrade if inference now returns non-'text'
4. UPDATE in single transaction

**Safety**:
- Upgrade-only (same predicate as upsert fix)
- Idempotent (safe to re-run)
- Dry-run default (`--dry-run=true`)
- Single source of truth (imports actual Go inference rules, not duplicated SQL)

**Usage**:
```bash
# 1. Dry-run (review upgrade plan):
export DATABASE_URL="postgres://..."
go run ./cmd/tools/backfill-modality

# 2. Commit:
go run ./cmd/tools/backfill-modality --dry-run=false
```

**Example output**:
```
found 127 models with modality='text'
upgrade candidates: 23
  id=42   glm-4.5v                                 text → vision
  id=58   qwen2.5-vl-72b                          text → vision
  id=91   gemini-2.5-flash                        text → multimodal
  id=103  gpt-5                                   text → multimodal
  ...
DRY-RUN mode: no changes committed
```

## Deployment Plan

### Phase 1: Code Deployment (All Environments)

Deploy commit containing:
1. Fixed discovery upsert (upgrade-only CASE)
2. Backfill tool (for admin execution)

**Impact**: New models auto-upgrade on re-discovery. Existing stale rows unchanged until Phase 2.

### Phase 2: Backfill Execution (Per Environment)

**154 production**:
```bash
ssh admin@154
cd /path/to/llm-gateway-go

# 1. Dry-run
export DATABASE_URL="postgres://..."
go run ./cmd/tools/backfill-modality

# 2. Review output, confirm upgrade list
# Expected: glm-4.5v, qwen2.5-vl-*, gpt-5*, etc.

# 3. Commit
go run ./cmd/tools/backfill-modality --dry-run=false

# 4. Verify
psql "$DATABASE_URL" -c "
  SELECT canonical_name, modality
  FROM models_canonical
  WHERE canonical_name IN ('glm-4.5v', 'qwen2.5-vl-72b', 'gpt-5')
  ORDER BY canonical_name;
"
# Expected: all show vision/multimodal
```

**245 testing** (same steps, different host).

### Phase 3: Verification

**Metrics to watch**:
- 503 rate for vision models (should drop to ~0%)
- `loadCandidatesByModalityDB` candidate count for vision requests (should increase)
- Request logs: vision requests to glm-4.5v / qwen2.5-vl should route successfully

**SQL verification**:
```sql
-- Count models by modality
SELECT modality, COUNT(*)
FROM models_canonical
WHERE status='active'
GROUP BY modality
ORDER BY modality;

-- Verify specific models
SELECT id, canonical_name, modality, source, updated_at
FROM models_canonical
WHERE canonical_name ~ '(glm-.*v[^-]|qwen.*-vl-|gpt-5|gemini-2\.5)'
  AND status='active'
ORDER BY canonical_name;
```

## Rollback Plan

If Phase 2 backfill causes issues:

```sql
BEGIN;

-- Rollback specific model (example: glm-4.5v)
UPDATE models_canonical
SET modality = 'text', updated_at = now()
WHERE canonical_name = 'glm-4.5v';

-- Or rollback all upgraded in last N minutes:
UPDATE models_canonical
SET modality = 'text'
WHERE modality IN ('vision', 'multimodal', 'audio')
  AND updated_at > now() - interval '10 minutes'
  AND source = 'discovery';

-- Verify before commit:
SELECT canonical_name, modality FROM models_canonical WHERE updated_at > now() - interval '1 hour';

COMMIT; -- or ROLLBACK;
```

**Note**: Rollback only needed if inference rules are wrong. The upsert fix itself is safe (upgrade-only, respects overrides).

## Testing

### Unit Test: Inference Rules

```bash
# Verify 115-model corpus + 44 targeted cases
go test ./modelname -run TestInferModality -v
```

### Integration Test: Discovery Upsert

**Manual SQL test** (requires test DB):
```sql
-- Setup
CREATE TEMP TABLE models_canonical (
    id serial PRIMARY KEY,
    canonical_name text UNIQUE NOT NULL,
    modality text NOT NULL DEFAULT 'text',
    CHECK (modality IN ('text','vision','audio','multimodal','embedding'))
);

-- Seed with stale 'text' value
INSERT INTO models_canonical (canonical_name, modality)
VALUES ('glm-4.5v', 'text');

-- Simulate discovery upsert (new CASE logic)
INSERT INTO models_canonical (canonical_name, modality)
VALUES ('glm-4.5v', 'vision')
ON CONFLICT (canonical_name) DO UPDATE SET
    modality = CASE
        WHEN models_canonical.modality = 'text' AND EXCLUDED.modality <> 'text'
        THEN EXCLUDED.modality
        ELSE models_canonical.modality
    END;

-- Verify upgrade
SELECT canonical_name, modality FROM models_canonical;
-- Expected: glm-4.5v | vision

-- Test: manual override not trampled
UPDATE models_canonical SET modality = 'multimodal' WHERE canonical_name = 'glm-4.5v';

-- Re-run discovery upsert (infers 'vision')
INSERT INTO models_canonical (canonical_name, modality)
VALUES ('glm-4.5v', 'vision')
ON CONFLICT (canonical_name) DO UPDATE SET
    modality = CASE
        WHEN models_canonical.modality = 'text' AND EXCLUDED.modality <> 'text'
        THEN EXCLUDED.modality
        ELSE models_canonical.modality
    END;

-- Verify no downgrade
SELECT canonical_name, modality FROM models_canonical;
-- Expected: glm-4.5v | multimodal (unchanged)
```

### Backfill Tool Test

```bash
# Dry-run on test DB
export DATABASE_URL="postgres://localhost/llm_gateway_test"
go run ./cmd/tools/backfill-modality

# Expected output:
# - Lists all models with modality='text'
# - Shows upgrade plan (text → vision/multimodal/audio)
# - Does NOT commit (dry-run=true default)
```

## FAQ

### Q1: Why not backfill in a migration?

**A**: Migrations run at startup, blocking the service. Backfill is a one-time admin operation that can run offline. Also, duplicating 100+ Go inference rules into SQL creates maintenance burden.

### Q2: What if inference rules change again?

**A**: The upsert fix ensures future changes auto-apply on re-discovery. Backfill tool can be re-run anytime to catch stragglers.

### Q3: Why "upgrade-only" instead of always overwriting?

**A**: Respects manual super_admin overrides. If an admin explicitly sets modality='multimodal' but inference says 'vision', we trust the admin.

### Q4: Can backfill downgrade a model?

**A**: No. Only upgrades from 'text' to richer modality. Never downgrades existing vision/audio/multimodal to text.

### Q5: Is this safe to run on production?

**A**: Yes, with dry-run first:
1. Upgrade-only (no downgrades)
2. Respects manual overrides
3. Idempotent (safe to re-run)
4. Single transaction (atomic commit/rollback)
5. Default dry-run=true

## Related

- **Commit 4639d4ae5**: Modality inference rule fixes (50+ models)
- **Commit 80d4484d1**: API Key authentication integration
- **admin/model_modality.go**: Layer 3 manual override handler
- **modelname/modality_defaults.go**: Single source of truth for inference rules
