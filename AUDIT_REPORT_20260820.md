# Audit Report — feat/standard-models-rollout (2026-08-20)

## Scope
Standard models rollout for **xAI / Grok**, **Moonshot / Kimi**, **Google Gemini** families.
Adds canonical metadata sync, vendor-prefix aliases, provider_models pre-fill,
binding placeholders, featured_models extension, and a DB-backed catalog
discovery. Replaces the static fallback in `domains/modelquality/discovery.go`.

## Audit Round 1 — Findings & Fixes (commit de0f1e91d)

Audited by parallel sub-agents against the comprehensive-code-audit skill
(data flow, closure, state machine, concurrency, compatibility).

### P0 — Addressed in this commit

1. **359.down `reasoning_caps->>'source' IS NULL` guard was always TRUE.**
   `internal/reasoncap/pgsource.go` `capsJSON` struct has no `Source` field, so
   the JSONB never contains a `source` key. The guard therefore matched every
   row, and the down migration would unconditionally wipe operator overrides.
   **Fix:** 359.up now writes `'source', 'migration-359'` into the JSONB;
   359.down only clears rows where `reasoning_caps->>'source' = 'migration-359'`.

2. **361 hard-coded `providers.id = 10/17/30` is environment-fragile.**
   On a DB where operators added `xai/moonshot/google-gemini` rows via the admin
   UI before the seed loaded, IDs would not match. **Fix:** 361 now uses
   `providers.code` lookup (`SELECT p.id FROM providers p WHERE p.code = 'xai'
   AND p.tenant_id = 'default'`).

3. **359.up assumed `models_canonical.reasoning_caps` column exists.**
   The column is added by 478 (startup migration). If 359 runs without 478
   (e.g. domain-only batch), the migration crashes. **Fix:** Defensive
   `ALTER TABLE models_canonical ADD COLUMN IF NOT EXISTS reasoning_caps JSONB`
   + matching indexes at the top of 359.up.

