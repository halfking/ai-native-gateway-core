# Request Fact Phase 0 Contract Freeze

Status: frozen for Phase 0 implementation

This document freezes the request-fact vocabulary before any production writer,
schema, outbox, local-file archive, or deployment change. `request_logs` remains
the request-audit main fact. Session V2 remains a shadow projection.

## Version Contract

`internal/requestfact` owns four independently versioned V1 values:

| Version | Scope | Rule |
| --- | --- | --- |
| `envelope_version` | `RequestArchiveEnvelope` outer wire format | Unknown required version fails closed. |
| `payload_version` | `CanonicalRequestFact` field contract | Unknown required version fails closed. |
| `codec_version` | JSON/hash encoding semantics | Unknown required version fails closed. |
| `projection_event_version` | Future body-free projection event contract | Frozen now; no event is emitted in Phase 0. |

These versions are unrelated to Session V2's `"$ir":1` marker. That marker is a
`v2.Message` content-shape discriminator and remains owned by the existing
`IRMessagesToV2` / `IRMessagesFromV2` adapter.

The codec accepts unknown additive JSON fields. It rejects unknown required
versions, malformed core documents, `null` JSON documents, and a payload hash
mismatch. `payload_sha256` is SHA-256 over canonical JSON for the V1-known
`payload` fields only; it excludes envelope metadata, the hash itself, and
unknown additive fields. Optional body digests, when supplied, are SHA-256 over
the canonical JSON document stored in their corresponding `raw_body` field, so
whitespace and object-member order do not create false corruption reports. A
digest mismatch still fails validation.

## Ownership and Field Matrix

| Field group | Source path | Required class | Canonical fact handling | Current/future consumer | Failure behavior |
| --- | --- | --- | --- | --- | --- |
| Identity | transport/session/request metadata | required | tenant, request; session/turn/task/parent when known | request_logs, V2, durable, stats, Redis metadata | terminal error |
| Lifecycle | SessionContext and telemetry terminal entry | required | terminal status, created/completed, optional start/deadline/error | request_logs, V2, stats | terminal error for missing terminal fact |
| Client routing | TransportContext and selected route | required | client protocol/model, optional canonical/upstream/provider/credential path | request_logs, V2, durable, stats | terminal error for client protocol/model |
| Client raw request | inbound transport body | raw-preserved and required | `request.raw_body` valid non-null JSON | request_logs bodies, archive, durable normalized subset | retry/DLQ later; never `{}` fallback |
| Client canonical IR | `internal/ir` parser result | raw-preserved and required | explicit `request.canonical_ir` JSON document | V2 adapter, archive | retry/DLQ later; never empty JSON |
| Client extensions | transport extension extractor | optional/raw-preserved | request/global extension documents | archive/V2 conversion context | warning only when explicitly omitted |
| Upstream request | serializer output and outbound IR | optional/raw-preserved | raw body, canonical IR, extensions | request_logs bodies, archive, V2 | warning or retry according to later projection policy |
| Response and stream | response IR, raw response, chunks and summary | optional/raw-preserved | response documents and stream JSON | request_logs, V2, stats | error only when a requested core projection needs it |
| Content richness | IR messages/tools/results/thinking/signature/provider raw | raw-preserved | remains in canonical IR document | Session V2 only through existing dual-shape adapter | loss must be structured warning, never silent |
| Usage/timeline | telemetry/session finalization | derived | usage and T0-T9/TTFT/latency | request_logs, stats, V2 | warning if optional derivative missing |
| Attachments/governance | SessionContext/extension metadata | optional/raw-preserved | JSON documents | archive and later V2 projection | warning if omitted |
| Body hashes | codec/input integrity calculation | derived | optional SHA-256 body hashes plus required payload hash | archive, outbox, verification | terminal error if supplied hash is malformed |
| Archive state | archive coordinator, not request execution | derived | active/terminal-pending/cleanup-pending | future local registry/file archive | invalid state is terminal codec error |
| Redis metadata | projection adapter | derived | excluded from complete payload persistence | queue/board mirrors | Redis loss never blocks recovery |

## Field-Level Contract Matrix

The following JSON paths are the V1 contract surface. A path not listed here is
additive and is ignored by a V1 reader.

