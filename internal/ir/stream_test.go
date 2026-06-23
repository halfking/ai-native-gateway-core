package ir

import (
	"encoding/json"
	"strings"
	"testing"
)

// ─── ParseOpenAIStreamChunk Tests ──────────────────────────────────────────

func TestParseOpenAIStreamChunk_Delta(t *testing.T) {
	line := `data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1234567890,"model":"gpt-4","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}`

	chunk, err := ParseOpenAIStreamChunk(line)
	if err != nil {
		t.Fatalf("ParseOpenAIStreamChunk failed: %v", err)
	}

	if chunk.Type != ChunkTypeDelta {
		t.Errorf("expected Type=delta, got %s", chunk.Type)
	}
	if chunk.ID != "chatcmpl-123" {
		t.Errorf("expected ID=chatcmpl-123, got %s", chunk.ID)
	}
	if chunk.Model != "gpt-4" {
		t.Errorf("expected Model=gpt-4, got %s", chunk.Model)
	}
	if chunk.Delta == nil {
		t.Fatal("Delta is nil")
	}
	if chunk.Delta.Content != "Hello" {
		t.Errorf("expected Content=Hello, got %s", chunk.Delta.Content)
	}
	if chunk.SourceProtocol != ProtocolOpenAIChat {
		t.Errorf("expected SourceProtocol=openai-chat, got %s", chunk.SourceProtocol)
	}
}

func TestParseOpenAIStreamChunk_Role(t *testing.T) {
	line := `data: {"id":"chatcmpl-123","choices":[{"delta":{"role":"assistant"}}]}`

	chunk, err := ParseOpenAIStreamChunk(line)
	if err != nil {
		t.Fatalf("ParseOpenAIStreamChunk failed: %v", err)
	}

	if chunk.Delta == nil || chunk.Delta.Role != "assistant" {
		t.Errorf("expected Role=assistant, got %v", chunk.Delta)
	}
}

func TestParseOpenAIStreamChunk_ReasoningContent(t *testing.T) {
	line := `data: {"choices":[{"delta":{"reasoning_content":"Let me think..."}}]}`

	chunk, err := ParseOpenAIStreamChunk(line)
	if err != nil {
		t.Fatalf("ParseOpenAIStreamChunk failed: %v", err)
	}

	if chunk.Delta == nil || chunk.Delta.ReasoningContent != "Let me think..." {
		t.Errorf("expected ReasoningContent, got %v", chunk.Delta)
	}
}

func TestParseOpenAIStreamChunk_ToolCalls(t *testing.T) {
	line := `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_123","type":"function","function":{"name":"get_weather","arguments":"{\"location\""}}]}}]}`

	chunk, err := ParseOpenAIStreamChunk(line)
	if err != nil {
		t.Fatalf("ParseOpenAIStreamChunk failed: %v", err)
	}

	if chunk.Delta == nil || len(chunk.Delta.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %v", chunk.Delta)
	}

	tc := chunk.Delta.ToolCalls[0]
	if tc.Index != 0 {
		t.Errorf("expected Index=0, got %d", tc.Index)
	}
	if tc.ID != "call_123" {
		t.Errorf("expected ID=call_123, got %s", tc.ID)
	}
	if tc.Name != "get_weather" {
		t.Errorf("expected Name=get_weather, got %s", tc.Name)
	}
	if !strings.Contains(tc.Arguments, "location") {
		t.Errorf("expected Arguments to contain 'location', got %s", tc.Arguments)
	}
}

func TestParseOpenAIStreamChunk_FinishReason(t *testing.T) {
	line := `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`

	chunk, err := ParseOpenAIStreamChunk(line)
	if err != nil {
		t.Fatalf("ParseOpenAIStreamChunk failed: %v", err)
	}

	if chunk.FinishReason != "stop" {
		t.Errorf("expected FinishReason=stop, got %s", chunk.FinishReason)
	}
}

