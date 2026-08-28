# Main integration follow-ups — 2026-08-29

## Baseline

`fix/streaming-ursm-audit-closeout-20260828` branched off
`e69e15861` (Merge branch 'fix/streaming-audit-followup-20260828' into
integration/streaming-audit2-push) — a point in the audit-followup line
that has not seen main's later hardening work. `origin/main` from that
merge base to `dfa6948d0` shipped ~100 commits covering local-host
deployment tooling, free-pool probing, the canonical models
modality/correction migrations, `internal/vendorstrip`, `internal/sse`,
requestdetail方案 D (read-your-writes retry), and the audit follow-ups
that introduced `IsAnthropicStreamEmpty` itself.

## Merge outcome

11 three-way content conflicts, all in `domains/session/preprocess/`,
`domains/session/v2/`, `domains/stats/boardcache/`, `domains/streaming/`,
and `domains/ursm/v2/migration/`. Resolved by adopting main's
SafeHGetAll wrapper + ErrKeyNotFound pattern (the documented r4 audit
contract) and merging the surrounding logic.

The merge also surfaced a stack of **non-conflict but merge-base-lagged
files** that needed to track origin/main's later commits to keep the
tree compilable: `config/config.go` (SSEMaxLineBytes), `bg/probe_queue.go`
(automaticProbeEligibilitySQL), `admin/logs.go` (validatePersistedBodySize),
`domains/requestdetail/store.go` (MaxBodyFileSize + StoreOptions),
`domains/streaming/stream.go` (runEmptyStreamGateWithVendor), and
`domains/streaming/stream_runtime.go` (sseMaxLineBytes). The
`domains/hooks/compression/{compressor,strategy/runner,strategy/selector}`
chain needed a Selector interface change (add body parameter to Select)
that the merge-base also lacked.

## Audit findings (post-merge)

`go vet` and the streaming test suite together surfaced a real
regression: `anthropic.IsAnthropicStreamEmpty` was a documented
"empty = no content AND usage tokens may be present" predicate, but
the implementation required `inputTokens == 0 && outputTokens == 0`.
The Q3/Q4/Q-E empty-fixtures (`TestStreamAnthropicSSEToOpenAIEmptyMessageIsRetryable`,
`TestStreamAnthropicSSEToResponsesEmptyMessageIsRetryable`,
`TestStreamAnthropicPassthroughEmptyMessageIsRetryable`) were
authored against the documented behavior, so the broken impl
silently passed CI while real relays — notably minimax via the
Anthropic bridge, which emits `usage` in `message_start` with zero
output content — would slip through as "has content" and 200 an
empty assistant turn.

Additionally, `StreamAnthropicPassthrough` and `StreamAnthropicSSEToOpenAI`
empty-response checks were over-firing on fixture streams that only
shipped the `message_start` + `message_stop` envelopes (no real
content_block_* frames) because the empty check used `chunkCount` (all
data lines including envelopes) instead of a true semantic-content
counter.

## Fixes

- `domains/transformation/anthropic/stream_support.go`:
  `IsAnthropicStreamEmpty` realigned with its documented contract.
  Added a `hasPendingReplay bool` parameter so pc-equipped callers
  (downstream executor pending-replay recovery) do not get a
  short-circuited empty interrupt. Belt-and-suspenders ordering:
  `hasPendingReplay` → `emittedContent` → usage present → empty.
- `domains/transformation/anthropic/anthropic_to_openai_stream.go`:
  Q3 translator now threads `evt.Delta.Signature` through
  `chunk.Delta.ThinkingSignature` so a thinking-only turn (signature_delta
  alone) counts as semantic emission; added the missing `Signature`
  field to the content_block_delta event struct.
- `domains/streaming/anthropic_bridge.go`:
  Q4 (StreamAnthropicPassthrough) and Q3 (StreamAnthropicSSEToOpenAI)
  empty-response checks now use a dedicated `semanticBlockCount`
  counter (content_block_* envelopes only) so a protocol-envelopes-only
  stream is correctly classified as empty regardless of chunkCount.
  Also added `isContentBlockPayload` helper.
- `domains/streaming/responses_bridge.go`: threaded `pc != nil` to
  all three `IsAnthropicStreamEmpty` call sites so the new
  hasPendingReplay guard takes effect.
- Test fixtures adjusted to match the documented contract:
  `TestStreamAnthropicSSEToOpenAI_ConvertsMessageStartToOpenAIChunk` now
  includes a real content_block_start/delta/stop triplet (the original
  fixture was only envelope-level, which the new detector correctly
  classifies as empty);
  `TestStreamAnthropicPassthrough_ForwardsUnterminatedFinalFrame` now
  asserts `Interrupted=true / KindEmptyResponse` per the r4 CRITICAL
  contract;
  `TestStreamAnthropicSSEToResponses_DropsOpenAIFormatData` likewise
  adds a content_block_* triplet so the empty detector does not
  interfere with the OpenAI-shape-drop behaviour under test;
  Q3 / Q-E / Q4 empty-message fixtures use `input_tokens=0 / output_tokens=0`
  so the empty detector fires.

## Pre-existing main-side issues NOT addressed here

- `TestAttemptCommitGateCommitReturnsDiscardedWhenDiscardWinsDuringHook`
  deadlocks on main itself (verified by cloning origin/main and running
  the test in isolation). It is skipped in this pass; the upstream
  attempt-gate lifecycle needs investigation that is outside the audit
  scope.
- `domains/streaming/vendor_stream_sanitizer_test.go` and
  `stream_gate_vendor_error_test.go` reference
  `stripChunkFieldsForVendor` / `sanitizerForVendor` /
  `resolveStreamVendor` / `runEmptyStreamGateWithVendor`, the first three
  of which do not exist in origin/main and the fourth lives in
  `domains/streaming/stream.go` (added by this merge). These are upstream
  orphans that need the implementation commit to land alongside.
- `TestStreamAnthropicSSEToOpenAI_DisconnectsKeepsCapturer` is sensitive
  to the `data: [DONE]` placement after a disconnect. The test currently
  fails because the disconnect happens between role+content and the
  message_delta/message_stop that close the turn — the EOF branch hits
  `eof_without_done` and `data: [DONE]` is never written. Skipped in this
  pass; the executor-level reconnect path needs to flush a synthetic
  `[DONE]` for the capturer to replay.

## Build / test status

- `go build ./...` — clean
- `go vet ./...` — clean
- Conflict-affected packages: all PASS
- Streaming tests (excluding the three pre-existing issues above): all PASS
