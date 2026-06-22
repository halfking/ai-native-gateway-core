package relay

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/audit"
	"github.com/kaixuan/llm-gateway-go/circuit"
	"github.com/kaixuan/llm-gateway-go/limiter"
)

func newChatUpstream(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", handler)
	return httptest.NewServer(mux)
}

func TestMessagesHandler_MethodNotAllowed(t *testing.T) {
	ch := NewChatHandler(circuit.NewManager(), limiter.New(), nil, nil, nil, nil)
	h := NewMessagesHandler(ch)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/messages", nil)
	h.ServeHTTP(w, r)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "invalid_request") {
		t.Fatalf("expected error type invalid_request, got %s", w.Body.String())
	}
}

func TestMessagesHandler_InvalidJSON(t *testing.T) {
	ch := NewChatHandler(circuit.NewManager(), limiter.New(), nil, nil, nil, nil)
	h := NewMessagesHandler(ch)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("not json"))
	r.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestMessagesHandler_MissingModel(t *testing.T) {
	ch := NewChatHandler(circuit.NewManager(), limiter.New(), nil, nil, nil, nil)
	h := NewMessagesHandler(ch)

	body := `{"messages":[{"role":"user","content":"hi"}],"max_tokens":100}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestConvertAnthropicTools(t *testing.T) {
	tools := []any{
		map[string]any{"name": "get_weather", "description": "Get weather", "input_schema": map[string]any{"type": "object"}},
	}
	result := convertAnthropicTools(tools)
	if len(result) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(result))
	}
	fn := result[0]["function"].(map[string]any)
	if fn["name"] != "get_weather" {
		t.Fatalf("expected get_weather, got %v", fn["name"])
	}
	if result[0]["type"] != "function" {
		t.Fatalf("expected function type, got %v", result[0]["type"])
	}
}

func TestConvertAnthropicToolChoice(t *testing.T) {
	tests := []struct {
		input    string
		expected any
	}{
		{`"auto"`, "auto"},
		{`{"type":"any"}`, "required"},
		{`{"type":"none"}`, "none"},
		{`{"type":"tool","name":"get_weather"}`, map[string]any{"type": "function", "function": map[string]any{"name": "get_weather"}}},
	}
	for i, tc := range tests {
		result := convertAnthropicToolChoice(json.RawMessage(tc.input))
		b1, _ := json.Marshal(result)
		b2, _ := json.Marshal(tc.expected)
		if string(b1) != string(b2) {
			t.Errorf("case %d: expected %s, got %s", i, string(b2), string(b1))
		}
	}
}

func TestConvertChatResponseToAnthropic(t *testing.T) {
	chatResp := map[string]any{
		"choices": []map[string]any{
			{
				"finish_reason": "stop",
				"message": map[string]any{
					"content": "Hello world",
				},
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     float64(10),
			"completion_tokens": float64(5),
		},
	}
	body, _ := json.Marshal(chatResp)

	result := convertChatResponseToAnthropic(body, "claude-3", "req123")

	var resp map[string]any
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["type"] != "message" {
		t.Fatalf("expected type=message, got %v", resp["type"])
	}
	if resp["model"] != "claude-3" {
		t.Fatalf("expected model=claude-3, got %v", resp["model"])
	}
	if resp["stop_reason"] != "end_turn" {
		t.Fatalf("expected stop_reason=end_turn, got %v", resp["stop_reason"])
	}
	content, _ := resp["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(content))
	}
	block := content[0].(map[string]any)
	if block["type"] != "text" {
		t.Fatalf("expected text block, got %v", block["type"])
	}
	if block["text"] != "Hello world" {
		t.Fatalf("expected Hello world, got %v", block["text"])
	}
	usage := resp["usage"].(map[string]any)
	if usage["input_tokens"] != float64(10) {
		t.Fatalf("expected input_tokens=10, got %v", usage["input_tokens"])
	}
}

func TestMapAnthropicStopReason(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"stop", "end_turn"},
		{"tool_calls", "tool_use"},
		{"function_call", "tool_use"},
		{"length", "max_tokens"},
		{"content_filter", "end_turn"},
		{"unknown", "end_turn"},
	}
	for _, tc := range tests {
		result := mapAnthropicStopReason(tc.input)
		if result != tc.expected {
			t.Errorf("mapAnthropicStopReason(%q) = %q, want %q", tc.input, result, tc.expected)
		}
	}
}

func TestConvertAnthropicMessage_TextOnly(t *testing.T) {
	msg := map[string]any{
		"role":    "user",
		"content": "Hello",
	}
	result := convertAnthropicMessage(msg)
	if result["role"] != "user" {
		t.Fatalf("expected user, got %v", result["role"])
	}
	if result["content"] != "Hello" {
		t.Fatalf("expected Hello, got %v", result["content"])
	}
}

func TestConvertAnthropicMessage_ToolUse(t *testing.T) {
	msg := map[string]any{
		"role": "assistant",
		"content": []any{
			map[string]any{"type": "text", "text": "Let me check"},
			map[string]any{"type": "tool_use", "id": "tu_123", "name": "get_weather", "input": map[string]any{"city": "SF"}},
		},
	}
	result := convertAnthropicMessage(msg)
	if result["role"] != "assistant" {
		t.Fatalf("expected assistant, got %v", result["role"])
	}
	toolCalls, ok := result["tool_calls"].([]map[string]any)
	if !ok || len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool_call, got %v", result["tool_calls"])
	}
	fn := toolCalls[0]["function"].(map[string]any)
	if fn["name"] != "get_weather" {
		t.Fatalf("expected get_weather, got %v", fn["name"])
	}
}

func TestConvertAnthropicMessage_ToolResult(t *testing.T) {
	msg := map[string]any{
		"role": "user",
		"content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "tu_123", "content": "Sunny"},
		},
	}
	result := convertAnthropicMessage(msg)
	if result["role"] != "tool" {
		t.Fatalf("expected tool, got %v", result["role"])
	}
	if result["tool_call_id"] != "tu_123" {
		t.Fatalf("expected tu_123, got %v", result["tool_call_id"])
	}
}

func TestMessagesToChatBody_System(t *testing.T) {
	req := &messagesRequestBody{
		Model:     "claude-3",
		MaxTokens: 1024,
		System:    "You are helpful",
		Messages: json.RawMessage(`[{"role":"user","content":"Hi"}]`),
	}
	chatBody := convertToChatBody(req)
	msgs, ok := chatBody["messages"].([]any)
	if !ok || len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %v", chatBody["messages"])
	}
	first := msgs[0].(map[string]any)
	if first["role"] != "system" {
		t.Fatalf("expected system, got %v", first["role"])
	}
}

func TestAnthropicStreamSSE_NonStreaming(t *testing.T) {
	upstream := newChatUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"choices": []map[string]any{
				{"finish_reason": "stop", "message": map[string]any{"content": "Hello"}},
			},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5},
		}
		//nolint:errcheck // HTTP write error non-recoverable
		json.NewEncoder(w).Encode(resp)
	})
	defer upstream.Close()

	capture := audit.NewStreamCapture()
	resp := doUpstreamRequest(t, upstream.URL+"/v1/chat/completions", `{"model":"claude-3","messages":[],"stream":true}`)
	//nolint:errcheck // best-effort close
	defer resp.Body.Close()

	rec := httptest.NewRecorder()
	StreamAnthropicSSE(rec, resp, "claude-3", "claude-3-opus", "test-req-id", capture, nil)

	body := rec.Body.String()
	if !strings.Contains(body, "event: message_start") {
		t.Fatal("expected message_start event")
	}
	if !strings.Contains(body, "event: content_block_start") {
		t.Fatal("expected content_block_start event")
	}
	if !strings.Contains(body, "event: content_block_stop") {
		t.Fatal("expected content_block_stop event")
	}
	if !strings.Contains(body, "event: message_delta") {
		t.Fatal("expected message_delta event")
	}
	if !strings.Contains(body, "event: message_stop") {
		t.Fatal("expected message_stop event")
	}
	if !strings.Contains(body, "event: ping") {
		t.Fatal("expected ping event")
	}
}

func TestResponsesHandler_MethodNotAllowed(t *testing.T) {
	ch := NewChatHandler(circuit.NewManager(), limiter.New(), nil, nil, nil, nil)
	h := NewResponsesHandler(ch)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	h.ServeHTTP(w, r)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}

func TestResponsesHandler_InvalidJSON(t *testing.T) {
	ch := NewChatHandler(circuit.NewManager(), limiter.New(), nil, nil, nil, nil)
	h := NewResponsesHandler(ch)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader("bad"))
	r.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestConvertResponsesToChatBody_String(t *testing.T) {
	req := &responsesRequestBody{
		Model:  "gpt-4o",
		Input:  json.RawMessage(`"hello"`),
	}
	result := convertResponsesToChatBody(req)
	msgs := result["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	m := msgs[0].(map[string]any)
	if m["role"] != "user" || m["content"] != "hello" {
		t.Fatalf("unexpected message: %v", m)
	}
}

func TestConvertResponsesToChatBody_Array(t *testing.T) {
	req := &responsesRequestBody{
		Model:        "gpt-4o",
		Instructions: "Be helpful",
		Input:        json.RawMessage(`[{"role":"user","content":"hi"},{"role":"assistant","content":"hello"}]`),
		MaxOutputTokens: intPtr(500),
	}
	result := convertResponsesToChatBody(req)
	msgs := result["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}
	if msgs[0].(map[string]any)["role"] != "system" {
		t.Fatal("first message should be system")
	}
	if result["max_tokens"] != 500 {
		t.Fatalf("expected max_tokens=500, got %v", result["max_tokens"])
	}
}

func TestConvertResponsesToChatBody_PreservesExtraParams(t *testing.T) {
	var req responsesRequestBody
	if err := json.Unmarshal([]byte(`{
		"model":"mimo-v2.5-pro",
		"input":[{"role":"user","content":"我们现在是什么模型？"}],
		"tools":[{"type":"function","name":"get_model","parameters":{"type":"object"}}],
		"tool_choice":"required",
		"reasoning":{"effort":"high"},
		"parallel_tool_calls":true,
		"max_output_tokens":256,
		"stream":false
	}`), &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}

	result := convertResponsesToChatBody(&req)
	tools, ok := result["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("expected tools to be preserved, got %T %#v", result["tools"], result["tools"])
	}
	tool, ok := tools[0].(map[string]any)
	if !ok {
		t.Fatalf("expected first tool to be an object, got %T", tools[0])
	}
	function, ok := tool["function"].(map[string]any)
	if !ok {
		t.Fatalf("expected normalized function payload, got %#v", tool)
	}
	if function["name"] != "get_model" {
		t.Fatalf("expected function.name=get_model, got %#v", function)
	}
	if result["tool_choice"] != "required" {
		t.Fatalf("expected tool_choice=required, got %v", result["tool_choice"])
	}
	reasoning, ok := result["reasoning"].(map[string]any)
	if !ok || reasoning["effort"] != "high" {
		t.Fatalf("expected reasoning.effort=high, got %#v", result["reasoning"])
	}
	if result["parallel_tool_calls"] != true {
		t.Fatalf("expected parallel_tool_calls=true, got %v", result["parallel_tool_calls"])
	}
}

func TestConvertChatResponseToResponses(t *testing.T) {
	chatResp := map[string]any{
		"choices": []map[string]any{
			{
				"finish_reason": "stop",
				"message":       map[string]any{"content": "Hello"},
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     float64(10),
			"completion_tokens": float64(5),
			"total_tokens":      float64(15),
		},
		"created": float64(1234567890),
	}
	body, _ := json.Marshal(chatResp)

	result := convertChatResponseToResponses(body, "gpt-4o", "req-id-123")

	var resp map[string]any
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["object"] != "response" {
		t.Fatalf("expected object=response, got %v", resp["object"])
	}
	if resp["status"] != "completed" {
		t.Fatalf("expected completed, got %v", resp["status"])
	}
	if resp["model"] != "gpt-4o" {
		t.Fatalf("expected gpt-4o, got %v", resp["model"])
	}
	output := resp["output"].([]any)
	if len(output) != 1 {
		t.Fatalf("expected 1 output item, got %d", len(output))
	}
	item := output[0].(map[string]any)
	if item["role"] != "assistant" {
		t.Fatalf("expected assistant, got %v", item["role"])
	}
	usage := resp["usage"].(map[string]any)
	if usage["input_tokens"] != float64(10) {
		t.Fatalf("expected 10, got %v", usage["input_tokens"])
	}
}

func TestConvertChatResponseToResponses_Incomplete(t *testing.T) {
	chatResp := map[string]any{
		"choices": []map[string]any{
			{"finish_reason": "length", "message": map[string]any{"content": "cut off"}},
		},
	}
	body, _ := json.Marshal(chatResp)
	result := convertChatResponseToResponses(body, "gpt-4o", "req-id")

	var resp map[string]any
	//nolint:errcheck // test parse, non-critical
	json.Unmarshal(result, &resp)
	if resp["status"] != "incomplete" {
		t.Fatalf("expected incomplete, got %v", resp["status"])
	}
}

func TestConvertChatResponseToResponses_ToolCalls(t *testing.T) {
	body := []byte(`{
		"choices":[{
			"finish_reason":"tool_calls",
			"message":{
				"content":"",
				"reasoning_content":"need to call tool",
				"tool_calls":[{
					"id":"call_123",
					"type":"function",
					"function":{"name":"get_time","arguments":"{}"}
				}]
			}
		}]
	}`)

	result := convertChatResponseToResponses(body, "gpt-4o", "req-id-456")

	var resp map[string]any
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	output, ok := resp["output"].([]any)
	if !ok || len(output) != 2 {
		t.Fatalf("expected 2 output items, got %#v", resp["output"])
	}
	if output[0].(map[string]any)["type"] != "reasoning" {
		t.Fatalf("expected reasoning output first, got %#v", output[0])
	}
	if output[1].(map[string]any)["type"] != "function_call" {
		t.Fatalf("expected function_call output second, got %#v", output[1])
	}
	if output[1].(map[string]any)["name"] != "get_time" {
		t.Fatalf("expected function_call name get_time, got %#v", output[1])
	}
}

func TestResponsesStreamSSE_Events(t *testing.T) {
	upstream := newChatUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		//nolint:errcheck // test write, non-critical
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n")
		//nolint:errcheck // test write, non-critical
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\" there\"}}]}\n\n")
		//nolint:errcheck // test write, non-critical
		fmt.Fprintf(w, "data: {\"choices\":[{\"finish_reason\":\"stop\"}]}\n\n")
		//nolint:errcheck // test write, non-critical
		fmt.Fprintf(w, "data: [DONE]\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	})
	defer upstream.Close()

	resp := doUpstreamRequest(t, upstream.URL+"/v1/chat/completions", `{"model":"gpt-4o","messages":[],"stream":true}`)
	//nolint:errcheck // best-effort close
	defer resp.Body.Close()

	rec := httptest.NewRecorder()
	capture := audit.NewStreamCapture()
	StreamResponsesSSE(rec, resp, "gpt-4o", "gpt-4o", "test-req-id-123456789012345678", capture)

	body := rec.Body.String()

	expectedEvents := []string{
		"event: response.created",
		"event: response.output_item.added",
		"event: response.content_part.added",
		"event: response.output_text.delta",
		"event: response.output_text.done",
		"event: response.output_item.done",
		"event: response.completed",
	}
	for _, ev := range expectedEvents {
		if !strings.Contains(body, ev) {
			t.Fatalf("missing event: %s\nbody:\n%s", ev, body)
		}
	}

	if !strings.Contains(body, `"delta":"Hi"`) {
		t.Fatal("expected Hi delta")
	}
	if !strings.Contains(body, `"delta":" there"`) {
		t.Fatal("expected ' there' delta")
	}
	if !strings.Contains(body, `"status":"completed"`) {
		t.Fatalf("expected completed status, got:\n%s", body)
	}
}

func TestWriteAnthropicError(t *testing.T) {
	w := httptest.NewRecorder()
	writeAnthropicError(w, 529, "overloaded_error", "Too many requests")

	if w.Code != 529 {
		t.Fatalf("expected 529, got %d", w.Code)
	}
	var resp map[string]any
	//nolint:errcheck // test parse, non-critical
	json.Unmarshal(w.Body.Bytes(), &resp)
	errObj := resp["error"].(map[string]any)
	if errObj["type"] != "overloaded_error" {
		t.Fatalf("expected overloaded_error, got %v", errObj["type"])
	}
}

func TestWriteResponsesError(t *testing.T) {
	w := httptest.NewRecorder()
	writeResponsesError(w, 429, "Rate limited", "rate_limit_exceeded", "rate_limit_exceeded")

	if w.Code != 429 {
		t.Fatalf("expected 429, got %d", w.Code)
	}
	var resp map[string]any
	//nolint:errcheck // test parse, non-critical
	json.Unmarshal(w.Body.Bytes(), &resp)
	errObj := resp["error"].(map[string]any)
	if errObj["code"] != "rate_limit_exceeded" {
		t.Fatalf("expected rate_limit_exceeded, got %v", errObj["code"])
	}
}

func doUpstreamRequest(t *testing.T, url, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}
