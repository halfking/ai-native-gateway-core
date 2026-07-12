# Route Incident Diagnosis Design

## Goal

Add an incident-driven diagnosis workflow to the dashboard's live request swim lanes. A lane shows a diagnostic entry only when an underlying route has a persistent failure episode. The entry disappears automatically only after verified recovery.

The work is delivered in two phases:

1. Detect, persist, present, and investigate route incidents without changing live routing state.
2. Add constrained tests, guarded recovery actions, and a sanitized evidence export.

## Scope And Terminology

A route incident identifies one operational route:

`tenant + endpoint/protocol + canonical or outbound model + provider + credential`

The swim lane is a presentation aggregate. It can represent one or more route incidents when grouped by vendor, provider, or model. It never decides incident status itself.

The first release is super-admin operated. Tenant-level visibility can be enabled only after route identity and evidence queries have a proven tenant isolation model. Every query still binds a tenant scope before assembling evidence. Phase-one super-admin responses do not expose tenant identity unless the view explicitly requires it; a cross-tenant resource is treated as not found to non-super-admin callers.

## Incident Lifecycle

The backend owns all counters and transitions. The frontend consumes the current state.

```text
healthy -- 3 consecutive terminal failures --> active
active -- terminal success --> recovering (1/5)
recovering -- terminal success --> recovering (n/5)
recovering -- 5 consecutive terminal successes --> recovered
active|recovering -- terminal failure --> active
```

Rules:

- A successful terminal request interrupts a failure streak.
- After an incident is active, one to four consecutive successes do not close it.
- Any failure during recovery resets the recovery streak and keeps the diagnostic entry visible.
- On the fifth consecutive success, the incident is marked recovered and its active lane indicator is removed immediately.
- Thresholds default to three failures and five successes. They are service configuration, not frontend constants.
- Only terminal requests that reached an upstream route qualify. Client cancellation, input validation, authorization, and other non-routing failures are excluded.

## Data Model

Create a dedicated current aggregate and an append-only evidence trail. Do not add incident state to the high-volume request log tables.

`route_incidents` contains the current incident state, `tenant_id NOT NULL`, route key, first/last timestamps, last request and error summary, failure/recovery streaks, aggregate counts, and resolution metadata. Its active partial unique index includes `tenant_id` and every route-key component, so a later tenant-facing release does not need an identity migration.

`route_incident_events` contains immutable opened, failure-observed, recovery-progress, recovered, diagnostic-run, and operator-action events. Each event may reference one request ID and stores only sanitized structured evidence.

One partial unique index permits only one active or recovering incident per route key. State transition SQL must be transactional and idempotent. Each event has a unique `(incident_id, request_id, terminal_status)` key. A transition locks the route's active aggregate with `SELECT ... FOR UPDATE`, applies terminal results in persisted request completion order, and ignores duplicate or stale events. This serializes concurrent success/failure arrivals and prevents skipped or double-counted streaks.

## Event Processing And SSE

Use the request-log persisted callback, not the pre-persistence emit callback. A bounded asynchronous observer receives terminal request results, deduplicates by request ID and terminal status, and performs the incident transition after the request record is durable. The observer has a fixed non-blocking queue, records an overflow metric and warning when full, and retries failed transitions with bounded backoff; it must never delay client traffic.

After a committed transition, publish an `incident_update` envelope through `admin.LiveStreamSSEHub`, alongside its existing request envelopes. The minimum contract is:

```json
{
  "type": "incident_update",
  "incident_id": "...",
  "state": "active|recovering|recovered",
  "failure_streak": 3,
  "recovery_streak": 0,
  "visible": true,
  "affected_lanes": [{"dimension": "provider", "value": "provider-a"}],
  "last_error": {"kind": "rate_limited", "stage": "upstream"}
}
```

`visible` is false only for a recovered incident. The envelope carries only display identity and sanitized status; full request bodies, credentials, tenant identifiers, and diagnostic detail never enter SSE.

The dashboard merges those updates into lane indicators. A lane shows a diagnostic icon and active count for active/recovering incidents. During recovery, it displays `Recovery n/5`. An update with `state: recovered` and `visible: false` removes the indicator immediately without polling or a page refresh. Recovered records remain retained for timeline and audit queries. The aggregate "Other" lane is not actionable because it has no stable route identity; its control is disabled with an explanatory tooltip and never opens the drawer.

## Read-Only Diagnosis API

Phase one adds authenticated, super-admin APIs:

```text
GET /api/admin/route-incidents
GET /api/admin/route-incidents/{id}
GET /api/admin/route-incidents/{id}/events
GET /api/admin/route-incidents/{id}/timeline?from=&to=
```

The detail response includes current route and routing decision, failure counts and classification, evidence-backed commonality findings, associated request IDs, recovery progress, 24-hour five-minute aggregates, and a read-only resource snapshot where available.

