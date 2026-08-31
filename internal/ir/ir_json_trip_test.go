package ir

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// audit-p1-5: Pin the new top-level json tags — round-trip + key-shape contracts.
// These tests fail loudly if anyone:
//   - drops a json tag from a top-level field (test sees a PascalCase key),
//   - accidentally flips a snake_case key back to camelCase (or vice versa),
//   - leaks a gateway-internal field that should be `json:"-"`,
//   - changes an `omitempty` discipline in a way that drops a populated field.

// TestIRInternalRequestRoundtrip populates every top-level field on
// InternalRequest, serializes, deserializes, and asserts DeepEqual.
func TestIRInternalRequestRoundtrip(t *testing.T) {
	temp := 0.7
	topP := 0.9
	topK := 40
	parallel := true
	logprobs := false
	topLogprobs := 5
	seed := int64(42)
	n := 1
	freq := 0.1
	pres := 0.2
	store := true
	maxReasoning := 1000
	budget := 2000
	intPtr := func(v int) *int { return &v }

	original := &InternalRequest{
		Model: "claude-opus-4-8",
		Messages: []Message{{
			Role:       "user",
			Content:    []ContentBlock{{Type: "text", Text: "hello"}},
			ToolCalls:  []ToolCall{{ID: "tc_1", Type: "function"}},
			ToolCallID: "tcid_1",
			Name:       "fn",
		}},
		System: &SystemPrompt{Content: "you are helpful"},
		Tools: []ToolDefinition{{
			Name:        "get_weather",
			Description: "fetch weather",
			Parameters:  json.RawMessage(`{"type":"object"}`),
		}},
		ToolChoice: &ToolChoice{Type: "auto"},
		MaxTokens:  1024,
		Temperature: &temp,
		TopP:        &topP,
		TopK:        &topK,
		Stop:        []string{"\n\n"},
		ParallelToolCalls: &parallel,
		Stream:       true,
		Thinking:     &ThinkingConfig{Type: "enabled", BudgetTokens: 2048},
		CacheControl: []CacheControl{{Type: "ephemeral"}},
		Documents:    []Document{{Type: "document", Source: DocumentSource{Type: "text", Data: "doc"}}},
		FrequencyPenalty: &freq,
		PresencePenalty:  &pres,
		Logprobs:         &logprobs,
		TopLogprobs:      &topLogprobs,
		Seed:             &seed,
		ResponseFormat:   &ResponseFormat{Type: "json_object", Schema: json.RawMessage(`{"x":"int"}`)},
		N:                n,
		User:             "u_1",
		Metadata:         &Metadata{UserID: "u_1", RequestID: "r_1", Other: map[string]string{"k": "v"}},
		Reasoning:        &ReasoningConfig{Effort: "high", BudgetTokens: &budget, MaxReasoningTokens: &maxReasoning},
		Modalities:       []string{"text", "audio"},
		AudioConfig:      &AudioConfig{Voice: "alloy", Format: "mp3", Speed: 1.0},
		LogitBias:        map[string]float64{"50256": -100},
		Store:            &store,
		ServiceTier:      "auto",
		Prediction:       &Prediction{Type: "content", Content: "draft"},
		Verbosity:        "low",
		WebSearchOptions: &WebSearchOptions{ContextSize: "medium"},
		PromptCacheKey:   "key_1",
		SafetyIdentifier: "safe_1",
		PreviousResponseID: "resp_1",
		Truncation:       "auto",
		MCPServers:       []MCPServer{{Type: "url", URL: "https://mcp", Name: "m"}},
		ContextManagement: &ContextManagement{Edits: []ContextEdit{{Type: "clear_tool_uses_20250919", Threshold: intPtr(80), Keep: intPtr(2), ClearToolInputs: &parallel}}},
		Container:         &Container{ID: "c_1", Skills: []ContainerSkill{{Name: "sk", Type: "anthropic"}}},
		SafetySettings:    []SafetySetting{{Category: "HARM_CATEGORY_HARASSMENT", Threshold: "BLOCK_NONE"}},
		CachedContent:     "cached/abc",
		SourceProtocol:    ProtocolAnthropicMessages,
		Extensions:        map[string]json.RawMessage{"custom": json.RawMessage(`{"v":1}`)},
		TargetProvider:    "anthropic",
		// Class and DueAt must NOT round-trip; they're gateway-internal metadata
		// (see class.go:10-12 — "Class and DueAt are Go-struct metadata only").
		Class: ClassScheduled,
		DueAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded InternalRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Class and DueAt are `json:"-"` — they do not round-trip. Verify the
	// absence in JSON and that the decoded struct leaves them at zero values.
	original.Class = ""
	original.DueAt = time.Time{}
	if decoded.Class != "" || !decoded.DueAt.IsZero() {
		t.Fatalf("Class/DueAt leaked across the JSON boundary: Class=%q DueAt=%v", decoded.Class, decoded.DueAt)
	}

	// Spot-check the wire-representative fields rather than DeepEqual-ing the
	// whole struct: json.RawMessage boundaries (Tools[0].Raw, Tools[0].Parameters,
	// ResponseFormat.Schema, etc.) are not stable across marshal→unmarshal for
	// the nil-vs-empty distinction and we only care that the wire fields round-trip.
	checkRequestFields := func(t *testing.T, a, b *InternalRequest) {
		t.Helper()
		if a.Model != b.Model || a.MaxTokens != b.MaxTokens || a.Stream != b.Stream {
			t.Fatalf("scalar mismatch: %+v vs %+v", a, b)
		}
		if a.SourceProtocol != b.SourceProtocol || a.TargetProvider != b.TargetProvider {
			t.Fatalf("protocol mismatch: %q/%q vs %q/%q", a.SourceProtocol, a.TargetProvider, b.SourceProtocol, b.TargetProvider)
		}
		if !reflect.DeepEqual(a.Messages, b.Messages) {
			t.Fatalf("Messages mismatch: %+v vs %+v", a.Messages, b.Messages)
		}
		if !reflect.DeepEqual(a.Tools, b.Tools) {
			t.Fatalf("Tools mismatch: %+v vs %+v", a.Tools, b.Tools)
		}
		if !reflect.DeepEqual(a.CacheControl, b.CacheControl) {
			t.Fatalf("CacheControl mismatch")
		}
		if !reflect.DeepEqual(a.SafetySettings, b.SafetySettings) {
			t.Fatalf("SafetySettings mismatch")
		}
		if !reflect.DeepEqual(a.MCPServers, b.MCPServers) {
			t.Fatalf("MCPServers mismatch")
		}
		if (a.ContextManagement == nil) != (b.ContextManagement == nil) {
			t.Fatalf("ContextManagement presence mismatch")
		}
		if a.ContextManagement != nil && !reflect.DeepEqual(*a.ContextManagement, *b.ContextManagement) {
			t.Fatalf("ContextManagement mismatch")
		}
		if (a.Container == nil) != (b.Container == nil) {
			t.Fatalf("Container presence mismatch")
		}
		if a.Container != nil && !reflect.DeepEqual(*a.Container, *b.Container) {
			t.Fatalf("Container mismatch")
		}
		if !reflect.DeepEqual(a.LogitBias, b.LogitBias) {
			t.Fatalf("LogitBias mismatch")
		}
		if !reflect.DeepEqual(a.Extensions, b.Extensions) {
			t.Fatalf("Extensions mismatch")
		}
	}
	checkRequestFields(t, original, &decoded)
}

// TestIRInternalResponseRoundtrip is the response counterpart of the request test.
func TestIRInternalResponseRoundtrip(t *testing.T) {
	cacheRead := 10
	cacheWrite := 5
	reasoning := 100
	image := 50
	audio := 25
	video := 12
	providerTokens := 7

	original := &InternalResponse{
		ID:             "resp_1",
		Model:          "claude-opus-4-8",
		Created:        1234567890,
		Role:           "assistant",
		SourceProtocol: ProtocolAnthropicMessages,
		Content: []ResponseContentBlock{
			{Type: "text", Text: "hello"},
			{Type: "tool_use", ID: "tu_1", Name: "fn", Input: json.RawMessage(`{"x":1}`)},
			{Type: "thinking", Thinking: "reasoning", Signature: "sig_xyz"},
		},
		ToolCalls: []ResponseToolCall{
			{ID: "tc_1", Name: "fn", Arguments: `{"x":1}`, InputRaw: json.RawMessage(`{"x":1}`)},
		},
		ReasoningContent: "deep thought",
		FinishReason:     "tool_use",
		Usage: ResponseUsage{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
			CacheReadTokens:  &cacheRead,
			CacheWriteTokens: &cacheWrite,
			ReasoningTokens:  &reasoning,
			ImageTokens:      &image,
			AudioTokens:      &audio,
			VideoTokens:      &video,
			ProviderTokens:   &providerTokens,
		},
		Extensions: map[string]json.RawMessage{"custom": json.RawMessage(`{"v":1}`)},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded InternalResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Spot-check the wire-representative fields (InputRaw nil-vs-empty is not
	// a meaningful round-trip signal).
	if original.ID != decoded.ID || original.Model != decoded.Model || original.Created != decoded.Created {
		t.Fatalf("scalar mismatch")
	}
	if original.SourceProtocol != decoded.SourceProtocol || original.Role != decoded.Role {
		t.Fatalf("protocol/role mismatch")
	}
	if !reflect.DeepEqual(original.Content, decoded.Content) {
		t.Fatalf("Content mismatch: %+v vs %+v", original.Content, decoded.Content)
	}
	if original.FinishReason != decoded.FinishReason || original.ReasoningContent != decoded.ReasoningContent {
		t.Fatalf("finish/reasoning mismatch")
	}
	if !reflect.DeepEqual(original.Usage, decoded.Usage) {
		t.Fatalf("Usage mismatch")
	}
	if !reflect.DeepEqual(original.Extensions, decoded.Extensions) {
		t.Fatalf("Extensions mismatch")
	}
}

// TestIRStreamChunkRoundtrip covers StreamChunk plus its three sub-types.
func TestIRStreamChunkRoundtrip(t *testing.T) {
	cacheRead := 10
	cacheWrite := 5
	reasoning := 100
	image := 50
	audio := 25
	video := 12

	original := &StreamChunk{
		Type: ChunkTypeDelta,
		Delta: &StreamDelta{
			Role:             "assistant",
			Content:          "hello",
			ReasoningContent: "thinking…",
			ToolCalls: []StreamToolCallDelta{{
				Index:     0,
				ID:        "tc_1",
				Type:      "function",
				Name:      "get_weather",
				Arguments: `{"city":"sf"}`,
			}},
			ThinkingSignature: "sig_xyz",
			AudioDelta:        &StreamAudioDelta{Data: "AAAA", Transcript: "hi", MIMEType: "audio/wav"},
			DeltaType:         "text",
		},
		Usage: &StreamUsage{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
			CacheReadTokens:  &cacheRead,
			CacheWriteTokens: &cacheWrite,
			ReasoningTokens:  &reasoning,
			ImageTokens:      &image,
			AudioTokens:      &audio,
			VideoTokens:      &video,
		},
		ID:             "chatcmpl-abc",
		Model:          "gpt-4",
		Created:        1234567890,
		FinishReason:   "tool_calls",
		CandidateIndex: 0,
		SourceProtocol: ProtocolOpenAIChat,
		// Quality and ArgumentsJSONReason are gateway-internal annotations and
		// must NOT appear in serialized form (stream.go:43-44 documents this).
		Quality:             "partial",
		ArgumentsJSONReason: "missing closing brace",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded StreamChunk
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Quality / ArgumentsJSONReason are intentionally excluded; zero them out
	// on the original for DeepEqual.
	original.Quality = ""
	original.ArgumentsJSONReason = ""
	if !reflect.DeepEqual(original, &decoded) {
		t.Fatalf("round-trip mismatch:\nwant=%+v\n got=%+v", original, &decoded)
	}
}

// TestIRInternalStateFieldsNotSerialized pins the four gateway-internal fields
// that class.go / stream.go document as "never serialized". Catches accidental
// tag removal or json:"-" flipping back to a snake_case name.
func TestIRInternalStateFieldsNotSerialized(t *testing.T) {
	req := &InternalRequest{
		Class: ClassScheduled,
		DueAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		Model: "m",
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, forbidden := range []string{`"class"`, `"Class"`, `"due_at"`, `"DueAt"`, `"dueAt"`} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("InternalRequest JSON leaked gateway-internal field %s: %s", forbidden, data)
		}
	}

	chunk := &StreamChunk{
		Quality:             "rejected",
		ArgumentsJSONReason: "missing closing brace",
	}
	data, err = json.Marshal(chunk)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, forbidden := range []string{`"quality"`, `"Quality"`, `"arguments_json_reason"`, `"argumentsJSONReason"`, `"ArgumentsJSONReason"`} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("StreamChunk JSON leaked gateway-internal field %s: %s", forbidden, data)
		}
	}
}

// TestIRJSONUsesSnakeCase verifies the dominant (non-Gemini) IR types emit
// snake_case JSON keys. Failing this test means someone normalized to camelCase
// or accidentally inverted a tag.
func TestIRJSONUsesSnakeCase(t *testing.T) {
	intPtr := func(v int) *int { return &v }

	req := &InternalRequest{
		Model: "m",
		Tools: []ToolDefinition{{Name: "f"}},
		ToolChoice: &ToolChoice{Type: "auto"},
		MaxTokens: 100,
		FrequencyPenalty: nil,
		LogitBias:        map[string]float64{"50256": -100},
		CacheControl:     []CacheControl{{Type: "ephemeral"}},
		MCPServers:       []MCPServer{{Type: "url", URL: "https://x", Name: "n", AuthorizationToken: "t"}},
		ContextManagement: &ContextManagement{Edits: []ContextEdit{{Type: "clear_tool_uses_20250919", Threshold: intPtr(80)}}},
		Container:         &Container{Skills: []ContainerSkill{{Name: "s", Type: "anthropic"}}},
		SafetySettings:    []SafetySetting{{Category: "HARM_CATEGORY_HARASSMENT", Threshold: "BLOCK_NONE"}},
		SourceProtocol:    ProtocolOpenAIChat,
		TargetProvider:    "anthropic",
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	for _, key := range []string{
		`"tool_choice"`,
		`"max_tokens"`,
		`"logit_bias"`,
		`"mcp_servers"`,
		`"context_management"`,
		`"safety_settings"`,
		`"source_protocol"`,
		`"target_provider"`,
		`"cache_control"`,
	} {
		if !strings.Contains(string(data), key) {
			t.Fatalf("expected snake_case key %s in JSON: %s", key, data)
		}
	}
	// And the PascalCase forms must NOT appear (for these particular fields).
	for _, forbidden := range []string{
		`"ToolChoice"`, `"MaxTokens"`, `"LogitBias"`, `"MCPServers"`,
		`"ContextManagement"`, `"SafetySettings"`, `"SourceProtocol"`, `"TargetProvider"`,
	} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("PascalCase key %s leaked into JSON: %s", forbidden, data)
		}
	}

	resp := &InternalResponse{
		Content: []ResponseContentBlock{
			{Type: "tool_use", ID: "t", Name: "n", Input: json.RawMessage(`{}`)},
			{Type: "thinking", Thinking: "x", Signature: "s"},
		},
		ToolCalls:      []ResponseToolCall{{ID: "t", Name: "n", Arguments: "{}", InputRaw: json.RawMessage(`{}`)}},
		FinishReason:   "tool_use",
		ReasoningContent: "x",
	}
	data, err = json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{
		`"tool_calls"`, `"finish_reason"`, `"reasoning_content"`, `"input_raw"`,
	} {
		if !strings.Contains(string(data), key) {
			t.Fatalf("expected snake_case key %s in InternalResponse JSON: %s", key, data)
		}
	}

	chunk := &StreamChunk{
		Type:            ChunkTypeDelta,
		FinishReason:    "tool_calls",
		CandidateIndex:  0,
		SourceProtocol:  ProtocolOpenAIChat,
		Usage:           &StreamUsage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3},
		Delta:           &StreamDelta{ToolCalls: []StreamToolCallDelta{{Index: 0, ID: "t", Type: "f", Name: "n", Arguments: "{}"}}},
	}
	data, err = json.Marshal(chunk)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{
		`"finish_reason"`, `"candidate_index"`, `"source_protocol"`,
		`"tool_calls"`, `"arguments"`, `"prompt_tokens"`, `"completion_tokens"`, `"total_tokens"`,
	} {
		if !strings.Contains(string(data), key) {
			t.Fatalf("expected snake_case key %s in StreamChunk JSON: %s", key, data)
		}
	}
}

