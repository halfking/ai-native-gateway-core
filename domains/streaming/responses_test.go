package streaming

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/credential"  //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResponsesHandler_MethodNotAllowed(t *testing.T) {
	ch := NewChatHandler(credential.NewManager(), credential.NewLimiter(), nil, nil, nil, nil)
	h := NewResponsesHandler(ch)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	h.ServeHTTP(w, r)

	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

func TestResponsesHandler_InvalidJSON(t *testing.T) {
	ch := NewChatHandler(credential.NewManager(), credential.NewLimiter(), nil, nil, nil, nil)
	h := NewResponsesHandler(ch)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader("bad"))
	r.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(w, r)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestConvertResponsesToChatBody_PreservesExtraParams(t *testing.T) {
	var req responsesRequestBody
	err := json.Unmarshal([]byte(`{
		"model":"mimo-v2.5-pro",
		"input":[{"role":"user","content":"我们现在是什么模型？"}],
		"tools":[{"type":"function","name":"get_model","parameters":{"type":"object"}}],
		"tool_choice":"required",
		"reasoning":{"effort":"high"},
		"parallel_tool_calls":true,
		"max_output_tokens":256,
		"stream":false
	}`), &req)
	require.NoError(t, err)

	result := convertResponsesToChatBody(&req)
	tools, ok := result["tools"].([]any)
	require.True(t, ok)
	require.Len(t, tools, 1)
	tool := tools[0].(map[string]any)
	fn, ok := tool["function"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "get_model", fn["name"])
	assert.Equal(t, "required", result["tool_choice"])
	reasoning, ok := result["reasoning"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "high", reasoning["effort"])
	assert.Equal(t, true, result["parallel_tool_calls"])
}

func TestConvertResponsesToChatBody_PreservesFunctionCallChain(t *testing.T) {
	var req responsesRequestBody
	err := json.Unmarshal([]byte(`{
		"model":"gpt-5.6-luna",
		"input":[
			{"role":"user","content":"look up the weather"},
			{"type":"function_call","id":"fc_1","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"Shanghai\"}"},
			{"type":"function_call_output","call_id":"call_1","output":"{\"temperature\":28} "},
			{"role":"user","content":"thanks"}
		]
	}`), &req)
	require.NoError(t, err)

	result := convertResponsesToChatBody(&req)
	messages, ok := result["messages"].([]any)
	require.True(t, ok)
	require.Len(t, messages, 4)

	assistant := messages[1].(map[string]any)
	toolCalls := assistant["tool_calls"].([]any)
	require.Len(t, toolCalls, 1)
	toolCall := toolCalls[0].(map[string]any)
	assert.Equal(t, "call_1", toolCall["id"])
	function := toolCall["function"].(map[string]any)
	assert.Equal(t, "get_weather", function["name"])
	assert.Equal(t, `{"city":"Shanghai"}`, function["arguments"])

	tool := messages[2].(map[string]any)
	assert.Equal(t, "tool", tool["role"])
	assert.Equal(t, "call_1", tool["tool_call_id"])
	assert.Equal(t, `{"temperature":28} `, tool["content"])
}

func TestConvertResponsesInputItem_UsesItemIDWhenCallIDMissing(t *testing.T) {
	message, ok := convertResponsesInputItem(map[string]any{
		"type":      "function_call",
		"id":        "fc_1",
		"name":      "lookup",
		"arguments": "{}",
	})
	require.True(t, ok)
	toolCalls := message["tool_calls"].([]any)
	assert.Equal(t, "fc_1", toolCalls[0].(map[string]any)["id"])
}

func TestConvertChatResponseToResponses(t *testing.T) {
	chatResp := map[string]any{
		"choices": []map[string]any{{
			"finish_reason": "stop",
			"message":       map[string]any{"content": "Hello"},
		}},
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
	require.NoError(t, json.Unmarshal(result, &resp))
	assert.Equal(t, "response", resp["object"])
	assert.Equal(t, "completed", resp["status"])
	assert.Equal(t, "gpt-4o", resp["model"])
	output := resp["output"].([]any)
	require.Len(t, output, 1)
	item := output[0].(map[string]any)
	assert.Equal(t, "assistant", item["role"])
	usage := resp["usage"].(map[string]any)
	assert.Equal(t, float64(10), usage["input_tokens"])
}

func TestResponsesStreamSSE_Events(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n")
		_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\" there\"}}]}\n\n")
		_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer upstream.Close()

	resp, err := http.Post(upstream.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"gpt-4o","messages":[],"stream":true}`))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	rec := httptest.NewRecorder()
	capture := audit.NewStreamCapture()
	StreamResponsesSSE(rec, resp, "gpt-4o", "gpt-4o", "test-req-id-123456789012345678", capture)

	body := rec.Body.String()
	for _, ev := range []string{
		"event: response.created",
		"event: response.output_item.added",
		"event: response.content_part.added",
		"event: response.output_text.delta",
		"event: response.output_text.done",
		"event: response.output_item.done",
		"event: response.completed",
	} {
		assert.Contains(t, body, ev)
	}
	assert.Contains(t, body, `"delta":"Hi"`)
	assert.Contains(t, body, `"delta":" there"`)
	assert.Contains(t, body, `"status":"completed"`)
}

func TestWriteResponsesError(t *testing.T) {
	w := httptest.NewRecorder()
	writeResponsesError(w, 429, "Rate limited", "rate_limit_exceeded", "rate_limit_exceeded")
	assert.Equal(t, 429, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	errObj := resp["error"].(map[string]any)
	assert.Equal(t, "rate_limit_exceeded", errObj["code"])
}

var _ = time.Now
