package streaming

import (
	"encoding/json"
	"fmt"
	"io"
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

func TestResponsesStreamSSE_SplitsDoneJoinedToJSON(t *testing.T) {
	resp := &http.Response{
		Body: io.NopCloser(strings.NewReader(
			`data: {"choices":[{"delta":{"content":"planning"},"finish_reason":null}]}[DONE].`,
		)),
		Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
	}
	rec := httptest.NewRecorder()

	out := StreamResponsesSSE(rec, resp, "gpt-5.6-sol", "gpt-5.6-sol", "req-combined-done", nil)

	assert.False(t, out.Interrupted)
	assert.Contains(t, rec.Body.String(), `"delta":"planning"`)
	assert.Contains(t, rec.Body.String(), "event: response.completed")
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

// ─── audit a9ff405d3b51cf18f953475855d7851c regression coverage ────────
// (2026-07-23) OpenAI Responses API input items with shape
// {"type":"input_text","text":"..."} were silently converted into
// empty user messages because convertResponsesInputItem only knew
// function_call / function_call_output. These tests pin the fix.

// TestConvertResponsesToChatBody_PreservesInputText mirrors the actual
// request body sent in record a9ff405d... — Chinese UTF-8 prompt wrapped
// in OpenAI Responses input_text format.
func TestConvertResponsesToChatBody_PreservesInputText(t *testing.T) {
	raw := `{
		"model":"9bfcd8c1-4428-4157-9231-88ebf221154f/claude-sonnet-5",
		"instructions":"You are ZCode, an interactive coding agent",
		"input":[
			{"type":"input_text","text":"\nYou are an interactive ZCode agent...\nIMPORTANT: Assist with authorized security testing..."},
			{"type":"input_text","text":"<system-reminder>The following skills are available...</system-reminder>"},
			{"type":"input_text","text":"请对12小时内的修订进行审计，并总结完成情况，给出改进意见。"}
		]
	}`
	var req responsesRequestBody
	require.NoError(t, json.Unmarshal([]byte(raw), &req))

	result := convertResponsesToChatBody(&req)
	msgs, ok := result["messages"].([]any)
	require.True(t, ok)
	require.Len(t, msgs, 4,
		"system instruction + 3 input_text items, no items should be silently dropped")

	// instruction → system
	require.Equal(t, "system", msgs[0].(map[string]any)["role"])

	// each input_text must produce a user message with non-empty content.
	for i := 1; i < 4; i++ {
		m := msgs[i].(map[string]any)
		require.Equal(t, "user", m["role"], "msg %d role", i)
		s, ok := m["content"].(string)
		require.True(t, ok, "msg %d content must be string", i)
		require.NotEmpty(t, s, "msg %d content must NOT be empty (audit invariant)", i)
	}

	// the Chinese user instruction must arrive verbatim — this is the bug
	// the fix targets.
	last := msgs[3].(map[string]any)["content"].(string)
	assert.Contains(t, last, "请对12小时内的修订进行审计",
		"Chinese user instruction must survive the OpenAI Responses → Chat conversion")
}

// TestConvertResponsesInputItem_MessageType mirrors the OpenAI Responses
// schema where text is sometimes wrapped as {"type":"message", ...}.
func TestConvertResponsesInputItem_MessageType(t *testing.T) {
	msg, ok := convertResponsesInputItem(map[string]any{
		"type": "message",
		"role": "user",
		"text": "审计最近 12 小时的 commit",
	})
	require.True(t, ok)
	assert.Equal(t, "user", msg["role"])
	assert.Equal(t, "审计最近 12 小时的 commit", msg["content"])
}

// TestConvertResponsesInputItem_EmptyTextYieldsDrop ensures that an
// input_text item with an empty string does NOT produce a
// {"role":"user","content":""} message — that's exactly the silent bug
// that triggered the original audit.
func TestConvertResponsesInputItem_EmptyTextYieldsDrop(t *testing.T) {
	_, ok := convertResponsesInputItem(map[string]any{
		"type": "input_text",
		"text": "",
	})
	assert.False(t, ok, "empty input_text must NOT be promoted to a chat message")
}

// TestConvertResponsesToChatBody_FallbackDropsEmptyItem exercises the
// legacy "role + content" branch when content is empty/blank. Audit invariant:
// no empty-content user message may reach the upstream model.
func TestConvertResponsesToChatBody_FallbackDropsEmptyItem(t *testing.T) {
	raw := `{
		"model":"gpt-4o",
		"input":[
			{"role":"user","content":""},
			{"role":"user","content":"   "},
			{"role":"user","content":"follow-up question"}
		]
	}`
	var req responsesRequestBody
	require.NoError(t, json.Unmarshal([]byte(raw), &req))

	result := convertResponsesToChatBody(&req)
	msgs := result["messages"].([]any)
	require.Len(t, msgs, 1,
		"two empty/blank items must be dropped, only the real question survives")
	assert.Equal(t, "follow-up question", msgs[0].(map[string]any)["content"])
}

// TestConvertResponsesToChatBody_ImageInputForwarded pins multimodal input
// handling so future refactors don't accidentally drop image URLs.
func TestConvertResponsesToChatBody_ImageInputForwarded(t *testing.T) {
	raw := `{
		"model":"claude-sonnet-5",
		"input":[
			{"type":"input_image","image_url":{"url":"https://example.test/a.png"}}
		]
	}`
	var req responsesRequestBody
	require.NoError(t, json.Unmarshal([]byte(raw), &req))

	result := convertResponsesToChatBody(&req)
	msgs := result["messages"].([]any)
	require.Len(t, msgs, 1)
	content := msgs[0].(map[string]any)["content"]
	parts, ok := content.([]any)
	require.True(t, ok)
	require.Len(t, parts, 1)
	part := parts[0].(map[string]any)
	assert.Equal(t, "image_url", part["type"])
}
