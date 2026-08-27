package executors

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/provider"
	upstreampkg "github.com/kaixuan/llm-gateway-go/upstream"
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
			//nolint:errcheck // HTTP write error non-recoverable
			w.Write([]byte(e))
			flusher.Flush()
		}
	}))
	defer upstream.Close()

	resp, err := http.Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	//nolint:errcheck // best-effort close
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

// TestApplyClientAnthropicHeaders verifies the 2026-08-04 fix: the client's
// anthropic-version and anthropic-beta headers must reach the upstream,
// overriding the hardcoded default version and restoring the (previously
// dropped) beta flags that agent clients depend on.
func TestApplyClientAnthropicHeaders(t *testing.T) {
	t.Run("forwards version and beta", func(t *testing.T) {
		dst := http.Header{}
		dst.Set("anthropic-version", anthropicVersion) // simulate BuildRequest default
		src := http.Header{}
		src.Set("anthropic-version", "2024-10-22")
		src.Set("anthropic-beta", "interleaved-thinking-2025-05-14,prompt-caching-2024-07-31")

		applyClientAnthropicHeaders(dst, src)

		if got := dst.Get("anthropic-version"); got != "2024-10-22" {
			t.Fatalf("anthropic-version = %q, want client value 2024-10-22", got)
		}
		if got := dst.Get("anthropic-beta"); got != "interleaved-thinking-2025-05-14,prompt-caching-2024-07-31" {
			t.Fatalf("anthropic-beta = %q, want client value forwarded", got)
		}
	})

	t.Run("preserves default version when client omits it", func(t *testing.T) {
		dst := http.Header{}
		dst.Set("anthropic-version", anthropicVersion)
		src := http.Header{} // no anthropic-* headers

		applyClientAnthropicHeaders(dst, src)

		if got := dst.Get("anthropic-version"); got != anthropicVersion {
			t.Fatalf("anthropic-version = %q, want default %q preserved", got, anthropicVersion)
		}
		if got := dst.Get("anthropic-beta"); got != "" {
			t.Fatalf("anthropic-beta = %q, want absent", got)
		}
	})

	t.Run("joins repeated beta headers", func(t *testing.T) {
		dst := http.Header{}
		src := http.Header{}
		src.Add("anthropic-beta", "prompt-caching-2024-07-31")
		src.Add("anthropic-beta", "interleaved-thinking-2025-05-14")

		applyClientAnthropicHeaders(dst, src)

		got := dst.Get("anthropic-beta")
		if !strings.Contains(got, "prompt-caching-2024-07-31") || !strings.Contains(got, "interleaved-thinking-2025-05-14") {
			t.Fatalf("anthropic-beta = %q, want both flags joined", got)
		}
	})

	t.Run("skips empty beta values", func(t *testing.T) {
		dst := http.Header{}
		src := http.Header{}
		src.Add("anthropic-beta", "prompt-caching-2024-07-31")
		src.Add("anthropic-beta", "  ") // whitespace-only
		src.Add("anthropic-beta", "")   // empty
		src.Add("anthropic-beta", "interleaved-thinking-2025-05-14")

		applyClientAnthropicHeaders(dst, src)

		got := dst.Get("anthropic-beta")
		if !strings.Contains(got, "prompt-caching-2024-07-31") || !strings.Contains(got, "interleaved-thinking-2025-05-14") {
			t.Fatalf("anthropic-beta = %q, want both non-empty flags", got)
		}
		// Ensure empty/whitespace values were NOT included
		if strings.Contains(got, "  ") || strings.HasSuffix(got, ",") || strings.HasPrefix(got, ",") {
			t.Fatalf("anthropic-beta = %q, should not contain empty values or trailing commas", got)
		}
	})

	t.Run("no beta header when all values empty", func(t *testing.T) {
		dst := http.Header{}
		src := http.Header{}
		src.Add("anthropic-beta", "")
		src.Add("anthropic-beta", "  ")

		applyClientAnthropicHeaders(dst, src)

		if got := dst.Get("anthropic-beta"); got != "" {
			t.Fatalf("anthropic-beta = %q, want absent when all values empty", got)
		}
	})

	t.Run("handles nil headers gracefully", func(t *testing.T) {
		// Should not panic
		applyClientAnthropicHeaders(nil, nil)
		applyClientAnthropicHeaders(http.Header{}, nil)
		applyClientAnthropicHeaders(nil, http.Header{})
	})
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
		//nolint:errcheck // HTTP write error non-recoverable
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

// TestAnthropicExecutor_4xxPassthrough_NotTruncated guards the 2026-07-27 fix
// (D-2): when an Anthropic upstream returns a non-retryable 4xx whose body
// exceeds the 4096-byte classification prefix, the raw-passthrough branch must
// forward the FULL body to the client. Previously only the first 4096 bytes
// reached the client, producing truncated/invalid JSON the SDK could not parse.
func TestAnthropicExecutor_4xxPassthrough_NotTruncated(t *testing.T) {
	cm := newCircuitManagerForTest()
	lim := newLimiterForTest()
	e := &Executor{
		Circuit:         cm,
		Limiter:         lim,
		UpstreamTimeout: 5 * time.Second,
		StreamTimeout:   10 * time.Second,
	}

	// Build a non-retryable 4xx body (tool_call_id_mismatch) larger than 4096
	// bytes by padding the message. This kind reaches the raw-passthrough
	// branch (not retryable, not context-length, not content-filter). A real
	// Anthropic 400 is small, but other Anthropic-protocol upstreams (e.g.
	// MiniMax via the messages path) can return large error bodies, and the
	// gateway must not truncate them.
	padding := strings.Repeat("x", 6000) // > 4096, forces the remainder-read path
	upstreamBody := `{"type":"error","error":{"type":"invalid_request_error","message":"tool_use_id not found (2013): ` +
		padding + `"}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		//nolint:errcheck // HTTP write error non-recoverable
		w.Write([]byte(upstreamBody))
	}))
	defer srv.Close()

	r := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(""))
	r.Header.Set("X-Request-Id", "test-trunc-1")
	rec := httptest.NewRecorder()
	params := &ExecParams{
		W:             rec,
		R:             r,
		BodyBytes:     []byte(`{"model":"claude-3-5-sonnet","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`),
		IsStream:      false,
		ClientModel:   "claude-3-5-sonnet",
		OutboundModel: "claude-3-5-sonnet",
	}
	cand := provider.Candidate{
		ProviderID:   1,
		CredentialID: 1,
		BaseURL:      srv.URL,
		Protocol:     "anthropic-messages",
		APIKey:       "sk-test",
	}

	_, err := e.executeAnthropic(params, cand, 2, time.Now(), nil)
	if err == nil {
		t.Fatal("expected non-nil error from 4xx upstream")
	}

	// The client must receive the 400 status.
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("client status = %d, want 400", rec.Code)
	}
	got := rec.Body.String()
	if len(got) < len(upstreamBody) {
		t.Fatalf("client body truncated: got %d bytes, want >= %d (full upstream body). got=%q...",
			len(got), len(upstreamBody), truncStr(got, 80))
	}
	// The trailing padding proves the END of the body was forwarded (the old
	// 4096 truncation would have cut it off mid-padding).
	if !strings.HasSuffix(got, padding+`"}}`) {
		t.Fatalf("client body does not end with the full padding suffix → truncated. tail=%q",
			truncStr(suffix(got, len(padding)+10), 120))
	}
}

