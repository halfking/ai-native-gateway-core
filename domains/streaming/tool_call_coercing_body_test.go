package streaming

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 2026-09-23 forensics round (user report #13): minimax-m3 leaked its
// internal token wrappers verbatim into client-visible text when the
// client protocol was anthropic-messages / openai-responses — the coercer
// was wired only into the OpenAI-chat passthrough loop. The bridge
// path now wraps resp.Body with NewXMLToolCallCoercingBody; these tests
// pin the reader-level behaviour, including the exact leaked shape from
// the production report.

func readAllBody(t *testing.T, body io.Reader) string {
	t.Helper()
	out, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(out)
}

func sseLine(payload string) string {
	return "data: " + payload + "\n"
}

func TestCoercingBodyCoercesMiniMaxWrappedToolCallLeak(t *testing.T) {
	// Exact production leak shape (user report #13): the command text and
	// description arrive as separate wrapped payloads.
	upstream := sseLine(`{"choices":[{"delta":{"content":"minimax[>[<tool_call> ]<]minimax[>[]<]minimax[>[ssh ls]<]minimax[>[]<]minimax[>[Check nginx]<]minimax[>[ ]<]minimax[>[</tool_call>"}}]}`) +
		sseLine(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`) +
		"data: [DONE]\n\n"
	body := NewXMLToolCallCoercingBody(io.NopCloser(strings.NewReader(upstream)), true)
	got := readAllBody(t, body)
	if strings.Contains(got, "minimax[>[") {
		t.Fatalf("wrapper leaked through coercing body: %q", got)
	}
	if !strings.Contains(got, `"tool_calls"`) {
		t.Fatalf("expected coerced tool_calls delta: %q", got)
	}
	if strings.Contains(got, `"content":"minimax`) {
		t.Fatalf("wrapper text must not remain in content: %q", got)
	}
}

func TestCoercingBodyHandlesMarkerSplitAcrossDeltas(t *testing.T) {
	// The wrapper opener split at an arbitrary byte offset across two SSE
	// deltas ("minimax[>" + "[<tool_call>…"). Pre-fix the first fragment
	// did not match any full trigger substring and passed through visible.
	upstream := sseLine(`{"choices":[{"delta":{"content":"minimax[>"}}]}`) +
		sseLine(`{"choices":[{"delta":{"content":"[<tool_call>run it now"}}]}`) +
		sseLine(`{"choices":[{"delta":{"content":"</tool_call>"}}]}`) +
		"data: [DONE]\n\n"
	body := NewXMLToolCallCoercingBody(io.NopCloser(strings.NewReader(upstream)), true)
	got := readAllBody(t, body)
	if strings.Contains(got, "minimax[>") || strings.Contains(got, "<tool_call>") {
		t.Fatalf("split marker leaked through coercing body: %q", got)
	}
	if !strings.Contains(got, `"tool_calls"`) {
		t.Fatalf("expected coerced tool_calls delta: %q", got)
	}
}

func TestCoercingBodyFlushesFalsePositivePrefixAsContent(t *testing.T) {
	// A delta ending in a marker PREFIX that turns out to be ordinary text
	// (e.g. "<tool" from "<toolbar>") must be flushed back as visible
	// content, not swallowed.
	upstream := sseLine(`{"choices":[{"delta":{"content":"use the <tool"}}]}`) +
		sseLine(`{"choices":[{"delta":{"content":"bar menu"}}]}`) +
		"data: [DONE]\n\n"
	body := NewXMLToolCallCoercingBody(io.NopCloser(strings.NewReader(upstream)), true)
	got := readAllBody(t, body)
	if !strings.Contains(got, "use the <toolbar menu") {
		t.Fatalf("false-positive prefix must flush as content: %q", got)
	}
	if strings.Contains(got, `"tool_calls"`) {
		t.Fatalf("plain text must not be coerced into a tool call: %q", got)
	}
}

func TestCoercingBodyPassthroughWithoutToolsRequested(t *testing.T) {
	upstream := sseLine(`{"choices":[{"delta":{"content":"plain"}}]}`) + "data: [DONE]\n\n"
	// toolsRequested=false must return the ORIGINAL reader semantics —
	// content bytes flow through untouched.
	body := NewXMLToolCallCoercingBody(io.NopCloser(strings.NewReader(upstream)), false)
	got := readAllBody(t, body)
	if got != upstream {
		t.Fatalf("tool-less passthrough must be byte-identical: %q", got)
	}
}

func TestCoercingBodyPreservesOrdinaryStream(t *testing.T) {
	upstream := sseLine(`{"choices":[{"delta":{"role":"assistant"}}]}`) +
		sseLine(`{"choices":[{"delta":{"content":"hello world"}}]}`) +
		sseLine(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`) +
		"data: [DONE]\n\n"
	body := NewXMLToolCallCoercingBody(io.NopCloser(strings.NewReader(upstream)), true)
	got := readAllBody(t, body)
	if got != upstream {
		t.Fatalf("ordinary stream must round-trip unchanged:\n got: %q\nwant: %q", got, upstream)
	}
}

func TestCoercingBodyPropagatesClose(t *testing.T) {
	rc := &trackingReadCloser{Reader: strings.NewReader("data: [DONE]\n\n")}
	body := NewXMLToolCallCoercingBody(rc, true)
	if err := body.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !rc.closed {
		t.Fatal("Close must propagate to the wrapped body")
	}
}

type trackingReadCloser struct {
	io.Reader
	closed bool
}

func (c *trackingReadCloser) Close() error {
	c.closed = true
	return nil
}

// Integration seam: the anthropic bridge (ZCode's /v1/messages path over an
// OpenAI-shaped upstream) reads through NewXMLToolCallCoercingBody-wrapped
// resp.Body (cmd/gateway/main.go wiring). The leaked minimax-m3 wrapper text
// must surface as a real Anthropic tool_use content block, not text_delta.
func TestOpenAIToAnthropicBridgeWithCoercingBodyConvertsLeakToToolUse(t *testing.T) {
	upstream := sseLine(`{"choices":[{"delta":{"content":"minimax[>[<tool_call> ]<]minimax[>[]<]minimax[>[ssh ls]<]minimax[>[]<]minimax[>[Check nginx]<]minimax[>[ ]<]minimax[>[</tool_call>"}}]}`) +
		sseLine(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`) +
		"data: [DONE]\n\n"
	resp := &http.Response{
		Body:    NewXMLToolCallCoercingBody(io.NopCloser(strings.NewReader(upstream)), true),
		Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
	}
	rec := httptest.NewRecorder()
	out := StreamOpenAIToAnthropicSSE(context.Background(), rec, resp, "minimax-m3", "MiniMax-M3", "req-coercing-bridge", nil, nil)
	if out.Interrupted {
		t.Fatalf("bridge outcome interrupted: %+v", out)
	}
	body := rec.Body.String()
	if strings.Contains(body, "minimax[>[") || strings.Contains(body, "<tool_call>") {
		t.Fatalf("wrapper/tool-call text leaked into anthropic events: %q", body)
	}
	if !strings.Contains(body, `"type":"tool_use"`) {
		t.Fatalf("expected a tool_use content block: %q", body)
	}
}
