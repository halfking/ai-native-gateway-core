package streaming

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClassifyNonStreamUpstreamResponse(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantFormat nonStreamResponseFormat
		wantEmpty  bool
	}{
		{"unknown JSON remains non-empty for compatibility", `{"id":"unknown"}`, nonStreamResponseUnknown, false},
		{"empty chat response is empty", `{"choices":[]}`, nonStreamResponseChat, true},
		{"later chat choice has content", `{"choices":[{"message":{"content":""}},{"message":{"content":"second"}}]}`, nonStreamResponseChat, false},
		{"anthropic text is not empty", `{"type":"message","content":[{"type":"text","text":"hello"}]}`, nonStreamResponseAnthropic, false},
		{"anthropic tool use is not empty", `{"type":"message","content":[{"type":"tool_use","id":"toolu_1","name":"weather","input":{}}]}`, nonStreamResponseAnthropic, false},
		{"anthropic tool use with input only is not empty", `{"type":"message","content":[{"type":"tool_use","input":{"city":"Paris"}}]}`, nonStreamResponseAnthropic, false},
		{"anthropic signed thinking is not empty", `{"type":"message","content":[{"type":"thinking","thinking":"","signature":"sig_1"}]}`, nonStreamResponseAnthropic, false},
		{"anthropic redacted thinking is not empty", `{"type":"message","content":[{"type":"redacted_thinking","data":"opaque"}]}`, nonStreamResponseAnthropic, false},
		{"anthropic server tool use is not empty", `{"type":"message","content":[{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{}}]}`, nonStreamResponseAnthropic, false},
		{"empty anthropic content is empty", `{"type":"message","content":[]}`, nonStreamResponseAnthropic, true},
		{"responses message is not empty", `{"object":"response","output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]}]}`, nonStreamResponseResponses, false},
		{"responses reasoning is not empty", `{"object":"response","output":[{"type":"reasoning","summary":[{"type":"summary_text","text":"thinking"}]}]}`, nonStreamResponseResponses, false},
		{"responses function call is not empty", `{"object":"response","output":[{"type":"function_call","call_id":"call_1","name":"weather","arguments":"{}"}]}`, nonStreamResponseResponses, false},
		{"empty responses output is empty", `{"object":"response","output":[]}`, nonStreamResponseResponses, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			format, empty := classifyNonStreamUpstreamResponse([]byte(tt.body))
			if format != tt.wantFormat || empty != tt.wantEmpty {
				t.Fatalf("classifyNonStreamUpstreamResponse() = (%v, %v), want (%v, %v)", format, empty, tt.wantFormat, tt.wantEmpty)
			}
		})
	}
}

func TestMessagesHandlerWriteNonStreamResponse_PassthroughAnthropic(t *testing.T) {
	body := []byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-5","content":[{"type":"tool_use","id":"toolu_1","name":"weather","input":{}}],"stop_reason":"tool_use"}`)
	recorder := httptest.NewRecorder()

	got := (&MessagesHandler{}).writeNonStreamResponse(recorder, body, "claude-sonnet-5", "req-1")
	if recorder.Code != http.StatusOK || string(got) != string(body) || recorder.Body.String() != string(body) {
		t.Fatalf("native Anthropic response was not preserved: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestMessagesHandlerWriteNonStreamResponse_RejectsEmptyAnthropic(t *testing.T) {
	recorder := httptest.NewRecorder()
	got := (&MessagesHandler{}).writeNonStreamResponse(recorder, []byte(`{"type":"message","content":[]}`), "claude-sonnet-5", "req-1")
	if got != nil || recorder.Code != http.StatusBadGateway {
		t.Fatalf("empty Anthropic response: got=%s status=%d", got, recorder.Code)
	}
}

func TestResponsesHandlerWriteNonStreamResponse_PassthroughResponses(t *testing.T) {
	body := []byte(`{"id":"resp_1","object":"response","output":[{"type":"function_call","call_id":"call_1","name":"weather","arguments":"{}"}]}`)
	recorder := httptest.NewRecorder()

	got := (&ResponsesHandler{}).writeNonStreamResponse(recorder, body, "claude-sonnet-5", "req-1")
	if recorder.Code != http.StatusOK || string(got) != string(body) || recorder.Body.String() != string(body) {
		t.Fatalf("native Responses response was not preserved: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestResponsesHandlerWriteNonStreamResponse_RejectsEmptyResponses(t *testing.T) {
	recorder := httptest.NewRecorder()
	got := (&ResponsesHandler{}).writeNonStreamResponse(recorder, []byte(`{"object":"response","output":[]}`), "claude-sonnet-5", "req-1")
	if got != nil || recorder.Code != http.StatusBadGateway {
		t.Fatalf("empty Responses response: got=%s status=%d", got, recorder.Code)
	}
}
