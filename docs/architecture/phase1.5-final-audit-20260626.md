# Phase 1.5 Final Audit Report (2026-06-26)

> **Audit Goal**: Verify that all old code under `_to-be-deprecated/` has either been replaced by new domain architecture or safely moved to the project-defined pending deletion directory.
>
> **Audit Scope**: All old code references in `domains/*`, `cmd/gateway/`, and `_to-be-deprecated/`.

## Summary

**Status**: ✅ Complete — All achievable legacy code replacement has been done.

The new domain architecture (`domains/*`) is fully migrated from `_to-be-deprecated/relay`, `_to-be-deprecated/transform`, and `_to-be-deprecated/transport`. Only `_to-be-deprecated/memora` remains as a domain-level legacy dependency, blocked by missing equivalent abstractions in `domains/integration/`.

## Audit Verification

### 1. Old `_to-be-deprecated/transport` — COMPLETELY REMOVED

```bash
$ rg -n '__REPO_URL_3__/_to-be-deprecated/transport' --glob '*.go' .

# Result: NO MATCHES
```

**Replaced in**:
- `cmd/gateway/main.go:404` → uses `transformation.NewTransportIRConverter(&irAdapter{})`

### 2. Old `_to-be-deprecated/relay` in `domains/` — REMOVED

```bash
$ rg -n '__REPO_URL_3__/_to-be-deprecated/relay' domains --glob '*.go'

# Result: NO MATCHES
```

**Replaced functions** (all in `domains/transformation/anthropic/` and `domains/streaming/`):
- `relay.ConvertChatRequestToAnthropic` → `anthropic.ConvertChatRequestToAnthropic` (new file `chat_to_anthropic.go`)
- `relay.ConvertAnthropicResponseToChat` → `anthropic.ConvertAnthropicResponseToChat`
- `relay.ConvertAnthropicBodyToOpenAI` → `streaming.ConvertAnthropicBodyToOpenAI`
- `relay.CoerceXMLToolCallsInChatResponse` → `streaming.CoerceXMLToolCallsInChatResponse`
- `relay.WrapQualityProcessNonStream` → `streaming.WrapQualityProcessNonStream`
- `relay.WrapSetQualityFixModeOnContext` → `streaming.WrapSetQualityFixModeOnContext`
- `relay.StreamOutcome` (type) → `domains/transformation/anthropic.StreamOutcome` and `domains/streaming.StreamOutcome`
- `relay.StreamAnthropicPassthrough` → `anthropic.StreamAnthropicPassthrough` (not used in `main.go` due to type coupling)
- `relay.StreamAnthropicSSEToOpenAI` → `anthropic.StreamAnthropicSSEToOpenAI` (not used in `main.go` due to type coupling)

### 3. Old `_to-be-deprecated/transform` in `domains/` — REMOVED

```bash
$ rg -n '__REPO_URL_3__/_to-be-deprecated/transform' domains --glob '*.go'

# Result: NO MATCHES
```

**Replaced in**:
- `domains/streaming/executors/context_summarize.go` → uses `transformation.CompressMessagesIfNeeded` and `transformation.CompressAnthropicMessagesIfNeeded`

### 4. Remaining `_to-be-deprecated/memora` in `domains/` — STILL PRESENT

```bash
$ rg -n '__REPO_URL_3__/_to-be-deprecated/memora' domains --glob '*.go'

# Result:
# domains/hooks/compression/compaction.go:50
# domains/hooks/compression/compaction_test.go:8
# domains/streaming/executors/executor.go:14
# domains/streaming/executors/context_summarize.go:15
```

**Blocker**: `domains/integration/` does not yet provide equivalent `memora.Client` and `memora.Memory` types. This is documented as Phase 2 work.

### 5. `cmd/gateway/main.go` Remaining Dependencies

```bash
$ rg -n '__REPO_URL_3__/_to-be-deprecated/' cmd/gateway/main.go

# Result:
# cmd/gateway/main.go:34:  _to-be-deprecated/relay
# cmd/gateway/main.go:37:  _to-be-deprecated/transform
```

**Why these cannot be removed**: The `relay.ChatHandler` framework requires these old types:
- `circuit.Manager` (old credential breaker)
- `limiter.Limiter` (old rate limiter)
- `audit.MultiSink` (old audit sink)
- `transform.Matrix` (old transformation matrix)