func TestParseOpenAIStreamChunk_Usage(t *testing.T) {
	line := `data: {"choices":[{"delta":{}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`

	chunk, err := ParseOpenAIStreamChunk(line)
	if err != nil {
		t.Fatalf("ParseOpenAIStreamChunk failed: %v", err)
	}

	if chunk.Type != ChunkTypeUsage {
		t.Errorf("expected Type=usage, got %s", chunk.Type)
	}
	if chunk.Usage == nil {
		t.Fatal("Usage is nil")
	}
	if chunk.Usage.PromptTokens != 10 {
		t.Errorf("expected PromptTokens=10, got %d", chunk.Usage.PromptTokens)
	}
	if chunk.Usage.CompletionTokens != 5 {
		t.Errorf("expected CompletionTokens=5, got %d", chunk.Usage.CompletionTokens)
	}
	if chunk.Usage.TotalTokens != 15 {
		t.Errorf("expected TotalTokens=15, got %d", chunk.Usage.TotalTokens)
	}
}

func TestParseOpenAIStreamChunk_Done(t *testing.T) {
	line := `data: [DONE]`

	chunk, err := ParseOpenAIStreamChunk(line)
	if err != nil {
		t.Fatalf("ParseOpenAIStreamChunk failed: %v", err)
	}

	if chunk.Type != ChunkTypeDone {
		t.Errorf("expected Type=done, got %s", chunk.Type)
	}
}

// ─── ParseAnthropicStreamEvent Tests ────────────────────────────────────────

func TestParseAnthropicStreamEvent_MessageStart(t *testing.T) {
	data := []byte(`{"type":"message_start","message":{"id":"msg_123","model":"claude-3","usage":{"input_tokens":10}}}`)

	chunk, err := ParseAnthropicStreamEvent("message_start", data)
	if err != nil {
		t.Fatalf("ParseAnthropicStreamEvent failed: %v", err)
	}

	if chunk.Type != ChunkTypeUsage {
		t.Errorf("expected Type=usage, got %s", chunk.Type)
	}
	if chunk.ID != "msg_123" {
		t.Errorf("expected ID=msg_123, got %s", chunk.ID)
	}
	if chunk.Model != "claude-3" {
		t.Errorf("expected Model=claude-3, got %s", chunk.Model)
	}
	if chunk.Usage == nil || chunk.Usage.PromptTokens != 10 {
		t.Errorf("expected PromptTokens=10, got %v", chunk.Usage)
	}
	if chunk.SourceProtocol != ProtocolAnthropicMessages {
		t.Errorf("expected SourceProtocol=anthropic-messages, got %s", chunk.SourceProtocol)
	}
}

func TestParseAnthropicStreamEvent_ContentBlockDelta_Text(t *testing.T) {
	data := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`)

	chunk, err := ParseAnthropicStreamEvent("content_block_delta", data)
	if err != nil {
		t.Fatalf("ParseAnthropicStreamEvent failed: %v", err)
	}

	if chunk.Type != ChunkTypeDelta {
		t.Errorf("expected Type=delta, got %s", chunk.Type)
	}
	if chunk.Delta == nil || chunk.Delta.Content != "Hello" {
		t.Errorf("expected Content=Hello, got %v", chunk.Delta)
	}
}

func TestParseAnthropicStreamEvent_ContentBlockDelta_Thinking(t *testing.T) {
	data := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me think..."}}`)

	chunk, err := ParseAnthropicStreamEvent("content_block_delta", data)
	if err != nil {
		t.Fatalf("ParseAnthropicStreamEvent failed: %v", err)
	}

	if chunk.Delta == nil || chunk.Delta.ReasoningContent != "Let me think..." {
		t.Errorf("expected ReasoningContent, got %v", chunk.Delta)
	}
}

