package ir

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/modelname"
)

// TestSimulate_OpenAIToAnthropic_Sonnet5 walks the full Q3 conversion path
// for a client sending "model":"claude-sonnet-5" in OpenAI Chat Completions
// shape, ending in the Anthropic Messages body that goes to the upstream.
//
// This is the symptom the user reported: "上游没收到用户的提示词" — we
// need to confirm whether the prompt survives every transformation stage.
func TestSimulate_OpenAIToAnthropic_Sonnet5(t *testing.T) {
	const USER_PROMPT = "你好,请用中文回答:1+1=?"
	const CLIENT_MODEL = "claude-sonnet-5"

	clientBody := []byte(fmt.Sprintf(`{
		"model": %q,
		"messages": [
			{"role": "system", "content": "You are a helpful assistant."},
			{"role": "user",   "content": %q}
		],
		"max_tokens": 1024,
		"temperature": 0.7,
		"stream": false
	}`, CLIENT_MODEL, USER_PROMPT))

	t.Logf("=== Stage 0: Client body (what the client sent) ===")
	prettyPrint(t, "client_body", clientBody)

	// ─── Stage 1: Protocol detection ─────────────────────────────
	proto, conf, err := DetectProtocol(clientBody)
	if err != nil {
		t.Fatalf("DetectProtocol error: %v", err)
	}
	t.Logf("=== Stage 1: Protocol detection ===")
	t.Logf("  detected=%q confidence=%.3f (expect %q since body has messages[])",
		proto, conf, ProtocolOpenAIChat)
	if proto != ProtocolOpenAIChat {
		t.Errorf("FAIL: protocol = %q, want %q", proto, ProtocolOpenAIChat)
	}

	// ─── Stage 2: OpenAI → IR ─────────────────────────────────────
	irReq, err := ParseOpenAI(clientBody)
	if err != nil {
		t.Fatalf("ParseOpenAI error: %v", err)
	}
	t.Logf("=== Stage 2: OpenAI body → IR ===")
	t.Logf("  ir.Model     = %q (raw, unmodified client value)", irReq.Model)
	t.Logf("  ir.System    = %+v (extracted from system message)", irReq.System)
	t.Logf("  ir.Messages  = %d messages", len(irReq.Messages))
	for i, m := range irReq.Messages {
		t.Logf("    [%d] role=%q content=%d blocks", i, m.Role, len(m.Content))
		for j, b := range m.Content {
			t.Logf("        block[%d] type=%q text=%q", j, b.Type, b.Text)
		}
	}
	if irReq.Model != CLIENT_MODEL {
		t.Errorf("FAIL: ir.Model = %q, want %q (parser must preserve client value)", irReq.Model, CLIENT_MODEL)
	}
	if irReq.System == nil || irReq.System.Content != "You are a helpful assistant." {
		t.Errorf("FAIL: system prompt not extracted; got %+v", irReq.System)
	}
	if len(irReq.Messages) != 1 {
		t.Errorf("FAIL: expected 1 message after system extraction, got %d", len(irReq.Messages))
	}
	if len(irReq.Messages) > 0 && irReq.Messages[0].Role != "user" {
		t.Errorf("FAIL: first remaining message role = %q, want user", irReq.Messages[0].Role)
	}
	if len(irReq.Messages) > 0 && len(irReq.Messages[0].Content) > 0 &&
		irReq.Messages[0].Content[0].Text != USER_PROMPT {
		t.Errorf("FAIL: user prompt not preserved: got %q", irReq.Messages[0].Content[0].Text)
	}

	// ─── Stage 3: Model replacement (simulating executor_anthropic.go:399) ─
	// In production this comes from resolveOutboundModel(params, cand)
	// which returns params.OutboundModel OR cand.RawModel.
	// We simulate 4 cases:
	//
	//   Case A: Upstream is configured to know claude-sonnet-5
	//           (cand.RawModel = "claude-sonnet-5")
	//   Case B: Upstream aliases it to a real Anthropic model
	//           (cand.RawModel = "claude-sonnet-4-5-20250929")
	//   Case C: Model is unknown → resolveOutboundModel returns "" → FALLBACK
	//   Case D: Transform matrix overrides to a different name
	cases := []struct {
		name           string
		outboundModel  string
		rawModel       string
		expectUpstream string
		expectSystem   string
	}{
		{
			name:           "A: upstream recognizes the literal 'claude-sonnet-5'",
			outboundModel:  "",
			rawModel:       "claude-sonnet-5",
			expectUpstream: "claude-sonnet-5",
		},
		{
			name:           "B: upstream aliases to claude-sonnet-4-5-20250929",
			outboundModel:  "",
			rawModel:       "claude-sonnet-4-5-20250929",
			expectUpstream: "claude-sonnet-4-5-20250929",
		},
		{
			name:           "C: no offer / no raw model → empty string (BUG)",
			outboundModel:  "",
			rawModel:       "",
			expectUpstream: "",
		},
		{
			name:           "D: transform matrix forces a specific name",
			outboundModel:  "claude-sonnet-4-5-20250929",
			rawModel:       "should-not-be-used",
			expectUpstream: "claude-sonnet-4-5-20250929",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			irCopy := *irReq
			irCopy.Model = ""
			if tc.outboundModel != "" {
				irCopy.Model = tc.outboundModel
			} else {
				irCopy.Model = tc.rawModel
			}
			body, err := SerializeAnthropic(&irCopy)
			if err != nil {
				t.Fatalf("SerializeAnthropic: %v", err)
			}
			t.Logf("  → upstream model = %q", irCopy.Model)
			prettyPrint(t, "upstream_body", body)

			var result map[string]any
			if err := json.Unmarshal(body, &result); err != nil {
				t.Fatalf("unmarshal upstream body: %v", err)
			}

			if result["model"] != tc.expectUpstream {
				t.Errorf("FAIL: upstream model = %v, want %q", result["model"], tc.expectUpstream)
			}
			// Confirm user prompt survived every stage
			msgs, _ := result["messages"].([]any)
			if len(msgs) == 0 {
				t.Errorf("FAIL: upstream body has no messages array")
			} else {
				first := msgs[0].(map[string]any)
				content := first["content"]
				if !contentMatches(content, USER_PROMPT) {
					t.Errorf("FAIL: user prompt lost in upstream body. content=%v", content)
				} else {
					t.Logf("  ✓ user prompt survives in upstream body")
				}
			}
			sys, hasSys := result["system"]
			if !hasSys {
				t.Errorf("FAIL: upstream body missing 'system' field")
			} else {
				t.Logf("  system field: %v", sys)
			}
		})
	}
}

