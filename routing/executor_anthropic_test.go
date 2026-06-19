package routing

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/provider"
)

func TestAnthropicExecutor_BuildRequest_Passthrough(t *testing.T) {
	ae := &AnthropicExecutor{}
	cand := provider.Candidate{
		BaseURL:  "https://api.minimaxi.com/anthropic",
		Protocol: "anthropic-messages",
		APIKey:   "sk-cp-test",
	}
	body := []byte(`{"model":"MiniMax-M2.7","max_tokens":256,"messages":[{"role":"user","content":"hi"}]}`)

	req, err := ae.BuildRequest(cand, body, false)
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	if req.URL.String() != "https://api.minimaxi.com/anthropic/v1/messages" {
		t.Errorf("URL = %q, want https://api.minimaxi.com/anthropic/v1/messages", req.URL.String())
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization header should be empty for Anthropic, got %q", got)
	}
	if got := req.Header.Get("x-api-key"); got != "sk-cp-test" {
		t.Errorf("x-api-key = %q, want sk-cp-test", got)
	}
	if got := req.Header.Get("anthropic-version"); got != "2023-06-01" {
		t.Errorf("anthropic-version = %q, want 2023-06-01", got)
	}
	bodyBytes, _ := io.ReadAll(req.Body)
	if !strings.Contains(string(bodyBytes), `"MiniMax-M2.7"`) {
		t.Errorf("body should be unmodified, got: %s", string(bodyBytes))
	}
}

func TestAnthropicExecutor_StreamResponse_Passthrough(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher := w.(http.Flusher)
		events := []string{
			"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"MiniMax-M2.7\",\"usage\":{\"input_tokens\":12,\"output_tokens\":0}}}\n\n",
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n",
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		}
		for _, e := range events {
			w.Write([]byte(e))
			flusher.Flush()
		}
	}))
	defer upstream.Close()

	resp, err := http.Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	rec := httptest.NewRecorder()
	ae := &AnthropicExecutor{}
	outcome := ae.StreamResponse(rec, resp)
	if outcome.Interrupted {
		t.Errorf("stream should not be interrupted: %s", outcome.Reason)
	}
	body := rec.Body.String()
	for _, expected := range []string{"message_start", "content_block_start", "content_block_delta", "message_stop"} {
		if !strings.Contains(body, expected) {
			t.Errorf("passthrough lost event %q\nfull body: %s", expected, body)
		}
	}
}

func TestAnthropicExecutor_CheckSoftMismatch(t *testing.T) {
	ae := &AnthropicExecutor{}
	mismatched, reason := ae.CheckSoftMismatch("MiniMax-XYZ", "MiniMax-M3")
	if !mismatched {
		t.Errorf("expected soft mismatch (minimax silent fallback)")
	}
	if reason == "" {
		t.Error("reason should be set for diagnostics")
	}
	mismatched, _ = ae.CheckSoftMismatch("MiniMax-M2.7", "MiniMax-M2.7")
	if mismatched {
		t.Error("matching models should not be flagged")
	}
}

