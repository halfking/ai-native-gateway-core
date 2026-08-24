# Dispatch Enqueue Lifecycle Order

## Problem

The new total queue and priority-affinity integration exposed a scheduler race:
after a request was sent to a credential forwarder channel, the forwarder could
emit `node_selected` before the producer emitted `node_enqueued`. This produced
non-monotonic live-action sequence numbers for one request and unstable SSE
timeline ordering.

## Fix

`credForwarder.handoffMu` now forms a short handoff boundary:

- `tryEnqueueCred` holds it from the credential queue send through the
  `node_enqueued` action and observation publication.
- The forwarder acquires/releases it immediately after receiving the request,
  before governor admission can emit `node_selected`.

The lock is not held during rate limiting, concurrency waits, retry scheduling,
or upstream execution.

## Verification

- `GOTOOLCHAIN=local go test ./domains/dispatch -run TestPipelineEmitsEnqueueActions -count=20`
- `GOTOOLCHAIN=local go test ./domains/dispatch ./bg ./admin -count=1`
- `GOTOOLCHAIN=local go test -race ./domains/dispatch -run 'TestPipelineEmitsEnqueueActions|TestPipelineQueueStats_RealPipelineTraffic' -count=3`