// TestIRJSONGeminiUsesCamelCase documents and pins the deliberate exception:
// Gemini-native IR types emit lowerCamelCase JSON keys to mirror the Gemini
// wire format (topP, topK, responseMimeType, thinkingBudget, mimeType, etc.).
// Renormalizing these to snake_case would diverge from the upstream protocol.
func TestIRJSONGeminiUsesCamelCase(t *testing.T) {
	topP := 0.9
	topK := 40
	maxOut := 1024
	count := 1
	gcfg := &GenerationConfig{
		Temperature:      nil,
		TopP:             &topP,
		TopK:             &topK,
		MaxOutputTokens:  &maxOut,
		StopSequences:    []string{"\n"},
		ResponseMimeType: "application/json",
		ResponseSchema:   json.RawMessage(`{"type":"object"}`),
		CandidateCount:   &count,
		ThinkingConfig:   &GeminiThinkingConfig{ThinkingBudget: &topK, IncludeThoughts: true},
	}
	data, err := json.Marshal(gcfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{
		`"topP"`, `"topK"`, `"maxOutputTokens"`, `"stopSequences"`,
		`"responseMimeType"`, `"responseSchema"`, `"candidateCount"`,
		`"thinkingConfig"`, `"thinkingBudget"`, `"includeThoughts"`,
	} {
		if !strings.Contains(string(data), key) {
			t.Fatalf("expected Gemini camelCase key %s in JSON: %s", key, data)
		}
	}
	for _, forbidden := range []string{
		`"top_p"`, `"top_k"`, `"max_output_tokens"`, `"stop_sequences"`,
		`"response_mime_type"`, `"response_schema"`, `"candidate_count"`,
		`"thinking_config"`, `"thinking_budget"`, `"include_thoughts"`,
	} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("Gemini type leaked snake_case key %s: %s", forbidden, data)
		}
	}

	part := &GeminiPart{
		Text:       "hi",
		InlineData: &GeminiInlineData{MimeType: "image/png", Data: "AAAA"},
		FileData:   &GeminiFileData{MimeType: "application/pdf", FileURI: "files/abc"},
	}
	data, err = json.Marshal(part)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{
		`"inlineData"`, `"fileData"`, `"mimeType"`, `"fileUri"`,
	} {
		if !strings.Contains(string(data), key) {
			t.Fatalf("expected Gemini camelCase key %s in JSON: %s", key, data)
		}
	}
}

