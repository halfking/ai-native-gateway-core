# Legacy Code Replacement - Final Status Report (2026-06-26)

## Executive Summary

This document records the final state of the legacy code replacement effort after exhaustive incremental migration from `_to-be-deprecated/*` to the new domain architecture (`domains/*`).

**Achievement**: Successfully removed `_to-be-deprecated/transport` from all production code paths and reduced new domain dependencies on old packages to memora only.

**Current State**: `cmd/gateway/main.go` still depends on `_to-be-deprecated/relay` and `_to-be-deprecated/transform` due to deep type coupling with the legacy handler framework.

## What Was Successfully Migrated

### 1. Complete Removal: `_to-be-deprecated/transport`

- ✅ `cmd/gateway/main.go:404` now uses `transformation.NewTransportIRConverter(&irAdapter{})`
- ✅ No production code path imports `_to-be-deprecated/transport`
- ✅ `domains/transformation/` provides full replacement

### 2. New Domain Packages No Longer Depend on Old Relay/Transform

All code under `domains/*` has been cleaned of direct imports to `_to-be-deprecated/relay` and `_to-be-deprecated/transform`:

| Old Import | New Replacement | Status |
|------------|-----------------|--------|
| `_to-be-deprecated/transform` | `domains/transformation` | ✅ Completed |
| `_to-be-deprecated/relay.ConvertChatRequestToAnthropic` | `domains/transformation/anthropic.ConvertChatRequestToAnthropic` | ✅ Completed |
| `_to-be-deprecated/relay.ConvertAnthropicResponseToChat` | `domains/transformation/anthropic.ConvertAnthropicResponseToChat` | ✅ Completed |
| `_to-be-deprecated/relay.StreamOutcome` | `domains/transformation/anthropic.StreamOutcome` | ✅ Completed |
| `_to-be-deprecated/relay.CoerceXMLToolCallsInChatResponse` | `domains/streaming.CoerceXMLToolCallsInChatResponse` | ✅ Completed |
| `_to-be-deprecated/relay.WrapQualityProcessNonStream` | `domains/streaming.WrapQualityProcessNonStream` | ✅ Completed |
| `_to-be-deprecated/relay.SetConfigStore` | `domains/streaming.SetConfigStore` | ✅ Completed |

**Only Remaining Domain-Level Old Dependency**: `domains/hooks/compression/` and `domains/streaming/executors/` still import `_to-be-deprecated/memora` because `domains/integration/` does not yet provide equivalent memora client/model abstractions.

### 3. Main Gateway Entry Point Progress

`cmd/gateway/main.go` successfully migrated these callbacks from `_to-be-deprecated/relay` to new domain packages:

- `routingExec.XMLCoerceNonStream` → `streaming.CoerceXMLToolCallsInChatResponse`
- `routingExec.QualityProcessNonStream` → `streaming.WrapQualityProcessNonStream()`
- `routingExec.QualitySetMode` → `streaming.WrapSetQualityFixModeOnContext()`
- `routingExec.ChatToAnthropic` → `anthropictransform.ConvertChatRequestToAnthropic`
- `routingExec.AnthropicToOpenAI` → `streaming.ConvertAnthropicBodyToOpenAI`
- `routingExec.AnthropicToChatResponse` → `anthropictransform.ConvertAnthropicResponseToChat`
- `routingExec.IR` → `transformation.NewTransportIRConverter(...)`

## What Cannot Be Migrated (Blockers)

### Root Cause: Legacy Handler Type Coupling

`cmd/gateway/main.go` still imports `_to-be-deprecated/relay` and `_to-be-deprecated/transform` because the entire handler framework is built on old types:

```go
chatHandler := relay.NewChatHandler(cm, lim, matrix, pools, resolver, auditSink)
```

This single line couples to:
- `circuit.Manager` (old credential circuit breaker)
- `limiter.Limiter` (old rate limiter)
- `transform.Matrix` (old transformation matrix)
- `audit.MultiSink` (old audit sink)

Moving to `streaming.NewChatHandler` would require:
- Replacing `circuit.Manager` → `domains/credential.Manager`
- Replacing `limiter.Limiter` → `domains/credential.Limiter`
- Replacing `audit.MultiSink` → `domains/hooks/audit.Sink`
- Replacing `transform.Matrix` → `domains/transformation.Matrix`
- Updating all downstream `chatHandler.Set*()` calls to use new types

**Estimated Effort**: 300+ lines across `main.go`, with risk of breaking existing hot paths.

### Remaining `relay` Dependencies in `main.go`

These cannot be replaced without handler migration:

