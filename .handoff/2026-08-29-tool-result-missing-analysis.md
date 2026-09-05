# Tool Result Missing Error Analysis

**Date**: 2026-08-29  
**Status**: In Progress  
**Priority**: High  
**Reporter**: User observation on production traffic

## Problem Statement

Users are encountering the following error:

```
Turn execution failed
provider=ef7bed64-de6f-42d8-86f2-eab4b62d9812 
model=claude-sonnet-5 
request=69366bb4-eaea-4b3c-a664-3d762fbf7105 
reason=unknown 
retryable=false

Tool result is missing for tool call call-d1a4bd8b-1a9f-4a00-b760-b1fecc98e2b4.
```

## Error Analysis

### What This Means

1. **Client-side detection**: This error is reported by the ZCode client, not the gateway
2. **Incomplete response**: The upstream returned a `tool_use` block but the corresponding `tool_result` is missing
3. **Non-retryable**: Marked as `retryable=false`, indicating the client won't automatically retry

### Possible Root Causes

#### 1. **Upstream Stream Interruption**
The upstream provider (Anthropic Claude) may have:
- Sent `content_block_start` with `type: tool_use`
- Started sending the tool execution
- Got interrupted before sending `content_block_start` with `type: tool_result`

This is similar to the JSON parsing error we just fixed - upstream instability causing incomplete responses.

#### 2. **Gateway Filtering/Dropping**
The gateway may be:
- Filtering out certain content blocks
- Dropping frames during stream processing
- Incorrectly handling content block boundaries

#### 3. **Survival Coordinator Interference**
The survival/recovery mechanism may be:
- Discarding buffered content that includes tool_result
- Triggering `resume_blocked` after tool_use but before tool_result
- Not properly preserving tool call state across retries

## Investigation Plan

### 1. Check Database for Patterns
- Query `request_logs_hot` for requests with tool_use but incomplete tool results
- Analyze correlation with `interrupted=true` flag
- Check `discard_events` JSONB column for tool-related discards

### 2. Validate Tool Call Integrity
Need to implement checks for:
- Every `tool_use` has a matching `tool_result`
- Tool results appear in the same order as tool uses
- Content blocks are not truncated mid-stream

### 3. Review Stream Processing Pipeline
Check these files for tool_use/tool_result handling:
- `domains/streaming/anthropic_bridge.go` - Anthropic SSE processing
- `domains/streaming/survival_coordinator.go` - Recovery logic
- `domains/streaming/attempt_commit_gate.go` - Buffering/commit logic

## Proposed Solutions

### Short-term: Detection and Logging
1. Add tool call completeness validation in the stream processor
2. Log when tool_use is seen but stream ends without tool_result
3. Mark such requests as `interrupted` with reason `incomplete_tool_call`

### Medium-term: Integrity Enforcement
1. Track active tool_use blocks in StreamCapture
2. Validate all tool uses have results before marking stream complete
3. If incomplete, mark as resumable and retry (if within holdback window)

### Long-term: Structured Tool Call State
1. Implement proper tool call state machine
2. Track: pending → executing → completed/failed
3. Expose tool call state in admin UI for debugging

## Code Locations

### Key Files to Review
```
domains/streaming/anthropic_bridge.go      # content_block_start/stop handling
domains/streaming/attempt_commit_gate.go   # Buffering logic
domains/streaming/survival_coordinator.go  # Recovery decisions
domains/hooks/audit/audit.go              # StreamCapture.ToolCalls
```

### Database Schema
```sql
-- request_logs_hot likely has:
-- - interrupted: boolean
-- - reason: text
-- - discard_events: jsonb
-- - (need to verify column names for response content)
```

## Next Steps

1. ✅ Create audit script to check incomplete tool calls in database
2. ⏳ Run script on 245/154 to get baseline metrics
3. ⏳ Implement tool call integrity validator (similar to SSE frame validator)
4. ⏳ Add detection logic to stream processor
5. ⏳ Test with synthetic incomplete tool_use responses
6. ⏳ Deploy and monitor

## Related Issues

- **JSON parsing error fix** (2026-08-29): Both issues stem from upstream instability
- **gateway_survival_resume_blocked**: Same root cause - partial responses being committed

## References

- User report: "Tool result is missing for tool call call-d1a4bd8b..."
- Similar pattern to minimax-m3/glm-5.2 incomplete JSON issue
- ZCode client performs tool call validation on received responses
