# Credential Console Follow-up Handoff

**Prepared:** September 3, 2026

## Scope completed in this session

### Backend credential state

- Added a centralized credential display-state derivation in `admin/credential_status.go`.
- Credential list and monitor DTOs now expose `effective_state` and `effective_reason`.
- Added `sql/migrations/startup/640_normalize_credential_health_status.sql` to normalize legacy `error` and `degraded` health values.
- Unit coverage was added in `admin/credential_status_test.go`.

### Credential-secret access

- Added `admin/credential_secret_routes.go` with a single `canManageCredentialSecrets` predicate.
- Added admin-authenticated routes:
  - `POST /api/credentials/{id}/reveal`
  - `POST /api/credentials/{id}/set-key`
- `super_admin` and legacy `admin_key` have access. `tenant_admin` access is limited to the default tenant and live default-tenant providers.
- Added audit logging for secret reveal.
- Kept the legacy provider-scoped rotate endpoint contract unchanged: its `raw_model_name` remains required. The new unified `set-key` route permits no model name.

### Credential-model pricing

- Extended `updateModelOffer` in `admin/provider_offer_force_recover.go` with optional:
  - `unit_price_in_per_1m`
  - `unit_price_out_per_1m`
  - `cache_read_price_per_1m`
  - `cache_write_price_per_1m`
  - `billing_mode`
- Added `admin/billing_mode.go` validation and routing-cache invalidation when these fields change.
- Updated `web/src/api/providers.ts` request/response and `ModelOffer` typings.

### Frontend shared primitives

- Added `web/src/utils/credentialStatus.ts`.
- Added `web/src/components/CredentialStatusBar.vue`.
- Added `web/src/components/CredentialKeyField.vue`.
- These are intentionally not yet wired into page-level UIs.

## Remaining implementation plan

### Phase 6 — wire shared primitives into the provider-detail console

1. Inspect `web/src/views/provider-detail/CredsTab.vue` before editing.
2. Replace page-local effective-status / health-badge rendering with `CredentialStatusBar` where a credential-level state is shown.
   - Preserve model-level `StatusBadge` handling; it represents a different state model.
3. Replace the drawer key-display/reveal area with `CredentialKeyField`.
   - Decide whether the component should accept a reveal callback or whether it should remain provider-scoped. Do not silently switch callers to `/api/credentials/{id}/reveal` without preserving their existing provider ID and authorization UX.
4. Add a `setUnifiedCredentialKey(credentialId, body)` client API method if the drawer should use the new unified `set-key` endpoint.
5. Inspect `web/src/views/provider-detail/CredentialModelsPanel.vue` and add an inline edit workflow for the five pricing/billing fields.
   - Use existing `updateModelOffer(providerId, offerId, body)`.
   - Validate numeric prices as non-negative finite values before submit.
   - Restrict billing mode choices to `per_token`, `free`, `token_plan`, `code_plan`, and `agent_plan`.
   - Refresh or patch the local `ModelOffer` row from the PATCH response after save.
6. Add focused Vitest tests for the updated API payload and key/status presentation behavior where existing conventions support them.

### Phase 7 — consolidate status mapping across operational views

1. Add `effective_state` and `effective_reason` to `web/src/api/credential-monitor.ts`'s `CredentialMonitorSummary`.
2. In `web/src/views/CredentialMonitorView.vue`, use `credentialDisplayState()` / `CredentialStatusBar` for credential-level rows. Continue using `StatusBadge` for model-specific effective states.
3. Inspect `web/src/views/ProvidersView.vue` and `web/src/views/FreePoolView.vue` for local mappings such as `healthLabel`, `healthBadge`, or hard-coded status colors.
4. Replace only credential-level duplicates with the shared mapping. Keep provider availability and free-pool key semantics separate unless the backend supplies the same normalized state contract.
5. Add translations for any new user-visible strings. The current new primitives use English fallback labels; convert them to the project's i18n conventions during page integration rather than adding another hard-coded-label surface.

### Phase 8 — documentation and full validation

1. Update the relevant feature-design documentation under `docs/03-design/02-feature-design/design/` to cover:
   - normalized effective credential state precedence;
   - secret-management authorization and audit behavior;
   - provider-scoped vs unified key routes;
   - model-offer price and billing mutation behavior.
2. Run targeted tests first, then full checks:

```bash
go test ./admin
go test ./...
cd web && npm run typecheck
cd web && npm test
cd web && npm run build
cd web && npm run i18n:check
```

3. Report pre-existing failures separately from failures introduced by this work. Do not overwrite or revert unrelated working-tree changes.

## Working-tree boundary

The tree contains substantial pre-existing and parallel work, especially under `autoroute/`, `domains/streaming/`, `internal/modelresponse/`, and several provider/discovery files. Treat only the following as this credential-console work unless a dependency requires otherwise:

```text
admin/billing_mode.go
admin/credential_monitor.go
admin/credential_secret_routes.go
admin/credential_status.go
admin/credential_status_test.go
admin/handler.go
admin/provider_access.go
admin/provider_cred_lifecycle.go
admin/provider_credential.go
admin/provider_offer_force_recover.go
sql/migrations/startup/640_normalize_credential_health_status.sql
web/src/api/providers.ts
web/src/components/CredentialKeyField.vue
web/src/components/CredentialStatusBar.vue
web/src/utils/credentialStatus.ts
```

Some of those files were already modified before this session; inspect the diff before editing and retain unrelated hunks.

## Last verified commands

All were successful on September 3, 2026:

```bash
go test ./admin -run 'TestDeriveCredentialDisplayState|TestProviderConsoleMiddleware|TestRotateCredentialPrimaryKeyRejectsInvalidInputBeforeDB'
go test ./admin -run 'TestNonExistent'
cd web && npm run typecheck
git diff --check
```