// TestExecutor_DispatchesAnthropic verifies the Q4 dispatcher in
// executor.go: when a candidate has protocol=anthropic-messages, the
// Executor.executeAnthropic() method must actually send the request to
// the upstream with x-api-key auth (not Bearer) and the /v1/messages
// path. The upstream stub verifies these invariants.
//
// This is the integration test that closes Phase 2: the dispatcher no
// longer just returns "not yet implemented".
func TestExecutor_DispatchesAnthropic(t *testing.T) {
	cm := newCircuitManagerForTest()
	lim := newLimiterForTest()
	e := &Executor{
		Circuit:         cm,
		Limiter:         lim,
		UpstreamTimeout: 5 * time.Second,
		StreamTimeout:   10 * time.Second,
	}

	// Stub upstream: capture headers + path, return Anthropic-shaped JSON.
	var seenAPIKey, seenPath, seenAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAPIKey = r.Header.Get("x-api-key")
		seenAuth = r.Header.Get("Authorization")
		seenPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"MiniMax-M2.7","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":1,"output_tokens":1},"stop_reason":"end_turn"}`))
	}))
	defer srv.Close()

	r := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(""))
	r.Header.Set("X-Request-Id", "test-req-1")
	rec := httptest.NewRecorder()
	params := &ExecParams{
		W:             rec,
		R:             r,
		BodyBytes:     []byte(`{"model":"MiniMax-M2.7","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`),
		IsStream:      false,
		ClientModel:   "MiniMax-M2.7",
		OutboundModel: "MiniMax-M2.7",
	}
	cand := provider.Candidate{
		ProviderID:   14,
		CredentialID: 6,
		BaseURL:      srv.URL,
		Protocol:     "anthropic-messages",
		APIKey:       "sk-cp-test",
	}

	_, err := e.executeAnthropic(params, cand, 2, time.Now(), nil)
	if err != nil {
		t.Fatalf("executeAnthropic: %v", err)
	}
	if seenAPIKey != "sk-cp-test" {
		t.Errorf("upstream saw x-api-key = %q, want sk-cp-test", seenAPIKey)
	}
	if seenAuth != "" {
		t.Errorf("upstream saw Authorization = %q, want empty (Anthropic uses x-api-key)", seenAuth)
	}
	if seenPath != "/v1/messages" {
		t.Errorf("upstream saw path = %q, want /v1/messages", seenPath)
	}
	// Verify the client got a 200 back.
	if rec.Code != 200 {
		t.Errorf("client got status %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"id":"msg_1"`) {
		t.Errorf("client body should contain Anthropic message; got: %s", rec.Body.String())
	}
}

func TestPrepareAnthropicRequestBody_CompressesOpenAIClient(t *testing.T) {
	ctxWin := 50
	long := strings.Repeat("a", 200)
	openaiBody := []byte(`{"model":"minimax-m3","messages":[
		{"role":"system","content":"sys"},
		{"role":"user","content":"` + long + `"},
		{"role":"assistant","content":"` + long + `"},
		{"role":"user","content":"` + long + `"},
		{"role":"assistant","content":"` + long + `"},
		{"role":"user","content":"latest"}
	]}`)

	e := &Executor{
		ChatToAnthropic: func(body []byte) ([]byte, error) {
			var req struct {
				Model    string            `json:"model"`
				Messages []json.RawMessage `json:"messages"`
			}
			if err := json.Unmarshal(body, &req); err != nil {
				return nil, err
			}
			out, err := json.Marshal(map[string]any{
				"model":      req.Model,
				"max_tokens": 256,
				"messages":   req.Messages,
			})
			return out, err
		},
	}

	var before struct {
		Messages []json.RawMessage `json:"messages"`
	}
	_ = json.Unmarshal(openaiBody, &before)

	out, err := e.prepareAnthropicRequestBody(&ExecParams{
		ClientProtocol: "openai-completions",
		ClientModel:    "minimax-m3",
	}, provider.Candidate{ContextWindow: &ctxWin}, openaiBody)
	if err != nil {
		t.Fatalf("prepareAnthropicRequestBody: %v", err)
	}

	var anthropic struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(out, &anthropic); err != nil {
		t.Fatalf("output not JSON: %v", err)
	}
	if len(anthropic.Messages) >= len(before.Messages) {
		t.Fatalf("expected trimmed anthropic messages; before=%d after=%d", len(before.Messages), len(anthropic.Messages))
	}
}

