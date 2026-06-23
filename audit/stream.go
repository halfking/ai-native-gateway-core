package audit

import (
	"crypto/sha256"
	"encoding/json"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

// ObserveChunk observes a structured StreamChunk from the IR layer.
// This is the NEW structured interface that replaces the string-based
// ObservePayload for code that uses the IR layer.
//
// Benefits:
//   - Type-safe: no manual JSON parsing
//   - Protocol-agnostic: works for both OpenAI and Anthropic
//   - Eliminates format ambiguity
//
// The old ObservePayload is kept for backward compatibility during migration.
func (sc *StreamCapture) ObserveChunk(chunk *ir.StreamChunk) {
	if sc == nil || chunk == nil {
		return
	}

	sc.mu.Lock()
	defer sc.mu.Unlock()

	// Update chunk count and timing
	sc.chunkCount++
	elapsed := int(time.Since(sc.startTime).Milliseconds())
	if sc.firstChunkMs == 0 {
		sc.firstChunkMs = elapsed
	}

	// Serialize chunk for checksum (protocol-agnostic)
	chunkBytes, _ := json.Marshal(chunk)
	sc.checksum = sha256.Sum256(append(sc.checksum[:], chunkBytes...))

	// Update preview (first 2KB of content)
	if len(sc.preview) < 2048 {
		previewText := extractChunkPreview(chunk)
		if previewText != "" {
			remaining := 2048 - len(sc.preview)
			if len(previewText) > remaining {
				previewText = safeTruncateUTF8(previewText, remaining)
			}
			sc.preview = append(sc.preview, previewText...)
		}
	}

	// Process based on chunk type
	switch chunk.Type {
	case ir.ChunkTypeDelta:
		if chunk.Delta != nil {
			// Capture reasoning content
			if chunk.Delta.ReasoningContent != "" {
				sc.appendText("<reasoning>\n")
				sc.appendText(chunk.Delta.ReasoningContent)
				sc.appendText("\n</reasoning>\n")
			}

			// Capture text content
			if chunk.Delta.Content != "" {
				sc.appendText(chunk.Delta.Content)
			}

			// Capture tool calls (both textContent for preview AND structured ToolCalls)
			for _, tc := range chunk.Delta.ToolCalls {
				// Legacy text preview (for backward compatibility)
				if tc.Name != "" {
					sc.appendText("\n[Tool Call: ")
					sc.appendText(tc.Name)
					sc.appendText("]\n")
				}
				if tc.Arguments != "" {
					sc.appendText(tc.Arguments)
				}

				// 2026-06-23: Structured tool_calls accumulation (migration 042).
				// Merge incremental tool call deltas into the ToolCalls array.
				// OpenAI streaming sends tool calls as:
				//   1. First chunk: {index: 0, id: "call_abc", type: "function", function: {name: "get_weather", arguments: ""}}
				//   2. Delta chunks: {index: 0, function: {arguments: "{\"loc\""}}
				//   3. More deltas: {index: 0, function: {arguments: "ation\":\""}}
				// We accumulate arguments across deltas for the same index.
				sc.mergeToolCall(tc)
			}
		}

		// Record finish reason if present
		if chunk.FinishReason != "" {
			sc.finalFinish = chunk.FinishReason
		}

	case ir.ChunkTypeUsage:
		if chunk.Usage != nil {
			if chunk.Usage.PromptTokens > 0 {
				pt := chunk.Usage.PromptTokens
				sc.promptTokens = &pt
			}
			if chunk.Usage.CompletionTokens > 0 {
				ct := chunk.Usage.CompletionTokens
				sc.completionTokens = &ct
			}
		}

		// Usage chunks can also carry finish_reason
		if chunk.FinishReason != "" {
			sc.finalFinish = chunk.FinishReason
		}

	case ir.ChunkTypeDone:
		sc.doneReceived = true
		if chunk.FinishReason != "" {
			sc.finalFinish = chunk.FinishReason
		}

	case ir.ChunkTypeError:
		sc.interrupted = true
		if chunk.Error != nil {
			sc.finalFinish = chunk.Error.Type
		}
	}
}

// mergeToolCall merges an incremental tool call delta into the accumulated ToolCalls array.
// Assumes sc.mu is already locked by the caller (ObserveChunk).
//
// OpenAI streaming protocol sends tool calls incrementally:
//   - First chunk: {index: 0, id: "call_abc", type: "function", function: {name: "foo", arguments: ""}}
//   - Delta chunks: {index: 0, function: {arguments: "{\"bar\""}} ... {index: 0, function: {arguments: "\":123}"}}
//
// We accumulate arguments for the same index across chunks.
func (sc *StreamCapture) mergeToolCall(tc ir.StreamToolCallDelta) {
	// Ensure ToolCalls array is large enough
	for len(sc.ToolCalls) <= tc.Index {
		sc.ToolCalls = append(sc.ToolCalls, nil)
	}

	existing := sc.ToolCalls[tc.Index]
	if existing == nil {
		// First chunk for this index: initialize with id, type, name
		existing = map[string]any{
			"index": tc.Index,
		}
		if tc.ID != "" {
			existing["id"] = tc.ID
		}
		if tc.Type != "" {
			existing["type"] = tc.Type
		}
		if tc.Name != "" {
			// Nested function object
			existing["function"] = map[string]any{
				"name":      tc.Name,
				"arguments": tc.Arguments,
			}
		} else if tc.Arguments != "" {
			// Delta chunk (no name): append arguments
			existing["function"] = map[string]any{
				"arguments": tc.Arguments,
			}
		}
		sc.ToolCalls[tc.Index] = existing
	} else {
		// Subsequent chunk: merge fields
		if tc.ID != "" {
			existing["id"] = tc.ID
		}
		if tc.Type != "" {
			existing["type"] = tc.Type
		}
		if tc.Name != "" || tc.Arguments != "" {
			fn, ok := existing["function"].(map[string]any)
			if !ok {
				fn = map[string]any{}
				existing["function"] = fn
			}
			if tc.Name != "" {
				fn["name"] = tc.Name
			}
			if tc.Arguments != "" {
				// Append arguments (incremental JSON string)
				prevArgs, _ := fn["arguments"].(string)
				fn["arguments"] = prevArgs + tc.Arguments
			}
		}
	}
}

// extractChunkPreview extracts a short text preview from a chunk for the preview field.
func extractChunkPreview(chunk *ir.StreamChunk) string {
	if chunk.Delta != nil {
		if chunk.Delta.Content != "" {
			return chunk.Delta.Content
		}
		if chunk.Delta.ReasoningContent != "" {
			return chunk.Delta.ReasoningContent
		}
	}
	return ""
}
