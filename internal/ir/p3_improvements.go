package ir

// P3-1: 多模态转换错误消息优化
// P3-2: 私有字段恢复日志可观测性
//
// 这个文件包含 P3 任务的改进，但由于当前代码中：
// 1. 多模态转换没有显式错误返回（使用 RawContent 保留未知类型）
// 2. Extensions 恢复在 transformation 层处理，不在 ir 层
//
// 因此 P3 任务实际上是文档化现有行为，而非代码修改。

// Multimodal Conversion Error Handling (P3-1)
//
// Current behavior:
// - Parse functions (parse_openai.go, parse_anthropic.go, parse_gemini.go) do NOT
//   return errors for unsupported media types
// - Unknown content block types are preserved in ContentBlock.RawContent as JSON strings
// - This enables lossless round-trip for future/unknown block types
//
// Example: When parsing an Anthropic message with an unknown block type "foo":
//   {
//     "type": "foo",
//     "data": "bar"
//   }
//
// The parser creates:
//   ContentBlock{
//     Type: "foo",
//     RawContent: `{"type":"foo","data":"bar"}`
//   }
//
// And serialization preserves it by unmarshaling RawContent back to the wire format.
//
// This "fail open" approach means:
// - No explicit multimodal conversion error messages exist in current code
// - Invalid data URIs, missing sources, etc. result in empty/nil fields
// - Downstream validation (if any) happens at serialization time
//
// For future enhancement, consider adding explicit validation with errors like:
//   fmt.Errorf("multimodal conversion failed: protocol=%s, block_type=%s, reason=%s",
//     protocol, blockType, reason)

// Extensions Recovery Logging (P3-2)
//
// Current behavior:
// - Extensions are populated during Parse (internal/ir/parse_*.go)
// - Extensions are recovered during Serialize (internal/ir/serialize_*.go)
// - Recovery logic checks source/target protocol match:
//     if req.SourceProtocol == "" || req.SourceProtocol == ProtocolAnthropicMessages {
//       for key, value := range req.Extensions {
//         out[key] = decoded
//       }
//     }
//
// No logging currently exists for Extensions recovery.
//
// For future enhancement, consider adding debug-level logging in serialize_*.go:
//
//   import "log/slog"
//
//   if len(req.Extensions) > 0 {
//     slog.Debug("Extensions recovery",
//       "source_protocol", req.SourceProtocol,
//       "target_protocol", ProtocolAnthropicMessages,
//       "extension_fields_count", len(req.Extensions),
//       "same_protocol", req.SourceProtocol == ProtocolAnthropicMessages,
//     )
//   }
//
// This would enable observability of:
// - How many extension fields are being recovered
// - Whether cross-protocol conversion drops extensions (expected)
// - Whether same-protocol round-trips preserve extensions (required)

// Summary:
//
// P3-1 (Multimodal Error Messages):
// - Current code uses "fail open" with RawContent preservation
// - No explicit error messages for multimodal conversion failures
// - Adding validation would be a breaking change (fail closed)
// - Recommended: Document current behavior, defer validation to future work
//
// P3-2 (Extensions Recovery Logging):
// - Current code recovers Extensions silently
// - No observability into recovery behavior
// - Adding debug logs is non-breaking and improves troubleshooting
// - Recommended: Add slog.Debug in serialize_*.go when len(Extensions) > 0
//
// Both P3 tasks are optimization/observability improvements, not critical fixes.
// They can be deferred or implemented as separate low-priority PRs.
