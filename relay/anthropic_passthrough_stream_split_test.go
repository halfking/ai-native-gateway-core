package relay

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/audit"
)

// TestStreamAnthropicPassthrough_SplitEmbeddedThink covers the realistic
// minimax-anthropic upstream shape: a single text block whose deltas
// begin with `<think>...</think>` followed by the visible answer. After
// processing, the forwarded SSE stream MUST contain an independent
// thinking content_block_start/delta/stop pair AND an independent text
// content_block_start/delta/stop pair with re-numbered indices.
func TestStreamAnthropicPassthrough_SplitEmbeddedThink(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		events := []string{
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"<think>plan</think>\"}}\n\n",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"\\n\\nHELLO\"}}\n\n",
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
		}
		for _, e := range events {
			w.Write([]byte(e))
		}
	}))
	defer upstream.Close()
	resp, err := http.Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	rec := httptest.NewRecorder()
	capture := audit.NewStreamCapture()
	_ = StreamAnthropicPassthrough(rec, resp, "minimax-m3", "MiniMax-M3", "req-split", capture)

	body := rec.Body.String()
	if !strings.Contains(body, `"type":"thinking_delta"`) {
		t.Errorf("body should contain a thinking_delta event; got:\n%s", body)
	}
	if !strings.Contains(body, `"thinking":"plan"`) {
		t.Errorf("body should carry the extracted thinking text; got:\n%s", body)
	}
	if !strings.Contains(body, `"text":"HELLO"`) {
		t.Errorf("body should carry the post-thinking text; got:\n%s", body)
	}
	// Original (un-rewritten) text_delta with `<think>...` must NOT
	// be present (we replaced it).
	if strings.Contains(body, `<think>plan</think>`) {
		t.Errorf("body should not contain the original `<think>` text; got:\n%s", body)
	}
	if !capture.HasThinking {
		t.Error("capture.HasThinking should be true after splitting")
	}
}

// TestStreamAnthropicPassthrough_NoThinkForwardsVerbatim covers the
// case where the text block contains NO <think> prefix: deltas must be
// forwarded byte-for-byte, in order.
func TestStreamAnthropicPassthrough_NoThinkForwardsVerbatim(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		events := []string{
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"HELLO \"}}\n\n",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"WORLD\"}}\n\n",
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
		}
		for _, e := range events {
			w.Write([]byte(e))
		}
	}))
	defer upstream.Close()
	resp, err := http.Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	rec := httptest.NewRecorder()
	capture := audit.NewStreamCapture()
	_ = StreamAnthropicPassthrough(rec, resp, "m", "m", "req-not", capture)

	body := rec.Body.String()
	if !strings.Contains(body, `"text":"HELLO "`) {
		t.Errorf("body should contain first delta; got:\n%s", body)
	}
	if !strings.Contains(body, `"text":"WORLD"`) {
		t.Errorf("body should contain second delta; got:\n%s", body)
	}
	if strings.Contains(body, `"type":"thinking_delta"`) {
		t.Errorf("body should NOT contain thinking_delta when there's no <think>; got:\n%s", body)
	}
	if capture.HasThinking {
		t.Errorf("capture.HasThinking should be false")
	}
}

// TestStreamAnthropicPassthrough_ThinkAcrossDeltas covers the realistic
// case where the `<think>...</think>` boundary lands in the middle of a
// delta (not on a delta boundary). The split logic must still produce
// the right two blocks.
func TestStreamAnthropicPassthrough_ThinkAcrossDeltas(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		events := []string{
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n",
			// Split happens mid-delta: "<think>plan, then</think>\nANSWER"
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"<think>plan, then\"}}\n\n",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"</think>\\nANSWER\"}}\n\n",
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
		}
		for _, e := range events {
			w.Write([]byte(e))
		}
	}))
	defer upstream.Close()
	resp, err := http.Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	rec := httptest.NewRecorder()
	capture := audit.NewStreamCapture()
	_ = StreamAnthropicPassthrough(rec, resp, "m", "m", "req-cross", capture)

	body := rec.Body.String()
	if !strings.Contains(body, `"thinking":"plan, then"`) {
		t.Errorf("body should carry the full extracted thinking text; got:\n%s", body)
	}
	if !strings.Contains(body, `"text":"ANSWER"`) {
		t.Errorf("body should carry ANSWER; got:\n%s", body)
	}
	if !capture.HasThinking {
		t.Errorf("capture.HasThinking should be true")
	}
}

