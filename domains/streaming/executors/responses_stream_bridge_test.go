package executors

// responses_stream_bridge_test.go — the Responses-SSE → chat-SSE reader.
//
// The fixtures are the REAL vapEUR frames captured 2026-10-02 against
// credential 126 (gpt-5.3-codex), including the relay's dialect quirk: every
// frame is a bare `data:` line with "type" inside the JSON and NO `event:` line
// at all. A parser that required the canonical `event:` line would pass on
// hand-written tests and swallow every real frame, so both dialects are pinned
// here.

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

const vapEURTextStream = "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_026e\",\"object\":\"response\",\"created_at\":1790913740,\"status\":\"in_progress\",\"model\":\"gpt-5.3-codex\"},\"sequence_number\":0}\n\n" +
	"data: {\"type\":\"response.in_progress\",\"response\":{\"id\":\"resp_026e\",\"status\":\"in_progress\"},\"sequence_number\":1}\n\n" +
	"data: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"msg_026e1\",\"type\":\"message\",\"status\":\"in_progress\",\"role\":\"assistant\",\"content\":[]},\"output_index\":0,\"sequence_number\":2}\n\n" +
	"data: {\"type\":\"response.content_part.added\",\"content_index\":0,\"item_id\":\"msg_026e1\",\"part\":{\"type\":\"output_text\",\"text\":\"\",\"annotations\":[]},\"output_index\":0,\"sequence_number\":3}\n\n" +
	"data: {\"type\":\"response.output_text.delta\",\"content_index\":0,\"delta\":\"Hi\",\"item_id\":\"msg_026e1\",\"obfuscation\":\"9poSMar5hWvVbX\",\"output_index\":0,\"sequence_number\":4}\n\n" +
	"data: {\"type\":\"response.output_text.delta\",\"content_index\":0,\"delta\":\" there\",\"item_id\":\"msg_026e1\",\"output_index\":0,\"sequence_number\":5}\n\n" +
	"data: {\"type\":\"response.output_text.done\",\"content_index\":0,\"item_id\":\"msg_026e1\",\"text\":\"Hi there\",\"output_index\":0,\"sequence_number\":6}\n\n" +
	"data: {\"type\":\"response.content_part.done\",\"content_index\":0,\"item_id\":\"msg_026e1\",\"part\":{\"type\":\"output_text\",\"text\":\"Hi there\"},\"output_index\":0,\"sequence_number\":7}\n\n" +
	"data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"msg_026e1\",\"type\":\"message\",\"status\":\"completed\"},\"output_index\":0,\"sequence_number\":8}\n\n" +
	"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_026e\",\"status\":\"completed\",\"usage\":{\"input_tokens\":8,\"output_tokens\":3,\"total_tokens\":11}},\"sequence_number\":9}\n\n"

const vapEURToolStream = "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_tool\",\"created_at\":1790913800,\"status\":\"in_progress\",\"model\":\"gpt-5.3-codex\"},\"sequence_number\":0}\n\n" +
	"data: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"fc_0f12\",\"type\":\"function_call\",\"status\":\"in_progress\",\"arguments\":\"\",\"call_id\":\"call_308C\",\"name\":\"get_time\"},\"output_index\":0,\"sequence_number\":2}\n\n" +
	"data: {\"type\":\"response.function_call_arguments.delta\",\"delta\":\"{\\\"\",\"item_id\":\"fc_0f12\",\"obfuscation\":\"v566UvRfsBvspk\",\"output_index\":0,\"sequence_number\":3}\n\n" +
	"data: {\"type\":\"response.function_call_arguments.delta\",\"delta\":\"tz\\\":\\\"\",\"item_id\":\"fc_0f12\",\"output_index\":0,\"sequence_number\":4}\n\n" +
	"data: {\"type\":\"response.function_call_arguments.delta\",\"delta\":\"UTC\\\"}\",\"item_id\":\"fc_0f12\",\"output_index\":0,\"sequence_number\":5}\n\n" +
	"data: {\"type\":\"response.function_call_arguments.done\",\"arguments\":\"{\\\"tz\\\":\\\"UTC\\\"}\",\"item_id\":\"fc_0f12\",\"output_index\":0,\"sequence_number\":6}\n\n" +
	"data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"fc_0f12\",\"type\":\"function_call\",\"status\":\"completed\",\"arguments\":\"{\\\"tz\\\":\\\"UTC\\\"}\",\"call_id\":\"call_308C\",\"name\":\"get_time\"},\"output_index\":0,\"sequence_number\":7}\n\n" +
	"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_tool\",\"status\":\"completed\",\"usage\":{\"input_tokens\":53,\"output_tokens\":18,\"total_tokens\":71}},\"sequence_number\":8}\n\n"

