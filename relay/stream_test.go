package relay

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/circuit"
	"github.com/kaixuan/llm-gateway-go/config"
	"github.com/kaixuan/llm-gateway-go/limiter"
)

// ---------------------------------------------------------------------------
// replaceModelInChunk unit tests
// ---------------------------------------------------------------------------

func TestReplaceModelInChunk_BasicReplacement(t *testing.T) {
	line := `data: {"id":"chat-1","object":"chat.completion.chunk","model":"gpt-4o-2024-08-06","choices":[{"delta":{"content":"Hello"},"index":0}]}` + "\n\n"
	result := replaceModelInChunk(line, "gpt-4o", "gpt-4o-2024-08-06")

	var obj map[string]any
	if err := json.Unmarshal([]byte(strings.TrimPrefix(result, "data: ")), &obj); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if obj["model"] != "gpt-4o" {
		t.Errorf("expected model=gpt-4o, got %v", obj["model"])
	}
}

func TestReplaceModelInChunk_NoMatch_NoReplace(t *testing.T) {
	line := `data: {"id":"chat-1","model":"gpt-4","choices":[{"delta":{"content":"Hi"}}]}` + "\n\n"
	result := replaceModelInChunk(line, "gpt-4o", "claude-sonnet-4-5")
	if result != line {
		t.Error("should not modify when model doesn't match outboundModel")
	}
}

func TestReplaceModelInChunk_SubstringSafety(t *testing.T) {
	line := `data: {"id":"chat-1","model":"gpt-4o","choices":[{"delta":{"content":"test"}}]}` + "\n\n"
	result := replaceModelInChunk(line, "gpt-4", "gpt-4o")

	var obj map[string]any
	raw := strings.TrimSuffix(strings.TrimPrefix(result, "data: "), "\n\n")
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	model, _ := obj["model"].(string)
	if model != "gpt-4" {
		t.Errorf("expected gpt-4, got %s", model)
	}
}

func TestReplaceModelInChunk_DoneEvent(t *testing.T) {
	line := "data: [DONE]\n\n"
	result := replaceModelInChunk(line, "gpt-4o", "gpt-4o-2024-08-06")
	if result != line {
		t.Error("[DONE] should pass through unchanged")
	}
}

func TestReplaceModelInChunk_NonDataLine(t *testing.T) {
	line := "event: ping\n\n"
	result := replaceModelInChunk(line, "gpt-4o", "gpt-4o-2024-08-06")
	if result != line {
		t.Error("non-data lines should pass through unchanged")
	}
}

func TestReplaceModelInChunk_EmptyModels(t *testing.T) {
	line := `data: {"model":"gpt-4o"}` + "\n\n"
	result := replaceModelInChunk(line, "", "gpt-4o")
	if result != line {
		t.Error("empty clientModel should skip replacement")
	}
	result = replaceModelInChunk(line, "gpt-4o", "")
	if result != line {
		t.Error("empty outboundModel should skip replacement")
	}
}

func TestReplaceModelInChunk_WhitespaceVariants(t *testing.T) {
	cases := []struct {
		name string
		line string
	}{
		{"no space after colon", `data: {"model":"outbound-model"}` + "\n\n"},
		{"one space", `data: {"model": "outbound-model"}` + "\n\n"},
		{"tab", `data: {"model"	:	"outbound-model"}` + "\n\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := replaceModelInChunk(tc.line, "client-model", "outbound-model")
			raw := strings.TrimSuffix(strings.TrimPrefix(result, "data: "), "\n\n")
			var obj map[string]any
			if err := json.Unmarshal([]byte(raw), &obj); err != nil {
				t.Fatalf("JSON parse failed: %v\ninput: %q", err, tc.line)
			}
			if obj["model"] != "client-model" {
				t.Errorf("expected client-model, got %v", obj["model"])
			}
		})
	}
}

