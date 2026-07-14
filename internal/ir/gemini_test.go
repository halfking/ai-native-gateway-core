package ir

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestParseGemini_BasicRequest verifies a minimal Gemini generateContent parse.
func TestParseGemini_BasicRequest(t *testing.T) {
	body := []byte(`{
		"contents": [
			{"role": "user", "parts": [{"text": "Hello Gemini"}]}
		],
		"systemInstruction": {"parts": [{"text": "You are helpful"}]},
		"generationConfig": {"temperature": 0.7, "maxOutputTokens": 1024}
	}`)

	ir, err := ParseGemini(body)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if ir.SourceProtocol != ProtocolGeminiGenerate {
		t.Errorf("SourceProtocol = %q, want %q", ir.SourceProtocol, ProtocolGeminiGenerate)
	}
	if len(ir.Messages) != 1 {
		t.Fatalf("Messages = %d, want 1", len(ir.Messages))
	}
	if ir.Messages[0].Role != "user" {
		t.Errorf("Role = %q, want user", ir.Messages[0].Role)
	}
	if ir.Messages[0].Content[0].Text != "Hello Gemini" {
		t.Errorf("Text = %q", ir.Messages[0].Content[0].Text)
	}
	if ir.System == nil || len(ir.System.Parts) == 0 {
		t.Fatal("System not parsed")
	}
	if ir.Temperature == nil || *ir.Temperature != 0.7 {
		t.Errorf("Temperature = %v, want 0.7", ir.Temperature)
	}
	if ir.MaxTokens != 1024 {
		t.Errorf("MaxTokens = %d, want 1024", ir.MaxTokens)
	}
}

// TestParseGemini_InlineImageData verifies base64 image parsing.
func TestParseGemini_InlineImageData(t *testing.T) {
	body := []byte(`{
		"contents": [{
			"role": "user",
			"parts": [
				{"text": "What's in this image?"},
				{"inlineData": {"mimeType": "image/png", "data": "iVBORw0KGgo..."}}
			]
		}]
	}`)

	ir, err := ParseGemini(body)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	blocks := ir.Messages[0].Content
	if len(blocks) != 2 {
		t.Fatalf("Content blocks = %d, want 2", len(blocks))
	}

	imgBlock := blocks[1]
	if imgBlock.Type != "image" {
		t.Errorf("Block type = %q, want image", imgBlock.Type)
	}
	if imgBlock.Image == nil {
		t.Fatal("Image is nil")
	}
	if imgBlock.Image.Type != "base64" {
		t.Errorf("Image.Type = %q, want base64", imgBlock.Image.Type)
	}
	if imgBlock.Image.MediaType != "image/png" {
		t.Errorf("Image.MediaType = %q", imgBlock.Image.MediaType)
	}
}

// TestParseGemini_FileURI verifies Gemini Files API reference parsing.
func TestParseGemini_FileURI(t *testing.T) {
	body := []byte(`{
		"contents": [{
			"role": "user",
			"parts": [
				{"fileData": {"mimeType": "video/mp4", "fileUri": "https://example.com/video.mp4"}}
			]
		}]
	}`)

	ir, err := ParseGemini(body)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	block := ir.Messages[0].Content[0]
	if block.Type != "video" {
		t.Errorf("Block type = %q, want video", block.Type)
	}
	if block.Video == nil {
		t.Fatal("Video is nil")
	}
	if block.Video.FileURI != "https://example.com/video.mp4" {
		t.Errorf("FileURI = %q", block.Video.FileURI)
	}
}

