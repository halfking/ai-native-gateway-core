# Incident Report: MiniMax M3 Tool Call ID Mismatch (2026-07-18)

## Summary

**Date**: 2026-07-18 01:01:54 - 02:21:00 (UTC+8)  
**Severity**: P1 (Production outage for minimax-m3 requests with tool calls)  
**Duration**: ~80 minutes (detection to fix deployment)  
**Impact**: All minimax-m3 requests containing orphan tool messages failed with 503 after 90s timeout  
**Root Cause**: `applyInlineValidation` function defined but never called in legacy routing path  

## Timeline (UTC+8)

| Time | Event |
|------|-------|
| 01:01:54 | Original incident: req `3905e839e0abab5a53efc09222e2d45b` failed with 503 |
| 01:28:00 | Oncall begins: investigation of journald logs (only 5 lines, insufficient) |
| 01:41:00 | Deployed observability enhancements (v1141) — added routing_resolve / finalizeOpenAIUpstreamBody / upstream_http_attempt logs |
| 01:50:00 | Created repro script `repro_tool_call_id_mismatch.py` to synthesize orphan tool messages |
| 01:56:00 | Deployed v1145 — added 4xx body preview logging |
| 02:02:00 | Repro test confirmed: orphan tool messages trigger MiniMax 4xx cascade → 90s timeout → 503 |
| 02:02:00 | **Root cause identified**: `applyInlineValidation` (inline_validation.go) has zero callers; legacy path (e.IR == nil) bypasses orphan sanitization |
| 02:11:00 | Deployed fix v1142 (245 + 154) — wired `applyInlineValidation` into legacy_no_ir branch |
| 02:21:00 | Verification: 9-line journald trail confirms orphan removal + upstream 200 response |

## Root Cause Analysis

### Primary Root Cause

**Code**: `domains/streaming/executors/executor_chat.go` — `applyInlineValidation` function (inline_validation.go, added 2026-07-12) performs `ParseOpenAI → ValidateAndFixRequest → SerializeOpenAI` to remove orphan tool messages, but was **never called**.

**Impact Path**:
```
Client sends tool_result with stale tool_call_id
  → Gateway routes to minimax-m3 (legacy path, e.IR == nil)
  → prepareRequestBody runs legacy transforms (whitelist / collapse / merge)
  → orphan tool message passes through unchanged
  → MiniMax upstream returns 400 "tool id not found (2013)"
  → Gateway retries (max 3 attempts)
  → All attempts fail within 90s
  → Client receives 503 Service Unavailable
```

**Why it wasn't caught earlier**:
- `SanitizeToolMessages` only called in IR converter path (Anthropic→OpenAI, Gemini→OpenAI)
- minimax-m3 uses legacy routingExec (no IR conversion), so sanitization was skipped
- No integration test covering "tool message with orphan call_id on stable model"

### Secondary Root Cause (Observability Gap)

**Symptom**: Original incident journald showed only 5 lines:
- audit (generic request log)
- safety_net_defer_fired
- http_request
- trace (generic)
- routeincident (error aggregation)

**Missing**:
- Which provider/credential/raw_model was selected
- Whether routing found candidates (candidates_count)
- What the request body looked like after transforms (pre/post body_bytes)
- What upstream returned (status / error body)
- Whether sanitization ran (removed tool messages count)

This made it impossible to diagnose without tcpdump or direct DB query of request_logs.

## Fix

### Code Changes (commit 6cdc3ed79)

**File**: `domains/streaming/executors/executor_chat.go`

**Change 1** — Wire `applyInlineValidation` into legacy_no_ir branch:

```go
} else {
    // e.IR == nil: legacy path WITHOUT IR converter
    preBodyBytes := len(bodyBytes)
    bodyBytes = applyInlineValidation(bodyBytes, params.RequestID)  // ← NEW
    postBodyBytes := len(bodyBytes)
    slog.Info("finalizeOpenAIUpstreamBody: legacy path (no IR) + inline validation",
        "request_id", params.RequestID,
        "path", "legacy_no_ir_with_inline",
        "pre_body_bytes", preBodyBytes,
        "post_body_bytes", postBodyBytes,
        "delta_bytes", postBodyBytes-preBodyBytes,
    )
}
```

**Change 2** — Add 4xx body preview to upstream_http_attempt:

```go
if resp != nil {
    attemptLog(resp.StatusCode, "", "")
    if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.Body != nil {
        peek := make([]byte, 256)
        n, _ := resp.Body.Read(peek)
        if n > 0 {
            attemptLog(0, "", strings.TrimSpace(string(peek[:n])))
        }
    }
}
```

### Deployment

- **245 (test)**: v1148-6cdc3ed7
- **154 (prod)**: v1142-6cdc3ed7
- **Rollback**: `bash scripts/deploy-seamless.sh rollback 154`

## Verification

### Before Fix

```
journald for req 3905e839e0abab5a53efc09222e2d45b (5 lines):
- audit: request completed (generic)
- safety_net_defer_fired
- http_request status=503
- trace (generic)
- routeincident (error aggregation)
```

No visibility into:
- Routing candidates
- Body transforms
- Upstream attempts
- Sanitization

### After Fix