// TestSimulate_Multimodal_ImageBase64 verifies that base64 image data
// survives the round-trip without being silently dropped (a previously
// observed bug — see 2026-07-01 comment in parse_openai.go).
func TestSimulate_Multimodal_ImageBase64(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-5",
		"messages": [{
			"role": "user",
			"content": [
				{"type": "text", "text": "What is in this image?"},
				{"type": "image_url", "image_url": {"url": "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII="}}
			]
		}],
		"max_tokens": 256
	}`)
	t.Logf("=== Multimodal image (data URI) ===")
	prettyPrint(t, "client_body", body)

	ir, err := ParseOpenAI(body)
	if err != nil {
		t.Fatalf("ParseOpenAI: %v", err)
	}
	if len(ir.Messages) != 1 || len(ir.Messages[0].Content) != 2 {
		t.Fatalf("FAIL: expected 1 message with 2 content blocks, got %d msgs", len(ir.Messages))
	}
	img := ir.Messages[0].Content[1].Image
	if img == nil {
		t.Fatalf("FAIL: image block missing")
	}
	t.Logf("  image parsed: type=%q media_type=%q data_len=%d url=%q",
		img.Type, img.MediaType, len(img.Data), img.URL)
	if img.Type != "base64" {
		t.Errorf("FAIL: image.Type = %q, want base64", img.Type)
	}
	if img.MediaType != "image/png" {
		t.Errorf("FAIL: MediaType = %q, want image/png", img.MediaType)
	}
	if len(img.Data) < 50 {
		t.Errorf("FAIL: base64 data truncated, got %d chars", len(img.Data))
	}

	// Serialize back to Anthropic
	ir.Model = "claude-sonnet-4-5-20250929"
	out, err := SerializeAnthropic(ir)
	if err != nil {
		t.Fatalf("SerializeAnthropic: %v", err)
	}
	t.Logf("=== Anthropic body ===")
	prettyPrint(t, "upstream_body", out)

	var result map[string]any
	_ = json.Unmarshal(out, &result)
	msgs := result["messages"].([]any)
	userMsg := msgs[0].(map[string]any)
	content := userMsg["content"].([]any)
	if len(content) != 2 {
		t.Errorf("FAIL: expected 2 content blocks, got %d", len(content))
	}
	imageBlock := content[1].(map[string]any)
	source := imageBlock["source"].(map[string]any)
	if source["type"] != "base64" {
		t.Errorf("FAIL: source.type = %v, want base64", source["type"])
	}
	if source["media_type"] != "image/png" {
		t.Errorf("FAIL: source.media_type = %v, want image/png", source["media_type"])
	}
	if data, ok := source["data"].(string); !ok || len(data) < 50 {
		t.Errorf("FAIL: source.data missing or truncated, got %v", source["data"])
	} else {
		t.Logf("  ✓ base64 image survived: %d chars", len(data))
	}
}

// TestSimulate_ToolCallConversion verifies tool_calls on assistant
// messages get correctly translated to Anthropic tool_use blocks.
func TestSimulate_ToolCallConversion(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-5",
		"messages": [
			{"role": "user", "content": "What is the weather in Tokyo?"},
			{"role": "assistant", "content": null, "tool_calls": [{
				"id": "call_abc123",
				"type": "function",
				"function": {
					"name": "get_weather",
					"arguments": "{\"location\": \"Tokyo\"}"
				}
			}]},
			{"role": "tool", "tool_call_id": "call_abc123", "content": "{\"temp\": 22, \"condition\": \"sunny\"}"}
		],
		"tools": [{
			"type": "function",
			"function": {
				"name": "get_weather",
				"description": "Get current weather",
				"parameters": {
					"type": "object",
					"properties": {"location": {"type": "string"}}
				}
			}
		}]
	}`)
	t.Logf("=== Tool calls ===")
	prettyPrint(t, "client_body", body)

	ir, err := ParseOpenAI(body)
	if err != nil {
		t.Fatalf("ParseOpenAI: %v", err)
	}
	t.Logf("  ir.Messages: %d", len(ir.Messages))
	for i, m := range ir.Messages {
		t.Logf("    [%d] role=%q content=%d blocks tool_calls=%d tool_call_id=%q",
			i, m.Role, len(m.Content), len(m.ToolCalls), m.ToolCallID)
	}
	if len(ir.Tools) != 1 {
		t.Errorf("FAIL: expected 1 tool, got %d", len(ir.Tools))
	}
	if len(ir.Tools) == 0 || ir.Tools[0].Name != "get_weather" {
		t.Errorf("FAIL: tool name lost")
	}
	if len(ir.Tools) > 0 && len(ir.Tools[0].Parameters) == 0 {
		t.Errorf("FAIL: tool parameters lost (the 2026-06-24 P0 bug)")
	}

	ir.Model = "claude-sonnet-4-5-20250929"
	out, err := SerializeAnthropic(ir)
	if err != nil {
		t.Fatalf("SerializeAnthropic: %v", err)
	}
	prettyPrint(t, "upstream_body", out)

	var result map[string]any
	_ = json.Unmarshal(out, &result)

	// Verify messages structure: tool role → user with tool_result,
	// assistant has tool_use block
	msgs := result["messages"].([]any)
	if len(msgs) != 3 {
		t.Errorf("FAIL: expected 3 messages (user, assistant, user-with-tool-result), got %d", len(msgs))
	}
	// Last message should be user with tool_result
	if m, ok := msgs[2].(map[string]any); ok {
		if m["role"] != "user" {
			t.Errorf("FAIL: tool role not converted to user; got %v", m["role"])
		}
	}
	tools, ok := result["tools"].([]any)
	if !ok || len(tools) == 0 {
		t.Errorf("FAIL: tools missing in upstream body")
	} else {
		tool := tools[0].(map[string]any)
		if tool["name"] != "get_weather" {
			t.Errorf("FAIL: tool name = %v", tool["name"])
		}
		if _, has := tool["input_schema"]; !has {
			t.Errorf("FAIL: input_schema missing on tool (Anthropic requires it)")
		}
	}
}

