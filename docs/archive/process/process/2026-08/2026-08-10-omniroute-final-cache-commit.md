# OmniRoute Final Cache Commit Optimization

Date: 2026-08-10
Status: approved

## Problem

`SessionCompressor.Prepare` currently writes the session cache before the chat
handler applies its final request transforms:

1. NeverWorse fallback;
2. cached-tools restoration;
3. prompt-prefix stabilization;
4. optional prompt-cache parameter injection.

The cached outbound body can therefore differ from the body sent to the
supplier. The next turn may compute its delta against a body that was never
sent upstream. `updateCache` also rebuilds `SessionState` from a small subset of
fields, dropping strip, cut-marker, approval, and audit metadata.

OmniRoute treats the post-guard body as the authoritative result. This design
adopts that invariant without changing the existing compression algorithms.

## Scope

### In scope

- Add `SessionCompressor.CommitFinal` as the public final-cache seam.
- Keep `Prepare` behavior compatible for replay and non-handler callers.
- Have the chat handler call `CommitFinal` after all common request transforms
  and immediately before executor/provider dispatch.
- Recompute body-derived metadata from the final body.
- Refresh the caller's `PrepareResult` in place so request telemetry and cache
  metadata describe the same final body.
- Preserve the complete previous `SessionState` when writing a new turn.
- Keep V2-sourced requests from writing into V1.

### Out of scope

- Unifying Chat, Messages, Responses, and Gemini request pipelines.
- Moving sanitize session resolution or response restoration.
- Changing Session V2 schemas or rollout flags.
- Porting OmniRoute hard-budget, fidelity-gate, or context-editing engines.

## Data Flow

```text
client body
  -> sanitize
  -> SessionCompressor.Prepare (compatible intermediate cache write)
  -> NeverWorse
  -> restore cached tools
  -> prefix stabilize
  -> optional cache-control injection
  -> SessionCompressor.CommitFinal
  -> executor/provider
```

`CommitFinal` stores the exact client-protocol body entering the executor. The
executor may still perform candidate-specific IR conversion; that converted
body is intentionally not the next-turn delta baseline.

## State Rules

- Copy the previous `SessionState` before applying current-turn fields.
- Always replace body-derived fields: hash, message count, token estimate,
  message hashes, and compressed-prefix hash.
- Refresh `AuditedAt` because the final body remains downstream of sanitize.
- Replace `AlignmentMap` only when the current result contains one; otherwise
  retain the last compression map.
- Preserve strip counters, cut markers, tools hash, system prompt, approval
  status, audit scores, and optimization metadata.
- Copy slice fields to avoid aliasing mutable caller state.

## Test Seams

1. `SessionCompressor.CommitFinal` with a recording `SessionCacheBackend`.
2. Mock HTTP supplier receives the final body; cache L1 returns the same bytes.
3. Previous state survives `updateCache` while current body metadata changes.
4. V2-origin result does not write V1 during final commit.
5. Existing compression, session, sanitize, build, vet, and race suites pass.

## Rollback

Revert the implementation commit. `Prepare` retains its historical cache write,
so removing the final overwrite restores prior behavior without data migration.