func TestReplaceModelInChunk_ModelInNestedObject(t *testing.T) {
	line := `data: {"id":"chat-1","model":"outbound-model","choices":[{"delta":{"content":"text"},"index":0}],"usage":{"prompt_tokens":10}}` + "\n\n"
	result := replaceModelInChunk(line, "client-model", "outbound-model")
	raw := strings.TrimSuffix(strings.TrimPrefix(result, "data: "), "\n\n")
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		t.Fatalf("JSON parse error: %v", err)
	}
	if obj["model"] != "client-model" {
		t.Errorf("expected client-model, got %v", obj["model"])
	}
	choices := obj["choices"].([]any)
	delta := choices[0].(map[string]any)["delta"].(map[string]any)
	if delta["content"] != "text" {
		t.Error("other fields should be preserved")
	}
}

func TestReplaceModelInChunk_InvalidJSON(t *testing.T) {
	line := "data: {not valid json}\n\n"
	result := replaceModelInChunk(line, "client", "outbound")
	if result != line {
		t.Error("invalid JSON should pass through unchanged")
	}
}

// ---------------------------------------------------------------------------
// replaceModelInRequestBody unit tests
// ---------------------------------------------------------------------------

func TestReplaceModelInRequestBody(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4.5","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	result := ReplaceModelInRequestBody(body, "claude-sonnet-4-5-20250929")

	var obj map[string]any
	if err := json.Unmarshal(result, &obj); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if obj["model"] != "claude-sonnet-4-5-20250929" {
		t.Errorf("expected model replaced, got %v", obj["model"])
	}
	if obj["stream"] != true {
		t.Error("other fields should be preserved")
	}
}

func TestReplaceModelInRequestBody_InvalidJSON(t *testing.T) {
	body := []byte(`not json`)
	result := ReplaceModelInRequestBody(body, "new-model")
	if string(result) != "not json" {
		t.Error("invalid JSON should be returned as-is")
	}
}

// ---------------------------------------------------------------------------
// replaceModelInResponse unit tests
// ---------------------------------------------------------------------------

func TestReplaceModelInResponse(t *testing.T) {
	body := []byte(`{"id":"chat-1","model":"claude-sonnet-4-5-20250929","choices":[{"message":{"role":"assistant","content":"Hello"}}]}`)
	result := ReplaceModelInResponseBody(body, "claude-sonnet-4.5")

	var obj map[string]any
	if err := json.Unmarshal(result, &obj); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if obj["model"] != "claude-sonnet-4.5" {
		t.Errorf("expected claude-sonnet-4.5, got %v", obj["model"])
	}
}

func TestReplaceModelInResponse_AlreadyClientModel(t *testing.T) {
	body := []byte(`{"model":"gpt-4o"}`)
	result := ReplaceModelInResponseBody(body, "gpt-4o")
	if string(result) != string(body) {
		t.Error("should not modify when already client model")
	}
}