// TestStreamAnthropicPassthrough_ThinkEmptyAfter covers a <think> that
// captures everything (no visible text after). Result should be a
// thinking block only, with NO empty trailing text block.
func TestStreamAnthropicPassthrough_ThinkEmptyAfter(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		events := []string{
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"<think>only thinking</think>\"}}\n\n",
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
		}
		for _, e := range events {
			w.Write([]byte(e))
		}
	}))
	defer upstream.Close()
	resp, err := http.Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	rec := httptest.NewRecorder()
	capture := audit.NewStreamCapture()
	_ = StreamAnthropicPassthrough(rec, resp, "m", "m", "req-empty", capture)

	body := rec.Body.String()
	if !strings.Contains(body, `"thinking":"only thinking"`) {
		t.Errorf("body should carry thinking; got:\n%s", body)
	}
	// When rest is empty, we should NOT emit a separate text block at all.
	// Check: no content_block_start with content_block.type=="text" appears
	// AFTER the content_block_stop for the thinking block.
	// Simpler check: count of text_block_start events == 1 (just the original buffer start, which we drop).
	// We won't emit any text block for empty rest.
	if strings.Contains(body, `"content_block":{"text":""`) {
		t.Errorf("body should NOT contain an empty trailing text block; got:\n%s", body)
	}
	if !capture.HasThinking {
		t.Errorf("capture.HasThinking should be true")
	}
}

// TestStreamAnthropicPassthrough_ToolUseUntouched covers a stream with
// a tool_use block AFTER a (possibly split) text block: tool_use must
// pass through unchanged.
func TestStreamAnthropicPassthrough_ToolUseUntouched(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		events := []string{
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"<tool_call>\"}}\n\n",
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_1\",\"name\":\"get_weather\",\"input\":{}}}\n\n",
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\n",
		}
		for _, e := range events {
			w.Write([]byte(e))
		}
	}))
	defer upstream.Close()
	resp, err := http.Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	rec := httptest.NewRecorder()
	capture := audit.NewStreamCapture()
	_ = StreamAnthropicPassthrough(rec, resp, "m", "m", "req-tool", capture)

	body := rec.Body.String()
	// tool_use must still be present byte-for-byte.
	if !strings.Contains(body, `"type":"tool_use"`) {
		t.Errorf("tool_use block must pass through unchanged; got:\n%s", body)
	}
	if !strings.Contains(body, `"name":"get_weather"`) {
		t.Errorf("tool_use.name lost; got:\n%s", body)
	}
	if !strings.Contains(body, `"id":"call_1"`) {
		t.Errorf("tool_use.id lost; got:\n%s", body)
	}
}

// TestStreamAnthropicPassthrough_MessageStartStop covers a complete
// minimal stream: just message_start and message_stop. Buffering logic
// must not corrupt such minimal streams.
func TestStreamAnthropicPassthrough_MessageStartStop(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_min\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n"))
		w.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer upstream.Close()
	resp, err := http.Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	rec := httptest.NewRecorder()
	capture := audit.NewStreamCapture()
	out := StreamAnthropicPassthrough(rec, resp, "m", "m", "req-min", capture)
	if out.Interrupted {
		t.Errorf("stream interrupted: %s", out.Reason)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"id":"msg_min"`) {
		t.Errorf("body must contain message_start payload; got:\n%s", body)
	}
	if !strings.Contains(body, `message_stop`) {
		t.Errorf("body must contain message_stop; got:\n%s", body)
	}
	if capture.InputTokens == nil || *capture.InputTokens != 1 {
		t.Errorf("usage.input_tokens = %v, want 1", capture.InputTokens)
	}
}

// TestStreamAnthropicPassthrough_RewrittenIndices ensures the post-split
// text block is emitted with a fresh, larger index than the original.
func TestStreamAnthropicPassthrough_RewrittenIndices(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		events := []string{
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":7,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":7,\"delta\":{\"type\":\"text_delta\",\"text\":\"<think>p</think>\\nX\"}}\n\n",
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":7}\n\n",
		}
		for _, e := range events {
			w.Write([]byte(e))
		}
	}))
	defer upstream.Close()
	resp, err := http.Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	rec := httptest.NewRecorder()
	capture := audit.NewStreamCapture()
	_ = StreamAnthropicPassthrough(rec, resp, "m", "m", "req-idx", capture)

	body := rec.Body.String()
	// Parse the rewritten SSE events and assert on the (index, type) of
	// each content_block_start / content_block_delta.
	type evt struct {
		Event string          `json:"-"`
		Data  json.RawMessage `json:"-"`
	}
	// Find content_block_start events with their index + content_block.type.
	starts := map[int]string{}
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		var s struct {
			Type         string `json:"type"`
			Index        int    `json:"index"`
			ContentBlock *struct {
				Type string `json:"type"`
			} `json:"content_block"`
		}
		if err := json.Unmarshal([]byte(payload), &s); err != nil {
			continue
		}
		if s.Type == "content_block_start" && s.ContentBlock != nil {
			starts[s.Index] = s.ContentBlock.Type
		}
	}
	if starts[7] != "thinking" {
		t.Errorf("expected content_block_start index=7 type=thinking; got %v", starts)
	}
	if starts[8] != "text" {
		t.Errorf("expected content_block_start index=8 type=text; got %v", starts)
	}
	if _, ok := starts[7]; ok {
		// already checked above that it is "thinking"
	}
	// Sanity: no raw `<think>...</think>` should remain in the body
	// (it must have been split out into the thinking block).
	if strings.Contains(body, "<think>p</think>") {
		t.Errorf("post-split must not contain raw `<think>p</think>`; got:\n%s", body)
	}
}

// TestStreamAnthropicPassthrough_BlankDeltasOK covers an edge case:
// empty text_delta (text=="") should still flush correctly through the
// buffered accumulator without crashing.
func TestStreamAnthropicPassthrough_BlankDeltasOK(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		events := []string{
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"\"}}\n\n",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"DONE\"}}\n\n",
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
		}
		for _, e := range events {
			w.Write([]byte(e))
		}
	}))
	defer upstream.Close()
	resp, err := http.Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	rec := httptest.NewRecorder()
	capture := audit.NewStreamCapture()
	out := StreamAnthropicPassthrough(rec, resp, "m", "m", "req-blank", capture)
	if out.Interrupted {
		t.Errorf("stream interrupted: %s", out.Reason)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"text":"DONE"`) {
		t.Errorf("body should contain DONE; got:\n%s", body)
	}
}