// TestBuildPreRequestTrimMeta_AnthropicPath verifies that the A4 audit fix
// correctly captures pre-request trim metadata for the Anthropic executor path.
// buildPreRequestTrimMeta (shared with OpenAI path) is called after
// prepareAnthropicRequestBody so any Anthropic-specific body trim is included.
func TestBuildPreRequestTrimMeta_AnthropicPath(t *testing.T) {
	// Simulate: sourceBody=1000 bytes, bodyBytes=800 bytes after anthropic trim
	// (trim happened), with a context window of 131072.
	cw := 131072
	meta := buildPreRequestTrimMeta(1000, 800, &cw)
	if meta == nil {
		t.Fatal("expected non-nil meta when bytes shrank in anthropic path")
	}
	var m map[string]any
	if err := json.Unmarshal(meta, &m); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if m["trim_phase"] != "pre_request" {
		t.Errorf("trim_phase = %v, want pre_request", m["trim_phase"])
	}
	if m["bytes_before"].(float64) != 1000 {
		t.Errorf("bytes_before = %v, want 1000", m["bytes_before"])
	}
	if m["bytes_after"].(float64) != 800 {
		t.Errorf("bytes_after = %v, want 800", m["bytes_after"])
	}

	// mergeCompressionMeta should pass through preTrimMeta when no recovery meta.
	merged := mergeCompressionMeta(nil, meta)
	if string(merged) != string(meta) {
		t.Errorf("mergeCompressionMeta(nil, preTrim) should return preTrim unchanged")
	}

	// When recovery meta also present, recovery wins on shared fields.
	recoveryMeta := []byte(`{"bytes_before":800,"bytes_after":600,"reason_detail":"4xx_recovery"}`)
	both := mergeCompressionMeta(recoveryMeta, meta)
	var both_m map[string]any
	if err := json.Unmarshal(both, &both_m); err != nil {
		t.Fatalf("merged invalid JSON: %v", err)
	}
	// 4xx recovery bytes_before (800) overrides preTrim bytes_before (1000).
	if both_m["bytes_before"].(float64) != 800 {
		t.Errorf("merged bytes_before = %v, want 800 (recovery wins)", both_m["bytes_before"])
	}
	// trim_phase from preTrim survives.
	if both_m["trim_phase"] != "pre_request" {
		t.Errorf("trim_phase = %v, want pre_request (from preTrim)", both_m["trim_phase"])
	}
}

