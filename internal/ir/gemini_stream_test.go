package ir

import (
	"encoding/json"
	"strings"
	"testing"
)

// ─── T5.A Gemini Stream Parser Tests ───────────────────────────────────────

// TestParseGeminiStreamChunk_TextDelta verifies basic text streaming.
func TestParseGeminiStreamChunk_TextDelta(t *testing.T) {
	line := `data: {"candidates":[{"content":{"parts":[{"text":"Hello"}],"role":"model"},"index":0}],"modelVersion":"gemini-2.5-pro"}`

	chunk, err := ParseGeminiStreamChunk(line)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if chunk.SourceProtocol != ProtocolGeminiGenerate {
		t.Errorf("SourceProtocol = %q, want %q", chunk.SourceProtocol, ProtocolGeminiGenerate)
	}
	if chunk.Type != ChunkTypeDelta {
		t.Errorf("Type = %v, want delta", chunk.Type)
	}
	if chunk.Delta == nil {
		t.Fatal("Delta is nil")
	}
	if chunk.Delta.Content != "Hello" {
		t.Errorf("Content = %q, want Hello", chunk.Delta.Content)
	}
	if chunk.Delta.DeltaType != "text" {
		t.Errorf("DeltaType = %q, want text", chunk.Delta.DeltaType)
	}
	if chunk.Model != "gemini-2.5-pro" {
		t.Errorf("Model = %q", chunk.Model)
	}
}

// TestParseGeminiStreamChunk_ThoughtDelta verifies Gemini 2.5+ thinking delta.
func TestParseGeminiStreamChunk_ThoughtDelta(t *testing.T) {
	line := `data: {"candidates":[{"content":{"parts":[{"thought":"Let me think about this..."}],"role":"model"},"index":0}]}`

	chunk, err := ParseGeminiStreamChunk(line)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if chunk.Delta == nil {
		t.Fatal("Delta is nil")
	}
	if chunk.Delta.ReasoningContent != "Let me think about this..." {
		t.Errorf("ReasoningContent = %q", chunk.Delta.ReasoningContent)
	}
	if chunk.Delta.DeltaType != "reasoning" {
		t.Errorf("DeltaType = %q, want reasoning", chunk.Delta.DeltaType)
	}
}

// TestParseGeminiStreamChunk_FunctionCall verifies streamed functionCall.
func TestParseGeminiStreamChunk_FunctionCall(t *testing.T) {
	line := `data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"get_weather","args":{"city":"Beijing"}}}],"role":"model"},"finishReason":"STOP","index":0}]}`

	chunk, err := ParseGeminiStreamChunk(line)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if chunk.Delta == nil {
		t.Fatal("Delta is nil")
	}
	if len(chunk.Delta.ToolCalls) != 1 {
		t.Fatalf("ToolCalls len = %d, want 1", len(chunk.Delta.ToolCalls))
	}
	tc := chunk.Delta.ToolCalls[0]
	if tc.Name != "get_weather" {
		t.Errorf("Name = %q, want get_weather", tc.Name)
	}
	if tc.Type != "function" {
		t.Errorf("Type = %q, want function", tc.Type)
	}
	if !strings.Contains(tc.Arguments, "Beijing") {
		t.Errorf("Arguments = %q", tc.Arguments)
	}
	if chunk.FinishReason != "stop" {
		t.Errorf("FinishReason = %q, want stop (STOP→stop)", chunk.FinishReason)
	}
}

func TestParseGeminiStreamChunk_AggregatesCandidateParts(t *testing.T) {
	line := `data: {"candidates":[{"content":{"parts":[{"text":"Hello"},{"thought":"considering"},{"text":" world"},{"functionCall":{"name":"lookup","args":{"city":"Beijing"}}},{"functionCall":{"args":{"unit":"celsius"}}},{"inlineData":{"mimeType":"audio/wav","data":"AQI="}},{"inlineData":{"mimeType":"audio/wav","data":"AwQ="}}],"role":"model"},"index":3}]}`

	chunk, err := ParseGeminiStreamChunk(line)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if chunk.Type != ChunkTypeDelta || chunk.Delta == nil {
		t.Fatalf("chunk = %#v, want delta with content", chunk)
	}
	if chunk.CandidateIndex != 3 {
		t.Errorf("CandidateIndex = %d, want 3", chunk.CandidateIndex)
	}
	if chunk.Delta.Content != "Hello world" {
		t.Errorf("Content = %q, want aggregated text", chunk.Delta.Content)
	}
	if chunk.Delta.ReasoningContent != "considering" {
		t.Errorf("ReasoningContent = %q", chunk.Delta.ReasoningContent)
	}
	if len(chunk.Delta.ToolCalls) != 2 {
		t.Fatalf("ToolCalls len = %d, want 2", len(chunk.Delta.ToolCalls))
	}
	if chunk.Delta.ToolCalls[1].Name != "" || !strings.Contains(chunk.Delta.ToolCalls[1].Arguments, "celsius") {
		t.Errorf("partial unnamed function call was not preserved: %#v", chunk.Delta.ToolCalls[1])
	}
	if chunk.Delta.AudioDelta == nil || chunk.Delta.AudioDelta.Data != "AQIDBA==" || chunk.Delta.AudioDelta.MIMEType != "audio/wav" {
		t.Errorf("AudioDelta = %#v, want aggregated wav audio", chunk.Delta.AudioDelta)
	}
}

