package v2

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

// ── Helpers ─────────────────────────────────────────────────────────────

// persistAndRecover simulates the full session_bodies round-trip:
//
//	ir.Message → v2.Message → JSON (the jsonb column) → v2.Message → ir.Message
//
// This is the path that actually matters. A test that only checks
// MessageFromIR/recoverIRRaw in memory passes even when MarshalJSON writes a
// shape the reader cannot parse, which is exactly how the multimodal payload
// loss went unnoticed.
func persistAndRecover(t *testing.T, in ir.Message) ir.Message {
	t.Helper()
	wire, err := json.Marshal([]Message{MessageFromIR(in)})
	if err != nil {
		t.Fatalf("marshal to wire: %v", err)
	}
	var readBack []Message
	if err := json.Unmarshal(wire, &readBack); err != nil {
		t.Fatalf("unmarshal from wire %s: %v", wire, err)
	}
	if len(readBack) != 1 {
		t.Fatalf("expected 1 message back, got %d (wire: %s)", len(readBack), wire)
	}
	got := IRMessagesFromV2(readBack)
	if len(got) != 1 {
		t.Fatalf("expected 1 IR message, got %d", len(got))
	}
	return got[0]
}

func intPtr(v int) *int { return &v }

// ── Lossless persistence, block by block ────────────────────────────────

