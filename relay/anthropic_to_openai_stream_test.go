package relay

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/audit"
)

func readOpenAIChunks(t *testing.T, body string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			continue
		}
		var c map[string]any
		if err := json.Unmarshal([]byte(payload), &c); err != nil {
			t.Fatalf("malformed chunk %q: %v", payload, err)
		}
		out = append(out, c)
	}
	return out
}

func TestStreamAnthropicSSEToOpenAI_TextOnly(t *testing.T) {
	events := strings.Join([]string{
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_01\",\"model\":\"MiniMax-M3\",\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text\",\"text\":\"Hello\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text\",\"text\":\" world\"}}\n\n",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	}, "")

	resp := &http.Response{
		Body:       io.NopCloser(strings.NewReader(events)),
		Request:    httptest.NewRequest("POST", "/", nil),
		Header:     http.Header{},
		StatusCode: 200,
	}
	rec := httptest.NewRecorder()

	out := StreamAnthropicSSEToOpenAI(rec, resp, "MiniMax-M3", "MiniMax-M3", "req-1", &audit.StreamCapture{}, nil)

	if out.Interrupted {
		t.Fatalf("expected clean completion, got interrupted: %s", out.Reason)
	}

	chunks := readOpenAIChunks(t, rec.Body.String())
	if len(chunks) < 3 {
		t.Fatalf("expected at least 3 chunks, got %d:\n%s", len(chunks), rec.Body.String())
	}

	if chunks[0]["model"] != "MiniMax-M3" {
		t.Errorf("chunk 0 model: got %v, want MiniMax-M3", chunks[0]["model"])
	}
	choices := chunks[0]["choices"].([]any)
	if c0 := choices[0].(map[string]any); c0["delta"].(map[string]any)["role"] != "assistant" {
		t.Errorf("chunk 0 role: got %v, want assistant", c0["delta"])
	}

	allContent := collectContent(chunks[1:])
	if allContent != "Hello world" {
		t.Errorf("concatenated content: got %q, want %q", allContent, "Hello world")
	}

	if !strings.Contains(rec.Body.String(), "data: [DONE]") {
		t.Errorf("missing [DONE] sentinel in output:\n%s", rec.Body.String())
	}
}

func TestStreamAnthropicSSEToOpenAI_WithThinkBlock(t *testing.T) {
	events := strings.Join([]string{
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_02\",\"model\":\"MiniMax-M3\",\"usage\":{\"input_tokens\":5,\"output_tokens\":0}}}\n\n",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text\",\"text\":\"<think>The user said hi.</think>\\n\\nHi there!\"}}\n\n",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":15}}\n\n",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	}, "")

	resp := &http.Response{
		Body:       io.NopCloser(strings.NewReader(events)),
		Request:    httptest.NewRequest("POST", "/", nil),
		StatusCode: 200,
	}
	rec := httptest.NewRecorder()

	out := StreamAnthropicSSEToOpenAI(rec, resp, "MiniMax-M3", "MiniMax-M3", "req-2", &audit.StreamCapture{}, nil)
	if out.Interrupted {
		t.Fatalf("expected clean completion, got interrupted: %s", out.Reason)
	}

	chunks := readOpenAIChunks(t, rec.Body.String())
	allReasoning := collectReasoning(chunks)
	allContent := collectContent(chunks)
	if allReasoning != "The user said hi." {
		t.Errorf("reasoning_content: got %q, want %q", allReasoning, "The user said hi.")
	}
	if allContent != "Hi there!" {
		t.Errorf("content: got %q, want %q", allContent, "Hi there!")
	}
}



func TestStreamAnthropicSSEToOpenAI_PingDropped(t *testing.T) {
	events := strings.Join([]string{
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_04\",\"model\":\"M\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n",
		"event: ping\ndata: {\"type\":\"ping\"}\n\n",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text\",\"text\":\"pong\"}}\n\n",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	}, "")

	resp := &http.Response{
		Body:       io.NopCloser(strings.NewReader(events)),
		Request:    httptest.NewRequest("POST", "/", nil),
		StatusCode: 200,
	}
	rec := httptest.NewRecorder()
	_ = StreamAnthropicSSEToOpenAI(rec, resp, "M", "M", "req-4", &audit.StreamCapture{}, nil)

	if strings.Contains(rec.Body.String(), `"type":"ping"`) {
		t.Errorf("ping event leaked into OpenAI output:\n%s", rec.Body.String())
	}
}



func collectContent(chunks []map[string]any) string {
	var s string
	for _, c := range chunks {
		choices, _ := c["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		delta, _ := choices[0].(map[string]any)["delta"].(map[string]any)
		if delta == nil {
			continue
		}
		if v, ok := delta["content"].(string); ok {
			s += v
		}
	}
	return s
}

func collectReasoning(chunks []map[string]any) string {
	var s string
	for _, c := range chunks {
		choices, _ := c["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		delta, _ := choices[0].(map[string]any)["delta"].(map[string]any)
		if delta == nil {
			continue
		}
		if v, ok := delta["reasoning_content"].(string); ok {
			s += v
		}
	}
	return s
}