// TestParseGeminiStreamChunk_DoneSentinel verifies [DONE] handling.
func TestParseGeminiStreamChunk_RejectsMultipleCandidates(t *testing.T) {
	line := `data: {"candidates":[{"content":{"parts":[{"text":"first"}]},"index":0},{"content":{"parts":[{"text":"second"}]},"index":1}]}`

	_, err := ParseGeminiStreamChunk(line)
	if err == nil || !strings.Contains(err.Error(), "2 candidates") {
		t.Fatalf("Parse error = %v, want explicit multi-candidate rejection", err)
	}
}

func TestParseGeminiStreamChunk_DoneSentinel(t *testing.T) {
	line := `data: [DONE]`

	chunk, err := ParseGeminiStreamChunk(line)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if chunk.Type != ChunkTypeDone {
		t.Errorf("Type = %v, want done", chunk.Type)
	}
}

// TestParseGeminiStreamChunk_UsageMetadata verifies end-of-stream usage.
func TestParseGeminiStreamChunk_UsageMetadata(t *testing.T) {
	line := `data: {"candidates":[{"content":{"parts":[{"text":"Hi"}],"role":"model"},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":50,"totalTokenCount":150,"cachedContentTokenCount":30,"thoughtsTokenCount":20}}`

	chunk, err := ParseGeminiStreamChunk(line)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if chunk.Usage == nil {
		t.Fatal("Usage is nil")
	}
	if chunk.Usage.PromptTokens != 100 {
		t.Errorf("PromptTokens = %d, want 100", chunk.Usage.PromptTokens)
	}
	if chunk.Usage.CompletionTokens != 50 {
		t.Errorf("CompletionTokens = %d, want 50", chunk.Usage.CompletionTokens)
	}
	if chunk.Usage.CacheReadTokens == nil || *chunk.Usage.CacheReadTokens != 30 {
		t.Errorf("CacheReadTokens = %v, want 30", chunk.Usage.CacheReadTokens)
	}
	if chunk.Usage.ReasoningTokens == nil || *chunk.Usage.ReasoningTokens != 20 {
		t.Errorf("ReasoningTokens = %v, want 20", chunk.Usage.ReasoningTokens)
	}
	if chunk.Delta == nil || chunk.Delta.Content != "Hi" {
		t.Errorf("final candidate content was lost: %#v", chunk.Delta)
	}
}

// TestParseGeminiStreamChunk_ModalityDetails verifies image/audio token breakdowns.
func TestParseGeminiStreamChunk_ModalityDetails(t *testing.T) {
	line := `data: {"candidates":[{"content":{"parts":[{"text":"I see an image"}],"role":"model"},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":1500,"candidatesTokenCount":50,"totalTokenCount":1550,"promptTokensDetails":[{"modality":"TEXT","tokenCount":1200},{"modality":"IMAGE","tokenCount":300}],"completionTokensDetails":[{"modality":"TEXT","tokenCount":50}]}}`

	chunk, err := ParseGeminiStreamChunk(line)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if chunk.Usage == nil {
		t.Fatal("Usage is nil")
	}
	if chunk.Usage.ImageTokens == nil || *chunk.Usage.ImageTokens != 300 {
		t.Errorf("ImageTokens = %v, want 300", chunk.Usage.ImageTokens)
	}
}