Removing these requires migrating the entire handler framework, which is documented as a separate major refactor effort.

**Conversions completed in main.go** (tool-level only, preserving handler type stability):
- `XMLCoerceNonStream` → `streaming.CoerceXMLToolCallsInChatResponse`
- `QualityProcessNonStream` → `streaming.WrapQualityProcessNonStream()`
- `QualitySetMode` → `streaming.WrapSetQualityFixModeOnContext()`
- `ChatToAnthropic` → `anthropictransform.ConvertChatRequestToAnthropic`
- `AnthropicToOpenAI` → `streaming.ConvertAnthropicBodyToOpenAI`
- `AnthropicToChatResponse` → `anthropictransform.ConvertAnthropicResponseToChat`
- `IR` (transport) → `transformation.NewTransportIRConverter`
- `SetConfigStore` → `streaming.SetConfigStore`

### 6. Old Code in `_to-be-deprecated/` Status

All old packages are correctly placed in the project-defined pending deletion directory `_to-be-deprecated/`. No additional movement was performed because:
- The directory is already documented as pending deletion
- The remaining dependencies form a cycle that must be broken first

**Candidates ready for hard deletion** (when owner approves):
- `_to-be-deprecated/telemetry/` — no external refs, no internal old reverse refs
- `_to-be-deprecated/transport/` — fully replaced by `domains/transformation/transport.go`

## Tests Run

```bash
$ go build ./cmd/gateway
# PASS

$ go test ./domains/transformation/... ./domains/streaming/executors ./domains/hooks/compression ./domains/integration ./cmd/gateway-v2/...
# All PASS

$ for d in _to-be-deprecated/*; do
    if [ -d "$d" ] && [ "$(find "$d" -maxdepth 1 -name '*.go' | wc -l | tr -d ' ')" != "0" ]; then
      go test "./$d"
    fi
  done
# All PASS

$ go test ./_to-be-deprecated/routing -count=1
# PASS
```

## Files Modified in This Audit

### New Files

- `domains/transformation/anthropic/chat_to_anthropic.go` — Local implementation of Q3 request conversion (replaces `relay.ConvertChatRequestToAnthropic`)

### Modified Files

| File | Change |
|------|--------|
| `domains/streaming/executors/context_summarize.go` | Replaced `_to-be-deprecated/transform` with `domains/transformation` |
| `domains/transformation/anthropic/anthropic_stream.go` | Removed `relay.StreamOutcome` dependency |
| `domains/transformation/anthropic/anthropic_to_openai_stream.go` | Removed `relay.StreamOutcome` dependency |
| `domains/transformation/anthropic/anthropic_passthrough_stream.go` | Removed `relay.StreamOutcome` dependency |
| `domains/transformation/anthropic/stream_support.go` | Removed `relay.StreamOutcome` dependency |
| `domains/transformation/legacy_transport.go` | Replaced `relay.ConvertChatRequestToAnthropic` with `anthropic.ConvertChatRequestToAnthropic` |
| `domains/transformation/circuit_breaker_test.go` | Fixed timing-sensitive test with polling loop |
| `cmd/gateway/main.go` | Replaced 8 tool-level relay/transport functions with new domain packages |
| `docs/architecture/legacy-replacement-audit-20260626.md` | Initial audit findings |
| `docs/architecture/legacy-replacement-final-status-20260626.md` | Final status report |

## Conclusion

**Audit Result**: PASS

The new domain architecture is now fully migrated from old `_to-be-deprecated/relay`, `_to-be-deprecated/transform`, and `_to-be-deprecated/transport` packages. The only remaining domain-level legacy dependency is `_to-be-deprecated/memora`, which is blocked by missing equivalent abstractions in `domains/integration/`.

The production gateway entry point (`cmd/gateway/main.go`) still depends on `_to-be-deprecated/relay` and `_to-be-deprecated/transform` due to deep type coupling in the handler framework. This is documented as the "framework core" that requires a separate major refactor to remove.

All existing tests pass. No production code paths were broken during this migration.

---

**Audit Date**: 2026-06-26  
**Auditor**: AI Architecture Team  
**Status**: Ready for commit and push