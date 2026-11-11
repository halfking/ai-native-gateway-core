package ir

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestResponsesReasoningItemNotPassedToAnthropic guards the 2026-11-09 fix for
// the claude-opus-5-5 empty_model_response incident on 211.
//
// Spec-conforming Responses clients (AI-SDK / ZCode 3.17.2) replay reasoning
// items from previous turns inside input[]:
//
//	{"type":"reasoning","id":"rs_...","summary":[{"type":"summary_text","text":"..."}]}
//
// These items carry NO role. The parser's unknown-item fallback used to wrap
// them as an assistant message with a raw content block, and the Anthropic
// serializer's default branch forwarded the raw item VERBATIM — emitting
// {"type":"reasoning", ...} content blocks into the Anthropic Messages wire.
// Anthropic Messages rejects unknown content-block types with a 400
// (invalid_request_error), so every ZCode turn whose history contained a
// reasoning item failed against anthropic-messages upstreams (apiclaude) with
// "Malformed conversation content" — while synthetic tests without reasoning
// items and direct apiclaude access both succeeded, which is exactly the
// incident's evidence pattern.
func TestResponsesReasoningItemNotPassedToAnthropic(t *testing.T) {
	body := []byte(`{
		"model": "claude-opus-5-5",
		"max_output_tokens": 64,
		"stream": true,
		"input": [
			{"type":"message","role":"user","content":[{"type":"input_text","text":"Review module 1."}]},
			{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"Need to read the file first."}],"encrypted_content":"enc-blob"},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Let me check."}]},
			{"type":"function_call","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"/src/m1.go\"}"},
			{"type":"function_call_output","call_id":"call_1","output":"package main"},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"Now summarize."}]}
		]
	}`)

	req, err := ParseResponses(body)
	if err != nil {
		t.Fatalf("ParseResponses: %v", err)
	}

	out, err := SerializeAnthropic(req)
	if err != nil {
		t.Fatalf("SerializeAnthropic: %v", err)
	}

	var wire struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &wire); err != nil {
		t.Fatalf("wire not valid JSON: %v\nbody: %s", err, out)
	}

	for i, m := range wire.Messages {
		var blocks []json.RawMessage
		if len(m.Content) > 0 && m.Content[0] == '"' {
			// content as a plain string is legal Anthropic wire shape.
			continue
		}
		if err := json.Unmarshal(m.Content, &blocks); err != nil {
			t.Fatalf("messages[%d].content not string or array: %v (%s)", i, err, m.Content)
		}
		for j, blk := range blocks {
			var probe struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(blk, &probe); err != nil {
				t.Fatalf("messages[%d].content[%d] not a JSON object: %v (%s)", i, j, err, blk)
			}
			// Anthropic Messages content-block closure: text, image, document,
			// tool_use, tool_result, thinking, redacted_thinking,
			// server_tool_use, web_search_tool_result, search_result,
			// container_upload, citations. "reasoning" (OpenAI Responses item
			// type) is NOT in it.
			//
			// ★ 2026-11-11 审计 R-3（★ 注释滞后修正）：★ 此前这份枚举里
			// ★ 有 guard_content（★ **不属于任何一处实现**）★ 且**漏了**
			// ★ search_result / container_upload（★ 两处实现都有）★★
			// ★★ ⇒ ★★ 这份注释此前是**假的**（★ 与两处实现都不一致）★★
			// ★★ ⇒ ★★ 现在它由门（★ protocol_block_closure_gate_test.go）★★
			// ★★ **逐项核对**：★ 注释里的枚举 vs 两处实现（★
			// ★★ internal/protocolformat 的 anthropicBlockTypes 与
			// ★★ internal/ir 的 anthropicLegalBlockType）★★ 的并集 ★★。
			if probe.Type == "reasoning" {
				t.Fatalf("OpenAI Responses reasoning item leaked verbatim into the "+
					"Anthropic wire: messages[%d].content[%d] type=%s (%s) — upstream "+
					"anthropic-messages rejects unknown block types with 400",
					i, j, probe.Type, truncateWire(string(blk), 160))
			}
		}
	}

	// The reasoning CONTENT must not vanish silently either: its text is the
	// model's own prior deliberation and is allowed to survive as a text/thinking
	// block, but the OpenAI item envelope (id rs_1 / encrypted_content /
	// summary array) must not leak.
	if strings.Contains(string(out), "encrypted_content") || strings.Contains(string(out), "\"summary\"") {
		t.Fatalf("Responses reasoning envelope leaked into Anthropic wire:\n%s", truncateWire(string(out), 400))
	}
}

// The same guard for the raw-item fallback in general: any unknown typed item
// without a role must not be forwarded verbatim to Anthropic.
func TestResponsesUnknownTypedItemNotPassedToAnthropic(t *testing.T) {
	body := []byte(`{
		"model": "claude-opus-5-5",
		"max_output_tokens": 64,
		"input": [
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},
			{"type":"some_future_item_type","id":"x_1","opaque":"payload"}
		]
	}`)

	req, err := ParseResponses(body)
	if err != nil {
		t.Fatalf("ParseResponses: %v", err)
	}
	out, err := SerializeAnthropic(req)
	if err != nil {
		t.Fatalf("SerializeAnthropic: %v", err)
	}
	var wire struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &wire); err != nil {
		t.Fatalf("wire not valid JSON: %v", err)
	}
	for i, m := range wire.Messages {
		if len(m.Content) > 0 && m.Content[0] == '"' {
			continue
		}
		var blocks []json.RawMessage
		if err := json.Unmarshal(m.Content, &blocks); err != nil {
			t.Fatalf("messages[%d].content not string or array: %v", i, err)
		}
		for j, blk := range blocks {
			var probe struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(blk, &probe); err != nil {
				t.Fatalf("messages[%d].content[%d] not a JSON object: %v", i, j, err)
			}
			if probe.Type == "some_future_item_type" {
				t.Fatalf("unknown Responses item type leaked verbatim into Anthropic wire: %s", blk)
			}
		}
	}
}

func truncateWire(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...[truncated]"
}
