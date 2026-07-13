# Route Incident Diagnosis — Phase 1 (Read-only)

**Session**: 2026-07-13 (续接)
**Worktree**: `/Users/xutaohuang/.local/share/opencode/worktree/5042a7829b25ab16d5edb8e3e4f47db47d882ffb/hidden-otter`
**Spec**: `docs/superpowers/specs/2026-07-13-route-incident-diagnosis-design.md`
**Mode**: AFK (no human in the loop for this continuation)
**Status**: needs-info → ready-for-agent → in-progress

## Scope of this session

Wave 0 + Wave 1 + Wave 2 UI only. Phase 2 (DiagnosticRun / mutating actions / evidence export) is explicitly **out of scope** until the read-only path is verified.

## Invariants (do not violate)

1. Client key errors / client cancellations / validation failures / auth errors **do not** count toward the 3-consecutive-failure trigger.
2. Route identity is `tenant + endpoint/protocol + canonical_or_outbound model + provider + credential`.
3. Trigger threshold = 3 consecutive terminal failures; recovery requires 5 consecutive terminal successes. Any failure during recovery resets progress.
4. The swim lane is an aggregate. Incident state is owned by the backend per-route key.
5. Cross-tenant resources return uniform 404 (no existence leak).
6. Phase 1 is **read-only** for the dashboard: no direct/gateway tests, no recovery action, no evidence export, no audit endpoints.
7. The "Other" aggregate lane (no stable route identity) MUST disable its diagnostic control with an explanatory tooltip.
8. Diagnostic SSE envelopes must NEVER include credentials, full bodies, tenant ids, or auth headers.

## Plan

### Wave 0 — domain contract (foundations)
- [ ] `domains/routeincident/state.go` — state machine + thresholds
- [ ] `domains/routeincident/types.go` — DTOs (RouteKey, Incident, Event, Finding, TimelinePoint)
- [ ] `domains/routeincident/redact.go` — sanitized evidence helpers
- [ ] `sql/migrations/startup/389_route_incidents.sql` (+ .down.sql) — current aggregate table
- [ ] `sql/migrations/startup/390_route_incident_events.sql` (+ .down.sql) — immutable event trail
- [ ] `db/db.go` — invoke new migrations at startup

### Wave 1 — backend implementation
- [ ] `domains/routeincident/store.go` — idempotent transactional transitions (`SELECT ... FOR UPDATE`)
- [ ] `domains/routeincident/observer.go` — bounded queue worker hooked to `telemetry.SetOnRequestLogPersisted`
- [ ] `admin/route_incidents.go` — read-only API (list/detail/events/timeline)
- [ ] `admin/handler.go` — register routes under `h.superAdmin(...)`
- [ ] `admin/live_stream_sse.go` — publish `incident_update` envelope
- [ ] `cmd/gateway/main.go` — wire observer + hub
- [ ] `domains/routeincident/store_test.go` + `observer_test.go`

### Wave 2 — dashboard UI
- [ ] `web/src/types/routeIncident.ts` — types
- [ ] `web/src/api/routeIncidents.ts` — typed client
- [ ] `web/src/composables/useRouteIncidents.ts` — SSE state merge
- [ ] `web/src/components/SwimLane.vue` — diagnose button + "Other" disabled tooltip
- [ ] `web/src/components/LiveRequestStreamV2.vue` — forward `diagnose` event
- [ ] `web/src/components/RouteIncidentDrawer.vue` — sections 1-7 (read-only)
- [ ] `web/src/i18n/{zh,en}.ts` — keys
- [ ] `web/src/test/routeIncident*.spec.ts` — unit tests
- [ ] `web/src/components/SwimLane.vue` mobile responsive (full-viewport drawer)

### Verification
- [ ] `go build ./...` + `go test ./domains/routeincident/... ./admin/...`
- [ ] `npm run build` + `npm run test`
- [ ] browser-use / playwright desktop + mobile screenshots
  - `ui-verify-route-incident-active-{timestamp}.png`
  - `ui-verify-route-incident-recovering-{timestamp}.png`
  - `ui-verify-route-incident-recovered-hidden-{timestamp}.png`
  - `ui-verify-route-incident-other-disabled-{timestamp}.png`
  - `ui-verify-route-incident-drawer-empty-{timestamp}.png`
  - `ui-verify-route-incident-drawer-loaded-{timestamp}.png`
  - `ui-verify-route-incident-drawer-insufficient-data-{timestamp}.png`
  - `ui-verify-route-incident-mobile-drawer-{timestamp}.png`

## Notes

- Reference `domains/hooks/observability/telemetry/client.go` `SetOnRequestLogPersisted` for the hook. Note that the live stream currently uses `SetOnRequestLogEmitted` (pre-DB). The incident observer MUST use `SetOnRequestLogPersisted` to guarantee state is computed from a durable row (per spec §"Event Processing And SSE").
- The `Other` lane is identified in `useSwimLane`/the `SwimLane.isOthers` flag; the diagnostic control must be disabled there.
- Existing `live_stream_sse.go` snapshot computation also touches `request_logs_with_current_month` — keep the incident observer on a separate goroutine so it never blocks the SSE broadcast path.