func TestReplaceModelInResponse_ToolCallsResponse(t *testing.T) {
	body := []byte(`{
		"id":"chat-1",
		"model":"gpt-4o-2024-08-06",
		"choices":[{
			"message":{
				"role":"assistant",
				"content":null,
				"tool_calls":[{
					"id":"call_abc",
					"type":"function",
					"function":{"name":"get_weather","arguments":"{\"city\":\"SF\"}"}
				}]
			},
			"finish_reason":"tool_calls"
		}],
		"usage":{"prompt_tokens":50,"completion_tokens":20,"total_tokens":70}
	}`)
	result := ReplaceModelInResponseBody(body, "gpt-4o")

	var obj map[string]any
	if err := json.Unmarshal(result, &obj); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if obj["model"] != "gpt-4o" {
		t.Errorf("expected gpt-4o, got %v", obj["model"])
	}
	choices := obj["choices"].([]any)
	msg := choices[0].(map[string]any)["message"].(map[string]any)
	tcs := msg["tool_calls"].([]any)
	if len(tcs) != 1 {
		t.Fatal("tool_calls should be preserved")
	}
	fn := tcs[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "get_weather" {
		t.Error("tool_call content should be intact")
	}
}

// ---------------------------------------------------------------------------
// End-to-end streaming model replacement
// ---------------------------------------------------------------------------

func TestStreamingModelReplacementE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E test (needs database)")
	}
	upstreamModel := "claude-sonnet-4-5-20250929"
	clientModel := "claude-sonnet-4.5"

	fakeUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		stream, _ := req["stream"].(bool)

		if !stream {
			t.Error("expected stream=true")
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)

		chunks := []string{
			fmt.Sprintf(`{"id":"chat-1","model":"%s","choices":[{"delta":{"role":"assistant","content":""},"index":0}]}`, upstreamModel),
			fmt.Sprintf(`{"id":"chat-1","model":"%s","choices":[{"delta":{"content":"Hello "},"index":0}]}`, upstreamModel),
			fmt.Sprintf(`{"id":"chat-1","model":"%s","choices":[{"delta":{"content":"world"},"index":0}]}`, upstreamModel),
			fmt.Sprintf(`{"id":"chat-1","model":"%s","choices":[{"delta":{},"finish_reason":"stop","index":0}]}`, upstreamModel),
		}
		for _, chunk := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", chunk)
			flusher.Flush()
		}
		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer fakeUpstream.Close()

	oldUpstream := upstream
	upstream = mustParse(fakeUpstream.URL)
	defer func() { upstream = oldUpstream }()

	cm := circuit.NewManager()
	lim := limiter.New()
	defer lim.Stop()

	handler := NewChatHandler(cm, lim, nil, nil, nil, nil)
	server := httptest.NewServer(handler)
	defer server.Close()

	body := fmt.Sprintf(`{"model":"%s","stream":true,"messages":[{"role":"user","content":"hi"}]}`, clientModel)
	req, _ := http.NewRequest("POST", server.URL+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	bodyBytes, _ := io.ReadAll(resp.Body)
	bodyStr := string(bodyBytes)

	if strings.Contains(bodyStr, upstreamModel) {
		t.Errorf("response should NOT contain upstream model name %q\nResponse:\n%s", upstreamModel, bodyStr)
	}

	if !strings.Contains(bodyStr, clientModel) {
		t.Errorf("response should contain client model name %q\nResponse:\n%s", clientModel, bodyStr)
	}

	if !strings.Contains(bodyStr, "Hello ") || !strings.Contains(bodyStr, "world") {
		t.Errorf("response should contain streamed text\nResponse:\n%s", bodyStr)
	}
}

// ---------------------------------------------------------------------------
// End-to-end non-streaming model replacement
// ---------------------------------------------------------------------------