4. **Seed divergence on `routing_policy.featured_models` (line 934).**
   `sql/schema/02-seed.sql` and the `deploy/.../02-seed.sql` +
   `installer/.../02-seed.sql` differ in the line-934 INSERT — the latter two
   already include `gemini-3.5-flash` and `gemini-3-flash-preview` that 363
   adds. **Fix:** 363's WHERE clause now uses `featured_models @> ARRAY[...5
   names]` so the migration only fires when ALL five are absent, avoiding
   duplicate writes on the deploy/installer seeds.

### P1 — Addressed

5. **361.down DELETE removed discovery-written rows.** No source-tag filter.
   **Fix:** Added `source text NOT NULL DEFAULT 'discovery'` column on
   `provider_models` (defensive `ADD COLUMN IF NOT EXISTS`); 361.up writes
   `source='migration-361'`; 361.down deletes only `source='migration-361'`.

6. **361.up `last_seen_at = NOW()` on UPDATE wipes discovery values.**
   Re-running 361 falsely makes every row look freshly probed.
   **Fix:** 361.up's `ON CONFLICT DO UPDATE` no longer sets `last_seen_at`,
   `canonical_raw_name`, `standardized_name`, `outbound_model_name`, or
   `available` — only the columns a manual migration could safely refresh
   (`canonical_id`, `modality`, `source`, `updated_at`).

7. **362.down DELETE could clobber upgraded bindings.** If an onboarding
   script forgot to NULL `unavailable_reason`, the down would delete a real
   binding. **Fix:** Added a 30-day `created_at` age guard.

8. **`CanonicalCatalogDiscovery` was dead code.** No production caller used
   it; both `cmd/gateway/main.go:3423` and `bg/model_quality_worker.go:330`
   hard-coded `GetDefaultMonitorModels()`. **Fix:** Added
   `ModelQualityWorker.SetDiscovery(d)` + `resolveDefaultTargetsLocked(ctx)`
   (held under `w.mu` write lock; concurrency-safe because the only current
   discovery impl `CanonicalCatalogDiscovery` does not re-enter `w`). main.go
   now calls `modelQualityWorker.SetDiscovery(modelquality.NewCanonicalCatalogDiscovery(dbConn.Pool()))`
   alongside `SetDBStorage`.

### P1 — Design improvements

9. **`CanonicalCatalogDiscovery` had a dead `timeout` field.** Constructor
   hard-coded 1s, no setter exposed. **Fix:** Added `WithTimeout(d)` option
   method.

10. **`discovery_canonical_test.go` had ~75 lines of dead mock types**
    (`fakeRow`, `fakePool`, `fakeRows`, `pgxRows`). **Fix:** Removed; kept
    only the three tests that actually run.

11. **`errors.As(err, &pgErr)` block was a tautology.** **Fix:** Replaced with
    `errors.Is(err, context.DeadlineExceeded)` for real signal.

## Audit Round 1 — P2 Deferred (not blocking)

- 360 / 361 / 362 silently no-op if upstream canonical rows don't exist yet
  (352-355 not applied). Acceptable: idempotent + no destructive side-effect.
- 363.down removes operator-added names that happen to overlap. Documented.
- `provider_models.source` column added in 361.up via `ADD COLUMN IF NOT
  EXISTS`. Production DBs likely already have `source` from earlier work;
  the `IF NOT EXISTS` makes it a no-op.
- Moonshot `currency = 'CNY'` in 362 is correct for the China-market Kimi
  provider; verified the billing pipeline handles per-provider currency.

## Verification

- `go build ./...` — clean.
- `go test ./domains/modelquality/ -run
  "TestCanonicalCatalogDiscovery|TestStaticModelDiscovery"` — pass.
- `go test ./bg/ -run TestModelQualityWorker` — pass (previously deadlocked
  on `RLock` self-deadlock when `Start` called the discovery helper under
  write lock; fixed by `resolveDefaultTargetsLocked` naming + lock-held
  invariant).
- `go test ./cmd/...` — pass.
- `make test` — full suite passes; only intermittent
  `TestTCPChecker_RealWorldScenario/Cloudflare_DNS` flake (pre-existing DNS
  flake, unrelated; passes in isolation).

## Audit Round 1 — Original Scope (unchanged from initial commit)

Below this line is the original commit `de0f1e91d` audit; the fixes above are
on top of that.

## Files Touched

### Migrations (new)
- `sql/migrations/domain/359_canonical_metadata_sync.sql` (+ `.down.sql`)
- `sql/migrations/domain/360_aliases_vendor_prefix.sql` (+ `.down.sql`)
- `sql/migrations/domain/361_standard_provider_models.sql` (+ `.down.sql`)
- `sql/migrations/domain/362_standard_binding_placeholders.sql` (+ `.down.sql`)
- `sql/migrations/domain/363_featured_models_standard.sql` (+ `.down.sql`)

All five are additive + idempotent (ON CONFLICT DO NOTHING / DO UPDATE only on
fields that are safe to refresh). No edits to 352-358.

### Code
- `domains/modelquality/discovery.go` — adds `CanonicalCatalogDiscovery`
  (DB-backed, reads `models_canonical × provider_models × providers`);
  marks `GetDefaultMonitorModels` as Deprecated.
- `domains/modelquality/discovery_canonical_test.go` — unit tests for the
  new discovery (nil pool, query error, static fallback path).

### Seeds (synchronized across three locations)
- `sql/schema/02-seed.sql`
- `deploy/sql/schemas/baseline/02-seed.sql`
- `installer/cmd/llm-gw-installer/embeddata/02-seed.sql`

Each receives an identical appended "standard models rollout" section that
mirrors 352/354/355/360 data so a fresh DB shows the catalog **before**
migrations are applied.

## Key Design Decisions

1. **Reasoning config sinks to DB via `reasoning_caps` JSONB** (added by
   `sql/migrations/startup/478_model_reasoning_caps.sql`). Migration 359
   backfills the values that were previously only in
   `internal/reasoncap/reasoning_defaults.go`. Tier-1 read (PGSource) now
   wins, tier-2 (name pattern) still serves as fallback. Operator can hot-tune.

2. **Modality drift defended**: 358 already aligned kimi-k3 to multimodal.
   359 re-asserts the canonical modality for grok-4.6 / kimi-k2.6 /
   kimi-k2.7-code / gemini-3.* so any future drift is caught and corrected.
   Idempotent (`IS DISTINCT FROM`).

3. **Binding placeholders use `credential_id = 0`** as a synthetic sentinel
   + `available = false` + `unavailable_reason =
   'placeholder_pending_credential'`. Routing filter at
   `sql/schema/01-schema.sql:17947` excludes them (`available=TRUE` AND
   `unavailable_reason NOT LIKE 'manual%'`). Onboarding flips `credential_id`
   and `available = true` in a single UPDATE.

4. **Provider_models pre-fill** (361) uses canonical_id resolved from
   `models_canonical.canonical_name`. If migration 352-358 haven't run yet,
   the INSERT becomes a no-op (no matching canonical_name → no row).
   Discovery worker overwrites any field after credential is connected.

5. **Discovery refactor is additive**: `StaticModelDiscovery`,
   `ConfigFileModelDiscovery`, and `GatewayModelDiscovery` are untouched.
   `CanonicalCatalogDiscovery` is opt-in via `NewCanonicalCatalogDiscovery(pool)`.
   `GetDefaultMonitorModels` is preserved as a fallback but Deprecated.

## Verification

- `go build ./domains/modelquality/` — clean.
- `go test ./domains/modelquality/ -run
  "TestCanonicalCatalogDiscovery|TestStaticModelDiscovery"` — pass.
- `make test` — full suite runs; only intermittent flake is
  `TestTCPChecker_RealWorldScenario/Cloudflare_DNS` (pre-existing DNS flake,
  unrelated to this rollout; passes in isolation).
- Three seed files byte-identical for the appended section (verified via
  `diff`).

## Migration Order (apply before 359 if upgrading an older DB)

1. `478_model_reasoning_caps.sql` (already merged) — adds the JSONB column.
2. `352-358` (already merged) — provide canonical rows.
3. `359_canonical_metadata_sync.sql` — backfill reasoning_caps + modality.
4. `360_aliases_vendor_prefix.sql` — add vendor-prefix aliases.
5. `361_standard_provider_models.sql` — pre-fill provider_models.
6. `362_standard_binding_placeholders.sql` — pre-fill disabled bindings.
7. `363_featured_models_standard.sql` — extend featured_models.

## Risks / Notes

- **No real API keys**: 362 placeholders do not participate in routing
  (`available=false`). When real xAI / Moonshot / Gemini credentials are
  provisioned, run:
  ```sql
  UPDATE credential_model_bindings
  SET credential_id = $new_cred_id,
      available = true,
      unavailable_reason = NULL,
      unavailable_at = NULL
  WHERE credential_id = 0
    AND unavailable_reason = 'placeholder_pending_credential'
    AND provider_model_id IN (
      SELECT id FROM provider_models WHERE provider_id IN (10, 17, 30)
    );
  ```

- **Seed files are sensitive to row order**: the appended section uses
  `ON CONFLICT (canonical_name) DO NOTHING` so partial application is safe.
  If the section is applied BEFORE 352-358, the canonical rows are
  pre-seeded; if after, the migration takes precedence via its own
  `ON CONFLICT (canonical_name) DO UPDATE`.

- **E2E smoke** (grok-4.6 reasoning low/med/high/xhigh, kimi-k3 multimodal,
  kimi-k2.6 vision, gemini-3.*) remains blocked on real provider credentials
  (handoff §3). Out of scope for this audit.

## Out of Scope (deliberately deferred)

- Adding reasoning_caps to models NOT in 359 (kept narrow to avoid drift).
- Removing `GetDefaultMonitorModels` entirely (callers may still reference it
  in offline/test contexts).
- Auto-creating `credentials` rows from the placeholders (requires admin auth;
  done via management UI / `scripts/run-migrations-strict.sh` onboarding flow).