| JSON path | Type | Class | Source/owner | Hash participation |
| --- | --- | --- | --- | --- |
| `payload.identity.tenant_id` | string | required | tenant context / fact owner | payload |
| `payload.identity.request_id` | string | required | request context / fact owner | payload |
| `payload.identity.session_id` | string | optional | session context | payload |
| `payload.identity.turn_id` | string | optional | session turn context | payload |
| `payload.identity.task_id` | string | optional | durable execution correlation | payload |
| `payload.identity.parent_request_id` | string | optional | continuation context | payload |
| `payload.lifecycle.status` | string | required | terminal telemetry state | payload |
| `payload.lifecycle.success` | boolean | optional | terminal result | payload |
| `payload.lifecycle.error_kind` | string | optional | error classifier | payload |
| `payload.lifecycle.error_code` | string | optional | provider/gateway error | payload |
| `payload.lifecycle.deadline_at` | timestamp | optional | request policy | payload |
| `payload.lifecycle.created_at` | timestamp | required | request start | payload |
| `payload.lifecycle.started_at` | timestamp | optional | dispatch start | payload |
| `payload.lifecycle.completed_at` | timestamp | required | terminalization | payload |
| `payload.routing.*` | scalar routing fields | required/optional | transport and selected route | payload |
| `payload.request.raw_body` | JSON document | required/raw-preserved | inbound transport body | body digest + payload |
| `payload.request.canonical_ir` | JSON document | required/raw-preserved | `internal/ir` conversion | payload |
| `payload.request.protocol` | string | optional | transport protocol | payload |
| `payload.request.extensions` | JSON document | optional/raw-preserved | transport extension bag | payload |
| `payload.upstream.raw_body` | JSON document | optional/raw-preserved | outbound serializer | body digest + payload |
| `payload.upstream.canonical_ir` | JSON document | optional/raw-preserved | outbound IR | payload |
| `payload.upstream.protocol` | string | optional | outbound route | payload |
| `payload.upstream.extensions` | JSON document | optional/raw-preserved | outbound extension bag | payload |
| `payload.response.*` | JSON documents | optional/raw-preserved | response parser/stream capture | response body digest + payload |
| `payload.usage.*` | scalar usage fields | optional/derived | terminal telemetry | payload |
| `payload.timeline.*` | timestamps/durations | optional/derived | request lifecycle instrumentation | payload |
| `payload.attachments` | JSON document | optional/raw-preserved | attachment pipeline | payload |
| `payload.extensions` | JSON document | optional/raw-preserved | governance/compression/URSM metadata | payload |
| `payload.warnings[]` | warning object | loss-reported | codec/projection adapter | payload |
| `payload.integrity.*` | SHA-256 hex | derived | codec/input integrity | payload |
| `archive.state` | enum | derived | future archive coordinator | envelope |
| `archive.attempts` | non-negative integer | derived | future archive coordinator | envelope |
| `archive.last_error` | string | optional | future archive coordinator | envelope |
| `archive.persisted_at` | timestamp | optional | future archive coordinator | envelope |
| `payload_sha256` | SHA-256 hex | required | codec | self-verifying |
| `extensions` | JSON document | optional/additive | future envelope metadata | excluded in V1 |

`payload.routing.*`, `payload.response.*`, `payload.usage.*`, and
`payload.timeline.*` use the exact member names declared in
`internal/requestfact/types.go`; the wildcard notation above groups fields only
for readability and does not authorize renaming or omission.

## Compatibility Boundaries


- **IR:** Phase 0 stores explicit JSON documents. Phase 1 must construct them
  from the existing `internal/ir` parser/serializer path; it must not create a
  second protocol parser.
- **Session V2:** a future projection must call the existing dual-shape adapter,
  preserving legacy text content and `$ir` envelopes for rich content. Phase 0
  does not change `session_turns`, `session_bodies`, or `sessionv2mirror`.
- **Durable execution:** a future adapter derives
  `streaming.DurableRequestSnapshotV1` from the canonical fact and passes its
  bytes to `durable.NewTask.Snapshot`. `durable` remains responsible for
  task-bound encryption, leases, fencing, and cross-restart execution recovery.
- **Local archive:** a future `LocalRequestArchive` owns tmp/fsync/rename,
  quarantine, restart scan, and cleanup after database acknowledgement. This
  contract does not define an encryption AAD domain or file retention policy.

## Non-Goals and Migration Inventory

Phase 0 changes no production write path, database schema, migration, installer
embed/copy list, deploy manifest, telemetry hook, Session V2 writer, durable
task store, Redis queue, or statistics worker.

Migrations `603_repair_request_logs_schema_consistency` and
`604_repair_request_logs_bodies_tenant_id` exist in the startup migration tree,
but this checkout does not establish installer or deployment registration. They
remain pending preflight under
`docs/standards/database-change-and-real-verification.md`; Phase 0 neither
executes nor registers them.