// TestPersist_EveryContentBlockKindSurvives walks every non-text variant of
// ir.ContentBlock through the disk round-trip.
//
// The adapter's first implementation serialised only Text/Image/Audio/
// Document/ToolUse, so Video, InputAudio, ToolResult, Thinking,
// RedactedThinking, CacheControl and Index were silently dropped on write.
// Each sub-test here fails if that regresses.
func TestPersist_EveryContentBlockKindSurvives(t *testing.T) {
	cases := []struct {
		name  string
		block ir.ContentBlock
		check func(*testing.T, ir.ContentBlock)
	}{
		{
			name: "image base64 keeps its real media type",
			block: ir.ContentBlock{Type: "image", Image: &ir.ImageSource{
				Type: "base64", MediaType: "image/webp", Data: "AAAB", Detail: "high",
			}},
			check: func(t *testing.T, b ir.ContentBlock) {
				if b.Image == nil {
					t.Fatal("image lost")
				}
				if b.Image.MediaType != "image/webp" {
					t.Fatalf("media type = %q, want image/webp", b.Image.MediaType)
				}
				if b.Image.Data != "AAAB" || b.Image.Detail != "high" {
					t.Fatalf("image payload lost: %+v", b.Image)
				}
			},
		},
		{
			name: "audio",
			block: ir.ContentBlock{Type: "audio", Audio: &ir.MediaSource{
				Kind: "audio", Format: "wav", Type: "base64", MediaType: "audio/wav", Data: "QUJD",
			}},
			check: func(t *testing.T, b ir.ContentBlock) {
				if b.Audio == nil {
					t.Fatal("audio lost")
				}
				if b.Audio.Format != "wav" || b.Audio.Data != "QUJD" {
					t.Fatalf("audio payload lost: %+v", b.Audio)
				}
			},
		},
		{
			name: "video",
			block: ir.ContentBlock{Type: "video", Video: &ir.MediaSource{
				Kind: "video", Format: "mp4", Type: "file_uri", FileURI: "gs://bucket/clip.mp4",
			}},
			check: func(t *testing.T, b ir.ContentBlock) {
				if b.Video == nil {
					t.Fatal("video lost")
				}
				if b.Video.FileURI != "gs://bucket/clip.mp4" {
					t.Fatalf("video payload lost: %+v", b.Video)
				}
			},
		},
		{
			name: "document",
			block: ir.ContentBlock{Type: "document", Document: &ir.DocumentBlock{
				Kind: "pdf", MIMEType: "application/pdf", Title: "spec",
				Source: &ir.DocumentSource{Type: "base64", MediaType: "application/pdf", Data: "JVBER"},
			}},
			check: func(t *testing.T, b ir.ContentBlock) {
				if b.Document == nil || b.Document.Source == nil {
					t.Fatalf("document lost: %+v", b.Document)
				}
				if b.Document.Source.Data != "JVBER" || b.Document.Title != "spec" {
					t.Fatalf("document payload lost: %+v", b.Document)
				}
			},
		},
		{
			name: "input_audio",
			block: ir.ContentBlock{Type: "input_audio", InputAudio: &ir.InputAudioBlock{
				Data: "QUJD", Format: "mp3",
			}},
			check: func(t *testing.T, b ir.ContentBlock) {
				if b.InputAudio == nil {
					t.Fatal("input_audio lost")
				}
				if b.InputAudio.Data != "QUJD" || b.InputAudio.Format != "mp3" {
					t.Fatalf("input_audio payload lost: %+v", b.InputAudio)
				}
			},
		},
		{
			name: "tool_use",
			block: ir.ContentBlock{Type: "tool_use", ToolUse: &ir.ToolUse{
				ID: "tu_1", Name: "lookup", Input: json.RawMessage(`{"q":"x"}`),
			}},
			check: func(t *testing.T, b ir.ContentBlock) {
				if b.ToolUse == nil {
					t.Fatal("tool_use lost")
				}
				if b.ToolUse.ID != "tu_1" || b.ToolUse.Name != "lookup" {
					t.Fatalf("tool_use payload lost: %+v", b.ToolUse)
				}
				if string(b.ToolUse.Input) != `{"q":"x"}` {
					t.Fatalf("tool_use input = %s", b.ToolUse.Input)
				}
			},
		},
		{
			name: "tool_result with nested blocks",
			block: ir.ContentBlock{Type: "tool_result", ToolResult: &ir.ToolResult{
				ToolUseID: "tu_1",
				IsError:   true,
				Content:   []ir.ContentBlock{{Type: "text", Text: "not found"}},
			}},
			check: func(t *testing.T, b ir.ContentBlock) {
				if b.ToolResult == nil {
					t.Fatal("tool_result lost")
				}
				if b.ToolResult.ToolUseID != "tu_1" || !b.ToolResult.IsError {
					t.Fatalf("tool_result payload lost: %+v", b.ToolResult)
				}
				if len(b.ToolResult.Content) != 1 || b.ToolResult.Content[0].Text != "not found" {
					t.Fatalf("nested tool_result content lost: %+v", b.ToolResult.Content)
				}
			},
		},
		{
			name: "tool_result keeps native Gemini response",
			block: ir.ContentBlock{Type: "tool_result", ToolResult: &ir.ToolResult{
				ToolUseID:      "gemini_call_lookup",
				Content:        []ir.ContentBlock{{Type: "text", Text: `{"value":7}`}},
				GeminiResponse: json.RawMessage(`{"value":7,"unknown":{"keep":true}}`),
			}},
			check: func(t *testing.T, b ir.ContentBlock) {
				if b.ToolResult == nil {
					t.Fatal("tool_result lost")
				}
				if b.ToolResult.GeminiResponse == nil {
					t.Fatal("GeminiResponse lost across persistence round-trip")
				}
				if string(b.ToolResult.GeminiResponse) != `{"value":7,"unknown":{"keep":true}}` {
					t.Fatalf("GeminiResponse = %s, want structured value preserved", b.ToolResult.GeminiResponse)
				}
			},
		},
		{
			name: "thinking keeps its signature",
			block: ir.ContentBlock{Type: "thinking", Thinking: &ir.ThinkingBlock{
				Thinking: "step one", Signature: "sig-abc",
			}},
			check: func(t *testing.T, b ir.ContentBlock) {
				if b.Thinking == nil {
					t.Fatal("thinking lost")
				}
				// Signature loss is what makes Anthropic reject the next turn
				// with HTTP 400 (see ir.ThinkingBlock's doc comment).
				if b.Thinking.Thinking != "step one" || b.Thinking.Signature != "sig-abc" {
					t.Fatalf("thinking payload lost: %+v", b.Thinking)
				}
			},
		},
		{
			name:  "redacted_thinking",
			block: ir.ContentBlock{Type: "redacted_thinking", RedactedThinking: "opaque-blob"},
			check: func(t *testing.T, b ir.ContentBlock) {
				if b.RedactedThinking != "opaque-blob" {
					t.Fatalf("redacted_thinking = %q", b.RedactedThinking)
				}
			},
		},
		{
			name: "cache_control and index",
			block: ir.ContentBlock{
				Type:         "text",
				Text:         "cached prefix",
				CacheControl: &ir.CacheControl{Type: "ephemeral"},
				Index:        intPtr(3),
			},
			check: func(t *testing.T, b ir.ContentBlock) {
				if b.CacheControl == nil || b.CacheControl.Type != "ephemeral" {
					t.Fatalf("cache_control lost: %+v", b.CacheControl)
				}
				if b.Index == nil || *b.Index != 3 {
					t.Fatalf("index lost: %+v", b.Index)
				}
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := ir.Message{Role: "user", Content: []ir.ContentBlock{c.block}}
			got := persistAndRecover(t, in)
			if got.Role != "user" {
				t.Fatalf("role = %q, want user", got.Role)
			}
			if len(got.Content) != 1 {
				t.Fatalf("expected 1 block back, got %d: %+v", len(got.Content), got.Content)
			}
			if got.Content[0].Type != c.block.Type {
				t.Fatalf("block type = %q, want %q", got.Content[0].Type, c.block.Type)
			}
			c.check(t, got.Content[0])
		})
	}
}

