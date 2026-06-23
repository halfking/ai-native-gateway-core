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

			// Capture tool calls
			for _, tc := range chunk.Delta.ToolCalls {
				if tc.Name != "" {
					sc.appendText("\n[Tool Call: ")
					sc.appendText(tc.Name)
					sc.appendText("]\n")
				}
				if tc.Arguments != "" {
					sc.appendText(tc.Arguments)
				}
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