func TestParseAnthropicStreamEvent_ContentBlockStart_ToolUse(t *testing.T) {
	data := []byte(`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_123","name":"get_weather","input":{}}}`)

	chunk, err := ParseAnthropicStreamEvent("content_block_start", data)
	if err != nil {
		t.Fatalf("ParseAnthropicStreamEvent failed: %v", err)
	}

	if chunk.Delta == nil || len(chunk.Delta.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %v", chunk.Delta)
	}

	tc := chunk.Delta.ToolCalls[0]
	if tc.Index != 1 {
		t.Errorf("expected Index=1, got %d", tc.Index)
	}
	if tc.ID != "toolu_123" {
		t.Errorf("expected ID=toolu_123, got %s", tc.ID)
	}
	if tc.Name != "get_weather" {
		t.Errorf("expected Name=get_weather, got %s", tc.Name)
	}
}

func TestParseAnthropicStreamEvent_MessageDelta(t *testing.T) {
	data := []byte(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":20}}`)

	chunk, err := ParseAnthropicStreamEvent("message_delta", data)
	if err != nil {
		t.Fatalf("ParseAnthropicStreamEvent failed: %v", err)
	}

	if chunk.Type != ChunkTypeUsage {
		t.Errorf("expected Type=usage, got %s", chunk.Type)
	}
	if chunk.Usage == nil || chunk.Usage.CompletionTokens != 20 {
		t.Errorf("expected CompletionTokens=20, got %v", chunk.Usage)
	}
	if chunk.FinishReason != "stop" {
		t.Errorf("expected FinishReason=stop (mapped from end_turn), got %s", chunk.FinishReason)
	}
}

func TestParseAnthropicStreamEvent_MessageStop(t *testing.T) {
	data := []byte(`{"type":"message_stop"}`)

	chunk, err := ParseAnthropicStreamEvent("message_stop", data)
	if err != nil {
		t.Fatalf("ParseAnthropicStreamEvent failed: %v", err)
	}

	if chunk.Type != ChunkTypeDone {
		t.Errorf("expected Type=done, got %s", chunk.Type)
	}
}

func TestParseAnthropicStreamEvent_Error(t *testing.T) {
	data := []byte(`{"type":"error","error":{"type":"overloaded_error","message":"System overloaded"}}`)

	chunk, err := ParseAnthropicStreamEvent("error", data)
	if err != nil {
		t.Fatalf("ParseAnthropicStreamEvent failed: %v", err)
	}

	if chunk.Type != ChunkTypeError {
		t.Errorf("expected Type=error, got %s", chunk.Type)
	}
	if chunk.Error == nil {
		t.Fatal("Error is nil")
	}
	if chunk.Error.Type != "overloaded_error" {
		t.Errorf("expected Error.Type=overloaded_error, got %s", chunk.Error.Type)
	}
	if chunk.Error.Message != "System overloaded" {
		t.Errorf("expected Error.Message=System overloaded, got %s", chunk.Error.Message)
	}
}

// ─── Serialization Tests ────────────────────────────────────────────────────

func TestStreamChunk_SerializeOpenAI_Delta(t *testing.T) {
	chunk := &StreamChunk{
		Type: ChunkTypeDelta,
		Delta: &StreamDelta{
			Role:    "assistant",
			Content: "Hello",
		},
		SourceProtocol: ProtocolOpenAIChat,
	}

	output := chunk.SerializeOpenAI("chatcmpl-123", "gpt-4", 1234567890)

	if !strings.HasPrefix(output, "data: ") {
		t.Errorf("expected output to start with 'data: ', got %s", output)
	}
	if !strings.HasSuffix(output, "\n\n") {
		t.Errorf("expected output to end with \\n\\n, got %s", output)
	}

	// Parse JSON payload
	payload := strings.TrimPrefix(output, "data: ")
	payload = strings.TrimSuffix(payload, "\n\n")

	var parsed map[string]any
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if parsed["id"] != "chatcmpl-123" {
		t.Errorf("expected id=chatcmpl-123, got %v", parsed["id"])
	}
	if parsed["model"] != "gpt-4" {
		t.Errorf("expected model=gpt-4, got %v", parsed["model"])
	}

	choices, ok := parsed["choices"].([]any)
	if !ok || len(choices) == 0 {
		t.Fatalf("expected choices array, got %v", parsed["choices"])
	}

	choice := choices[0].(map[string]any)
	delta := choice["delta"].(map[string]any)
	if delta["role"] != "assistant" {
		t.Errorf("expected role=assistant, got %v", delta["role"])
	}
	if delta["content"] != "Hello" {
		t.Errorf("expected content=Hello, got %v", delta["content"])
	}
}

func TestStreamChunk_SerializeOpenAI_Done(t *testing.T) {
	chunk := &StreamChunk{
		Type:           ChunkTypeDone,
		SourceProtocol: ProtocolOpenAIChat,
	}

	output := chunk.SerializeOpenAI("", "", 0)

	expected := "data: [DONE]\n\n"
	if output != expected {
		t.Errorf("expected %q, got %q", expected, output)
	}
}

func TestStreamChunk_SerializeAnthropic_MessageStart(t *testing.T) {
	chunk := &StreamChunk{
		Type: ChunkTypeUsage,
		Usage: &StreamUsage{
			PromptTokens: 10,
		},
		ID:             "msg_123",
		Model:          "claude-3",
		SourceProtocol: ProtocolAnthropicMessages,
	}

	output := chunk.SerializeAnthropic("msg_123", "claude-3")

	if !strings.Contains(output, "event: message_start") {
		t.Errorf("expected output to contain 'event: message_start', got %s", output)
	}
	if !strings.Contains(output, `"type":"message_start"`) {
		t.Errorf("expected JSON to contain type=message_start, got %s", output)
	}
	if !strings.Contains(output, `"input_tokens":10`) {
		t.Errorf("expected JSON to contain input_tokens=10, got %s", output)
	}
}

func TestStreamChunk_SerializeAnthropic_ContentDelta(t *testing.T) {
	chunk := &StreamChunk{
		Type: ChunkTypeDelta,
		Delta: &StreamDelta{
			Content: "Hello",
		},
		SourceProtocol: ProtocolAnthropicMessages,
	}

	output := chunk.SerializeAnthropic("", "")

	if !strings.Contains(output, "event: content_block_delta") {
		t.Errorf("expected output to contain 'event: content_block_delta', got %s", output)
	}
	if !strings.Contains(output, `"text_delta"`) {
		t.Errorf("expected JSON to contain text_delta, got %s", output)
	}
	if !strings.Contains(output, `"text":"Hello"`) {
		t.Errorf("expected JSON to contain text=Hello, got %s", output)
	}
}

func TestStreamChunk_SerializeAnthropic_Done(t *testing.T) {
	chunk := &StreamChunk{
		Type:           ChunkTypeDone,
		SourceProtocol: ProtocolAnthropicMessages,
	}

	output := chunk.SerializeAnthropic("", "")

	expected := "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	if output != expected {
		t.Errorf("expected %q, got %q", expected, output)
	}
}