// TestPersist_UnknownProviderBlockKeepsPayload — the IR parsers preserve a
// block type they cannot model by stashing its JSON in
// ContentBlock.RawContent (parse_openai.go:331 and friends). The envelope must
// carry that field, otherwise the block persists as a bare {"type":"..."} and
// the original fields are gone.
func TestPersist_UnknownProviderBlockKeepsPayload(t *testing.T) {
	original := `{"type":"provider_widget","widget":{"id":7,"label":"go"}}`
	in := ir.Message{
		Role: "user",
		Content: []ir.ContentBlock{
			{Type: "text", Text: "render this"},
			{Type: "provider_widget", RawContent: original},
		},
	}

	got := persistAndRecover(t, in)
	if len(got.Content) != 2 {
		t.Fatalf("expected 2 blocks, got %d: %+v", len(got.Content), got.Content)
	}
	if got.Content[0].Text != "render this" {
		t.Fatalf("sibling text block lost: %+v", got.Content[0])
	}

	// The IR contract is a JSON *string*, not json.RawMessage: every
	// serializer reads it via block.RawContent.(string). A json.RawMessage
	// here would fail that assertion and the block would be dropped on the
	// way back to the provider.
	raw, ok := got.Content[1].RawContent.(string)
	if !ok {
		t.Fatalf("RawContent type = %T, want string (internal/ir serializers assert .(string))",
			got.Content[1].RawContent)
	}
	var round map[string]any
	if err := json.Unmarshal([]byte(raw), &round); err != nil {
		t.Fatalf("RawContent is not valid JSON: %v (%s)", err, raw)
	}
	widget, ok := round["widget"].(map[string]any)
	if !ok {
		t.Fatalf("widget payload lost: %s", raw)
	}
	if widget["label"] != "go" {
		t.Fatalf("widget.label = %v, want go (%s)", widget["label"], raw)
	}
}

// TestPersist_MultimodalSurvivesDiskRoundTrip is the regression guard for the
// headline bug: MessageFromIR stored the IR envelope under RawContent, but
// MarshalJSON wrote that whole envelope into the `content` field, producing a
// nested message the reader could not decode. Multimodal payload reached the
// DB and could not be read back.
func TestPersist_MultimodalSurvivesDiskRoundTrip(t *testing.T) {
	in := ir.Message{
		Role: "user",
		Content: []ir.ContentBlock{
			{Type: "text", Text: "what is in this image?"},
			{Type: "image", Image: &ir.ImageSource{
				Type: "url", MediaType: "image/jpeg", URL: "https://example.com/cat.jpg",
			}},
		},
	}

	got := persistAndRecover(t, in)
	if len(got.Content) != 2 {
		t.Fatalf("expected 2 blocks after round-trip, got %d: %+v", len(got.Content), got.Content)
	}
	if got.Content[0].Text != "what is in this image?" {
		t.Fatalf("text block lost: %+v", got.Content[0])
	}
	if got.Content[1].Image == nil {
		t.Fatalf("image block lost: %+v", got.Content[1])
	}
	if got.Content[1].Image.URL != "https://example.com/cat.jpg" {
		t.Fatalf("image URL = %q", got.Content[1].Image.URL)
	}
	if got.Content[1].Image.MediaType != "image/jpeg" {
		t.Fatalf("image media type = %q, want image/jpeg", got.Content[1].Image.MediaType)
	}
}

// TestPersist_ContentColumnIsNeverANestedMessage pins the wire shape itself.
// The `content` column must hold a string or a block array — never a nested
// {"role":...,"content":...} object.
func TestPersist_ContentColumnIsNeverANestedMessage(t *testing.T) {
	in := ir.Message{
		Role: "user",
		Content: []ir.ContentBlock{
			{Type: "text", Text: "look"},
			{Type: "image", Image: &ir.ImageSource{Type: "url", URL: "https://example.com/x.png"}},
		},
	}
	wire, err := json.Marshal(MessageFromIR(in))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var probe struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(wire, &probe); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if probe.Role != "user" {
		t.Fatalf("role = %q, want user (wire: %s)", probe.Role, wire)
	}
	if len(probe.Content) == 0 || probe.Content[0] != '[' {
		t.Fatalf("content must be a block array, got %s (full wire: %s)", probe.Content, wire)
	}
	if strings.Contains(string(probe.Content), `"role"`) {
		t.Fatalf("content must not nest a message envelope: %s", probe.Content)
	}
}

// TestPersist_ToolCallsStayLegacyShaped — assistant tool calls must persist in
// OpenAI's nested {"function":{"name","arguments"}} shape. A flat
// {"name","arguments"} pair parses in the legacy probe as a tool call with an
// empty function name, so the shape is load-bearing for readers that do not
// know about the envelope.
func TestPersist_ToolCallsStayLegacyShaped(t *testing.T) {
	in := ir.Message{
		Role: "assistant",
		ToolCalls: []ir.ToolCall{{
			ID:   "call_1",
			Type: "function",
			Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "get_weather", Arguments: `{"city":"SF"}`},
		}},
	}

	wire, err := json.Marshal(MessageFromIR(in))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var probe struct {
		ToolCalls []struct {
			ID       string `json:"id"`
			Type     string `json:"type"`
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	}
	if err := json.Unmarshal(wire, &probe); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(probe.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call on the wire, got %d: %s", len(probe.ToolCalls), wire)
	}
	if probe.ToolCalls[0].Function.Name != "get_weather" {
		t.Fatalf("nested function.name lost: %s", wire)
	}
	if probe.ToolCalls[0].Function.Arguments != `{"city":"SF"}` {
		t.Fatalf("nested function.arguments lost: %s", wire)
	}

	got := persistAndRecover(t, in)
	if len(got.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call after round-trip, got %d", len(got.ToolCalls))
	}
	if got.ToolCalls[0].Function.Name != "get_weather" {
		t.Fatalf("tool call name lost: %+v", got.ToolCalls[0])
	}
	if got.ToolCalls[0].Function.Arguments != `{"city":"SF"}` {
		t.Fatalf("tool call arguments lost: %+v", got.ToolCalls[0])
	}
}