// TestParseGeminiStreamChunk_AudioModality verifies audio token extraction.
func TestParseGeminiStreamChunk_AudioModality(t *testing.T) {
	line := `data: {"candidates":[{"content":{"parts":[],"role":"model"},"index":0}],"usageMetadata":{"promptTokenCount":500,"candidatesTokenCount":0,"totalTokenCount":500,"promptTokensDetails":[{"modality":"AUDIO","tokenCount":500}]}}`

	chunk, err := ParseGeminiStreamChunk(line)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if chunk.Usage.AudioTokens == nil || *chunk.Usage.AudioTokens != 500 {
		t.Errorf("AudioTokens = %v, want 500", chunk.Usage.AudioTokens)
	}
}

// TestParseGeminiStreamChunk_FinishReasons verifies all Gemini finish reasons.
func TestParseGeminiStreamChunk_FinishReasons(t *testing.T) {
	cases := []struct {
		geminiReason string
		wantIRReason string
	}{
		{"STOP", "stop"},
		{"MAX_TOKENS", "length"},
		{"SAFETY", "content_filter"},
		{"RECITATION", "content_filter"},
		{"BLOCKLIST", "content_filter"},
		{"PROHIBITED_CONTENT", "content_filter"},
		{"SPII", "content_filter"},
		{"MALFORMED_FUNCTION_CALL", "tool_calls"},
		{"OTHER", "stop"},
	}

	for _, tc := range cases {
		t.Run(tc.geminiReason, func(t *testing.T) {
			line := `data: {"candidates":[{"content":{"parts":[],"role":"model"},"finishReason":"` + tc.geminiReason + `","index":0}]}`
			chunk, err := ParseGeminiStreamChunk(line)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if chunk.FinishReason != tc.wantIRReason {
				t.Errorf("FinishReason = %q, want %q", chunk.FinishReason, tc.wantIRReason)
			}
		})
	}
}

// ─── T5.B Gemini Stream Serializer Tests ───────────────────────────────────

// TestSerializeGemini_TextDelta verifies text delta output.
func TestSerializeGemini_TextDelta(t *testing.T) {
	chunk := &StreamChunk{
		Type:  ChunkTypeDelta,
		Model: "gemini-2.5-pro",
		Delta: &StreamDelta{Content: "Hello world"},
	}

	out := chunk.SerializeGemini()
	if !strings.HasPrefix(out, "data: ") {
		t.Errorf("Missing data: prefix: %q", out)
	}
	if !strings.Contains(out, `"text":"Hello world"`) {
		t.Errorf("Text not in output: %s", out)
	}
	if !strings.Contains(out, `"role":"model"`) {
		t.Errorf("Model role missing: %s", out)
	}
	if !strings.Contains(out, `"modelVersion":"gemini-2.5-pro"`) {
		t.Errorf("Model version missing: %s", out)
	}
}

// TestSerializeGemini_ThoughtDelta verifies reasoning delta output.
func TestSerializeGemini_ThoughtDelta(t *testing.T) {
	chunk := &StreamChunk{
		Type:  ChunkTypeDelta,
		Delta: &StreamDelta{ReasoningContent: "thinking..."},
	}

	out := chunk.SerializeGemini()
	if !strings.Contains(out, `"thought":"thinking..."`) {
		t.Errorf("Thought not in output: %s", out)
	}
}

// TestSerializeGemini_FunctionCall verifies tool call output.
func TestSerializeGemini_FunctionCall(t *testing.T) {
	chunk := &StreamChunk{
		Type: ChunkTypeDelta,
		Delta: &StreamDelta{
			ToolCalls: []StreamToolCallDelta{{
				Index:     0,
				Name:      "get_weather",
				Arguments: `{"city":"Beijing"}`,
			}},
		},
	}

	out := chunk.SerializeGemini()
	if !strings.Contains(out, `"functionCall"`) {
		t.Errorf("functionCall not in output: %s", out)
	}
	if !strings.Contains(out, `"name":"get_weather"`) {
		t.Errorf("function name missing: %s", out)
	}
}

