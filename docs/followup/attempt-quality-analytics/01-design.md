# Attempt-Level Supplier Quality Analytics

## Status

Proposed after the 2026-08-26 code audit. This document defines the missing
analytics bridge; it does not claim that the bridge is implemented.

## Goal

Preserve every upstream attempt made for one client request and aggregate those
attempts into supplier, credential, and model quality metrics. A final request
success must not erase a failed earlier attempt on another node.

## Non-Goals

- Do not duplicate request or response bodies into attempt analytics.
- Do not replace the existing request journey timeline or final request logs.
- Do not treat final-request success rate as the same metric as attempt success
  rate.

## Existing Evidence

- RequestJourney persists content-free events keyed by tenant, request, and
  sequence in `request_state_transitions`; see
  `sql/migrations/startup/530_request_journey_contract.sql`.
- Dispatch emits `attempt_started`, `attempt_failed`, `attempt_succeeded`,
  `retry_scheduled`, `node_switched`, and `model_switched`; see
  `domains/dispatch/{journey.go,forwarder.go,failover.go}`.
- `request_logs_hot` has one row per `request_id`; see
  `sql/migrations/startup/455_request_id_unique_for_hot_tables.sql`.
- Provider profiles currently analyze `request_logs_hot`; see
  `domains/providerprofile/adapters.go`.
- Existing single-request evidence remains available through
  `admin/request_journey.go`.

## Proposed Read Model

Create an attempt-level SQL read model from `request_state_transitions`.
Its grain is `tenant_id + request_id + attempt_id`; no request content is
selected or copied.

For each attempt, derive these fields from the ordered event stream:

| Field | Source | Rule |
|---|---|---|
| tenant_id, request_id, attempt_id, attempt_no | attempt events | Required identity |
| provider_id, credential_id, model | attempt events | Required routing dimensions |
| started_at | `attempt_started` | Required |
| first_byte_at | `first_byte` | Nullable |
| ended_at, outcome, error_kind, http_status | attempt terminal event | Required after terminal |
| retry_scheduled | `retry_scheduled` after this attempt | Boolean |
| node_switched, model_switched | switch events after this attempt | Boolean plus reason |
| observation_status | all related journey events | Exclude or flag degraded rows |

Use a view or a daily/hourly aggregate table according to measured query cost.
The initial implementation should use an explicit-column SQL view and a
repository query; do not use `SELECT *`.

## Metrics Contract

Expose two separate metric families.

| Family | Meaning | Primary source |
|---|---|---|
| final request metrics | One result per client request | request_logs_hot / usage ledger |
| attempt quality metrics | One result per upstream node attempt | request journey attempt read model |

Attempt quality metrics, grouped by provider, credential, and model, must
include:

- attempt_total, attempt_success, attempt_failure, attempt_canceled
- first_attempt_success_rate
- attempt_success_rate
- retry_rate and same_node_retry_rate
- node_switch_out_rate and node_switch_in_success_rate
- model_switch_rate
- error counts by error_kind and HTTP status
- time to first byte and attempt latency percentiles
- observation_degraded_count, reported separately from failures

Final request metrics retain final_request_success_rate and token/cost values.
Dashboards must label the two families explicitly.

## Implementation Slices

### LP1: Attempt fact query and repository

Input: existing RequestJourney events in `request_state_transitions`.

Output: a content-free `AttemptFact` query/repository with explicit fields and
tenant filtering.

Acceptance criteria:

- A request with two attempts returns two facts with distinct attempt IDs and
  attempt numbers 1 and 2.
- A failed first node and successful second node preserve each node's own
  provider, credential, model, outcome, and error kind.
- Degraded observations are identifiable and are not silently counted as clean
  failures.
- The query does not select request or response bodies.

### LP2: Attempt-quality aggregation

Input: LP1 facts over a bounded time range.

Output: provider/credential/model aggregates and a scheduled updater or
on-demand repository method.

Acceptance criteria:

- A failed attempt followed by final success contributes one failure to its
  first node and one success to its second node.
- Final request success remains one request-level success, not two successes.
- Aggregates separate retry, node switch, and model switch rates.
- Empty and degraded windows return explicit zero/degraded semantics.

### LP3: Quality API and dashboard contract

Input: LP2 aggregates plus existing final request statistics.

Output: additive API fields or endpoint that expose both metric families.

Acceptance criteria:

- Provider and model filters are honored; do not silently ignore model scope.
- Responses identify the time window, sample size, and observation status.
- Existing quality API behavior remains compatible unless a versioned endpoint
  is introduced.
- UI work is separately verified in a browser if any user-visible page changes.

## Test Plan

Add an integration test that drives the dispatch pipeline through:

1. node A fails before first byte;
2. the same request switches to node B and succeeds;
3. a model-switch variant succeeds on model B;
4. the durable journey and attempt aggregate are queried.

Assert that attempt and final-request figures differ as designed. Add unit tests
for aggregation rates, degraded observations, and tenant isolation. Run the
existing dispatch/requestjourney/streamretry suites before broader integration
verification.

## Risks And Decisions

- RequestJourney is asynchronous and may be degraded. Quality outputs must
  expose completeness instead of silently treating missing observations as zero.
- Existing profile data is credential-grain; model-grain output requires the
  attempt source rather than the current profile table alone.
- SQL must use `request_state_transitions` as the schema truth and explicit
  columns. Any migration must follow the repository's migration and schema
  verification conventions.

## References

- `domains/requestjourney/contract.go`
- `domains/requestjourney/repository.go`
- `domains/dispatch/journey_test.go`
- `domains/providerprofile/adapters.go`
- `internal/handlers/quality_handler.go`
- `docs/03-design/02-feature-design/会话优化v4/05-会话分析与模型选择设计.md`