// TestReadBack_NullContentKeepsRoleAndToolCalls — `content: null` alongside
// `tool_calls` is the standard OpenAI assistant shape. UnmarshalJSON mirrors
// that null into RawContent, where json.Unmarshal decodes it into the envelope
// struct *without error* and yields a zero envelope. Treating that as a
// successful recovery made ToIR return the empty message and discard the role
// and tool calls it already held.
func TestReadBack_NullContentKeepsRoleAndToolCalls(t *testing.T) {
	row := []byte(`[{"role":"assistant","content":null,` +
		`"tool_calls":[{"id":"call_1","type":"function",` +
		`"function":{"name":"get_weather","arguments":"{\"city\":\"SF\"}"}}],` +
		`"name":"weather_agent"}]`)

	var msgs []Message
	if err := json.Unmarshal(row, &msgs); err != nil {
		t.Fatalf("unmarshal row: %v", err)
	}
	got := IRMessagesFromV2(msgs)
	if len(got) != 1 {
		t.Fatalf("expected 1 IR message, got %d", len(got))
	}
	if got[0].Role != "assistant" {
		t.Fatalf("role lost: %q (whole message: %+v)", got[0].Role, got[0])
	}
	if got[0].Name != "weather_agent" {
		t.Fatalf("name lost: %q", got[0].Name)
	}
	if len(got[0].ToolCalls) != 1 {
		t.Fatalf("tool calls lost: %+v", got[0])
	}
	if got[0].ToolCalls[0].Function.Name != "get_weather" {
		t.Fatalf("tool call name lost: %+v", got[0].ToolCalls[0])
	}
}

// TestPersist_IndexOnlyTextBlockKeepsIndex — a text block carrying only Index
// has no nested object field, so the pre-marker heuristic classified it as a
// legacy wire block and the wire decoder (which reads only type/text) dropped
// Index. The explicit envelope marker makes the classification exact.
func TestPersist_IndexOnlyTextBlockKeepsIndex(t *testing.T) {
	in := ir.Message{
		Role:    "user",
		Content: []ir.ContentBlock{{Type: "text", Text: "interleaved", Index: intPtr(7)}},
	}
	got := persistAndRecover(t, in)
	if len(got.Content) != 1 {
		t.Fatalf("expected 1 block, got %d", len(got.Content))
	}
	if got.Content[0].Text != "interleaved" {
		t.Fatalf("text lost: %+v", got.Content[0])
	}
	if got.Content[0].Index == nil || *got.Content[0].Index != 7 {
		t.Fatalf("index lost: %+v", got.Content[0].Index)
	}
}

// TestDecode_AnthropicBlockWithCacheControl — Anthropic attaches an
// object-valued `cache_control` to genuine wire blocks. Treating that key as an
// envelope marker routed the block to the envelope decoder, which has no
// `source` field, so an image's payload was lost purely because it carried a
// cache hint.
//
// The invariant: presence of `cache_control` must not change how the rest of
// the block decodes. Each case asserts the payload AND, where present, the hint.
func TestDecode_AnthropicBlockWithCacheControl(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantHint bool
		check    func(*testing.T, ir.ContentBlock)
	}{
		{
			name: "image with cache_control",
			body: `[{"role":"user","content":[
				{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBOR"},
				 "cache_control":{"type":"ephemeral"}}
			]}]`,
			wantHint: true,
			check:    assertAnthropicImage,
		},
		{
			name: "image without cache_control",
			body: `[{"role":"user","content":[
				{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBOR"}}
			]}]`,
			wantHint: false,
			check:    assertAnthropicImage,
		},
		{
			name: "document with cache_control",
			body: `[{"role":"user","content":[
				{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"JVBER"},
				 "cache_control":{"type":"ephemeral"}}
			]}]`,
			wantHint: true,
			check: func(t *testing.T, b ir.ContentBlock) {
				if b.Document == nil || b.Document.Source == nil {
					t.Fatalf("document payload lost: %+v", b)
					return
				}
				if b.Document.Source.Data != "JVBER" {
					t.Fatalf("document data lost: %+v", b.Document.Source)
				}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := IRMessagesFromJSON(json.RawMessage(c.body))
			if len(got) != 1 || len(got[0].Content) != 1 {
				t.Fatalf("unexpected parse result: %+v", got)
			}
			block := got[0].Content[0]
			c.check(t, block)
			if c.wantHint && (block.CacheControl == nil || block.CacheControl.Type != "ephemeral") {
				t.Fatalf("cache hint lost: %+v", block.CacheControl)
			}
			if !c.wantHint && block.CacheControl != nil {
				t.Fatalf("cache hint invented: %+v", block.CacheControl)
			}
		})
	}
}

func assertAnthropicImage(t *testing.T, b ir.ContentBlock) {
	t.Helper()
	if b.Type != "image" {
		t.Fatalf("type = %q, want image", b.Type)
	}
	if b.Image == nil {
		t.Fatalf("image payload lost: %+v", b)
		return
	}
	if b.Image.Data != "iVBOR" || b.Image.MediaType != "image/png" {
		t.Fatalf("image payload wrong: %+v", b.Image)
	}
}