// TestAnthropicExecutor_Q3QualityFix_RenamesEmptyToolName is the
// regression test for the gpt-5.4 + Anthropic-via-OpenAI-routing
// case: a minimax-anthropic-shaped upstream (or any Anthropic
// provider that wraps a buggy openai-compat endpoint) returns a
// tool_use block with empty name; ChatResponseConverter translates
// the body to OpenAI chat.completion; the Q3 path must then run
// the same OpenAI quality processor that ChatExecutor uses to
// rewrite the empty name to __unknown_tool_<i>__.
//
// This is the Q3-specific counterpart to relay/tool_call_quality_test.go's
// TestProcessNonStreamBody_FixMode_RenamesEmptyName. We test the
// full AnthropicExecutor.WriteNonStreamResponse pipeline end-to-end
// (with a stubbed ChatResponseConverter) to make sure the hook
// actually fires after the conversion.
func TestAnthropicExecutor_Q3QualityFix_RenamesEmptyToolName(t *testing.T) {
	// Stub ChatResponseConverter: just emit a fixed OpenAI-shaped
	// body with one empty-named tool_call. The real converter lives
	// in relay/anthropic_to_chat.go and is exercised by the
	// integration tests; here we only need the contract: the
	// converted body is OpenAI-shaped.
	convertedBody := []byte(`{
		"choices":[{
			"message":{
				"tool_calls":[
					{"id":"a","type":"function","function":{"name":"","arguments":"{}"}}
				]
			},
			"finish_reason":"tool_calls"
		}]
	}`)

	// Inline minimal stand-in for relay.ProcessNonStreamBody in fix
	// mode. We re-implement the rewrite here instead of importing
	// relay (routing cannot import relay) so the test stays in
	// package routing. The behaviour we care about is: empty
	// function.name becomes __unknown_tool_<i>__.
	hook := func(body []byte, mode string) ([]byte, []string, []byte, *float64) {
		if mode == "" {
			return body, nil, nil, nil
		}
		var resp struct {
			Choices []struct {
				Message struct {
					ToolCalls []map[string]any `json:"tool_calls"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			return body, nil, nil, nil
		}
		flags := []string{}
		score := 1.0
		rewrote := false
		for ci, ch := range resp.Choices {
			for i, tc := range ch.Message.ToolCalls {
				fn, _ := tc["function"].(map[string]any)
				if fn == nil {
					continue
				}
				if name, _ := fn["name"].(string); name == "" {
					fn["name"] = "__unknown_tool_" + strconvItoa(i) + "__"
					rewrote = true
					flags = append(flags, "empty_tool_name")
					score = 0.5
				}
				_ = ci
			}
		}
		var out []byte
		if rewrote {
			out, _ = json.Marshal(resp)
		} else {
			out = body
		}
		var scorePtr *float64
		if len(flags) > 0 {
			scorePtr = &score
		}
		return out, flags, nil, scorePtr
	}

	ae := &AnthropicExecutor{
		ClientProtocol: "openai-completions",
		ChatResponseConverter: func(body []byte, clientModel string) ([]byte, error) {
			return convertedBody, nil
		},
		QualityProcessNonStream: hook,
	}

	rec := httptest.NewRecorder()
	resp := &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader([]byte(`{"content":[{"type":"text","text":"hi"}]}`))),
	}
	if err := ae.WriteNonStreamResponse(rec, resp, "client-model", "fix", nil); err != nil {
		t.Fatalf("WriteNonStreamResponse: %v", err)
	}
	out := rec.Body.String()
	if !strings.Contains(out, `__unknown_tool_0__`) {
		t.Fatalf("Q3 quality fix did not rewrite empty tool name; body=%s", out)
	}
}

// TestAnthropicExecutor_Q3QualityOffModePassesThrough is the
// off-mode counterpart: when the executor is called with
// qualityFixMode="", the body is byte-identical to what the
// ChatResponseConverter produced. The default off mode keeps the
// pre-existing behaviour for every existing provider.
func TestAnthropicExecutor_Q3QualityOffModePassesThrough(t *testing.T) {
	convertedBody := []byte(`{"choices":[{"message":{"tool_calls":[{"id":"a","function":{"name":""}}]}}]}`)
	hook := func(body []byte, mode string) ([]byte, []string, []byte, *float64) {
		// Off mode: do nothing regardless of body content.
		return body, nil, nil, nil
	}
	ae := &AnthropicExecutor{
		ClientProtocol: "openai-completions",
		ChatResponseConverter: func(body []byte, clientModel string) ([]byte, error) {
			return convertedBody, nil
		},
		QualityProcessNonStream: hook,
	}
	rec := httptest.NewRecorder()
	resp := &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader([]byte(`{}`))),
	}
	if err := ae.WriteNonStreamResponse(rec, resp, "client-model", "off", nil); err != nil {
		t.Fatalf("WriteNonStreamResponse: %v", err)
	}
	if rec.Body.String() != string(convertedBody) {
		t.Fatalf("off mode must be byte-identical; got diff")
	}
}

// TestAnthropicExecutor_Q3QualitySignalsReturnedViaOutParam covers
// the audit fix: the Q3 non-stream path must surface the
// post-processor signals back to the executor so emitTelemetry
// can persist them on the request_log row. Before the fix, the
// signals were discarded (only the body was rewritten) so a
// Q3 non-stream gpt-5.4 response was silently fixed but the row
// carried no quality_flags and the rollup dashboard undercounted
// the bad rate.
func TestAnthropicExecutor_Q3QualitySignalsReturnedViaOutParam(t *testing.T) {
	convertedBody := []byte(`{
		"choices":[{
			"message":{"tool_calls":[
				{"id":"a","type":"function","function":{"name":"","arguments":"{}"}},
				{"id":"b","type":"function","function":{"name":"get_weather","arguments":"{}"}}
			]},
			"finish_reason":"tool_calls"
		}]
	}`)
	hook := func(body []byte, mode string) ([]byte, []string, []byte, *float64) {
		var resp struct {
			Choices []struct {
				Message struct {
					ToolCalls []map[string]any `json:"tool_calls"`
				} `json:"message"`
			} `json:"choices"`
		}
		_ = json.Unmarshal(body, &resp)
		flags := []string{"empty_tool_name"}
		score := 0.5
		actions := []byte(`{"empty_tool_name":{"detected":1,"renamed":1}}`)
		for ci, ch := range resp.Choices {
			for i, tc := range ch.Message.ToolCalls {
				fn, _ := tc["function"].(map[string]any)
				if fn == nil {
					continue
				}
				if name, _ := fn["name"].(string); name == "" {
					fn["name"] = "__unknown_tool_" + strconvItoa(i) + "__"
				}
				_ = ci
			}
		}
		out, _ := json.Marshal(resp)
		return out, flags, actions, &score
	}
	ae := &AnthropicExecutor{
		ClientProtocol: "openai-completions",
		ChatResponseConverter: func(body []byte, clientModel string) ([]byte, error) {
			return convertedBody, nil
		},
		QualityProcessNonStream: hook,
	}
	rec := httptest.NewRecorder()
	resp := &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader([]byte(`{}`))),
	}
	var sig QualitySignals
	if err := ae.WriteNonStreamResponse(rec, resp, "client-model", "fix", &sig); err != nil {
		t.Fatalf("WriteNonStreamResponse: %v", err)
	}
	if len(sig.Flags) == 0 || sig.Flags[0] != "empty_tool_name" {
		t.Fatalf("qualitySignals.Flags must carry empty_tool_name; got %v", sig.Flags)
	}
	if sig.Score == nil || *sig.Score != 0.5 {
		t.Fatalf("qualitySignals.Score must carry 0.5; got %v", sig.Score)
	}
	if len(sig.FixActions) == 0 {
		t.Fatalf("qualitySignals.FixActions must carry the per-flag action summary; got empty")
	}
}

// TestAnthropicExecutor_Q4PassthroughSkipsQualityHook documents
// the Q4 (anthropic passthrough) behaviour: the quality hook is
// intentionally NOT invoked on Anthropic-shape bodies. Empty
// tool_use.name in Anthropic wire format is a hard SDK error
// (no friendly fallback), and adding a separate Anthropic-shape
// processor is tracked in the deployment notes.
func TestAnthropicExecutor_Q4PassthroughSkipsQualityHook(t *testing.T) {
	hookCalled := false
	hook := func(body []byte, mode string) ([]byte, []string, []byte, *float64) {
		hookCalled = true
		return body, nil, nil, nil
	}
	ae := &AnthropicExecutor{
		ClientProtocol: "anthropic-messages", // Q4 path
		QualityProcessNonStream: hook,
	}
	anthropicBody := []byte(`{"content":[{"type":"tool_use","id":"x","name":"","input":{}}],"stop_reason":"tool_use"}`)
	rec := httptest.NewRecorder()
	resp := &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(anthropicBody)),
	}
	if err := ae.WriteNonStreamResponse(rec, resp, "claude-opus-4-8", "fix", nil); err != nil {
		t.Fatalf("WriteNonStreamResponse: %v", err)
	}
	if hookCalled {
		t.Fatal("Q4 passthrough must not invoke the OpenAI-shaped quality hook (Anthropic schema differs)")
	}
	// Body must still be passed through to the client.
	if !strings.Contains(rec.Body.String(), `"tool_use"`) {
		t.Fatalf("Q4 body must pass through, got %s", rec.Body.String())
	}
}

// strconvItoa is a tiny inlined strconv.Itoa. We avoid importing
// strconv at the package level so the test imports stay minimal;
// naming it strconvItoa (not itoa) avoids colliding with the
// package-internal itoa helper in mnf_streak.go.
func strconvItoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
