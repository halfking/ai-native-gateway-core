package ir

import (
	"encoding/json"
	"strings"
	"testing"
)

// ─── Reasoning Config Tests (audit-provider-multimodal, 2026-07-13) ──────

// TestParseOpenAI_ReasoningEffort verifies that OpenAI reasoning_effort is parsed
// into the structured ReasoningConfig field.
func TestParseOpenAI_ReasoningEffort(t *testing.T) {
	body := []byte(`{
		"model": "o1-preview",
		"messages": [{"role": "user", "content": "Solve 2+2"}],
		"reasoning_effort": "high"
	}`)

	ir, err := ParseOpenAI(body)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if ir.Reasoning == nil {
		t.Fatal("Reasoning is nil")
	}
	if ir.Reasoning.Effort != "high" {
		t.Errorf("Reasoning.Effort = %q, want \"high\"", ir.Reasoning.Effort)
	}
}

// TestSerializeOpenAI_ReasoningEffort verifies that ReasoningConfig.Effort
// is serialized as reasoning_effort in OpenAI format.
func TestSerializeOpenAI_ReasoningEffort(t *testing.T) {
	ir := &InternalRequest{
		Model:     "o1-preview",
		Messages:  []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "Solve 2+2"}}}},
		Reasoning: &ReasoningConfig{Effort: "medium"},
	}

	body, err := SerializeOpenAI(ir)
	if err != nil {
		t.Fatalf("Serialize: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if out["reasoning_effort"] != "medium" {
		t.Errorf("reasoning_effort = %v, want \"medium\"", out["reasoning_effort"])
	}
}