// ─── Round-trip Tests ───────────────────────────────────────────────────────

func TestRoundTrip_OpenAI_ToOpenAI(t *testing.T) {
	original := `data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1234567890,"model":"gpt-4","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}`

	// Parse
	chunk, err := ParseOpenAIStreamChunk(original)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	// Serialize
	serialized := chunk.SerializeOpenAI("chatcmpl-123", "gpt-4", 1234567890)

	// Extract JSON payloads
	origJSON := strings.TrimPrefix(strings.TrimSuffix(original, "\n\n"), "data: ")
	serJSON := strings.TrimPrefix(strings.TrimSuffix(serialized, "\n\n"), "data: ")

	// Compare JSON (order-independent)
	var origMap, serMap map[string]any
	json.Unmarshal([]byte(origJSON), &origMap)
	json.Unmarshal([]byte(serJSON), &serMap)

	origBytes, _ := json.Marshal(origMap)
	serBytes, _ := json.Marshal(serMap)

	if string(origBytes) != string(serBytes) {
		t.Errorf("round-trip mismatch:\noriginal: %s\nserialized: %s", origBytes, serBytes)
	}
}

func TestRoundTrip_Anthropic_ToAnthropic(t *testing.T) {
	data := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`)

	// Parse
	chunk, err := ParseAnthropicStreamEvent("content_block_delta", data)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	// Serialize
	serialized := chunk.SerializeAnthropic("msg_123", "claude-3")

	// Should contain the same text
	if !strings.Contains(serialized, "Hello") {
		t.Errorf("serialized output should contain 'Hello', got %s", serialized)
	}
	if !strings.Contains(serialized, "text_delta") {
		t.Errorf("serialized output should contain 'text_delta', got %s", serialized)
	}
}

func TestCrossProtocol_Anthropic_ToOpenAI(t *testing.T) {
	// Anthropic text delta
	data := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`)

	// Parse Anthropic
	chunk, err := ParseAnthropicStreamEvent("content_block_delta", data)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	// Serialize as OpenAI
	openaiOutput := chunk.SerializeOpenAI("chatcmpl-123", "gpt-4", 1234567890)

	// Should be valid OpenAI format
	if !strings.HasPrefix(openaiOutput, "data: ") {
		t.Errorf("expected OpenAI format to start with 'data: ', got %s", openaiOutput)
	}

	// Parse the JSON
	payload := strings.TrimPrefix(strings.TrimSuffix(openaiOutput, "\n\n"), "data: ")
	var parsed map[string]any
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		t.Fatalf("failed to parse OpenAI JSON: %v", err)
	}

	// Verify content
	choices := parsed["choices"].([]any)
	choice := choices[0].(map[string]any)
	delta := choice["delta"].(map[string]any)
	if delta["content"] != "Hello" {
		t.Errorf("expected content=Hello, got %v", delta["content"])
	}
}