// TestPersist_MessageLevelRawContentSurvives — ir.Message.RawContent (as
// opposed to the per-block field) is written to a sibling `raw` key. Previously
// the reader looked for that key but nothing ever wrote it, so the payload was
// dropped on write.
func TestPersist_MessageLevelRawContentSurvives(t *testing.T) {
	in := ir.Message{
		Role:       "user",
		Content:    []ir.ContentBlock{{Type: "text", Text: "hi"}},
		RawContent: `{"provider_extension":{"beta":true}}`,
	}

	wire, err := json.Marshal(MessageFromIR(in))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(wire), "provider_extension") {
		t.Fatalf("message-level raw not written to the wire: %s", wire)
	}

	got := persistAndRecover(t, in)
	raw, ok := got.RawContent.(string)
	if !ok {
		t.Fatalf("RawContent type = %T, want string: %+v", got.RawContent, got)
	}
	if !strings.Contains(raw, "provider_extension") {
		t.Fatalf("message-level raw lost: %s", raw)
	}
	if len(got.Content) != 1 || got.Content[0].Text != "hi" {
		t.Fatalf("content lost alongside raw: %+v", got.Content)
	}
}

// TestPersist_RoundTripIsIdempotent — persisting an already-persisted message
// must not accumulate anything (a growing RawContent, or a message flipping
// between the collapse branch and the envelope branch on each pass).
func TestPersist_RoundTripIsIdempotent(t *testing.T) {
	cases := []ir.Message{
		{Role: "user", Content: []ir.ContentBlock{{Type: "text", Text: "plain"}}},
		{Role: "user", Content: []ir.ContentBlock{
			{Type: "text", Text: "look"},
			{Type: "image", Image: &ir.ImageSource{Type: "url", URL: "https://example.com/x.png"}},
		}},
		{Role: "user", Content: []ir.ContentBlock{
			{Type: "text", Text: "cached", CacheControl: &ir.CacheControl{Type: "ephemeral"}},
		}},
	}
	for i, in := range cases {
		first := MessageFromIR(in)
		wire1, err := json.Marshal(first)
		if err != nil {
			t.Fatalf("case %d marshal 1: %v", i, err)
		}
		var readBack Message
		if err := json.Unmarshal(wire1, &readBack); err != nil {
			t.Fatalf("case %d unmarshal 1: %v", i, err)
		}
		wire2, err := json.Marshal(MessageFromIR(readBack.ToIR()))
		if err != nil {
			t.Fatalf("case %d marshal 2: %v", i, err)
		}
		if string(wire1) != string(wire2) {
			t.Fatalf("case %d not idempotent:\n pass1 %s\n pass2 %s", i, wire1, wire2)
		}
	}
}

// TestReadBack_ProviderNativeContentSurvivesAlongsideMessageRaw — a row can
// carry provider-native `content` blocks *and* a message-level `raw` key.
// Rebuilding a full envelope from that row forced the native blocks through the
// envelope's narrower block struct (no `image_url`, no Anthropic `source`) and,
// because the envelope then took precedence, the intact ContentRaw was never
// consulted. Both must survive.
func TestReadBack_ProviderNativeContentSurvivesAlongsideMessageRaw(t *testing.T) {
	cases := []struct {
		name  string
		row   string
		check func(*testing.T, ir.ContentBlock)
	}{
		{
			name: "OpenAI image_url",
			row: `[{"role":"user","content":[{"type":"image_url",` +
				`"image_url":{"url":"https://e.com/a.png"}}],"raw":{"m":1}}]`,
			check: func(t *testing.T, b ir.ContentBlock) {
				if b.Image == nil || b.Image.URL != "https://e.com/a.png" {
					t.Fatalf("native image_url lost: %+v", b)
				}
			},
		},
		{
			name: "Anthropic source-shaped image",
			row: `[{"role":"user","content":[{"type":"image",` +
				`"source":{"type":"base64","media_type":"image/png","data":"iVBOR"}}],"raw":{"m":1}}]`,
			check: func(t *testing.T, b ir.ContentBlock) {
				if b.Type != "image" {
					t.Fatalf("type = %q, want image", b.Type)
				}
				if b.Image == nil || b.Image.Data != "iVBOR" {
					t.Fatalf("Anthropic source-shaped image lost: %+v", b.Image)
				}
			},
		},
		{
			name: "unknown provider block",
			row:  `[{"role":"user","content":[{"type":"widget","w":7}],"raw":{"m":1}}]`,
			check: func(t *testing.T, b ir.ContentBlock) {
				raw, ok := b.RawContent.(string)
				if !ok {
					t.Fatalf("unknown block lost (RawContent=%T): %+v", b.RawContent, b)
				}
				if !strings.Contains(raw, `"w":7`) {
					t.Fatalf("unknown block payload lost: %s", raw)
				}
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var msgs []Message
			if err := json.Unmarshal([]byte(c.row), &msgs); err != nil {
				t.Fatalf("unmarshal row: %v", err)
			}
			got := IRMessagesFromV2(msgs)
			if len(got) != 1 {
				t.Fatalf("expected 1 message, got %d", len(got))
			}
			if got[0].Role != "user" {
				t.Fatalf("role lost: %q", got[0].Role)
			}
			// The message-level raw must survive too.
			raw, ok := got[0].RawContent.(string)
			if !ok || !strings.Contains(raw, `"m":1`) {
				t.Fatalf("message-level raw lost: %#v", got[0].RawContent)
			}
			if len(got[0].Content) != 1 {
				t.Fatalf("expected 1 content block, got %d: %+v", len(got[0].Content), got[0].Content)
			}
			c.check(t, got[0].Content[0])
		})
	}
}

