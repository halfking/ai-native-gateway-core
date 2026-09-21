# 2026-08-28 Compression and response-integrity fixes

## Scope

This change closes several independent reliability gaps found during the compression, protocol-conversion, session-cache, and model-quality audit.

## Behavior changes

- **Cross-protocol non-stream responses:** a failed OpenAI ↔ Anthropic or Anthropic → Responses conversion now returns a classified `conversion_error`; the gateway no longer writes a successful response using the upstream's incompatible JSON schema.
- **Request serialization:** a legacy IR serialization failure preserves the original outbound request body instead of replacing it with a nil or partial serialization result.
- **Forced compression:** `X-Gw-Force-Compression: true` now reaches the executor's final outbound-body strategy runner. It bypasses only the automatic threshold and still requires the relevant strategy runner and mode to be enabled. The runner continues to reject non-reducing results.
- **Session cache updates:** `SessionCache.Update` serializes a session-scoped read-modify-write operation through bounded striped locks. L1 cache state and bodies are copied at the cache boundary; callers cannot mutate cached data through retained slices. Session audit and approval updates preserve the existing outbound body.
- **Detached durable streams:** streams explicitly authorized to outlive client cancellation have a two-hour wall-clock cap for both dispatch waiting and upstream execution. Ordinary streams still inherit client cancellation.
- **Recovery coordinator:** `RecoveryDeps.MaxRetries` is retained for source compatibility but intentionally ignored. Retry ownership remains with the executor's upstream-attempt budget, preventing an independent compression retry multiplier.
- **Model quality benchmark:** node-level checks now use a shared in-flight gate; score-history/latest persistence is transactional and guards against stale latest writes. HTTP transports bound per-host connections and error-body reads.

## Validation

Run focused coverage:

```bash
go test ./domains/streaming/executors ./domains/hooks/compression ./domains/hooks/sessionaudit ./domains/modelquality ./bg ./cmd/gateway -count=1
go test -race ./domains/hooks/compression ./domains/hooks/sessionaudit ./domains/modelquality -count=1
```

Run the repository suite before merging:

```bash
go test ./...
git diff --check
```

## Operator notes

`X-Gw-Force-Compression` is not an override for disabled compression policies. Enable and configure the appropriate strategy runner first; the header only asks that an enabled pre-request policy be considered even when its normal utilization threshold has not been crossed.