// The canonical OpenAI dialect: an explicit `event:` line before each payload.
const canonicalTextStream = "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_canon\",\"created_at\":1790913900,\"model\":\"gpt-5.3-codex\"}}\n\n" +
	"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"Yo\"}\n\n" +
	"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"

func collectChatChunks(t *testing.T, raw string) []map[string]any {
	t.Helper()
	rc := responsesToChatStream(context.Background(), io.NopCloser(strings.NewReader(raw)), "fallback-model")
	out, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read converted stream: %v", err)
	}
	var chunks []map[string]any
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		if payload == "[DONE]" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(payload), &m); err != nil {
			t.Fatalf("emitted frame is not JSON: %v (%s)", err, payload)
		}
		chunks = append(chunks, m)
	}
	if !strings.Contains(string(out), "data: [DONE]") {
		t.Fatal("converted stream must terminate with data: [DONE]")
	}
	return chunks
}

func chunkDeltas(chunks []map[string]any) string {
	var b strings.Builder
	for _, c := range chunks {
		choices, _ := c["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		ch, _ := choices[0].(map[string]any)
		delta, _ := ch["delta"].(map[string]any)
		if s, _ := delta["content"].(string); s != "" {
			b.WriteString(s)
		}
	}
	return b.String()
}

func lastFinishReason(chunks []map[string]any) string {
	if len(chunks) == 0 {
		return ""
	}
	choices, _ := chunks[len(chunks)-1]["choices"].([]any)
	if len(choices) == 0 {
		return ""
	}
	ch, _ := choices[0].(map[string]any)
	s, _ := ch["finish_reason"].(string)
	return s
}

func TestResponsesStreamBridge_TextStream(t *testing.T) {
	chunks := collectChatChunks(t, vapEURTextStream)
	if got := chunkDeltas(chunks); got != "Hi there" {
		t.Fatalf("assembled text = %q, want %q (frames=%d)", got, "Hi there", len(chunks))
	}
	if got := lastFinishReason(chunks); got != "stop" {
		t.Fatalf("finish_reason = %q, want stop", got)
	}
	// The model must come from the upstream envelope, not the caller's guess.
	if chunks[0]["model"] != "gpt-5.3-codex" {
		t.Fatalf("model = %v, want the model from response.created", chunks[0]["model"])
	}
	if chunks[0]["object"] != "chat.completion.chunk" {
		t.Fatalf("object = %v, want chat.completion.chunk", chunks[0]["object"])
	}
	// The first chunk must carry role, which every OpenAI SDK expects.
	choices, _ := chunks[0]["choices"].([]any)
	ch, _ := choices[0].(map[string]any)
	delta, _ := ch["delta"].(map[string]any)
	if delta["role"] != "assistant" {
		t.Fatalf("first delta.role = %v, want assistant", delta["role"])
	}
}

func TestResponsesStreamBridge_ToolCallStream(t *testing.T) {
	chunks := collectChatChunks(t, vapEURToolStream)

	if got := lastFinishReason(chunks); got != "tool_calls" {
		t.Fatalf("finish_reason = %q, want tool_calls when a function call was emitted", got)
	}

	var (
		sawName bool
		args    strings.Builder
		callID  string
		indices = map[float64]bool{}
	)
	for _, c := range chunks {
		choices, _ := c["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		ch, _ := choices[0].(map[string]any)
		delta, _ := ch["delta"].(map[string]any)
		calls, _ := delta["tool_calls"].([]any)
		for _, raw := range calls {
			tc, _ := raw.(map[string]any)
			if idx, ok := tc["index"].(float64); ok {
				indices[idx] = true
			}
			if id, _ := tc["id"].(string); id != "" {
				callID = id
			}
			fn, _ := tc["function"].(map[string]any)
			if n, _ := fn["name"].(string); n != "" {
				sawName = true
				if n != "get_time" {
					t.Fatalf("tool name = %q, want get_time", n)
				}
			}
			if a, _ := fn["arguments"].(string); a != "" {
				args.WriteString(a)
			}
		}
	}
	if !sawName {
		t.Fatal("never emitted the tool name")
	}
	if callID != "call_308C" {
		t.Fatalf("tool call id = %q, want call_308C", callID)
	}
	// The fragmented arguments must reassemble into valid JSON.
	if got := args.String(); got != `{"tz":"UTC"}` {
		t.Fatalf("assembled arguments = %q, want {\"tz\":\"UTC\"}", got)
	}
	if len(indices) != 1 {
		t.Fatalf("tool_call indexes = %v, want a single stable index (fragments must share it)", indices)
	}
}

func TestResponsesStreamBridge_AcceptsCanonicalEventLines(t *testing.T) {
	chunks := collectChatChunks(t, canonicalTextStream)
	if got := chunkDeltas(chunks); got != "Yo" {
		t.Fatalf("assembled text = %q, want Yo", got)
	}
	if got := lastFinishReason(chunks); got != "stop" {
		t.Fatalf("finish_reason = %q, want stop", got)
	}
}

// A stream that ends without response.completed must still be terminated, or a
// chat client hangs waiting for [DONE].
func TestResponsesStreamBridge_TruncatedStreamStillTerminates(t *testing.T) {
	truncated := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"
	chunks := collectChatChunks(t, truncated)
	if got := chunkDeltas(chunks); got != "partial" {
		t.Fatalf("assembled text = %q, want the one delta that arrived", got)
	}
	if len(chunks) == 0 {
		t.Fatal("a truncated stream must still emit a terminal frame")
	}
}

func TestParseResponsesSSEFrame_DualDialect(t *testing.T) {
	t.Run("type inside payload", func(t *testing.T) {
		f := parseResponsesSSEFrame("", []byte(`{"type":"response.output_text.delta","delta":"x"}`))
		if f.EventType != "response.output_text.delta" {
			t.Fatalf("EventType = %q", f.EventType)
		}
	})
	t.Run("event line wins", func(t *testing.T) {
		f := parseResponsesSSEFrame("response.completed", []byte(`{"type":"response.output_text.delta"}`))
		if f.EventType != "response.completed" {
			t.Fatalf("EventType = %q, want the event line to win", f.EventType)
		}
	})
	t.Run("neither", func(t *testing.T) {
		f := parseResponsesSSEFrame("", []byte(`not json`))
		if f.EventType != "" {
			t.Fatalf("EventType = %q, want empty for an unidentifiable frame", f.EventType)
		}
	})
}

// Keep-alive comments must not produce frames.
func TestResponsesStreamBridge_IgnoresComments(t *testing.T) {
	withKeepalive := ": keep-alive\n\n" + vapEURTextStream
	chunks := collectChatChunks(t, withKeepalive)
	if got := chunkDeltas(chunks); got != "Hi there" {
		t.Fatalf("assembled text = %q, want Hi there", got)
	}
}