func TestNonStreamingModelReplacementE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E test (needs database)")
	}
	upstreamModel := "gpt-4o-2024-08-06"
	clientModel := "gpt-4o"

	fakeUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"id":      "chat-1",
			"object":  "chat.completion",
			"model":   upstreamModel,
			"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": "Hello"}}},
		})
	}))
	defer fakeUpstream.Close()

	oldUpstream := upstream
	upstream = mustParse(fakeUpstream.URL)
	defer func() { upstream = oldUpstream }()

	cm := circuit.NewManager()
	lim := limiter.New()
	defer lim.Stop()

	handler := NewChatHandler(cm, lim, nil, nil, nil, nil)
	server := httptest.NewServer(handler)
	defer server.Close()

	body := fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"hi"}]}`, clientModel)
	req, _ := http.NewRequest("POST", server.URL+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)

	if result["model"] != clientModel {
		t.Errorf("expected model=%s, got %v", clientModel, result["model"])
	}

	if strings.Contains(fmt.Sprint(result), upstreamModel) {
		t.Errorf("response should not contain upstream model name: %v", result)
	}
}

// ---------------------------------------------------------------------------
// Streaming with tool_calls
// ---------------------------------------------------------------------------

func TestStreamingToolCallsE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E test (needs database)")
	}
	upstreamModel := "claude-sonnet-4-5-20250929"
	clientModel := "claude-sonnet-4.5"

	fakeUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)

		chunks := []string{
			fmt.Sprintf(`{"id":"chat-1","model":"%s","choices":[{"delta":{"role":"assistant","content":null,"tool_calls":[{"index":0,"id":"call_abc","type":"function","function":{"name":"get_weather","arguments":""}}]},"index":0}]}`, upstreamModel),
			fmt.Sprintf(`{"id":"chat-1","model":"%s","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":"}}]},"index":0}]}`, upstreamModel),
			fmt.Sprintf(`{"id":"chat-1","model":"%s","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"SF\"}"}}]},"index":0}]}`, upstreamModel),
			fmt.Sprintf(`{"id":"chat-1","model":"%s","choices":[{"delta":{},"finish_reason":"tool_calls","index":0}]}`, upstreamModel),
		}
		for _, chunk := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", chunk)
			flusher.Flush()
		}
		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer fakeUpstream.Close()

	oldUpstream := upstream
	upstream = mustParse(fakeUpstream.URL)
	defer func() { upstream = oldUpstream }()

	cm := circuit.NewManager()
	lim := limiter.New()
	defer lim.Stop()

	handler := NewChatHandler(cm, lim, nil, nil, nil, nil)
	server := httptest.NewServer(handler)
	defer server.Close()

	body := fmt.Sprintf(`{"model":"%s","stream":true,"messages":[{"role":"user","content":"weather"}],"tools":[{"type":"function","function":{"name":"get_weather"}}]}`, clientModel)
	req, _ := http.NewRequest("POST", server.URL+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	bodyStr := string(bodyBytes)

	if strings.Contains(bodyStr, upstreamModel) {
		t.Errorf("tool_calls streaming response should NOT contain upstream model:\n%s", bodyStr)
	}

	if !strings.Contains(bodyStr, "get_weather") {
		t.Errorf("tool_calls should be preserved:\n%s", bodyStr)
	}

	if !strings.Contains(bodyStr, clientModel) {
		t.Errorf("tool_calls streaming response should contain client model:\n%s", bodyStr)
	}
}

// ---------------------------------------------------------------------------
// SSE line boundary handling
// ---------------------------------------------------------------------------

