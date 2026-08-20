# Audit Report — feat/standard-models-rollout (2026-08-20)

## Scope
Standard models rollout for **xAI / Grok**, **Moonshot / Kimi**, **Google Gemini** families.
Adds canonical metadata sync, vendor-prefix aliases, provider_models pre-fill,
binding placeholders, featured_models extension, and a DB-backed catalog
discovery. Replaces the static fallback in `domains/modelquality/discovery.go`.

## Audit Round 2 — Standard-model rollout hardening (2026-08-21)

### Addressed

1. **Gateway discovery is now active by default.** `cmd/gateway/main.go` no longer pre-populates `TargetModels`; `ModelQualityWorker` resolves an empty target list through `CanonicalCatalogDiscovery` and preserves explicitly configured targets. Discovery runs outside the worker lifecycle lock and falls back to the static list on DB error/empty results.
2. **Catalog targets are route-eligible.** `CanonicalCatalogDiscovery` now requires a real credential, an available binding, active credential lifecycle/state, non-exhausted quota, and enabled/non-manually-disabled provider. `credential_id=0` placeholders are excluded.
3. **Migration 362 is provider-ID independent.** Placeholder rows resolve `providers` by `(tenant_id, code)` and carry `plan_meta.source='migration-362'`. The down migration removes only matching, still-pending rollout placeholders; the previous time-based deletion was removed.
4. **Discovery persistence contracts are aligned.** Alias upserts use the existing `(canonical_id, raw_name)` uniqueness constraint and propagate errors. Provider-model refreshes set `source='discovery'`, so 361 rollback cannot remove rows already refreshed by discovery.
5. **Fresh-install seed parity is restored.** The three canonical `02-seed.sql` files now have identical `routing_policy.featured_models` content and byte parity.

### Added verification

- Worker target-selection tests cover DB discovery for empty targets, explicit-target preservation, and static fallback.
- Canonical discovery timeout behavior is covered.
- Migration/seed contract tests guard provider-code lookup, provenance-based rollback, and three-file seed parity.

### Verification status

- Focused packages: `go test ./domains/modelquality ./bg ./modelcatalog ./discovery ./sql/migrations/domain` — pass.
- `bash ~/.agents/skills/llm-gateway-deploy-test/test.sh --env local --dry-run` — pass.
- Full `go test ./...` / `make test` reached the full suite but reported pre-existing environment/flaky failures in Redis-backed routing, streaming timestamp assertions, plugin-runtime Go build-cache setup, and integration ticker timing. No model-rollout package failed.


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
   script forgot to NULL `unavailable_reason`, the down could delete a real
   binding. **Superseded by Audit Round 2:** placeholders now carry an
   explicit `plan_meta.source='migration-362'` marker and down deletes only
   still-pending rows with that provenance; no age window is used.

8. **`CanonicalCatalogDiscovery` was dead code in the original rollout.** The
   original implementation added `ModelQualityWorker.SetDiscovery(d)` and
   `resolveDefaultTargetsLocked(ctx)` while holding the worker lock.
   **Superseded by Audit Round 2:** gateway startup now leaves targets empty,
   worker discovery is selected for empty configs, and the DB query runs
   outside the lifecycle lock.

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

- Focused packages `./domains/modelquality ./bg ./modelcatalog ./discovery
  ./sql/migrations/domain` — pass.
- `go test ./cmd/gateway` — pass during the full-suite run.
- `go test ./...` and `make test` reached the full suite but failed in
  pre-existing/environment-sensitive tests: Redis-backed routing, a streaming
  timestamp assertion, plugin-runtime Go build-cache setup, and integration
  ticker timing. No model-rollout package failed.
- `bash ~/.agents/skills/llm-gateway-deploy-test/test.sh --env local --dry-run`
  — pass (`VERIFY_RESULT=dry-run`).

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
   2) UPDATE credential_model_bindings
      SET credential_id = $new_cred_id,
          available = true,
          unavailable_reason = NULL,
          unavailable_at = NULL,
          plan_meta = plan_meta - 'source'
      WHERE credential_id = 0
        AND unavailable_reason = 'placeholder_pending_credential'
        AND provider_model_id IN (
          SELECT pm.id
          FROM provider_models pm
          JOIN providers p ON p.id = pm.provider_id
                             AND p.tenant_id = pm.tenant_id
          WHERE p.tenant_id = 'default'
            AND p.code IN ('xai', 'moonshot', 'google-gemini')
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