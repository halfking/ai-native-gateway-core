# Audit Report — feat/standard-models-rollout (2026-08-20)

## Scope
Standard models rollout for **xAI / Grok**, **Moonshot / Kimi**, **Google Gemini** families.
Adds canonical metadata sync, vendor-prefix aliases, provider_models pre-fill,
binding placeholders, featured_models extension, and a DB-backed catalog
discovery. Replaces the static fallback in `domains/modelquality/discovery.go`.

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