func TestStreamingSSEEmptyLineHandling(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E test (needs database)")
	}
	fakeUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)

		fmt.Fprintf(w, "data: {\"model\":\"outbound\"}\n\n")
		fmt.Fprintf(w, "\n")
		fmt.Fprintf(w, "data: {\"model\":\"outbound\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer fakeUpstream.Close()

	oldUpstream := upstream
	upstream = mustParse(fakeUpstream.URL)
	defer func() { upstream = oldUpstream }()

	cm := circuit.NewManager()
	lim := limiter.New()
	defer lim.Stop()

	handler := NewChatHandler(cm, lim, nil, nil, nil, nil)
	server := httptest.NewServer(handler)
	defer server.Close()

	body := `{"model":"client","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	req, _ := http.NewRequest("POST", server.URL+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(bodyBytes))
	}
}

// ---------------------------------------------------------------------------
// Streaming with usage chunk
// ---------------------------------------------------------------------------

func TestStreamingWithUsageChunk(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E test (needs database)")
	}
	fakeUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)

		fmt.Fprintf(w, "data: {\"model\":\"outbound\",\"choices\":[{\"delta\":{\"content\":\"hi\"},\"index\":0}]}\n\n")
		flusher.Flush()
		fmt.Fprintf(w, "data: {\"model\":\"outbound\",\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5}}\n\n")
		flusher.Flush()
		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer fakeUpstream.Close()

	oldUpstream := upstream
	upstream = mustParse(fakeUpstream.URL)
	defer func() { upstream = oldUpstream }()

	cm := circuit.NewManager()
	lim := limiter.New()
	defer lim.Stop()

	handler := NewChatHandler(cm, lim, nil, nil, nil, nil)
	server := httptest.NewServer(handler)
	defer server.Close()

	body := `{"model":"client","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	req, _ := http.NewRequest("POST", server.URL+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	bodyStr := string(bodyBytes)

	if strings.Contains(bodyStr, `"outbound"`) {
		t.Errorf("usage chunk model should also be replaced:\n%s", bodyStr)
	}
}

// ---------------------------------------------------------------------------
// Benchmark: model replacement performance
// ---------------------------------------------------------------------------

func BenchmarkReplaceModelInChunk(b *testing.B) {
	line := `data: {"id":"chat-1","object":"chat.completion.chunk","model":"claude-sonnet-4-5-20250929","choices":[{"delta":{"content":"Hello world this is a test of streaming performance"},"index":0}],"usage":{"prompt_tokens":10,"completion_tokens":5}}` + "\n\n"
	for i := 0; i < b.N; i++ {
		replaceModelInChunk(line, "claude-sonnet-4.5", "claude-sonnet-4-5-20250929")
	}
}

func BenchmarkReplaceModelInChunk_NoMatch(b *testing.B) {
	line := `data: {"id":"chat-1","model":"some-other-model","choices":[{"delta":{"content":"test"}}]}` + "\n\n"
	for i := 0; i < b.N; i++ {
		replaceModelInChunk(line, "claude-sonnet-4.5", "claude-sonnet-4-5-20250929")
	}
}

func TestStreamTimeoutsUseConfigStore(t *testing.T) {
	store := config.NewStore(&config.Config{
		UpstreamTimeout:    11,
		StreamTimeout:      22,
		StreamChunkTimeout: 33,
		FirstByteTimeout:   44,
		KeepaliveInterval:  55,
	})
	SetConfigStore(store)
	t.Cleanup(func() { SetConfigStore(nil) })

	runtimeCfg := currentStreamRuntimeConfig()
	if runtimeCfg.upstreamTimeout != 11*time.Second {
		t.Fatalf("expected upstream timeout 11s, got %v", runtimeCfg.upstreamTimeout)
	}
	if runtimeCfg.streamTimeout != 22*time.Second {
		t.Fatalf("expected stream timeout 22s, got %v", runtimeCfg.streamTimeout)
	}
	if runtimeCfg.streamChunkTimeout != 33*time.Second {
		t.Fatalf("expected stream chunk timeout 33s, got %v", runtimeCfg.streamChunkTimeout)
	}
	if runtimeCfg.firstByteTimeout != 44*time.Second {
		t.Fatalf("expected first byte timeout 44s, got %v", runtimeCfg.firstByteTimeout)
	}
	if runtimeCfg.keepaliveInterval != 55*time.Second {
		t.Fatalf("expected keepalive interval 55s, got %v", runtimeCfg.keepaliveInterval)
	}
}

func TestMaybeSendKeepalive(t *testing.T) {
	rec := httptest.NewRecorder()
	lastSend := time.Now().Add(-2 * time.Second)
	maybeSendKeepalive(rec, &lastSend, time.Second)
	if got := rec.Body.String(); got != sseKeepaliveComment {
		t.Fatalf("expected keepalive comment, got %q", got)
	}
}

func TestReadNextStreamLineEOF(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader(""))
	lastSend := time.Now()
	result := readNextStreamLine(context.Background(), reader, httptest.NewRecorder(), &lastSend, streamRuntimeConfig{streamChunkTimeout: time.Second})
	if !result.EOF {
		t.Fatal("expected EOF result")
	}
	if result.state != streamReadEOF {
		t.Fatalf("expected EOF state, got %v", result.state)
	}
}
