package streaming

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type r1032LogWriter struct {
	http.ResponseWriter
	t *testing.T
}

func (l *r1032LogWriter) Write(p []byte) (int, error) {
	l.t.Logf("CLIENT WIRE WRITE: %.200q", string(p))
	return l.ResponseWriter.Write(p)
}

func (l *r1032LogWriter) Flush() {
	l.t.Log("CLIENT WIRE FLUSH")
	if f, ok := l.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// r1032 repro: claude-sonnet-5 via vapeur chat upstream, tool-call-only
// response, /v1/responses client. Captured frames from raw-logs request
// 279e4627a757ba65914478e5e58e23a2 (2026-10-15). Client received ZERO
// frames (only ": keep-alive"). This test drives the bridge in isolation
// to separate "bridge broken" from "dispatch never reached the bridge".
func TestR1032ClaudeToolOnlyChatToResponsesBridge(t *testing.T) {
	frames := strings.Join([]string{
		`data: {"id":"chatcmpl-3dd0bb00","object":"chat.completion.chunk","created":1792039047,"model":"claude-sonnet-5","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"toolu_bdrk_01ABC","type":"function","function":{"name":"get_time","arguments":""}}]}}]}` + "\n",
		`data: {"id":"chatcmpl-3dd0bb00","object":"chat.completion.chunk","created":1792039047,"model":"claude-sonnet-5","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":""}}]}}]}` + "\n",
		`data: {"id":"chatcmpl-3dd0bb00","object":"chat.completion.chunk","created":1792039047,"model":"claude-sonnet-5","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n",
		`data: {"id":"chatcmpl-3dd0bb00","object":"chat.completion.chunk","created":1792039047,"model":"claude-sonnet-5","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":100,"completion_tokens":5,"total_tokens":105}}` + "\n",
		"\n",
	}, "")
	resp := &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(frames)), Header: http.Header{}}
	rec := httptest.NewRecorder()
	lw := &r1032LogWriter{ResponseWriter: rec, t: t}
	outcome := StreamOpenAIToResponsesSSE(t.Context(), lw, resp, "claude-sonnet-5", "claude-sonnet-5", "req-r1032", nil, nil)
	body := rec.Body.String()
	t.Logf("outcome=%+v bodyLen=%d body=%.400s", outcome, len(body), body)
	if outcome.Interrupted {
		t.Fatalf("bridge interrupted: %+v", outcome)
	}
	if !strings.Contains(body, "response.created") {
		t.Fatalf("no response.created frame")
	}
	if !strings.Contains(body, `"type":"function_call"`) {
		t.Fatalf("no function_call item frame; body=%s", body)
	}
	if !strings.Contains(body, "response.completed") {
		t.Fatalf("no response.completed frame; body=%s", body)
	}
}

// TestR1032ResponsesItemAddedClassifierBothDirections pins the classifier
// contract at the unit layer: tool-shaped item opens are semantic (force
// commit), message/reasoning item opens stay droppable metadata.
func TestR1032ResponsesItemAddedClassifierBothDirections(t *testing.T) {
	toolItem := "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":1,\"item\":{\"type\":\"function_call\",\"id\":\"fc_1\",\"call_id\":\"call_1\",\"name\":\"get_deploy_config\",\"arguments\":\"\",\"status\":\"in_progress\"}}\n\n"
	if got := ClassifyClientFrame(ProtocolOpenAIResponses, toolItem); got != FrameClassToolCall {
		t.Fatalf("function_call item add = %v, want FrameClassToolCall", got)
	}
	customToolItem := "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":2,\"item\":{\"type\":\"custom_tool_call\",\"id\":\"ct_1\",\"status\":\"in_progress\"}}\n\n"
	if got := ClassifyClientFrame(ProtocolOpenAIResponses, customToolItem); got != FrameClassToolCall {
		t.Fatalf("custom_tool_call item add = %v, want FrameClassToolCall", got)
	}
	msgItem := "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"message\",\"id\":\"msg_1\",\"status\":\"in_progress\",\"role\":\"assistant\",\"content\":[]}}\n\n"
	if got := ClassifyClientFrame(ProtocolOpenAIResponses, msgItem); got != FrameClassAttemptMetadata {
		t.Fatalf("message item add = %v, want FrameClassAttemptMetadata", got)
	}
	reasoningItem := "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"reasoning\",\"id\":\"rs_1\",\"status\":\"in_progress\"}}\n\n"
	if got := ClassifyClientFrame(ProtocolOpenAIResponses, reasoningItem); got != FrameClassAttemptMetadata {
		t.Fatalf("reasoning item add = %v, want FrameClassAttemptMetadata", got)
	}
}
