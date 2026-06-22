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

// streamSourceServer spins up an upstream HTTP server that emits the
// given SSE chunks in OpenAI Chat Completions stream shape and then
// signals end-of-stream with the "[DONE]" sentinel.
func streamSourceServer(t *testing.T, chunks []string) string {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher, _ := w.(http.Flusher)
		for _, c := range chunks {
			//nolint:errcheck // test write, non-critical
			io.WriteString(w, "data: "+c+"\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
		//nolint:errcheck // test write, non-critical
		io.WriteString(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	t.Cleanup(upstream.Close)
	return upstream.URL
}

// TestStreamAnthropicSSE_SplitsEmbeddedThink covers the realistic
// minimax upstream shape: the OpenAI `delta.content` field contains
// `<think>...</think>` followed by the visible answer. After
// processing, the forwarded Anthropic SSE stream MUST contain an
// independent thinking content_block (start+delta+stop at index 0)
// AND an independent text content_block (start+delta+stop at index 1)
// with the post-thinking text.
func TestStreamAnthropicSSE_SplitsEmbeddedThink(t *testing.T) {
	chunks := []string{
		`{"choices":[{"index":0,"delta":{"role":"assistant","content":"<think>"}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"plan step by step"}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"</think>\n\nHELLO"}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":11}}`,
	}
	url := streamSourceServer(t, chunks)

	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	//nolint:errcheck // best-effort close
	defer resp.Body.Close()

	capture := audit.NewStreamCapture()
	rec := httptest.NewRecorder()
	StreamAnthropicSSE(rec, resp, "minimax-m3", "MiniMax-M3", "req-split", capture, nil)

	body := rec.Body.String()
	type block struct {
		Index    int
		Type     string
		Thinking string
		Text     string
	}
	var blocks []block
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		var v struct {
			Type         string `json:"type"`
			Index        int    `json:"index"`
			ContentBlock *struct {
				Type string `json:"type"`
			} `json:"content_block"`
			Delta *struct {
				Type     string `json:"type"`
				Thinking string `json:"thinking"`
				Text     string `json:"text"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(payload), &v); err != nil {
			continue
		}
		switch v.Type {
		case "content_block_start":
			if v.ContentBlock != nil {
				blocks = append(blocks, block{Index: v.Index, Type: v.ContentBlock.Type})
			}
		case "content_block_delta":
			if v.Delta != nil && len(blocks) > 0 && blocks[len(blocks)-1].Index == v.Index {
				last := &blocks[len(blocks)-1]
				if v.Delta.Thinking != "" {
					last.Thinking = v.Delta.Thinking
				}
				if v.Delta.Text != "" {
					last.Text = v.Delta.Text
				}
			}
		}
	}
	if len(blocks) < 3 {
		t.Fatalf("expected at least 3 blocks (pre-declared text + thinking + post-think text); got %d: %+v body=%s", len(blocks), blocks, body)
	}
	if blocks[0].Type != "text" {
		t.Errorf("block[0] = %+v, want text (pre-declared, closed empty)", blocks[0])
	}
	if blocks[1].Type != "thinking" || blocks[1].Thinking != "plan step by step" {
		t.Errorf("block[1] = %+v, want thinking+plan step by step", blocks[1])
	}
	if blocks[2].Type != "text" || blocks[2].Text != "HELLO" {
		t.Errorf("block[2] = %+v, want text+HELLO", blocks[2])
	}
	if strings.Contains(body, `"text":"<think>`) {
		t.Errorf("body should not contain raw `<think>` prefix in text_delta; got:\n%s", body)
	}
	if !capture.HasThinking {
		t.Error("capture.HasThinking should be true after splitting")
	}
}

// TestStreamAnthropicSSE_NoThinkForwardsVerbatim covers no-<think> case.
func TestStreamAnthropicSSE_NoThinkForwardsVerbatim(t *testing.T) {
	chunks := []string{
		`{"choices":[{"index":0,"delta":{"role":"assistant","content":"HI"}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
	}
	url := streamSourceServer(t, chunks)
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	//nolint:errcheck // best-effort close
	defer resp.Body.Close()
	capture := audit.NewStreamCapture()
	rec := httptest.NewRecorder()
	StreamAnthropicSSE(rec, resp, "m", "m", "req-no", capture, nil)
	body := rec.Body.String()
	if !strings.Contains(body, `"text":"HI"`) {
		t.Errorf("expected text_delta carrying HI; got:\n%s", body)
	}
	if strings.Contains(body, `thinking_delta`) {
		t.Errorf("body should NOT contain thinking_delta when no <think>; got:\n%s", body)
	}
	if capture.HasThinking {
		t.Errorf("capture.HasThinking should be false")
	}
}

// TestStreamAnthropicSSE_ThinkAcrossChunks covers a <think> split
// across multiple upstream chunks.
func TestStreamAnthropicSSE_ThinkAcrossChunks(t *testing.T) {
	chunks := []string{
		`{"choices":[{"index":0,"delta":{"role":"assistant","content":"<th"}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"ink>step 1\nstep 2"}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"</think>\n\nDONE"}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":3}}`,
	}
	url := streamSourceServer(t, chunks)
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	//nolint:errcheck // best-effort close
	defer resp.Body.Close()
	capture := audit.NewStreamCapture()
	rec := httptest.NewRecorder()
	StreamAnthropicSSE(rec, resp, "m", "m", "req-cross", capture, nil)
	body := rec.Body.String()
	if !strings.Contains(body, `"thinking":"step 1\nstep 2"`) {
		t.Errorf("expected thinking_delta with full multi-chunk content; got:\n%s", body)
	}
	if !strings.Contains(body, `"text":"DONE"`) {
		t.Errorf("expected text_delta carrying DONE; got:\n%s", body)
	}
	if !capture.HasThinking {
		t.Errorf("capture.HasThinking should be true")
	}
}

// TestStreamAnthropicSSE_ThinkOnlyEmptyAfter covers a <think> that
// captures everything (no visible text after). No trailing empty text block.
func TestStreamAnthropicSSE_ThinkOnlyEmptyAfter(t *testing.T) {
	chunks := []string{
		`{"choices":[{"index":0,"delta":{"role":"assistant","content":"<think>only thinking</think>"}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2}}`,
	}
	url := streamSourceServer(t, chunks)
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	//nolint:errcheck // best-effort close
	defer resp.Body.Close()
	capture := audit.NewStreamCapture()
	rec := httptest.NewRecorder()
	StreamAnthropicSSE(rec, resp, "m", "m", "req-empty", capture, nil)
	body := rec.Body.String()
	if !strings.Contains(body, `"thinking":"only thinking"`) {
		t.Errorf("expected thinking_delta; got:\n%s", body)
	}
	if strings.Contains(body, `"index":1`) {
		t.Errorf("should NOT emit an empty trailing text block at index 1; got:\n%s", body)
	}
	if !capture.HasThinking {
		t.Errorf("capture.HasThinking should be true")
	}
}

// TestStreamAnthropicSSE_EmptyContent covers a stream where no text was emitted.
func TestStreamAnthropicSSE_EmptyContent(t *testing.T) {
	chunks := []string{
		`{"choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":0}}`,
	}
	url := streamSourceServer(t, chunks)
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	//nolint:errcheck // best-effort close
	defer resp.Body.Close()
	rec := httptest.NewRecorder()
	capture := audit.NewStreamCapture()
	StreamAnthropicSSE(rec, resp, "m", "m", "req-nocontent", capture, nil)
	body := rec.Body.String()
	if !strings.Contains(body, `"index":0,"type":"content_block_stop"`) {
		t.Errorf("expected content_block_stop at index 0; got:\n%s", body)
	}
	if strings.Contains(body, `"index":1`) {
		t.Errorf("no second block should be emitted for empty content; got:\n%s", body)
	}
}

// TestStreamAnthropicSSE_UsageAndMessageStop covers message_tail emission.
func TestStreamAnthropicSSE_UsageAndMessageStop(t *testing.T) {
	chunks := []string{
		`{"choices":[{"index":0,"delta":{"role":"assistant","content":"plain answer"}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"end_turn"}],"usage":{"prompt_tokens":7,"completion_tokens":3}}`,
	}
	url := streamSourceServer(t, chunks)
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	//nolint:errcheck // best-effort close
	defer resp.Body.Close()
	capture := audit.NewStreamCapture()
	rec := httptest.NewRecorder()
	StreamAnthropicSSE(rec, resp, "m", "m", "req-tail", capture, nil)
	body := rec.Body.String()
	if !strings.Contains(body, `"output_tokens":3`) {
		t.Errorf("message_delta should carry output_tokens; got:\n%s", body)
	}
	if !strings.Contains(body, `"type":"message_stop"`) {
		t.Errorf("message_stop must be emitted; got:\n%s", body)
	}
	if !strings.Contains(body, `"stop_reason":"end_turn"`) {
		t.Errorf("stop_reason should be mapped; got:\n%s", body)
	}
}

// TestStreamAnthropicSSE_PreservesAnthropicEnvelope ensures the
// envelope events are present regardless of split.
func TestStreamAnthropicSSE_PreservesAnthropicEnvelope(t *testing.T) {
	chunks := []string{
		`{"choices":[{"index":0,"delta":{"role":"assistant","content":"<think>p</think>\nOK"}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"end_turn"}],"usage":{"prompt_tokens":1,"completion_tokens":2}}`,
	}
	url := streamSourceServer(t, chunks)
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	//nolint:errcheck // best-effort close
	defer resp.Body.Close()
	rec := httptest.NewRecorder()
	StreamAnthropicSSE(rec, resp, "m", "m", "req-env", nil, nil)
	body := rec.Body.String()
	for _, evt := range []string{
		"event: message_start",
		"event: ping",
		"event: message_delta",
		"event: message_stop",
	} {
		if !strings.Contains(body, evt) {
			t.Errorf("envelope missing %q; got:\n%s", evt, body)
		}
	}
}