func TestParseGemini_MixedAttachmentMetadata(t *testing.T) {
	body := []byte(`{
		"contents": [{"role": "user", "parts": [
			{"inlineData": {"mimeType": "image/png", "data": "img"}},
			{"inlineData": {"mimeType": "audio/mpeg", "data": "audio"}},
			{"inlineData": {"mimeType": "video/mp4", "data": "video"}},
			{"inlineData": {"mimeType": "application/pdf", "data": "pdf"}},
			{"fileData": {"mimeType": "text/plain", "fileUri": "files/text-1"}}
		]}]
	}`)

	parsed, err := ParseGemini(body)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	blocks := parsed.Messages[0].Content
	if len(blocks) != 5 {
		t.Fatalf("Content blocks = %d, want 5", len(blocks))
	}
	want := []struct {
		typ  string
		mime string
	}{
		{"image", "image/png"},
		{"audio", "audio/mpeg"},
		{"video", "video/mp4"},
		{"document", "application/pdf"},
		{"document", "text/plain"},
	}
	for i, expected := range want {
		if blocks[i].Type != expected.typ {
			t.Errorf("block %d type = %q, want %q", i, blocks[i].Type, expected.typ)
		}
		mime := ""
		switch blocks[i].Type {
		case "image":
			mime = blocks[i].Image.MediaType
		case "audio":
			mime = blocks[i].Audio.MediaType
		case "video":
			mime = blocks[i].Video.MediaType
		case "document":
			mime = blocks[i].Document.Source.MediaType
		}
		if mime != expected.mime {
			t.Errorf("block %d mime = %q, want %q", i, mime, expected.mime)
		}
	}
	if got := blocks[4].Document.Source.FileURI; got != "files/text-1" {
		t.Errorf("file URI = %q, want files/text-1", got)
	}
}

// TestParseGemini_ToolConfigModes verifies AUTO/ANY/NONE/tool mapping.
func TestParseGemini_ToolConfigModes(t *testing.T) {
	cases := []struct {
		name        string
		mode        string
		allowedFunc []string
		wantType    string
		wantName    string
	}{
		{"AUTO", "AUTO", nil, "auto", ""},
		{"ANY", "ANY", nil, "any", ""},
		{"NONE", "NONE", nil, "none", ""},
		{"ANY with one allowed", "ANY", []string{"get_weather"}, "tool", "get_weather"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			allowed := "null"
			if tc.allowedFunc != nil {
				b, _ := json.Marshal(tc.allowedFunc)
				allowed = string(b)
			}
			body := []byte(`{
				"toolConfig": {"functionCallingConfig": {"mode": "` + tc.mode + `", "allowedFunctionNames": ` + allowed + `}}
			}`)
			ir, err := ParseGemini(body)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if ir.ToolChoice == nil {
				t.Fatal("ToolChoice is nil")
			}
			if ir.ToolChoice.Type != tc.wantType {
				t.Errorf("ToolChoice.Type = %q, want %q", ir.ToolChoice.Type, tc.wantType)
			}
			if ir.ToolChoice.Name != tc.wantName {
				t.Errorf("ToolChoice.Name = %q, want %q", ir.ToolChoice.Name, tc.wantName)
			}
		})
	}
}

// TestParseGemini_FunctionCall verifies tool call parsing.
func TestParseGemini_FunctionCall(t *testing.T) {
	body := []byte(`{
		"contents": [{
			"role": "model",
			"parts": [{
				"functionCall": {"name": "get_weather", "args": {"city": "Beijing"}}
			}]
		}]
	}`)

	ir, err := ParseGemini(body)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	msg := ir.Messages[0]
	if msg.Role != "assistant" {
		t.Errorf("Role = %q, want assistant (model→assistant)", msg.Role)
	}
	if len(msg.Content) != 1 {
		t.Fatalf("Content blocks = %d", len(msg.Content))
	}
	block := msg.Content[0]
	if block.Type != "tool_use" {
		t.Errorf("Block type = %q, want tool_use", block.Type)
	}
	if block.ToolUse == nil || block.ToolUse.Name != "get_weather" {
		t.Errorf("ToolUse = %v", block.ToolUse)
	}
}