| Call Site | Blocker |
|-----------|---------|
| `relay.NewChatHandler` | Requires all dependency types to migrate |
| `relay.NewHealthHandler` | Same |
| `relay.NewMessagesHandler` | Takes `relay.ChatHandler` parameter |
| `relay.NewResponsesHandler` | Takes `relay.ChatHandler` parameter |
| `relay.StreamChatWithPendingCapture` | Takes `relay.Normalizer`, returns old `audit.StreamCapture` |
| `relay.StreamAnthropicPassthrough` | Takes old `audit.StreamCapture` |
| `relay.StreamAnthropicSSEToOpenAI` | Takes old `audit.StreamCapture` |
| `relay.PendingCapturer` | Coupled with old `audit` and `sessions` |
| `relay.SanitizeAnthropicToolsInBody` | Could be migrated but low priority |
| `relay.NormalizeToolsInChatBody` | Could be migrated but low priority |
| `relay.StripMinimaxFieldsBody` | Could be migrated but low priority |
| `relay.NewIdempotentCache` | `chatHandler.SetIdempotentCache` expects old type |
| `relay.NewMetaToolInterceptor` | `chatHandler.SetMetaToolInterceptor` expects old type |
| `relay.ClientHasSessionID` | Could be migrated but coupled with PendingCapturer |
| `relay.SessionIDFromResp` | Could be migrated but coupled with PendingCapturer |
| `relay.RequestIDFromResp` | Could be migrated but coupled with PendingCapturer |
| `relay.NewNormalizer` | Could be migrated but `routing.Executor` expects old type |

## Recommended Next Steps

### Option 1: Big Bang Handler Migration (High Risk)
Migrate `cmd/gateway/main.go` from `relay.ChatHandler` to `streaming.ChatHandler` in one go, updating all type dependencies. This requires extensive testing and carries production risk.

### Option 2: Gradual Adapter Layer (Medium Risk)
Introduce adapter types that bridge old→new interfaces, allowing incremental migration of individual handler methods without breaking existing hot paths.

### Option 3: Accept Current State (Zero Risk)
Document that `_to-be-deprecated/relay` and `_to-be-deprecated/transform` are "legacy framework core" that will remain until a major version refactor. New features go into `domains/*`, old code is frozen.

## Recommendation

**Option 3** is recommended for production stability. The marginal benefit of removing the last two old imports does not justify the risk when:
- New domain packages are already independent
- No external dependencies on `_to-be-deprecated/*` exist
- The legacy handler framework is stable and well-tested
- All new protocol conversion logic already lives in `domains/*`

## Verification Commands

```bash
# Verify no new domains depend on old relay/transform
rg -n 'github.com/kaixuan/llm-gateway-go/_to-be-deprecated/(relay|transform|transport)' domains --glob '*.go'

# Should only return memora imports:
# domains/hooks/compression/compaction.go:50:  "_to-be-deprecated/memora"
# domains/streaming/executors/executor.go:14:  "_to-be-deprecated/memora"
# domains/streaming/executors/context_summarize.go:15:  "_to-be-deprecated/memora"

# Verify transport is completely removed
rg -n '_to-be-deprecated/transport' . --glob '*.go'

# Should return no matches

# Build gateway
go build ./cmd/gateway

# Test new domains
go test ./domains/transformation/... ./domains/streaming/executors
```

## Files Modified in This Effort

- `domains/streaming/executors/context_summarize.go` - Changed from `_to-be-deprecated/transform` to `domains/transformation`
- `domains/transformation/anthropic/chat_to_anthropic.go` - New file, replaces `relay.ConvertChatRequestToAnthropic`
- `domains/transformation/anthropic/anthropic_stream.go` - Removed relay dependency
- `domains/transformation/anthropic/anthropic_to_openai_stream.go` - Removed relay dependency
- `domains/transformation/anthropic/anthropic_passthrough_stream.go` - Removed relay dependency
- `domains/transformation/anthropic/stream_support.go` - Removed relay dependency
- `domains/transformation/legacy_transport.go` - Changed from `relay.ConvertChatRequestToAnthropic` to `anthropic.ConvertChatRequestToAnthropic`
- `domains/transformation/circuit_breaker_test.go` - Fixed timing-sensitive test
- `cmd/gateway/main.go` - Migrated 7 protocol conversion callbacks to new domain packages
- `docs/architecture/legacy-replacement-audit-20260626.md` - Initial audit findings

---

**Status**: Delivered as far as incremental migration allows without breaking production.  
**Date**: 2026-06-26  
**Next Owner Decision**: Choose Option 1, 2, or 3 above.