// TestDecode_WireCacheControlAndIndexOnAnyBlockType — cache_control and index
// are cross-cutting Anthropic wire fields that ride on a block of any type,
// text included. The wire decoder must read them directly; relying on them as
// envelope markers either misrouted image/document blocks or silently dropped
// the hint on text blocks.
func TestDecode_WireCacheControlAndIndexOnAnyBlockType(t *testing.T) {
	raw := json.RawMessage(`[{"role":"user","content":[
		{"type":"text","text":"cached prefix","cache_control":{"type":"ephemeral"},"index":2}
	]}]`)
	got := IRMessagesFromJSON(raw)
	if len(got) != 1 || len(got[0].Content) != 1 {
		t.Fatalf("unexpected parse result: %+v", got)
	}
	b := got[0].Content[0]
	if b.Text != "cached prefix" {
		t.Fatalf("text lost: %+v", b)
	}
	if b.CacheControl == nil || b.CacheControl.Type != "ephemeral" {
		t.Fatalf("wire cache_control lost: %+v", b.CacheControl)
	}
	if b.Index == nil || *b.Index != 2 {
		t.Fatalf("wire index lost: %+v", b.Index)
	}
}

// TestPersist_EnvelopeMarkerIsNeverZero — the marker's value is reserved for a
// future version check, so a re-encoding path must not emit `"$ir":0`.
func TestPersist_EnvelopeMarkerIsNeverZero(t *testing.T) {
	in := ir.Message{Role: "user", Content: []ir.ContentBlock{
		{Type: "image", Image: &ir.ImageSource{Type: "url", URL: "https://e.com/x.png"}},
	}}
	wire, err := json.Marshal(MessageFromIR(in))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(wire), `"$ir":0`) {
		t.Fatalf("marker emitted as 0: %s", wire)
	}
	if !strings.Contains(string(wire), `"$ir":1`) {
		t.Fatalf("envelope block missing its marker: %s", wire)
	}
}

// ── OpenAI wire decoding ────────────────────────────────────────────────

// TestDecodeImageBlock_DoesNotFabricateMediaType — the decoder used to hardcode
// MediaType="image/png" for every image block regardless of the actual image,
// so a JPEG persisted as a PNG and any downstream consumer trusting the field
// was misled. The wire body carries no media type, so the field must stay empty.
func TestDecodeImageBlock_DoesNotFabricateMediaType(t *testing.T) {
	raw := json.RawMessage(`{"messages":[
		{"role":"user","content":[
			{"type":"image_url","image_url":{"url":"https://example.com/a.jpg","detail":"low"}}
		]}
	]}`)
	got := IRMessagesFromJSON(raw)
	if len(got) != 1 || len(got[0].Content) != 1 {
		t.Fatalf("unexpected parse result: %+v", got)
	}
	img := got[0].Content[0].Image
	if img == nil {
		t.Fatalf("image lost: %+v", got[0].Content[0])
		return
	}
	if img.MediaType != "" {
		t.Fatalf("media type = %q, want empty (the wire body does not carry one)", img.MediaType)
	}
	if img.URL != "https://example.com/a.jpg" {
		t.Fatalf("url = %q", img.URL)
	}
	if img.Detail != "low" {
		t.Fatalf("detail = %q, want low", img.Detail)
	}
	if img.Type != "url" {
		t.Fatalf("source type = %q, want url", img.Type)
	}
}

// TestDecodeImageBlock_BareStringForm — some clients send image_url as a bare
// string rather than the {"url":..} object. Both must decode.
//
// The type must be one the decoder models ("image_url"); an unmodelled type
// like "input_image" takes the default branch and never reaches
// decodeImageSource, which would make this test vacuous.
func TestDecodeImageBlock_BareStringForm(t *testing.T) {
	raw := json.RawMessage(`[{"role":"user","content":[
		{"type":"image_url","image_url":"https://example.com/b.png"}
	]}]`)
	got := IRMessagesFromJSON(raw)
	if len(got) != 1 || len(got[0].Content) != 1 {
		t.Fatalf("unexpected parse result: %+v", got)
	}
	img := got[0].Content[0].Image
	if img == nil {
		t.Fatalf("bare-string image_url not decoded: %+v", got[0].Content[0])
		return
	}
	if img.URL != "https://example.com/b.png" {
		t.Fatalf("url = %q", img.URL)
	}
	if img.Type != "url" {
		t.Fatalf("source type = %q, want url", img.Type)
	}
}