// TestParseGemini_FunctionResponse verifies tool result parsing.
func TestParseGemini_FunctionResponse(t *testing.T) {
	body := []byte(`{
		"contents": [{
			"role": "function",
			"parts": [{
				"functionResponse": {"name": "get_weather", "response": {"temperature": 25}}
			}]
		}]
	}`)

	ir, err := ParseGemini(body)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	msg := ir.Messages[0]
	if msg.Role != "tool" {
		t.Errorf("Role = %q, want tool (function→tool)", msg.Role)
	}
	block := msg.Content[0]
	if block.Type != "tool_result" {
		t.Errorf("Block type = %q, want tool_result", block.Type)
	}
}

// TestParseGemini_ThinkingConfig verifies Gemini 2.5 thinking config mapping.
func TestParseGemini_ThinkingConfig(t *testing.T) {
	body := []byte(`{
		"generationConfig": {
			"thinkingConfig": {"thinkingBudget": 8192, "includeThoughts": true}
		}
	}`)

	ir, err := ParseGemini(body)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if ir.Reasoning == nil {
		t.Fatal("Reasoning is nil")
	}
	if ir.Reasoning.BudgetTokens == nil || *ir.Reasoning.BudgetTokens != 8192 {
		t.Errorf("BudgetTokens = %v, want 8192", ir.Reasoning.BudgetTokens)
	}
}

// TestSerializeGemini_Basic verifies Gemini serialization produces expected structure.
func TestSerializeGemini_Basic(t *testing.T) {
	ir := &InternalRequest{
		Model: "gemini-2.5-pro",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "Hi"}}},
		},
		SourceProtocol: ProtocolGeminiGenerate,
		MaxTokens:      1024,
		Temperature:    ptrFloat(0.5),
	}

	body, err := SerializeGemini(ir)
	if err != nil {
		t.Fatalf("Serialize: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	contents, ok := out["contents"].([]any)
	if !ok {
		t.Fatal("contents missing")
	}
	if len(contents) != 1 {
		t.Fatalf("contents len = %d, want 1", len(contents))
	}
	c := contents[0].(map[string]any)
	if c["role"] != "user" {
		t.Errorf("role = %v, want user", c["role"])
	}

	gc, ok := out["generationConfig"].(map[string]any)
	if !ok {
		t.Fatal("generationConfig missing")
	}
	if gc["maxOutputTokens"].(float64) != 1024 {
		t.Errorf("maxOutputTokens = %v", gc["maxOutputTokens"])
	}
	if gc["temperature"].(float64) != 0.5 {
		t.Errorf("temperature = %v", gc["temperature"])
	}
}

// TestSerializeGemini_AssistantToModel verifies role mapping.
func TestSerializeGemini_AssistantToModel(t *testing.T) {
	ir := &InternalRequest{
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "Hi"}}},
			{Role: "assistant", Content: []ContentBlock{{Type: "text", Text: "Hello"}}},
		},
		SourceProtocol: ProtocolGeminiGenerate,
	}

	body, _ := SerializeGemini(ir)
	var out map[string]any
	json.Unmarshal(body, &out)
	contents := out["contents"].([]any)

	if contents[0].(map[string]any)["role"] != "user" {
		t.Error("user role not preserved")
	}
	if contents[1].(map[string]any)["role"] != "model" {
		t.Error("assistant role not mapped to model")
	}
}