```
journald for req diag-933795e3986c46e29e98d1f6 (9 lines):
02:02:04 routing_resolve                       candidates_count=1, provider_id=18, raw_model=minimaxai/minimax-m3
02:02:04 sanitize_tool_messages: removing orphaned tool message  tool_call_id=call_repro_valid_001
02:02:04 sanitize_tool_messages: removed orphaned tool messages  removed=1, original=6, sanitized=5
02:02:04 validate_and_fix_request              removed=1
02:02:04 finalizeOpenAIUpstreamBody: legacy path (no IR) + inline validation
                                               pre_body_bytes=22172, post_body_bytes=11244, delta=-10928
02:02:23 upstream_http_attempt                 status=200, latency_ms=18220
02:02:23 audit: request completed              success=true
02:02:23 safety_net_defer_fired
02:02:23 http_request                          status=200, duration_ms=18434
```

**Result**:
- ✅ Orphan tool message detected and removed (body shrank by 10928 bytes)
- ✅ Upstream (NVIDIA NIM proxy) returned 200 instead of 400
- ✅ Client received successful response instead of 503

## Follow-up Actions

### Immediate (This Week)

1. **Fix model_aliases deprecated status** [HIGH]
   - **Issue**: `models_canonical.id=5704` (minimax-m3) has all `model_aliases` rows with `status='deprecated'`
   - **Impact**: client_model='minimax-m3' routes to 0 candidates → no_candidate 503
   - **Note**: NVIDIA NIM's `minimaxai/minimax-m3` alias still works
   - **Action**: Reactivate alias row OR migrate clients to canonical names
   - **Owner**: @platform

2. **Add deploy gate for commit message vs diff mismatch** [MEDIUM]
   - **Issue**: Commits 7ba14196c / 1771088f9 said "wire applyInlineValidation" but actual diff was empty (git rebase interrupted)
   - **Action**: Add pre-build check: if commit message contains "wire" / "add" but `git diff HEAD~1` is empty, refuse to build
   - **Owner**: @infra

### Short-term (Q3 2026)

3. **Add integration test for orphan tool messages** [MEDIUM]
   - Test: Send request with `tool_result.tool_call_id` not in `assistant.tool_calls[]`
   - Assert: Gateway removes orphan before upstream
   - Cover: Both IR path AND legacy path
   - **Owner**: @qa

4. **Deprecate legacy routing path** [LOW]
   - Migrate all stable models (minimax-m3, deepseek-chat, etc.) to IR converter path
   - Simplify code: single path for all models
   - **Owner**: @backend

### Long-term (Q4 2026)

5. **Add structured observability layer** [LOW]
   - Replace ad-hoc slog.Info calls with unified pipeline events
   - OpenTelemetry spans for routing / transform / upstream
   - **Owner**: @observability

## Lessons Learned

### What Went Well

- Repro script quickly synthesized the failure mode
- Observability enhancements (v1141/v1145) made root cause obvious
- Fix was surgical (3 lines of code) and low-risk

### What Went Wrong

- `applyInlineValidation` was dead code for 6 days (2026-07-12 to 2026-07-18)
- No static analysis caught "function with zero callers"
- Original incident had insufficient logs to diagnose without deep dive
- Git rebase interrupted deploy, causing 3 failed commit attempts

### Process Improvements

1. **Add dead code detection to CI**:
   ```bash
   # Fail if any exported function has zero callers (excluding _test.go)
   go run golang.org/x/tools/cmd/deadcode@latest ./... | grep -v _test.go
   ```

2. **Add "observability checklist" to PR template**:
   - [ ] New code path has structured log at entry/exit
   - [ ] Error paths log enough context for diagnosis (request_id + key params)
   - [ ] Upstream calls log provider/credential/status/latency

3. **Require repro script for all oncalls**:
   - Incident closed only when repro script committed to `docs/incidents/<date>/`
   - Future oncalls can run script to verify fix persistence

## References

- **Original incident request_id**: `3905e839e0abab5a53efc09222e2d45b`
- **Repro script**: `.scratch/minimax-m3-toolcall-2026-07-18/repro_tool_call_id_mismatch.py`
- **Fix commit**: `6cdc3ed79` (take 3 after rebase interruptions)
- **Deploy versions**: 154=v1142, 245=v1148
- **Related commits**:
  - `b07b4ffae` — Initial observability enhancements (routing_resolve, finalizeOpenAIUpstreamBody, upstream_http_attempt logs)
  - `7ba14196c` — Added 4xx body preview (but applyInlineValidation wiring failed to persist)
  - `1771088f9` — Second attempt (also failed due to rebase)
  - `6cdc3ed79` — Final successful commit

## Appendix: Repro Script

See: `.scratch/minimax-m3-toolcall-2026-07-18/repro_tool_call_id_mismatch.py`

The script synthesizes 3 request variants:
1. **baseline_normal**: Valid tool_result referencing existing tool_call_id
2. **malicious_orphan_tail**: Valid tool_calls + tool_result, followed by orphan tool_result at tail
3. **malicious_parallel_orphan**: tool_result with call_id not in tool_calls array

Before fix: #2 and #3 triggered 90s timeout → 503  
After fix: #2 and #3 sanitized → 200

---

**Report prepared by**: ACC Agent (oncall 2026-07-18)  
**Reviewed by**: TBD  
**Status**: RESOLVED