// truncStr returns the first n bytes of s (for log-safe previews).
func truncStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// suffix returns the last n bytes of s.
func suffix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
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
	// package streaming. The behaviour we care about is: empty
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
	if _, err := ae.WriteNonStreamResponse(rec, resp, "client-model", "fix", nil); err != nil {
		t.Fatalf("WriteNonStreamResponse: %v", err)
	}
	if got := rec.Header().Get("Content-Length"); got != strconvItoa(rec.Body.Len()) {
		t.Fatalf("Q3 Content-Length = %q, body length = %d", got, rec.Body.Len())
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
	if _, err := ae.WriteNonStreamResponse(rec, resp, "client-model", "off", nil); err != nil {
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
	if _, err := ae.WriteNonStreamResponse(rec, resp, "client-model", "fix", &sig); err != nil {
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
		ClientProtocol:          "anthropic-messages", // Q4 path
		QualityProcessNonStream: hook,
	}
	anthropicBody := []byte(`{"content":[{"type":"tool_use","id":"x","name":"","input":{}}],"stop_reason":"tool_use"}`)
	rec := httptest.NewRecorder()
	resp := &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(anthropicBody)),
	}
	if _, err := ae.WriteNonStreamResponse(rec, resp, "claude-opus-4-8", "fix", nil); err != nil {
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

func TestAnthropicExecutor_EmptyNativeMessagesResponseIsRetryable(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"missing content", `{"type":"message"}`},
		{"empty content", `{"type":"message","content":[]}`},
		{"empty text", `{"type":"message","content":[{"type":"text","text":""}]}`},
		{"empty thinking", `{"type":"message","content":[{"type":"thinking","thinking":""}]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !isEmptyAnthropicMessagesResponse([]byte(tt.body)) {
				t.Fatalf("isEmptyAnthropicMessagesResponse(%s) = false, want true", tt.body)
			}
		})
	}

	for _, body := range []string{
		`{"type":"message","content":[{"type":"text","text":"hello"}]}`,
		`{"type":"message","content":[{"type":"thinking","thinking":"reasoning"}]}`,
		`{"type":"message","content":[{"type":"thinking","thinking":"","signature":"sig_1"}]}`,
		`{"type":"message","content":[{"type":"redacted_thinking","data":"opaque"}]}`,
		`{"type":"message","content":[{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{}}]}`,
		`{"type":"message","content":[{"type":"tool_use","id":"toolu_1","name":"weather","input":{}}]}`,
		`{"type":"error","error":{"type":"api_error","message":"boom"}}`,
		`{"id":"unknown-shape"}`,
	} {
		if isEmptyAnthropicMessagesResponse([]byte(body)) {
			t.Fatalf("isEmptyAnthropicMessagesResponse(%s) = true, want false", body)
		}
	}
}

func TestExecutorAnthropic_EmptyNativeMessagesResponseDoesNotWriteClient(t *testing.T) {
	upstreamHits := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_empty","type":"message","content":[]}`))
	}))
	defer upstream.Close()

	e := &Executor{UpstreamTimeout: time.Second, StreamTimeout: time.Second}
	params := &ExecParams{
		R:              httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-5","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)),
		W:              httptest.NewRecorder(),
		BodyBytes:      []byte(`{"model":"claude-sonnet-5","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`),
		Model:          "claude-sonnet-5",
		ClientModel:    "claude-sonnet-5",
		ClientProtocol: "anthropic-messages",
	}
	candidate := provider.Candidate{BaseURL: upstream.URL, APIKey: "test", Protocol: "anthropic-messages", RawModel: "claude-sonnet-5"}

	// maxRetries=2 proves the same-credential retry ladder is skipped: an
	// empty response must go straight to candidate failover (KindEmptyResponse
	// is deliberately outside errorsx.IsRetryable).
	_, err := e.executeAnthropic(params, candidate, 2, time.Now(), nil)
	var ue *upstreampkg.Error
	if !errors.As(err, &ue) {
		t.Fatalf("error = %T %v, want bare *upstreampkg.Error for candidate failover", err, err)
	}
	var retry *retryableError
	if errors.As(err, &retry) {
		t.Fatal("empty response must not be wrapped in retryableError (no same-credential retries)")
	}
	if ue.Kind != errorsx.KindEmptyResponse {
		t.Fatalf("kind = %q, want %q", ue.Kind, errorsx.KindEmptyResponse)
	}
	if got := classifyExecError(err); got != errorsx.KindEmptyResponse {
		t.Fatalf("classifyExecError = %q, want %q", got, errorsx.KindEmptyResponse)
	}
	if upstreamHits != 1 {
		t.Fatalf("upstream hits = %d, want exactly 1 (empty response must not retry the same credential)", upstreamHits)
	}
	if got := params.W.(*httptest.ResponseRecorder).Body.String(); got != "" {
		t.Fatalf("client received body before failover: %q", got)
	}
}

