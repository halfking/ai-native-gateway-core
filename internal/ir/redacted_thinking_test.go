package ir

import (
	"encoding/json"
	"strings"
	"testing"
)

// A-#17 (audit round2): the real Anthropic wire format for redacted thinking
// is {"type":"redacted_thinking","data":"..."} — the former "thinking" key
// read/write pair silently dropped real-client payloads.
func TestRedactedThinkingWireFormat(t *testing.T) {
	raw := []byte(`{
		"model": "claude-sonnet-4-5",
		"max_tokens": 64,
		"messages": [
			{"role": "assistant", "content": [
				{"type": "redacted_thinking", "data": "EncBase64Payload=="},
				{"type": "text", "text": "answer"}
			]},
			{"role": "user", "content": "go on"}
		]
	}`)

	req, err := ParseAnthropic(raw)
	if err != nil {
		t.Fatalf("ParseAnthropic failed: %v", err)
	}
	blocks := req.Messages[0].Content
	if len(blocks) == 0 || blocks[0].RedactedThinking != "EncBase64Payload==" {
		t.Fatalf("redacted_thinking data not parsed: %+v", blocks)
	}

	out, err := SerializeAnthropic(req)
	if err != nil {
		t.Fatalf("SerializeAnthropic failed: %v", err)
	}
	if !strings.Contains(string(out), `"redacted_thinking"`) || !strings.Contains(string(out), `"data":"EncBase64Payload=="`) {
		t.Fatalf("wire output lost data key: %s", out)
	}
	if strings.Contains(string(out), `"thinking":"EncBase64Payload=="`) {
		t.Fatalf("legacy thinking key leaked into wire output: %s", out)
	}
}

// Response direction: upstream redacted_thinking blocks must survive parse →
// anthropic serialize (next-turn thinking chain verification).
func TestRedactedThinkingResponseRoundTrip(t *testing.T) {
	body := []byte(`{
		"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-sonnet-4-5",
		"content": [
			{"type": "redacted_thinking", "data": "RespPayload=="},
			{"type": "text", "text": "hi"}
		],
		"stop_reason": "end_turn",
		"usage": {"input_tokens": 1, "output_tokens": 1}
	}`)

	resp, err := ParseAnthropicResponse(body)
	if err != nil {
		t.Fatalf("ParseAnthropicResponse failed: %v", err)
	}
	found := false
	for _, c := range resp.Content {
		if c.Type == "redacted_thinking" {
			found = true
			if c.Data != "RespPayload==" {
				t.Fatalf("redacted payload lost: %+v", c)
			}
		}
	}
	if !found {
		t.Fatalf("redacted_thinking block dropped by response parse")
	}

	// Marshal → unmarshal the IR (storage path) and rebuild anthropic content.
	irJSON, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal IR: %v", err)
	}
	var resp2 InternalResponse
	if err := json.Unmarshal(irJSON, &resp2); err != nil {
		t.Fatalf("unmarshal IR: %v", err)
	}
	content := buildAnthropicResponseContent(&resp2)
	ok := false
	for _, blk := range content {
		if blk["type"] == "redacted_thinking" && blk["data"] == "RespPayload==" {
			ok = true
		}
	}
	if !ok {
		t.Fatalf("redacted_thinking lost in anthropic response content: %+v", content)
	}
}