// TestSimulate_ModelNameVariants walks through all the spelling variants
// "claude-sonnet-5" can take, and shows the result of NormalizeRouteKey
// and NormalizeRouteKeyAliases for each.
func TestSimulate_ModelNameVariants(t *testing.T) {
	inputs := []string{
		"claude-sonnet-5",
		"claude-sonnet-4-5",
		"claude-sonnet-4.5",
		"Claude-Sonnet-5",
		"claude-sonnet-5-20250929",
		"anthropic/claude-sonnet-5",
		"claude_sonnet_5",
	}
	for _, name := range inputs {
		t.Run("input="+name, func(t *testing.T) {
			canonical := modelname.NormalizeRouteKey(name)
			aliases := modelname.NormalizeRouteKeyAliases(name)
			t.Logf("  %-32s → canonical=%-28q  aliases=%v", name, canonical, aliases)
		})
	}
}

func prettyPrint(t *testing.T, label string, raw []byte) {
	t.Helper()
	var any_ any
	if err := json.Unmarshal(raw, &any_); err != nil {
		t.Logf("  [%s] (raw) %s", label, string(raw))
		return
	}
	pretty, _ := json.MarshalIndent(any_, "    ", "  ")
	t.Logf("  --- %s ---\n    %s", label, string(pretty))
}

func contentMatches(content any, want string) bool {
	switch c := content.(type) {
	case string:
		return strings.Contains(c, want)
	case []any:
		for _, block := range c {
			if bm, ok := block.(map[string]any); ok {
				if text, ok := bm["text"].(string); ok && strings.Contains(text, want) {
					return true
				}
			}
		}
	}
	return false
}
