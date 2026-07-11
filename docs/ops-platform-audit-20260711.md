# Ops Platform Audit: 2026-07-11

## Follow-up Audit: Recovery And Observability Integration

The follow-up audit was performed after the ops-platform fixes landed on
`origin/main` and before pushing the recovery/observability integration.

- Recovery uses the existing `credentialstate.StateCacheResetter`, breaker
  resetter, and `CredentialProbeV2`; no second credential-state authority was
  introduced.
- Request-log metadata now reuses `telemetry.ExtractClientIP`,
  `ExtractForwardedFor`, `ExtractAgentName`, `ExtractAgentType`, and
  `MaskAPIKey`. The observability hook only owns persistence mapping and the
  512-byte forwarded-chain bound.
- Vendor request-field stripping remains centralized through the existing
  vendor-specific helpers and the dispatcher; callers do not duplicate JSON
  mutation logic.
- Auto-update keeps `/api/admin/releases/*` as the single canonical route.

## Scope

Audited the Phase 1-8 operations platform after merging the branch
`audit/session-cache-correctness` into `origin/main` at `bf77da226`.

The audit covered the five Go modules, startup migrations 371-377, Echo route
registration, the shared frontend API client, the five operations views, and
the integration-test packages. Existing unrelated scripts, generated test
data, and reports were excluded from the commit.

## Findings And Fixes

- High: Operations Echo routes were previously vulnerable to inconsistent
  authentication wrapping. The merged main path now mounts the Echo handler
  below `/api/admin/` using the existing `admin.AdminMiddleware`; module code
  derives the authenticated operator from `admin.GetAuthContext`.
- High: VibeCoding project/session/review writes could trust a client-supplied
  tenant ID. Project, session, and review creation now derive tenant identity
  through `admin.EffectiveTenantID`; review creation also requires an auth
  context.
- High: The frontend API client and views used stale field names and route
  shapes. They now use the backend's snake_case contracts and shared response
  normalization in `web/src/api/ops.ts`.
- High: Integration-test cleanup used obsolete table names and hard-coded
  foreign keys. Cleanup now targets the tables from migrations 371-377 and
  creates parent rows before dependent rows.
- Medium: Remote log collection accepted arbitrary `tail -n` input. The
  command executor validates an integer range before invoking `tail`.
- Medium: Fault script execution is disabled unless
  `LLM_GATEWAY_FAULT_SCRIPT_DIR` is configured and the resolved script path is
  inside that directory.
- Medium: Auto-update routing no longer registers the same handler under
  multiple prefixes. The canonical admin route is `/api/admin/releases/*`.
- Medium: The first recovery/observability implementation duplicated request
  metadata extraction already provided by `telemetry/request_metadata.go`.
  The follow-up audit removed that duplication and delegates to the shared
  package.
- Medium: Recovery state transitions are now ordered as cache reset, breaker
  reset, then authoritative real probe; the credential is kept out of routing
  until the probe succeeds.
- Medium: Shared agent classification was missing Postman/Insomnia as API
  clients. The canonical `telemetry.ExtractAgentType` helper now covers both;
  observability callers continue to delegate to it.

## Verification

Passed on the merged main source tree:

- `go build ./cmd/gateway ./licensing/... ./fault/... ./autoupdate/... ./center/... ./vibecoding/... ./db/...`
- `go vet ./cmd/gateway ./licensing/... ./fault/... ./autoupdate/... ./center/... ./vibecoding/... ./db/...`
- `go test -tags=integration ./licensing ./fault ./autoupdate ./center ./vibecoding -run '^$'`

The frontend build passed in the original feature worktree after the UI
changes. The isolated main-sync worktree did not contain `web/node_modules`,
so a second `npm run build` there failed before Vite startup with
`Cannot find module 'vite'`.

## Remaining Risks

- The repository pre-commit gate currently fails on pre-existing duplicate
  migration numbers `341`, `360`, and `361`, and on a large existing global
  `vue-tsc` error set. These are outside this audit's module scope and were
  not bypassed by altering the unrelated files.
- Full integration execution requires a configured PostgreSQL instance and
  the startup migrations applied in order. The verification above compiles
  the integration suites without connecting to a database.
- Browser-level verification remains pending because no running authenticated
  web/API environment was available in the isolated sync worktree.
- The local recovery UI smoke evidence was produced in a separate worktree and
  is intentionally not committed as source code or test data.

## Commits

- Feature branch fix: `cc1e50899`
- Main merge: `bf77da226`
- Follow-up recovery/observability integration: rebased on latest
  `origin/main` before push.
