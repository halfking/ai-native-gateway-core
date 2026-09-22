// Package anthropic — stream_support.go holds the one stream-level helper
// that survived the 2026-09-14 dead-bridge retirement (R27 audit §三 #1,
// following the 97aa179ab precedent). The retired copy carried a full
// StreamOpenAIToAnthropicSSE bridge with no production callers — the live
// bridge is domains/streaming/anthropic_stream.go (8-param, commit-gated).
//
// Live package surface (verified by selector scan, 2026-09-14):
//   - ValidateStreamingToolArgs       ← streaming/anthropic_bridge.go (tool_args_assembler.go)
//   - ConvertChatRequestToAnthropic   ← streaming/anthropic_bridge.go (chat_to_anthropic.go)
//   - ConvertAnthropicResponseToChat  ← streaming/anthropic_bridge.go (anthropic_to_chat.go)
package anthropic