// TestSerializeGemini_UsageMetadata verifies end-of-stream usage output.
func TestSerializeGemini_PreservesPartialFunctionCallAndAudio(t *testing.T) {
	chunk := &StreamChunk{
		Type:           ChunkTypeDelta,
		CandidateIndex: 2,
		Delta: &StreamDelta{
			ToolCalls:  []StreamToolCallDelta{{Index: 2, Arguments: `{"city":"`}},
			AudioDelta: &StreamAudioDelta{MIMEType: "audio/wav", Data: "AQID"},
		},
	}

	out := chunk.SerializeGemini()
	var payload struct {
		Candidates []struct {
			Index   int `json:"index"`
			Content struct {
				Parts []struct {
					FunctionCall map[string]any `json:"functionCall"`
					InlineData   map[string]any `json:"inlineData"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(strings.TrimSpace(out), "data: ")), &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(payload.Candidates) != 1 || payload.Candidates[0].Index != 2 {
		t.Fatalf("candidate = %#v, want index 2", payload.Candidates)
	}
	parts := payload.Candidates[0].Content.Parts
	if len(parts) != 2 || parts[0].FunctionCall["args"] != `{"city":"` {
		t.Fatalf("partial function call was lost: %#v", parts)
	}
	if _, ok := parts[0].FunctionCall["name"]; ok {
		t.Errorf("unnamed partial call unexpectedly gained name: %#v", parts[0].FunctionCall)
	}
	if parts[1].InlineData["mimeType"] != "audio/wav" || parts[1].InlineData["data"] != "AQID" {
		t.Errorf("audio was not serialized: %#v", parts[1].InlineData)
	}
}

func TestSerializeGemini_UsageMetadata(t *testing.T) {
	cacheRead := 30
	reasoning := 20
	image := 300
	chunk := &StreamChunk{
		Type: ChunkTypeUsage,
		Usage: &StreamUsage{
			PromptTokens:     1500,
			CompletionTokens: 50,
			TotalTokens:      1550,
			CacheReadTokens:  &cacheRead,
			ReasoningTokens:  &reasoning,
			ImageTokens:      &image,
		},
	}

	out := chunk.SerializeGemini()
	var parsed map[string]any
	if err := json.Unmarshal([]byte(strings.TrimPrefix(strings.TrimSpace(out), "data: ")), &parsed); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	usageMeta, ok := parsed["usageMetadata"].(map[string]any)
	if !ok {
		t.Fatal("usageMetadata missing")
	}
	if usageMeta["promptTokenCount"].(float64) != 1500 {
		t.Errorf("promptTokenCount = %v", usageMeta["promptTokenCount"])
	}
	if usageMeta["cachedContentTokenCount"].(float64) != 30 {
		t.Errorf("cachedContentTokenCount = %v", usageMeta["cachedContentTokenCount"])
	}
	if usageMeta["thoughtsTokenCount"].(float64) != 20 {
		t.Errorf("thoughtsTokenCount = %v", usageMeta["thoughtsTokenCount"])
	}

	promptDetails, ok := usageMeta["promptTokensDetails"].([]any)
	if !ok || len(promptDetails) == 0 {
		t.Fatal("promptTokensDetails missing")
	}
	// Find the IMAGE entry
	hasImage := false
	for _, d := range promptDetails {
		m := d.(map[string]any)
		if m["modality"] == "IMAGE" && m["tokenCount"].(float64) == 300 {
			hasImage = true
		}
	}
	if !hasImage {
		t.Errorf("IMAGE modality missing or wrong: %+v", promptDetails)
	}
}

// TestSerializeGemini_DoneSentinel verifies [DONE] output.
func TestSerializeGemini_DoneSentinel(t *testing.T) {
	chunk := &StreamChunk{Type: ChunkTypeDone}
	out := chunk.SerializeGemini()
	if !strings.Contains(out, "[DONE]") {
		t.Errorf("DONE missing: %q", out)
	}
}

// TestSerializeGemini_FinishReason verifies Gemini finish reason mapping.
func TestSerializeGemini_FinishReason(t *testing.T) {
	cases := []struct {
		irReason   string
		wantGemini string
	}{
		{"stop", "STOP"},
		{"length", "MAX_TOKENS"},
		{"content_filter", "SAFETY"},
	}

	for _, tc := range cases {
		t.Run(tc.irReason, func(t *testing.T) {
			chunk := &StreamChunk{
				Type:         ChunkTypeDelta,
				FinishReason: tc.irReason,
				Delta:        &StreamDelta{Content: "x"},
			}
			out := chunk.SerializeGemini()
			if !strings.Contains(out, `"finishReason":"`+tc.wantGemini+`"`) {
				t.Errorf("finishReason missing %s: %s", tc.wantGemini, out)
			}
		})
	}
}

// ─── T5.C Round-trip Test ──────────────────────────────────────────────────

// TestRoundTripGeminiStream parses multiple sequential chunks and re-serializes them.
// Verifies no semantic loss across the IR boundary.
func TestRoundTripGeminiStream(t *testing.T) {
	chunks := []string{
		`data: {"candidates":[{"content":{"parts":[{"text":"Hello"}],"role":"model"},"index":0}],"modelVersion":"gemini-2.5-pro"}`,
		`data: {"candidates":[{"content":{"parts":[{"text":" world"}],"role":"model"},"index":0}],"modelVersion":"gemini-2.5-pro"}`,
		`data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"get_weather","args":{"city":"Shanghai"}}}],"role":"model"},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":50,"candidatesTokenCount":20,"totalTokenCount":70},"modelVersion":"gemini-2.5-pro"}`,
		`data: [DONE]`,
	}

	var combinedText string
	var finishReason string
	chunkCount := 0

	for _, line := range chunks {
		chunk, err := ParseGeminiStreamChunk(line)
		if err != nil {
			t.Fatalf("Parse chunk %d: %v", chunkCount, err)
		}
		chunkCount++

		switch chunk.Type {
		case ChunkTypeDelta:
			if chunk.Delta != nil {
				combinedText += chunk.Delta.Content
			}
		case ChunkTypeUsage:
			if chunk.FinishReason != "" {
				finishReason = chunk.FinishReason
			}
		case ChunkTypeDone:
			// terminal
		}
	}

	if combinedText != "Hello world" {
		t.Errorf("Combined text = %q, want 'Hello world'", combinedText)
	}
	if finishReason != "stop" {
		t.Errorf("FinishReason = %q, want stop", finishReason)
	}
}

// TestParseGeminiStreamChunk_EmptyAndInvalidInputs verifies error handling.
func TestParseGeminiStreamChunk_EmptyAndInvalidInputs(t *testing.T) {
	cases := []string{
		"",
		"not-a-data-line",
		`data: `,
		`data: invalid json`,
	}

	for i, c := range cases {
		_, err := ParseGeminiStreamChunk(c)
		if err == nil {
			t.Errorf("Case %d (%q): expected error, got nil", i, c)
		}
	}
}

// ─── T5.D Cross-Protocol Conversion Tests ─────────────────────────────────

// TestGeminiStreamToOpenAIConversion verifies IR can route Gemini stream → OpenAI stream.
func TestGeminiStreamToOpenAIConversion(t *testing.T) {
	line := `data: {"candidates":[{"content":{"parts":[{"text":"Hello"}],"role":"model"},"index":0}],"modelVersion":"gemini-2.5-pro"}`

	chunk, err := ParseGeminiStreamChunk(line)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Convert to OpenAI format using existing SerializeOpenAI
	out := chunk.SerializeOpenAI("chatcmpl-gemini", "gemini-2.5-pro", 1700000000)

	if !strings.Contains(out, "Hello") {
		t.Errorf("Cross-protocol conversion lost text: %s", out)
	}
	if !strings.Contains(out, `"object":"chat.completion.chunk"`) {
		t.Errorf("Not converted to OpenAI format: %s", out)
	}
}

// TestGeminiStreamToAnthropicConversion verifies IR can route Gemini stream → Anthropic stream.
func TestGeminiStreamToAnthropicConversion(t *testing.T) {
	line := `data: {"candidates":[{"content":{"parts":[{"text":"Hello"}],"role":"model"},"index":0}],"modelVersion":"gemini-2.5-pro"}`

	chunk, err := ParseGeminiStreamChunk(line)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Convert to Anthropic format using existing SerializeAnthropic
	out := chunk.SerializeAnthropic("msg_gemini", "claude-sonnet-4-5")

	if !strings.Contains(out, "content_block_delta") {
		t.Errorf("Not converted to Anthropic format: %s", out)
	}
	if !strings.Contains(out, "Hello") {
		t.Errorf("Cross-protocol conversion lost text: %s", out)
	}
}

// TestGeminiStreamUsageChunkIndependent verifies usage chunk without delta content.
func TestGeminiStreamUsageChunkIndependent(t *testing.T) {
	// Some Gemini variants send usage in a dedicated final chunk
	line := `data: {"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":50,"totalTokenCount":150}}`

	chunk, err := ParseGeminiStreamChunk(line)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if chunk.Type != ChunkTypeUsage {
		t.Errorf("Type = %v, want usage", chunk.Type)
	}
	if chunk.Usage == nil {
		t.Fatal("Usage is nil")
	}
	if chunk.Usage.PromptTokens != 100 {
		t.Errorf("PromptTokens = %d", chunk.Usage.PromptTokens)
	}
}