// TestStreamAnthropicPassthrough_PreservesOtherEvents covers the case
// where a stream contains message_start, message_delta, message_stop
// in addition to the text block — they must all pass through.
func TestStreamAnthropicPassthrough_PreservesOtherEvents(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		events := []string{
			"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_evts\",\"model\":\"minimax-m3\",\"usage\":{\"input_tokens\":11,\"output_tokens\":0}}}\n\n",
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"<think>p</think>\\nOK\"}}\n\n",
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":42}}\n\n",
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		}
		for _, e := range events {
			w.Write([]byte(e))
		}
	}))
	defer upstream.Close()
	resp, err := http.Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	rec := httptest.NewRecorder()
	capture := audit.NewStreamCapture()
	_ = StreamAnthropicPassthrough(rec, resp, "m", "m", "req-evts", capture)

	body := rec.Body.String()
	if !strings.Contains(body, "msg_evts") {
		t.Errorf("message_start lost; got:\n%s", body)
	}
	if !strings.Contains(body, "end_turn") {
		t.Errorf("message_delta stop_reason lost; got:\n%s", body)
	}
	if !strings.Contains(body, `"output_tokens":42`) {
		t.Errorf("message_delta usage lost; got:\n%s", body)
	}
	if capture.OutputTokens == nil || *capture.OutputTokens != 42 {
		t.Errorf("capture.OutputTokens = %v, want 42", capture.OutputTokens)
	}
}

// TestStreamAnthropicPassthrough_ThinkWithMultilineContent covers a
// thinking block containing newlines, which is typical of reasoning
// models that produce paragraph-long plans.
func TestStreamAnthropicPassthrough_ThinkWithMultilineContent(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		events := []string{
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"<think>step 1\\nstep 2\\nstep 3</think>\\nANSWER\"}}\n\n",
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
		}
		for _, e := range events {
			w.Write([]byte(e))
		}
	}))
	defer upstream.Close()
	resp, err := http.Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	rec := httptest.NewRecorder()
	capture := audit.NewStreamCapture()
	_ = StreamAnthropicPassthrough(rec, resp, "m", "m", "req-ml", capture)

	body := rec.Body.String()
	// Multiline thinking text must be preserved verbatim.
	want, _ := json.Marshal(map[string]any{
		"type":  "thinking_delta",
		"thinking": "step 1\nstep 2\nstep 3",
	})
	if !strings.Contains(body, string(want)) {
		t.Errorf("body should carry multiline thinking_delta payload %q; got:\n%s", want, body)
	}
	if !strings.Contains(body, `"text":"ANSWER"`) {
		t.Errorf("body should carry ANSWER; got:\n%s", body)
	}
}