// TestSerializeAnthropic_ReasoningEffortToThinking verifies that OpenAI-style
// reasoning_effort is mapped to Anthropic thinking block with budget_tokens.
func TestSerializeAnthropic_ReasoningEffortToThinking(t *testing.T) {
	ir := &InternalRequest{
		Model:     "claude-3-5-sonnet-20241022",
		MaxTokens: 1024,
		Messages:  []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "Think hard"}}}},
		Reasoning: &ReasoningConfig{Effort: "high"},
	}

	body, err := SerializeAnthropic(ir)
	if err != nil {
		t.Fatalf("Serialize: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	thinking, ok := out["thinking"].(map[string]any)
	if !ok {
		t.Fatal("thinking block not present")
	}
	if thinking["type"] != "enabled" {
		t.Errorf("thinking.type = %v, want \"enabled\"", thinking["type"])
	}
	if budget, ok := thinking["budget_tokens"].(float64); !ok || budget < 1024 {
		t.Errorf("thinking.budget_tokens = %v, want >= 1024", thinking["budget_tokens"])
	}
}

// ─── Modalities & Audio Config Tests ──────────────────────────────

// TestParseOpenAI_ModalitiesAndAudio verifies parsing of multimodal output config.
func TestParseOpenAI_ModalitiesAndAudio(t *testing.T) {
	body := []byte(`{
		"model": "gpt-4o-audio-preview",
		"messages": [{"role": "user", "content": "Hi"}],
		"modalities": ["text", "audio"],
		"audio": {"voice": "alloy", "format": "mp3", "speed": 1.2}
	}`)

	ir, err := ParseOpenAI(body)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if len(ir.Modalities) != 2 {
		t.Errorf("Modalities = %v, want 2 entries", ir.Modalities)
	}
	if ir.Modalities[0] != "text" || ir.Modalities[1] != "audio" {
		t.Errorf("Modalities = %v", ir.Modalities)
	}

	if ir.AudioConfig == nil {
		t.Fatal("AudioConfig is nil")
	}
	if ir.AudioConfig.Voice != "alloy" {
		t.Errorf("AudioConfig.Voice = %q, want \"alloy\"", ir.AudioConfig.Voice)
	}
	if ir.AudioConfig.Format != "mp3" {
		t.Errorf("AudioConfig.Format = %q, want \"mp3\"", ir.AudioConfig.Format)
	}
	if ir.AudioConfig.Speed != 1.2 {
		t.Errorf("AudioConfig.Speed = %v, want 1.2", ir.AudioConfig.Speed)
	}
}

// TestSerializeOpenAI_ModalitiesAndAudio verifies output of multimodal config.
func TestSerializeOpenAI_ModalitiesAndAudio(t *testing.T) {
	ir := &InternalRequest{
		Model:       "gpt-4o-audio-preview",
		Messages:    []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "Hi"}}}},
		Modalities:  []string{"text", "audio"},
		AudioConfig: &AudioConfig{Voice: "echo", Format: "wav", Speed: 0.9},
	}

	body, err := SerializeOpenAI(ir)
	if err != nil {
		t.Fatalf("Serialize: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	mods, ok := out["modalities"].([]any)
	if !ok || len(mods) != 2 {
		t.Errorf("modalities = %v, want [text,audio]", out["modalities"])
	}

	audio, ok := out["audio"].(map[string]any)
	if !ok {
		t.Fatal("audio not present")
	}
	if audio["voice"] != "echo" {
		t.Errorf("audio.voice = %v, want echo", audio["voice"])
	}
}

// ─── Logit Bias Test ──────────────────────────────────────────────

func TestParseOpenAI_LogitBias(t *testing.T) {
	body := []byte(`{
		"model": "gpt-4",
		"messages": [{"role": "user", "content": "Hi"}],
		"logit_bias": {"50256": -100, "198": 50}
	}`)

	ir, err := ParseOpenAI(body)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if ir.LogitBias["50256"] != -100 {
		t.Errorf("LogitBias[50256] = %v, want -100", ir.LogitBias["50256"])
	}
	if ir.LogitBias["198"] != 50 {
		t.Errorf("LogitBias[198] = %v, want 50", ir.LogitBias["198"])
	}
}

// ─── Audio Input Block Tests ──────────────────────────────────────

func TestParseOpenAI_InputAudioBlock(t *testing.T) {
	body := []byte(`{
		"model": "gpt-4o-audio-preview",
		"messages": [{
			"role": "user",
			"content": [
				{"type": "text", "text": "What's in this audio?"},
				{"type": "input_audio", "input_audio": {"data": "QkFURQ==", "format": "wav"}}
			]
		}]
	}`)

	ir, err := ParseOpenAI(body)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if len(ir.Messages) == 0 {
		t.Fatal("No messages parsed")
	}

	blocks := ir.Messages[0].Content
	if len(blocks) != 2 {
		t.Fatalf("Content blocks = %d, want 2", len(blocks))
	}

	// Find input_audio block
	var audioBlock *InputAudioBlock
	for _, b := range blocks {
		if b.Type == "input_audio" && b.InputAudio != nil {
			audioBlock = b.InputAudio
			break
		}
	}
	if audioBlock == nil {
		t.Fatal("input_audio block not parsed")
	}
	if audioBlock.Data != "QkFURQ==" {
		t.Errorf("Data = %q, want QkFURQ==", audioBlock.Data)
	}
	if audioBlock.Format != "wav" {
		t.Errorf("Format = %q, want wav", audioBlock.Format)
	}
}

// TestSerializeOpenAI_InputAudioBlock verifies that InputAudio is round-tripped.
func TestSerializeOpenAI_InputAudioBlock(t *testing.T) {
	ir := &InternalRequest{
		Model: "gpt-4o-audio-preview",
		Messages: []Message{{
			Role: "user",
			Content: []ContentBlock{
				{Type: "text", Text: "Listen:"},
				{Type: "input_audio", InputAudio: &InputAudioBlock{Data: "QkFURQ==", Format: "wav"}},
			},
		}},
	}

	body, err := SerializeOpenAI(ir)
	if err != nil {
		t.Fatalf("Serialize: %v", err)
	}

	if !strings.Contains(string(body), `"input_audio"`) {
		t.Error("input_audio not serialized")
	}
	if !strings.Contains(string(body), `"QkFURQ=="`) {
		t.Error("audio data not serialized")
	}
}

// ─── Document/File Input Block Tests ──────────────────────────────

func TestParseOpenAI_FileBlock(t *testing.T) {
	body := []byte(`{
		"model": "gpt-4o",
		"messages": [{
			"role": "user",
			"content": [
				{"type": "text", "text": "Summarize"},
				{"type": "file", "file": {"filename": "report.pdf", "file_data": "data:application/pdf;base64,JVBERi0xLjQK"}}
			]
		}]
	}`)

	ir, err := ParseOpenAI(body)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	blocks := ir.Messages[0].Content
	var docBlock *DocumentBlock
	for _, b := range blocks {
		if b.Type == "document" && b.Document != nil {
			docBlock = b.Document
			break
		}
	}
	if docBlock == nil {
		t.Fatal("document block not parsed")
	}
	if docBlock.Title != "report.pdf" {
		t.Errorf("Title = %q, want report.pdf", docBlock.Title)
	}
	if docBlock.Source == nil {
		t.Fatal("Document.Source is nil")
	}
	if docBlock.Source.Type != "base64" {
		t.Errorf("Source.Type = %q, want base64", docBlock.Source.Type)
	}
	if docBlock.Source.MediaType != "application/pdf" {
		t.Errorf("Source.MediaType = %q, want application/pdf", docBlock.Source.MediaType)
	}
	if docBlock.Source.Data != "JVBERi0xLjQK" {
		t.Errorf("Source.Data = %q, want JVBERi0xLjQK", docBlock.Source.Data)
	}
}

// ─── Web Search Options Test ──────────────────────────────────────

func TestParseAndSerializeOpenAI_WebSearchOptions(t *testing.T) {
	body := []byte(`{
		"model": "gpt-4o-search-preview",
		"messages": [{"role": "user", "content": "What's new?"}],
		"web_search_options": {
			"search_context_size": "high",
			"user_location": {"type": "approximate", "city": "Beijing", "country": "CN"}
		}
	}`)

	ir, err := ParseOpenAI(body)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if ir.WebSearchOptions == nil {
		t.Fatal("WebSearchOptions is nil")
	}
	if ir.WebSearchOptions.SearchContextSize != "high" {
		t.Errorf("SearchContextSize = %q, want high", ir.WebSearchOptions.SearchContextSize)
	}
	if ir.WebSearchOptions.UserLocation == nil {
		t.Fatal("UserLocation is nil")
	}
	if ir.WebSearchOptions.UserLocation.City != "Beijing" {
		t.Errorf("City = %q, want Beijing", ir.WebSearchOptions.UserLocation.City)
	}

	// Serialize back
	out, err := SerializeOpenAI(ir)
	if err != nil {
		t.Fatalf("Serialize: %v", err)
	}

	var m map[string]any
	json.Unmarshal(out, &m)
	wso, ok := m["web_search_options"].(map[string]any)
	if !ok {
		t.Fatal("web_search_options not in output")
	}
	if wso["search_context_size"] != "high" {
		t.Errorf("search_context_size not serialized")
	}
}

// ─── Prediction Test ──────────────────────────────────────────────

func TestParseAndSerializeOpenAI_Prediction(t *testing.T) {
	body := []byte(`{
		"model": "gpt-4o",
		"messages": [{"role": "user", "content": "Continue the code"}],
		"prediction": {"type": "content", "content": "def hello():\n    return \"world\""}
	}`)

	ir, err := ParseOpenAI(body)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if ir.Prediction == nil {
		t.Fatal("Prediction is nil")
	}
	if ir.Prediction.Content == "" {
		t.Error("Prediction.Content empty")
	}
	if !strings.Contains(ir.Prediction.Content, "def hello") {
		t.Errorf("Prediction.Content = %q", ir.Prediction.Content)
	}
}

// ─── Verbosity / Store / ServiceTier / Truncation Tests ───────────

func TestParseOpenAI_OpenAIAdvancedFields(t *testing.T) {
	body := []byte(`{
		"model": "gpt-4o",
		"messages": [{"role": "user", "content": "Hi"}],
		"verbosity": "low",
		"store": false,
		"service_tier": "priority",
		"truncation": "auto",
		"prompt_cache_key": "team-alpha",
		"safety_identifier": "user-123",
		"previous_response_id": "resp_abc"
	}`)

	ir, err := ParseOpenAI(body)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if ir.Verbosity != "low" {
		t.Errorf("Verbosity = %q, want low", ir.Verbosity)
	}
	if ir.Store == nil || *ir.Store != false {
		t.Errorf("Store = %v, want false", ir.Store)
	}
	if ir.ServiceTier != "priority" {
		t.Errorf("ServiceTier = %q, want priority", ir.ServiceTier)
	}
	if ir.Truncation != "auto" {
		t.Errorf("Truncation = %q, want auto", ir.Truncation)
	}
	if ir.PromptCacheKey != "team-alpha" {
		t.Errorf("PromptCacheKey = %q", ir.PromptCacheKey)
	}
	if ir.SafetyIdentifier != "user-123" {
		t.Errorf("SafetyIdentifier = %q", ir.SafetyIdentifier)
	}
	if ir.PreviousResponseID != "resp_abc" {
		t.Errorf("PreviousResponseID = %q", ir.PreviousResponseID)
	}
}

// ─── MapEffortToBudget Test ───────────────────────────────────────

func TestMapEffortToBudget(t *testing.T) {
	cases := []struct {
		effort string
		want   int
	}{
		{"low", 2048},
		{"medium", 8192},
		{"high", 16384},
		{"unknown", 8192},
	}
	for _, tc := range cases {
		if got := mapEffortToBudget(tc.effort); got != tc.want {
			t.Errorf("mapEffortToBudget(%q) = %d, want %d", tc.effort, got, tc.want)
		}
	}
}