// A mid-read client cancellation reads as KindCanceled: it must surface as a
// bare upstream error so executeAnthropic's retry ladder returns it
// immediately instead of retrying a dead request with backoff.
func TestAnthropicReadBodyError_CanceledNotRetryable(t *testing.T) {
	err := anthropicReadBodyError(context.Canceled, &http.Response{StatusCode: 200, Header: http.Header{}})
	var retry *retryableError
	if errors.As(err, &retry) {
		t.Fatal("canceled read must not be wrapped in retryableError")
	}
	var ue *upstreampkg.Error
	if !errors.As(err, &ue) || ue.Kind != errorsx.KindCanceled {
		t.Fatalf("err = %T %v, want upstream.Error with kind canceled", err, err)
	}
}

func TestAnthropicReadBodyError_NetworkRetryable(t *testing.T) {
	err := anthropicReadBodyError(errors.New("read tcp: connection reset by peer"), &http.Response{StatusCode: 200, Header: http.Header{}})
	var retry *retryableError
	if !errors.As(err, &retry) {
		t.Fatalf("network read error must stay retryable, got %T", err)
	}
	if got := classifyExecError(err); got != errorsx.KindNetwork {
		t.Fatalf("kind = %q, want network", got)
	}
}

type closeTrackingBody struct {
	io.Reader
	closed bool
}

func (b *closeTrackingBody) Close() error { b.closed = true; return nil }

func TestDefaultAnthropicPassthrough_ClosesBody(t *testing.T) {
	body := &closeTrackingBody{Reader: strings.NewReader("data: hello\n\n")}
	rec := httptest.NewRecorder()

	outcome := defaultAnthropicPassthrough(rec, &http.Response{Body: body, StatusCode: 200})

	if outcome.Interrupted || outcome.Reason != "" {
		t.Fatalf("outcome = %+v, want zero value", outcome)
	}
	if rec.Body.String() != "data: hello\n\n" {
		t.Fatalf("body = %q, want passthrough bytes", rec.Body.String())
	}
	if !body.closed {
		t.Fatal("defaultAnthropicPassthrough must close the upstream body")
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