// TestSerializeGemini_ToolCallToFunctionCall verifies tool_use → functionCall mapping.
func TestSerializeGemini_ToolCallToFunctionCall(t *testing.T) {
	ir := &InternalRequest{
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "Weather?"}}},
			{Role: "assistant", Content: []ContentBlock{
				{Type: "tool_use", ToolUse: &ToolUse{
					Name:  "get_weather",
					Input: json.RawMessage(`{"city":"Beijing"}`),
				}},
			}},
		},
		Tools: []ToolDefinition{
			{Name: "get_weather", Description: "Get weather"},
		},
		ToolChoice:     &ToolChoice{Type: "auto"},
		SourceProtocol: ProtocolGeminiGenerate,
	}

	body, _ := SerializeGemini(ir)
	var out map[string]any
	json.Unmarshal(body, &out)

	// Check functionCall present in assistant message
	contents := out["contents"].([]any)
	assistantMsg := contents[1].(map[string]any)
	parts := assistantMsg["parts"].([]any)
	if len(parts) == 0 {
		t.Fatal("No parts in assistant message")
	}
	part := parts[0].(map[string]any)
	fc, ok := part["functionCall"]
	if !ok {
		t.Fatal("functionCall not present")
	}
	fcMap := fc.(map[string]any)
	if fcMap["name"] != "get_weather" {
		t.Errorf("name = %v", fcMap["name"])
	}

	// Check tools structure
	tools := out["tools"].([]any)
	if len(tools) == 0 {
		t.Fatal("tools not present")
	}
	tool := tools[0].(map[string]any)
	decls, ok := tool["functionDeclarations"].([]any)
	if !ok {
		t.Fatal("functionDeclarations missing")
	}
	if len(decls) != 1 {
		t.Errorf("functionDeclarations len = %d, want 1", len(decls))
	}

	// Check toolConfig
	tc := out["toolConfig"].(map[string]any)
	fcc := tc["functionCallingConfig"].(map[string]any)
	if fcc["mode"] != "AUTO" {
		t.Errorf("mode = %v, want AUTO", fcc["mode"])
	}
}

// TestRoundTripGemini parses a request, serializes it, and verifies key fields survive.
func TestRoundTripGemini(t *testing.T) {
	original := []byte(`{
		"contents": [{"role": "user", "parts": [{"text": "Hello"}]}],
		"systemInstruction": {"parts": [{"text": "Be concise"}]},
		"generationConfig": {"temperature": 0.3, "topP": 0.9, "maxOutputTokens": 512}
	}`)

	ir, err := ParseGemini(original)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	out, err := SerializeGemini(ir)
	if err != nil {
		t.Fatalf("Serialize: %v", err)
	}

	var m map[string]any
	json.Unmarshal(out, &m)

	// systemInstruction should survive
	if _, ok := m["systemInstruction"]; !ok {
		t.Error("systemInstruction lost in round-trip")
	}

	// generationConfig temperature should be preserved
	gc := m["generationConfig"].(map[string]any)
	if gc["temperature"].(float64) != 0.3 {
		t.Errorf("temperature lost: %v", gc["temperature"])
	}
	if gc["maxOutputTokens"].(float64) != 512 {
		t.Errorf("maxOutputTokens lost: %v", gc["maxOutputTokens"])
	}
}

// TestSerializeGemini_ResponseMimeType verifies JSON output format conversion.
func TestSerializeGemini_ResponseMimeType(t *testing.T) {
	schemaJSON := json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`)
	ir := &InternalRequest{
		Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "City?"}}}},
		ResponseFormat: &ResponseFormat{
			Type:   "json_schema",
			Schema: schemaJSON,
		},
		SourceProtocol: ProtocolGeminiGenerate,
	}

	body, _ := SerializeGemini(ir)
	var out map[string]any
	json.Unmarshal(body, &out)

	gc := out["generationConfig"].(map[string]any)
	if gc["responseMimeType"] != "application/json" {
		t.Errorf("responseMimeType = %v, want application/json", gc["responseMimeType"])
	}
	if _, ok := gc["responseSchema"]; !ok {
		t.Error("responseSchema missing")
	}
}

// Helper
func ptrFloat(f float64) *float64 { return &f }

// Sanity check: verify the type discriminator string
func TestGeminiProtocolConstant(t *testing.T) {
	if ProtocolGeminiGenerate != "gemini-generate" {
		t.Errorf("ProtocolGeminiGenerate = %q, want gemini-generate", ProtocolGeminiGenerate)
	}
	if !strings.HasPrefix(ProtocolGeminiGenerate, "gemini") {
		t.Error("Protocol constant should be prefixed with gemini-")
	}
}