// TestDecodeContentBlock_UnmodelledTypeIsPreserved — a block type the decoder
// does not model must survive verbatim under RawContent (as a string, matching
// internal/ir's own parsers) rather than being emptied to a bare {"type":..}.
func TestDecodeContentBlock_UnmodelledTypeIsPreserved(t *testing.T) {
	raw := json.RawMessage(`[{"role":"user","content":[
		{"type":"input_image","image_url":"https://example.com/b.png"}
	]}]`)
	got := IRMessagesFromJSON(raw)
	if len(got) != 1 || len(got[0].Content) != 1 {
		t.Fatalf("unexpected parse result: %+v", got)
	}
	preserved, ok := got[0].Content[0].RawContent.(string)
	if !ok {
		t.Fatalf("RawContent type = %T, want string: %+v",
			got[0].Content[0].RawContent, got[0].Content[0])
	}
	if !strings.Contains(preserved, "https://example.com/b.png") {
		t.Fatalf("unmodelled block payload lost: %s", preserved)
	}
}

// TestDecode_NormalizesImageDiscriminant — internal/ir's own parser rewrites
// OpenAI's "image_url" type to "image" (parse_openai.go:310) and both
// serializers switch on `Type == "image"`. A block left as "image_url" matches
// no case, so the image is dropped on the way back out to the provider.
func TestDecode_NormalizesImageDiscriminant(t *testing.T) {
	cases := []struct {
		name string
		body string
		want ir.ImageSource
	}{
		{
			name: "OpenAI image_url object",
			body: `[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://e.com/a.png"}}]}]`,
			want: ir.ImageSource{Type: "url", URL: "https://e.com/a.png"},
		},
		{
			name: "Anthropic source carrier",
			body: `[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBOR"}}]}]`,
			want: ir.ImageSource{Type: "base64", MediaType: "image/png", Data: "iVBOR"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := IRMessagesFromJSON(json.RawMessage(c.body))
			if len(got) != 1 || len(got[0].Content) != 1 {
				t.Fatalf("unexpected parse result: %+v", got)
			}
			b := got[0].Content[0]
			if b.Type != "image" {
				t.Fatalf("type = %q, want image (serializers switch on it)", b.Type)
			}
			if b.Image == nil {
				t.Fatalf("image payload lost: %+v", b)
				return
			}
			if b.Image.Type != c.want.Type || b.Image.URL != c.want.URL ||
				b.Image.Data != c.want.Data || b.Image.MediaType != c.want.MediaType {
				t.Fatalf("image = %+v, want %+v", b.Image, c.want)
			}
		})
	}
}

// TestDecode_UndecodableImageDoesNotKeepImageDiscriminant — a block we cannot
// model must not keep `type:"image"` with a nil Image: serialize_anthropic's
// validator rejects the entire request with "source is missing", and its image
// branch dereferences block.Image unguarded. Preserving the block verbatim is
// the safe outcome.
func TestDecode_UndecodableImageDoesNotKeepImageDiscriminant(t *testing.T) {
	cases := []string{
		`[{"role":"user","content":[{"type":"image"}]}]`,
		`[{"role":"user","content":[{"type":"image","source":{}}]}]`,
		`[{"role":"user","content":[{"type":"image","source":{"type":"base64"}}]}]`,
	}
	for _, body := range cases {
		got := IRMessagesFromJSON(json.RawMessage(body))
		if len(got) != 1 || len(got[0].Content) != 1 {
			t.Fatalf("unexpected parse result for %s: %+v", body, got)
		}
		b := got[0].Content[0]
		if b.Image != nil {
			t.Fatalf("%s: fabricated an empty image source: %+v", body, b.Image)
		}
		if _, ok := b.RawContent.(string); !ok {
			t.Fatalf("%s: block neither decoded nor preserved: %+v", body, b)
		}
	}
}

// TestDecode_AnthropicDocumentSource — a document carrying a usable `source`
// decodes into ir.DocumentBlock; one without keeps no "document" discriminant,
// for the same validator reason as images above.
func TestDecode_AnthropicDocumentSource(t *testing.T) {
	body := `[{"role":"user","content":[{"type":"document","title":"spec",
		"source":{"type":"base64","media_type":"application/pdf","data":"JVBER"}}]}]`
	got := IRMessagesFromJSON(json.RawMessage(body))
	if len(got) != 1 || len(got[0].Content) != 1 {
		t.Fatalf("unexpected parse result: %+v", got)
	}
	b := got[0].Content[0]
	if b.Document == nil || b.Document.Source == nil {
		t.Fatalf("document not decoded: %+v", b)
		return
	}
	if b.Document.Source.Data != "JVBER" {
		t.Fatalf("document data lost: %+v", b.Document.Source)
	}
	if b.Document.MIMEType != "application/pdf" {
		t.Fatalf("mime type = %q", b.Document.MIMEType)
	}
	if b.Document.Title != "spec" {
		t.Fatalf("title = %q", b.Document.Title)
	}

	// No usable source → must not stay a "document" with a nil Document.
	bare := IRMessagesFromJSON(json.RawMessage(`[{"role":"user","content":[{"type":"document","x":1}]}]`))
	if len(bare) != 1 || len(bare[0].Content) != 1 {
		t.Fatalf("unexpected parse result: %+v", bare)
	}
	if bare[0].Content[0].Type == "document" && bare[0].Content[0].Document == nil {
		t.Fatalf("kept document discriminant with nil Document: %+v", bare[0].Content[0])
	}
	if _, ok := bare[0].Content[0].RawContent.(string); !ok {
		t.Fatalf("undecodable document not preserved: %+v", bare[0].Content[0])
	}
}