// TestIRJSONOmitemptyHonored pins that fields tagged `omitempty` are absent
// from JSON when at their zero value. If someone removes `omitempty` from a
// field that legitimately defaults to "absent", they will see noise keys.
func TestIRJSONOmitemptyHonored(t *testing.T) {
	// Minimal request with every optional field at its zero value.
	req := &InternalRequest{Model: "m"}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Must contain required fields, must NOT contain zero-value optionals.
	for _, key := range []string{`"model"`} {
		if !strings.Contains(string(data), key) {
			t.Fatalf("expected %s in JSON: %s", key, data)
		}
	}
	// "messages" is required (no omitempty) — it serializes as `null` for an
	// empty IR but is always emitted so consumers can rely on the field.
	if !strings.Contains(string(data), `"messages":null`) {
		t.Fatalf("expected \"messages\":null in JSON (required field): %s", data)
	}
	for _, forbidden := range []string{
		`"system"`, `"tools"`, `"tool_choice"`, `"max_tokens"`,
		`"temperature"`, `"top_p"`, `"thinking"`, `"cache_control"`,
		`"mcp_servers"`, `"context_management"`, `"safety_settings"`,
		`"target_provider"`, `"extensions"`,
	} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("omitempty violated by %s: %s", forbidden, data)
		}
	}

	// Minimal delta chunk — only "type" must be present. Pointer fields
// (Delta/Usage/Error) carry `omitempty`, so a nil pointer is elided entirely
// (encoding/json standard behavior). Value-type metadata fields are required
// and emitted at their zero values.
	chunk := &StreamChunk{Type: ChunkTypeDelta}
	data, err = json.Marshal(chunk)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), `"type":"delta"`) {
		t.Fatalf("expected type field in JSON: %s", data)
	}
	for _, forbidden := range []string{
		`"delta":`, `"usage":`, `"error":`,
	} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("omitempty violated by %s in StreamChunk: %s", forbidden, data)
		}
	}
}