func TestCrossProtocol_OpenAI_ToAnthropic(t *testing.T) {
	// OpenAI delta
	line := `data: {"choices":[{"delta":{"content":"Hello"}}]}`

	// Parse OpenAI
	chunk, err := ParseOpenAIStreamChunk(line)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	// Serialize as Anthropic
	anthropicOutput := chunk.SerializeAnthropic("msg_123", "claude-3")

	// Should be valid Anthropic format
	if !strings.Contains(anthropicOutput, "event: content_block_delta") {
		t.Errorf("expected Anthropic format to contain 'event: content_block_delta', got %s", anthropicOutput)
	}
	if !strings.Contains(anthropicOutput, `"text":"Hello"`) {
		t.Errorf("expected content to contain Hello, got %s", anthropicOutput)
	}
}

// ─── Finish Reason Mapping Tests ───────────────────────────────────────────

func TestFinishReasonMapping_AnthropicToOpenAI(t *testing.T) {
	tests := []struct {
		anthropic string
		openai    string
	}{
		{"end_turn", "stop"},
		{"tool_use", "tool_calls"},
		{"max_tokens", "length"},
		{"stop_sequence", "stop"},
		{"refusal", "content_filter"},
		{"unknown", "stop"},
	}

	for _, tt := range tests {
		result := mapAnthropicFinishReasonToOpenAI(tt.anthropic)
		if result != tt.openai {
			t.Errorf("mapAnthropicFinishReasonToOpenAI(%q) = %q, want %q", tt.anthropic, result, tt.openai)
		}
	}
}

func TestFinishReasonMapping_OpenAIToAnthropic(t *testing.T) {
	tests := []struct {
		openai    string
		anthropic string
	}{
		{"stop", "end_turn"},
		{"tool_calls", "tool_use"},
		{"length", "max_tokens"},
		{"content_filter", "refusal"},
		{"unknown", "end_turn"},
	}

	for _, tt := range tests {
		result := mapOpenAIFinishReasonToAnthropic(tt.openai)
		if result != tt.anthropic {
			t.Errorf("mapOpenAIFinishReasonToAnthropic(%q) = %q, want %q", tt.openai, result, tt.anthropic)
		}
	}
}
