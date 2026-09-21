# Gateway Merge & 4-Hour Audit Handoff — 2026-09-03

## Completed and pushed

- Remote `main` was fetched twice because it advanced during the first push attempt.
- The final non-force push succeeded. Both `origin/main` and `origin/fix/gateway-provider-survival-20260901` now point to `95f76b40e` (`merge: sync latest main`).
- The final merge included concurrent upstream work for provider credential diagnostics, failure records, attachment/data lifecycle administration, and recovery logic. No remote work was force-pushed or overwritten.
- Validation passed:
  - `go test ./...`
  - `go test -race ./db`
  - focused protocol and streaming tests for `internal/ir`, `domains/transformation`, `domains/hooks/compression`, `domains/streaming`, and `domains/streaming/executors`
  - pre-commit: `go vet`, SQL placeholder check, migration number check, migration down-file check.

## Fixed in the merged result

### Responses protocol and compression

1. Resolved compression merge conflicts in `domains/hooks/compression/compressor.go` and `domains/transformation/ctx_compress_test.go`.
2. Added safe native Responses compression in `domains/transformation/responses_compress.go`:
   - preserves top-level envelope fields;
   - honors normal threshold and aggressive recovery modes;
   - retains the latest input item;
   - keeps `function_call` and its matching `function_call_output` atomic;
   - fail-opens for malformed or unknown input shapes;
   - supports string input trimming only after threshold gating.
3. Native `/v1/responses` is excluded from the Chat/Anthropic session compressor because that component stores `messages`-based summaries and hashes. Native Responses remains input-shaped and uses the Responses-specific candidate-window and overflow-recovery compression path. This avoids persisting a Chat-shaped cache that disagrees with `ResponsesBodyBytes`.
4. Added and updated regression coverage in native Responses and transformation tests.

### IR and database lifecycle

1. Roleless Responses `input_text`, `text`, and `message` input now map to `user` text messages before generic typed-item fallback in `internal/ir/parse_responses.go`.
2. `DB.Stdlib()` and `DB.Close()` now share lifecycle synchronization. After shutdown, `Stdlib()`/`Pool()` return `nil`, and callers cannot race bridge construction with closure.
3. Added concurrent `Stdlib()`/`Close()` regression coverage and verified it with `go test -race ./db`.

## Audit findings requiring a separate design-and-implementation change

These findings were not folded into the merge because they alter persistence/data-retention semantics across multiple deployments and require a migration/reconciliation design.

### P0: attachment cleanup lacks a three-store consistency closure

The following stores can be changed independently:

- `request_logs*.attachments` JSONB metadata;
- `request_attachments` query/index records;
- attachment files on the mounted filesystem.

Current cleanup paths can delete only metadata or only files. Historical columnar partitions cannot be updated. Implement an append-only attachment cleanup ledger keyed by `(cleanup_run_id, request_id, attachment_hash)`, with explicit states such as `pending`, `file_deleted`, `metadata_tombstoned`, `failed`, plus retry/reconciliation.

Relevant areas:

- `domains/attachments/repository.go`
- `admin/data_lifecycle_attachments.go`
- `admin/data_lifecycle_attachments_filesystem.go`
- `installer/cmd/llm-gw-installer/embeddata/startup/629_audit_attachments_cleanup.sql`

### P1: retention terms are not consistently 8 hours

The data architecture is generally `hot heap + monthly columnar partition + promote job + unified view`, and update/delete-heavy paths correctly target hot tables. However, individual defaults differ: session bodies and some handoff data use 8h, candidate failures use 24h, and request-body retention has longer settings.

Define and expose an explicit table-level retention contract:

| table/view | hot retention | promotion cadence | partition mode | historical correction method |
|---|---:|---:|---|---|

Add startup/CI checks to verify that all deployed promote functions remain atomic: explicit columns, lock/claim batch, insert before delete, no swallowed migration errors.

### P1: provider error aggregates lack a session summary dimension

Credential-specific detail aggregation is now available, and raw failures can be queried by session. To make credential-quality diagnosis closed-loop without exploding aggregate cardinality, add either `distinct_session_count` to provider error details or a bounded detail-to-session association table.

### P2: Redis live-stream state durability needs an explicit tiering contract

Classify each Redis-backed live-stream key as either disposable UI acceleration or durable business state. Durable queue state should have a PostgreSQL/outbox source of truth and startup reconciliation; Redis should only accelerate it.

## Existing untracked files intentionally excluded from the pushed merge

The worktree still has unrelated/untracked documents, scripts, attachment fixtures, and exploratory source, including `internal/probe/`, `internal/ir/parse_responses_roleless_test.go`, deployment scripts, and `data/attachments/2026/09/`. They were preserved and not staged or published by this merge.

## Copyable follow-up prompts

### Attachment reconciliation design

> In `/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go`, inspect attachment persistence and lifecycle paths. Design and implement a durable reconciliation workflow spanning `request_logs*.attachments` JSONB, `request_attachments`, and mounted attachment files. Historical columnar partitions must remain append-only: use a cleanup/tombstone ledger rather than UPDATE. Include run IDs, retryable states, idempotency keys, admin visibility, and tests. Do not delete existing user files during tests.

### Retention contract audit

> Read-only audit this repository's hot-heap and columnar-partition retention model. Produce a per-table contract listing hot duration, promotion job/function, cadence, historical storage, and allowed mutation operations. Verify every promote function is atomic (claim/lock, insert before delete, explicit columns, no swallowed failure). Identify deployment source drift between SQL objects, migrations, and installer-embedded migrations with file:line evidence.

### Provider error/session observability

> In this gateway, trace candidate failures from upstream error through credential aggregation and admin API. Propose a bounded session-level observability extension that preserves credential/provider/model error aggregation while reporting distinct sessions and allowing session drill-down. Include schema migration, hot/columnar handling, backfill behavior, API DTO changes, and focused tests.

### Redis live-stream recovery

> Read-only trace live-stream queue and Redis metadata. Classify keys by durability requirement, identify restart/Redis-outage behavior, and propose a PostgreSQL/outbox-backed recovery design for durable items. Include idempotency, leases, metrics, reconciliation, and degradation behavior. Do not modify files.