// TestDecodeContentBlocks_MixedShapesInOneMessage — the envelope decision is
// per-block, not per-message. The previous whole-message sniff meant a single
// legacy-looking block forced every sibling onto the lossy decoder.
func TestDecodeContentBlocks_MixedShapesInOneMessage(t *testing.T) {
	// Block 1 is OpenAI wire (image_url), block 2 is envelope-encoded (image).
	raw := json.RawMessage(`[{"role":"user","content":[
		{"type":"image_url","image_url":{"url":"https://example.com/wire.png"}},
		{"type":"image","image":{"type":"base64","media_type":"image/gif","data":"R0lG"}}
	]}]`)
	got := IRMessagesFromJSON(raw)
	if len(got) != 1 {
		t.Fatalf("expected 1 message, got %d", len(got))
	}
	if len(got[0].Content) != 2 {
		t.Fatalf("expected 2 blocks, got %d: %+v", len(got[0].Content), got[0].Content)
	}
	if got[0].Content[0].Image == nil || got[0].Content[0].Image.URL != "https://example.com/wire.png" {
		t.Fatalf("wire-shaped block lost: %+v", got[0].Content[0])
	}
	if got[0].Content[1].Image == nil {
		t.Fatalf("envelope-shaped block lost: %+v", got[0].Content[1])
	}
	if got[0].Content[1].Image.MediaType != "image/gif" || got[0].Content[1].Image.Data != "R0lG" {
		t.Fatalf("envelope image payload lost: %+v", got[0].Content[1].Image)
	}
}

// TestPersist_TextOnlyStaysByteIdenticalToLegacy — the dominant production
// shape must not change. Any drift here rewrites every existing row's format.
func TestPersist_TextOnlyStaysByteIdenticalToLegacy(t *testing.T) {
	in := []ir.Message{
		{Role: "system", Content: []ir.ContentBlock{{Type: "text", Text: "be brief"}}},
		{Role: "user", Content: []ir.ContentBlock{{Type: "text", Text: "hi"}}},
	}
	wire, err := safeJSONMarshal(IRMessagesToV2(in))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `[{"role":"system","content":"be brief"},{"role":"user","content":"hi"}]`
	if string(wire) != want {
		t.Fatalf("legacy wire shape changed:\n got %s\nwant %s", wire, want)
	}
}

// TestPersist_GeminiStructuredResponseSurvivesRelayRoundTrip exercises the full
// Gemini same-protocol path: a native functionResponse.response (object with
// unknown members) is parsed from the wire, persisted via the v2 envelope
// round-trip, and serialized back to Gemini. Without the GeminiResponse
// persistence fix the structured value collapses to a {"result":"..."} text
// wrapper after the session round-trip.
func TestPersist_GeminiStructuredResponseSurvivesRelayRoundTrip(t *testing.T) {
	wire := `{"contents":[{"role":"function","parts":[{
		"functionResponse":{"name":"lookup","response":{"value":7,"unknown":{"keep":true}}}
	}]}]}`

	parsed, err := ir.ParseGemini([]byte(wire))
	if err != nil {
		t.Fatalf("ParseGemini: %v", err)
	}
	if len(parsed.Messages) != 1 {
		t.Fatalf("want 1 message, got %d", len(parsed.Messages))
	}

	// Persist through the v2 envelope round-trip.
	persisted := persistAndRecover(t, parsed.Messages[0])
	if len(persisted.Content) != 1 || persisted.Content[0].ToolResult == nil {
		t.Fatalf("tool_result lost: %#v", persisted.Content)
	}
	tr := persisted.Content[0].ToolResult
	if tr.GeminiResponse == nil {
		t.Fatal("GeminiResponse lost across persistence round-trip")
	}
	if string(tr.GeminiResponse) != `{"value":7,"unknown":{"keep":true}}` {
		t.Fatalf("GeminiResponse = %s, want structured value preserved", tr.GeminiResponse)
	}

	// Serialize back to Gemini and confirm the structured shape is intact.
	out, err := ir.SerializeGemini(&ir.InternalRequest{
		Messages: []ir.Message{persisted},
	})
	if err != nil {
		t.Fatalf("SerializeGemini: %v", err)
	}
	var gemini struct {
		Contents []struct {
			Parts []struct {
				FunctionResponse json.RawMessage `json:"functionResponse"`
			} `json:"parts"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(out, &gemini); err != nil {
		t.Fatalf("unmarshal Gemini output: %v", err)
	}
	if len(gemini.Contents) != 1 || len(gemini.Contents[0].Parts) != 1 {
		t.Fatalf("unexpected Gemini output: %s", out)
	}
	var fr struct {
		Response json.RawMessage `json:"response"`
	}
	if err := json.Unmarshal(gemini.Contents[0].Parts[0].FunctionResponse, &fr); err != nil {
		t.Fatalf("unmarshal functionResponse: %v", err)
	}
	var value any
	if err := json.Unmarshal(fr.Response, &value); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	want := map[string]any{
		"value":   float64(7),
		"unknown": map[string]any{"keep": true},
	}
	if !reflect.DeepEqual(value, want) {
		t.Fatalf("round-trip response = %#v, want %#v", value, want)
	}
}
