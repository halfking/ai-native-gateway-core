# Self-check tool continuation empty-response fix

**Date**: 2026-07-18  
**Type**: Bug fix  
**Impact**: System self-check accuracy for tool-capable models

## Summary

Fixed false `empty_response` failures in the second round of the system self-check tool conversation.

## Root Cause

Round two appended the assistant tool call and tool result while retaining the instruction to call the tool and output nothing else. `gpt-5.6-luna` followed that instruction by returning an empty assistant message. The gateway then correctly classified the response as `empty_response` because it contained no content, reasoning, or tool calls.

## Fix

Both tool conversation rounds now ask the model to call `get_current_time` and output only the query result after the tool completes. This preserves the round-one tool-call requirement while making round two produce a verifiable final response.

## Files Changed

| File | Change |
|------|--------|
| `bg/self_check_worker.go` | Require a final tool result in rounds one and two |
| `bg/self_check_worker_test.go` | Add an HTTP-level regression test for the round-two prompt |
| `CHANGELOG.md` | Record the fix |

## Verification

- Regression test failed with the old prompt and passed after the fix.
- Production A/B test: the old instruction returned no bytes within 30 seconds; the corrected continuation returned HTTP 200 in 2.749 seconds.
- Production round-one test with the corrected prompt returned `finish_reason=tool_calls` and a valid `get_current_time` call.

## Rollback

Revert the fix commit to restore the previous self-check prompt and remove the regression test.
