# Fix: Tool Call ID Missing in Anthropic SSE Stream (2026-06-23)

## Problem

When using `claude-sonnet-4-6` with tool calls enabled, the llm-gateway-go returned errors:

```
Tool execution was interrupted during streaming recovery before this tool was executed. 
Treat this tool call as failed and do not retry blindly.
```

Followed by:

```
Turn execution failed
Expected 'id' to be a string.
provider=ef7bed64-de6f-42d8-86f2-eab4b62d9812 model=claude-sonnet-4-6 
request=c3cb888d-52e5-4243-a715-53a8eeec5552 reason=unknown retryable=false
```

## Root Cause

The `StreamAnthropicSSEToOpenAI` function in `relay/anthropic_to_openai_stream.go` was not properly tracking tool call IDs across streaming events:

1. **Anthropic SSE Event Flow**:
   - `content_block_start` (tool_use) → Contains `ID`, `Name`, and initial `input`
   - `content_block_delta` (input_json_delta) → Contains only `partial_json` (NO ID)
   - `content_block_stop` → Flush accumulated arguments

2. **Bug Location** (lines 270-296):
   ```go
   emitToolCall := func(toolID, toolName string, args json.RawMessage) {
       entry := map[string]any{"index": idx}
       if toolID != "" {
           entry["id"] = toolID  // ❌ ID not set when toolID is empty
       }
   }
   ```

3. **Problematic Calls**:
   - Line 449: `emitToolCall("", "", d.InputJSON)` - Empty toolID for `input_json` delta
   - Line 492: `emitToolCall("", "", json.RawMessage(bufferedToolArgs.String()))` - Empty toolID when flushing `input_json_delta` args

4. **Impact**: OpenAI tool call format **requires** every tool_calls chunk to have an `id` field. Without it, clients fail with "Expected 'id' to be a string".

## Solution

### Changes Made

1. **Added ID caching** (line 122):
   ```go
   var (
       // ... existing vars ...
       currentToolCallID string  // Cache the current tool_use block ID for delta updates
   )
   ```

2. **Cache ID on `content_block_start`** (line 428):
   ```go
   case "content_block_start":
       // ... parse event ...
       if err := json.Unmarshal(data, &evt); err == nil && evt.ContentBlock.Type == "tool_use" {
           currentToolCallID = evt.ContentBlock.ID  // ✅ Cache the ID
           emitToolCall(evt.ContentBlock.ID, evt.ContentBlock.Name, evt.ContentBlock.InputRaw)
       }
   ```

3. **Use cached ID for `input_json` delta** (line 450):
   ```go
   case "input_json":
       emitToolCall(currentToolCallID, "", d.InputJSON)  // ✅ Use cached ID
   ```

4. **Use cached ID when flushing `input_json_delta`** (line 493):
   ```go
   case "content_block_stop":
       // ... flush text ...
       if bufferedToolArgs.Len() > 0 {
           emitToolCall(currentToolCallID, "", json.RawMessage(bufferedToolArgs.String()))  // ✅ Use cached ID
           bufferedToolArgs.Reset()
       }
       currentToolCallID = ""  // ✅ Clear cache when block ends
   ```

5. **Added fallback ID generation** (lines 278-287):
   ```go
   emitToolCall := func(toolID, toolName string, args json.RawMessage) {
       // ... existing logic ...
       if toolID != "" {
           entry["id"] = toolID
       } else {
           // ✅ Fallback: generate synthetic ID if none provided
           entry["id"] = fmt.Sprintf("call_%s_%d", requestID, idx)
       }
   }
   ```

## Testing

### New Tests Added

**File**: `relay/anthropic_to_openai_stream_tool_id_test.go`

1. **TestAnthropicToOpenAIStream_ToolCallIDPersistence**
   - Simulates real Anthropic stream with `content_block_start` + multiple `input_json_delta` events
   - Verifies every tool_calls chunk has a non-empty string `id` field
   - Confirms all chunks use the same ID from `content_block_start`
   - **Result**: ✅ PASS - "Successfully verified 2 tool_call chunks, all with ID 'toolu_xyz789'"

2. **TestAnthropicToOpenAIStream_ToolCallIDFallback**
   - Tests edge case where `input_json` arrives without preceding `content_block_start`
   - Verifies fallback ID generation works correctly
   - **Result**: ✅ PASS

### Existing Tests

All existing tool call tests continue to pass:
- `TestAnthropicToOpenAIStream_ToolCallsFinishReason` ✅
- `TestAnthropicToOpenAIStream_InconsistentToolCalls` ✅
- `TestAnthropicToOpenAIStream_ConsistentToolCalls` ✅
- `TestStreamingToolCallsE2E` ✅

## Deployment

### Build Verification

```bash
cd services/llm-gateway-go
go test ./relay -v -run ToolCall     # ✅ All tests pass
go build -o /tmp/llm-gateway-go ./cmd/gateway  # ✅ Clean build
```

### Deployment Steps

1. **Build new image** with the fix
2. **Deploy to 184 k3s** (llmgo.kxpms.cn)
3. **Deploy to 71 systemd** (llm.kxpms.cn)
4. **Verify** with a claude-sonnet-4-6 tool call request

### Verification Commands

```bash
# Test tool call with claude-sonnet-4-6
curl -X POST https://llmgo.kxpms.cn/v1/chat/completions \
  -H "Authorization: Bearer <api-key>" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-sonnet-4-6",
    "messages": [{"role": "user", "content": "Run ls command"}],
    "tools": [{"type": "function", "function": {"name": "bash", "parameters": {}}}],
    "stream": true
  }'

# Expected: All tool_calls chunks include "id" field, no "Expected 'id' to be a string" errors
```

## Related Issues

- **Original Error**: "Expected 'id' to be a string" when using claude-sonnet-4-6 with tools
- **Previous Fix** (2026-06-23, line 528-544): Detects inconsistent `finish_reason:"tool_calls"` when no tool_use blocks were emitted
- **Current Fix**: Ensures tool_use blocks always have valid `id` fields in streaming responses

## Impact

- **Models Affected**: claude-sonnet-4-6, and potentially other Anthropic models that use `input_json_delta` for streaming tool arguments
- **Client Impact**: OpenAI-compatible clients (including Cursor, Continue, etc.) that expect strict OpenAI streaming format
- **Severity**: HIGH - Tool calls were completely broken for affected models
- **Fix Status**: ✅ Complete - ID tracking and fallback generation implemented

## References

- File: `services/llm-gateway-go/relay/anthropic_to_openai_stream.go`
- Test: `services/llm-gateway-go/relay/anthropic_to_openai_stream_tool_id_test.go`
- Anthropic Messages API: https://docs.anthropic.com/en/api/messages-streaming
- OpenAI Chat Completions API: https://platform.openai.com/docs/api-reference/chat/streaming
