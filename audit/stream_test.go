package audit

import (
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

// ─── ObserveChunk Tests ─────────────────────────────────────────────────────

func TestObserveChunk_Delta_Content(t *testing.T) {
	capture := NewStreamCapture()

	chunk := &ir.StreamChunk{
		Type: ir.ChunkTypeDelta,
		Delta: &ir.StreamDelta{
			Content: "Hello World",
		},
	}

	capture.ObserveChunk(chunk)

	summary := capture.SummaryAsMap()
	textContent := summary["stream_text_content"].(string)

	if textContent != "Hello World" {
		t.Errorf("expected textContent='Hello World', got %q", textContent)
	}

	if summary["stream_chunk_count"].(int) != 1 {
		t.Errorf("expected chunk_count=1, got %d", summary["stream_chunk_count"])
	}
}

func TestObserveChunk_Delta_ReasoningContent(t *testing.T) {
	capture := NewStreamCapture()

	chunk := &ir.StreamChunk{
		Type: ir.ChunkTypeDelta,
		Delta: &ir.StreamDelta{
			ReasoningContent: "Let me think...",
		},
	}

	capture.ObserveChunk(chunk)

	summary := capture.SummaryAsMap()
	textContent := summary["stream_text_content"].(string)

	// Reasoning content should be wrapped in <reasoning> tags
	if !strings.Contains(textContent, "<reasoning>") {
		t.Errorf("expected textContent to contain <reasoning>, got %q", textContent)
	}
	if !strings.Contains(textContent, "Let me think...") {
		t.Errorf("expected textContent to contain reasoning text, got %q", textContent)
	}
	if !strings.Contains(textContent, "</reasoning>") {
		t.Errorf("expected textContent to contain </reasoning>, got %q", textContent)
	}
}

func TestObserveChunk_Delta_ToolCalls(t *testing.T) {
	capture := NewStreamCapture()

	chunk := &ir.StreamChunk{
		Type: ir.ChunkTypeDelta,
		Delta: &ir.StreamDelta{
			ToolCalls: []ir.StreamToolCallDelta{
				{
					Index:     0,
					ID:        "call_123",
					Name:      "get_weather",
					Arguments: `{"location":"NYC"}`,
				},
			},
		},
	}

	capture.ObserveChunk(chunk)

	summary := capture.SummaryAsMap()
	textContent := summary["stream_text_content"].(string)

	if !strings.Contains(textContent, "[Tool Call: get_weather]") {
		t.Errorf("expected textContent to contain tool call marker, got %q", textContent)
	}
	if !strings.Contains(textContent, `{"location":"NYC"}`) {
		t.Errorf("expected textContent to contain arguments, got %q", textContent)
	}
}

func TestObserveChunk_Usage(t *testing.T) {
	capture := NewStreamCapture()

	chunk := &ir.StreamChunk{
		Type: ir.ChunkTypeUsage,
		Usage: &ir.StreamUsage{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
		},
	}

	capture.ObserveChunk(chunk)

	summary := capture.SummaryAsMap()

	if summary["prompt_tokens"].(int) != 100 {
		t.Errorf("expected prompt_tokens=100, got %d", summary["prompt_tokens"])
	}
	if summary["completion_tokens"].(int) != 50 {
		t.Errorf("expected completion_tokens=50, got %d", summary["completion_tokens"])
	}
}

func TestObserveChunk_FinishReason(t *testing.T) {
	capture := NewStreamCapture()

	chunk := &ir.StreamChunk{
		Type:         ir.ChunkTypeDelta,
		Delta:        &ir.StreamDelta{Content: "Done"},
		FinishReason: "stop",
	}

	capture.ObserveChunk(chunk)

	summary := capture.SummaryAsMap()

	if summary["upstream_finish_reason"].(string) != "stop" {
		t.Errorf("expected finish_reason=stop, got %s", summary["upstream_finish_reason"])
	}
}

func TestObserveChunk_Done(t *testing.T) {
	capture := NewStreamCapture()

	chunk := &ir.StreamChunk{
		Type:         ir.ChunkTypeDone,
		FinishReason: "stop",
	}

	capture.ObserveChunk(chunk)

	summary := capture.SummaryAsMap()

	if !summary["stream_done_received"].(bool) {
		t.Error("expected stream_done_received=true")
	}
	if summary["upstream_finish_reason"].(string) != "stop" {
		t.Errorf("expected finish_reason=stop, got %s", summary["upstream_finish_reason"])
	}
}

func TestObserveChunk_Error(t *testing.T) {
	capture := NewStreamCapture()

	chunk := &ir.StreamChunk{
		Type: ir.ChunkTypeError,
		Error: &ir.StreamError{
			Type:    "timeout",
			Message: "Stream timeout",
			Code:    "stream_timeout",
		},
	}

	capture.ObserveChunk(chunk)

	summary := capture.SummaryAsMap()

	if !summary["stream_interrupted"].(bool) {
		t.Error("expected stream_interrupted=true")
	}
	if summary["upstream_finish_reason"].(string) != "timeout" {
		t.Errorf("expected finish_reason=timeout, got %s", summary["upstream_finish_reason"])
	}
}

func TestObserveChunk_MultipleChunks(t *testing.T) {
	capture := NewStreamCapture()

	// Simulate a real stream
	chunks := []*ir.StreamChunk{
		{
			Type:  ir.ChunkTypeDelta,
			Delta: &ir.StreamDelta{Role: "assistant"},
		},
		{
			Type:  ir.ChunkTypeDelta,
			Delta: &ir.StreamDelta{Content: "Hello"},
		},
		{
			Type:  ir.ChunkTypeDelta,
			Delta: &ir.StreamDelta{Content: " World"},
		},
		{
			Type: ir.ChunkTypeUsage,
			Usage: &ir.StreamUsage{
				PromptTokens:     10,
				CompletionTokens: 5,
			},
		},
		{
			Type:         ir.ChunkTypeDone,
			FinishReason: "stop",
		},
	}

	for _, chunk := range chunks {
		capture.ObserveChunk(chunk)
	}

	summary := capture.SummaryAsMap()

	if summary["stream_text_content"].(string) != "Hello World" {
		t.Errorf("expected textContent='Hello World', got %q", summary["stream_text_content"])
	}
	if summary["stream_chunk_count"].(int) != 5 {
		t.Errorf("expected chunk_count=5, got %d", summary["stream_chunk_count"])
	}
	if summary["prompt_tokens"].(int) != 10 {
		t.Errorf("expected prompt_tokens=10, got %d", summary["prompt_tokens"])
	}
	if summary["completion_tokens"].(int) != 5 {
		t.Errorf("expected completion_tokens=5, got %d", summary["completion_tokens"])
	}
	if !summary["stream_done_received"].(bool) {
		t.Error("expected stream_done_received=true")
	}
	if summary["upstream_finish_reason"].(string) != "stop" {
		t.Errorf("expected finish_reason=stop, got %s", summary["upstream_finish_reason"])
	}
}

func TestObserveChunk_LargeContent(t *testing.T) {
	capture := NewStreamCapture()

	// Simulate 35K+ tokens (回归 bug e0f10f81)
	longText := strings.Repeat("Lorem ipsum dolor sit amet, ", 3000) // ~84K chars

	// Split into chunks
	chunkSize := 100
	for i := 0; i < len(longText); i += chunkSize {
		end := i + chunkSize
		if end > len(longText) {
			end = len(longText)
		}
		chunk := &ir.StreamChunk{
			Type: ir.ChunkTypeDelta,
			Delta: &ir.StreamDelta{
				Content: longText[i:end],
			},
		}
		capture.ObserveChunk(chunk)
	}

	summary := capture.SummaryAsMap()
	textContent := summary["stream_text_content"].(string)

	// Should capture content up to maxTextContentBytes (2MB)
	if len(textContent) == 0 {
		t.Error("expected non-empty textContent")
	}

	// Should not exceed 2MB
	maxBytes := 2 * 1024 * 1024
	if len(textContent) > maxBytes {
		t.Errorf("textContent exceeds 2MB limit: %d bytes", len(textContent))
	}
}

func TestObserveChunk_Preview(t *testing.T) {
	capture := NewStreamCapture()

	chunk := &ir.StreamChunk{
		Type:  ir.ChunkTypeDelta,
		Delta: &ir.StreamDelta{Content: "Preview text"},
	}

	capture.ObserveChunk(chunk)

	summary := capture.SummaryAsMap()
	preview := summary["response_preview"].(string)

	if preview != "Preview text" {
		t.Errorf("expected preview='Preview text', got %q", preview)
	}
}

func TestObserveChunk_MixedContentAndReasoning(t *testing.T) {
	capture := NewStreamCapture()

	chunks := []*ir.StreamChunk{
		{
			Type:  ir.ChunkTypeDelta,
			Delta: &ir.StreamDelta{ReasoningContent: "Thinking step 1"},
		},
		{
			Type:  ir.ChunkTypeDelta,
			Delta: &ir.StreamDelta{ReasoningContent: "Thinking step 2"},
		},
		{
			Type:  ir.ChunkTypeDelta,
			Delta: &ir.StreamDelta{Content: "Final answer"},
		},
	}

	for _, chunk := range chunks {
		capture.ObserveChunk(chunk)
	}

	summary := capture.SummaryAsMap()
	textContent := summary["stream_text_content"].(string)

	// Should have both reasoning and content
	if !strings.Contains(textContent, "<reasoning>") {
		t.Error("expected reasoning markers")
	}
	if !strings.Contains(textContent, "Thinking step 1") {
		t.Error("expected first reasoning step")
	}
	if !strings.Contains(textContent, "Thinking step 2") {
		t.Error("expected second reasoning step")
	}
	if !strings.Contains(textContent, "Final answer") {
		t.Error("expected final answer")
	}
}

func TestObserveChunk_NilChunk(t *testing.T) {
	capture := NewStreamCapture()

	// Should not panic
	capture.ObserveChunk(nil)

	summary := capture.SummaryAsMap()
	if summary["stream_chunk_count"].(int) != 0 {
		t.Errorf("expected chunk_count=0 for nil chunk, got %d", summary["stream_chunk_count"])
	}
}

func TestObserveChunk_NilCapture(t *testing.T) {
	var capture *StreamCapture

	chunk := &ir.StreamChunk{
		Type:  ir.ChunkTypeDelta,
		Delta: &ir.StreamDelta{Content: "Test"},
	}

	// Should not panic
	capture.ObserveChunk(chunk)
}