Findings must identify their evidence sample count, interval, and request IDs. Insufficient data is explicit; the API does not infer a root cause from missing evidence.

## Dashboard Experience

`SwimLane` gains a keyboard-accessible diagnose button beside failure stats. `LiveRequestStreamV2` forwards a diagnostic target to both dashboard consumers. A new right-side diagnostic drawer is aggregate-first and preserves the current request-log drawer for per-request drilling.

Drawer sections, in order:

1. Status header: affected route, state, failure streak, recovery progress, first/last failure, and refresh/export controls.
2. Current route: protocol, model mapping, provider, credential identifier, routing decision and retry path.
3. Evidence summary: error kinds, failure stage, latency change, correlated dimensions, and sample request links.
4. Last 24 hours: five-minute request, error, latency, and recovery timeline; show insufficient-data state when needed.
5. Request and transformation comparison: client, normalized, outbound, upstream, and gateway response summaries using field-path differences and redacted previews only.
6. Resource snapshot: slot and concurrency state, circuit/quota/availability state, and action history.
7. Logs and requests: time-correlated records and existing request-detail drill-down.

Desktop uses the existing right drawer treatment with a fixed header and scrollable body. Mobile uses the full viewport. The drawer has dialog semantics, focus trapping, Escape close, focus restoration, and reduced-motion support.

## Phase Two Diagnostic Runs And Actions

Create a persisted `DiagnosticRun` service with immutable, sanitized results. It offers a fixed action/test allowlist:

- `direct_upstream_test`: provider/credential/model selected from stored configuration only.
- `through_gateway_test`: constrained production-path diagnostic mode with a server-owned safe prompt, timeout, token/cost cap, response cap, rate limit, and diagnostic tag.
- `reprobe`, `release_slot`, `reset_slots`, `reset_availability`, and `recover`.

No action accepts an arbitrary URL, raw request body, shell command, or SQL. Existing direct probe and reset endpoints are supporting primitives, not direct UI dependencies.

Mutating actions require super-admin authorization, explicit reason, confirmation token, idempotency key, stale-state/version check, pre-action snapshot, post-action verification, and an immutable `routing_audit_log` entry with the authenticated actor. The entry records timestamp, actor, action, sanitized route key, reason, confirmation-token hash, before state, after state, and diagnostic-run ID. The audit row is committed with the action state transition. Recovery means requesting a controlled re-probe and only re-enabling a route after verified state, never bypassing health checks.

## Evidence Export And Redaction

Evidence export is generated only from a completed `DiagnosticRun` and a fixed allowlist DTO. It contains timestamps, classifications, status codes, latency, route decisions, aggregate resource counts, sanitized logs, and an integrity checksum.

Never export credentials, authorization headers, cookies, full request/response bodies, client IP, user agent, attachment paths, raw upstream errors, session titles/identifiers, or raw slot holders. Export size and retention are bounded; downloads are short-lived and every export is audited.

## Security And Tenant Boundaries

- New APIs use super-admin middleware in phase one.
- All resource and request lookup queries are parameterized and tenant-filtered before any evidence is assembled.
- Cross-tenant resources receive a uniform not-found response.
- Diagnostic URLs are always derived from stored provider configuration and validated against scheme/host policy before use.
- Diagnostic tests are rate-limited and cannot replay a customer request by default.
- API errors are sanitized; full underlying errors are logged server-side only.

## Delivery Plan

Wave 0: define the domain contract, state machine, DTOs, thresholds, permissions, and redaction policy.

Wave 1 in parallel: route incident schema/store; terminal request observer; read-only incident APIs; live-stream event contract; lifecycle, migration, authorization, and SSE tests.

Wave 2 in parallel: swim-lane entry; diagnostic drawer; API client/i18n; responsive/accessibility work; frontend unit tests and browser validation.

Wave 3: DiagnosticRun, constrained direct/gateway tests, guarded action commands, evidence export, security tests, and runbook.

## Acceptance Criteria

- Three qualifying consecutive failures create one active incident and display the lane entry.
- One through four consecutive successes keep the entry visible and report recovery progress.
- Five consecutive successes recover the incident and hide the entry without a page refresh.
- A recovery-period failure returns the incident to active and resets recovery progress.
- Lane aggregation never changes the route-level lifecycle calculation.
- Concurrent terminal events for one route produce one deterministic, deduplicated transition sequence.
- `incident_update` with `state: recovered` and `visible: false` removes the lane entry immediately; the recovered record remains queryable.
- The `Other` aggregate lane never opens diagnosis and explains why the control is unavailable.
- All diagnosis detail is evidence-backed, tenant-safe, and redacted.
- No phase-two action can alter routing state without authorization, confirmation, audit, and verification.
- Desktop and mobile browser tests cover trigger, recovery hiding, drawer navigation, error state, and request drill-down.
