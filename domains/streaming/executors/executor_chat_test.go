package executors

import (
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// stubTimeoutCalculator is a test double for TimeoutCalculator that
// returns the configured value verbatim (ignoring input). It lets us
// pin the streaming-timeout invariant: adaptive must NEVER shorten a
// stream below StreamTimeout, even when the calculator claims a smaller
// value.
type stubTimeoutCalculator struct {
	value time.Duration
}

func (s stubTimeoutCalculator) Calculate(_ AdaptiveTimeoutInput) time.Duration {
	return s.value
}

func TestChatExecutor_BuildRequest(t *testing.T) {
	ce := &ChatExecutor{}
	cand := provider.Candidate{
		BaseURL:  "https://api.openai.com",
		Protocol: "openai-completions",
		APIKey:   "sk-test",
	}
	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)

	req, err := ce.BuildRequest(cand, body, false)
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	if req.URL.String() != "https://api.openai.com/v1/chat/completions" {
		t.Errorf("URL = %q, want https://api.openai.com/v1/chat/completions", req.URL.String())
	}
	if got := req.Header.Get("Authorization"); got != "Bearer sk-test" {
		t.Errorf("Authorization = %q, want Bearer sk-test", got)
	}
	if !strings.Contains(req.Header.Get("Content-Type"), "application/json") {
		t.Errorf("Content-Type = %q, want application/json", req.Header.Get("Content-Type"))
	}
}

func TestChatExecutor_CheckSoftMismatch_NotImplemented(t *testing.T) {
	// For OpenAI, upstream returns the same model it was asked for
	// (no silent fallback). This MUST return false.
	ce := &ChatExecutor{}
	mismatched, reason := ce.CheckSoftMismatch("gpt-4", "gpt-4")
	if mismatched {
		t.Errorf("OpenAI never silently falls back; mismatched should be false (reason=%q)", reason)
	}
}

// TestExtractResponseModel (2026-07-28) verifies the helper that powers
// the OpenAI non-stream model-mismatch check. It must tolerate missing
// fields and malformed bodies (return "" rather than panic / return
// junk that would produce false-positive mismatches).
func TestExtractResponseModel(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"empty", "", ""},
		{"malformed", `{not json`, ""},
		{"no-model", `{"choices":[]}`, ""},
		{"openai-typical", `{"model":"gpt-4o-2024-08-06","choices":[]}`, "gpt-4o-2024-08-06"},
		{"glm-typical", `{"model":"glm-4.6","choices":[]}`, "glm-4.6"},
		{"trimmed-whitespace", `{"model":"  glm-4.6  ","choices":[]}`, "glm-4.6"},
		{"nested-other-fields", `{"id":"x","model":"a","object":"chat.completion","choices":[]}`, "a"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := extractResponseModel([]byte(c.body)); got != c.want {
				t.Fatalf("got %q want %q", got, c.want)
			}
		})
	}
}

func TestExecutorStripVendorFieldsUsesCandidateCatalog(t *testing.T) {
	strip := func(body []byte) []byte {
		return append(body, []byte("-stripped")...)
	}
	e := &Executor{
		StripDoubaoFields:   strip,
		StripDeepSeekFields: strip,
		StripMinimaxFields:  strip,
		StripZhipuFields:    strip,
	}

	for _, tc := range []struct {
		name    string
		catalog string
		want    string
	}{
		{name: "doubao", catalog: "doubao", want: "body-stripped"},
		{name: "aggregate is not doubao", catalog: "volcengine-coding", want: "body"},
		{name: "unknown is not doubao", catalog: "", want: "body"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := string(e.stripVendorFields([]byte("body"), tc.catalog))
			if got != tc.want {
				t.Fatalf("stripVendorFields(%q) = %q, want %q", tc.catalog, got, tc.want)
			}
		})
	}
}

// TestPrepareRequestBody_InjectsStreamOptionsForOpenAI pins the OpenAI
// behaviour: when params.IsStream=true and the upstream is openai-completions,
// prepareRequestBody MUST inject "stream_options":{"include_usage":true} so
// the upstream returns a final usage chunk we can attribute for billing.
func TestPrepareRequestBody_InjectsStreamOptionsForOpenAI(t *testing.T) {
	params := &ExecParams{
		BodyBytes:   []byte(`{"model":"gpt-4","stream":true,"messages":[]}`),
		ClientModel: "gpt-4",
		IsStream:    true,
	}
	cand := provider.Candidate{
		Protocol:    "openai-completions",
		CatalogCode: "openai",
		RawModel:    "gpt-4",
	}

	got := prepareRequestBody(params, cand)
	if !strings.Contains(string(got), `"stream_options"`) {
		t.Errorf("OpenAI streaming body should include stream_options, got: %s", string(got))
	}
}

// TestPrepareRequestBody_SkipsStreamOptionsForAnthropic pins the Anthropic
// guard: when params.IsStream=true and the upstream speaks anthropic-messages,
// prepareRequestBody MUST NOT inject "stream_options" because Anthropic has
// no such field (usage arrives via message_start + message_delta events).
// Injecting it would either be silently ignored or rejected by strict
// providers and complicates protocol passthrough debugging.
func TestPrepareRequestBody_SkipsStreamOptionsForAnthropic(t *testing.T) {
	params := &ExecParams{
		BodyBytes:   []byte(`{"model":"claude-3-5-sonnet","stream":true,"max_tokens":256,"messages":[]}`),
		ClientModel: "claude-3-5-sonnet",
		IsStream:    true,
	}
	cand := provider.Candidate{
		Protocol:    "anthropic-messages",
		CatalogCode: "anthropic",
		RawModel:    "claude-3-5-sonnet",
	}

	got := prepareRequestBody(params, cand)
	if strings.Contains(string(got), `"stream_options"`) {
		t.Errorf("Anthropic streaming body should NOT include stream_options, got: %s", string(got))
	}
	if !strings.Contains(string(got), `"stream":true`) {
		t.Errorf("Anthropic body should keep stream:true, got: %s", string(got))
	}
}

// TestSelectUpstreamTimeout_NonStreamUsesUpstreamTimeout pins the
// non-streaming path: regardless of what the adaptive calculator would
// say, non-streaming requests must use UpstreamTimeout verbatim
// (adaptive is intentionally a no-op for non-streaming).
func TestSelectUpstreamTimeout_NonStreamUsesUpstreamTimeout(t *testing.T) {
	e := &Executor{
		UpstreamTimeout: 120 * time.Second,
		StreamTimeout:   900 * time.Second,
		TimeoutAdapter:  stubTimeoutCalculator{value: 30 * time.Second}, // shorter than UpstreamTimeout
	}
	params := &ExecParams{IsStream: false}
	cand := provider.Candidate{BaseURL: "https://example.com"}

	got := e.selectUpstreamTimeout(params, cand, 1024)
	if got != 120*time.Second {
		t.Errorf("non-streaming should use UpstreamTimeout=120s, got %v", got)
	}
}

// TestSelectUpstreamTimeout_AdaptiveNeverShortensStream is the core
// streaming-timeout invariant added in 81e627ff1 (2026-07-29): the
// adaptive calculator must NEVER shorten a streaming response below
// StreamTimeout. Long-running LLM reasoning chains were being killed
// mid-stream when adaptive capped the upstream context to 180s.
func TestSelectUpstreamTimeout_AdaptiveNeverShortensStream(t *testing.T) {
	e := &Executor{
		UpstreamTimeout: 120 * time.Second,
		StreamTimeout:   900 * time.Second,
		TimeoutAdapter:  stubTimeoutCalculator{value: 30 * time.Second}, // far shorter than StreamTimeout
	}
	params := &ExecParams{IsStream: true}
	cand := provider.Candidate{BaseURL: "https://example.com"}

	got := e.selectUpstreamTimeout(params, cand, 1024)
	if got != 900*time.Second {
		t.Fatalf("streaming timeout must NOT be shortened by adaptive; want 900s, got %v", got)
	}
}

// TestSelectUpstreamTimeout_AdaptiveCanExtendStream verifies the
// complementary case: when the adaptive calculator recommends a value
// longer than StreamTimeout (e.g. for a slow node or huge context), the
// recommendation wins. Without this path, operators would have no way
// to lengthen the upstream context beyond StreamTimeout.
func TestSelectUpstreamTimeout_AdaptiveCanExtendStream(t *testing.T) {
	e := &Executor{
		UpstreamTimeout: 120 * time.Second,
		StreamTimeout:   900 * time.Second,
		TimeoutAdapter:  stubTimeoutCalculator{value: 1800 * time.Second}, // longer than StreamTimeout
	}
	params := &ExecParams{IsStream: true}
	cand := provider.Candidate{BaseURL: "https://example.com"}

	got := e.selectUpstreamTimeout(params, cand, 1024)
	if got != 1800*time.Second {
		t.Fatalf("adaptive timeout > StreamTimeout should win; want 1800s, got %v", got)
	}
}

// TestSelectUpstreamTimeout_NoAdapterUsesStreamTimeout pins the
// no-adapter case: without TimeoutAdapter the streaming timeout must
// fall back to StreamTimeout verbatim (no adaptive math involved).
func TestSelectUpstreamTimeout_NoAdapterUsesStreamTimeout(t *testing.T) {
	e := &Executor{
		UpstreamTimeout: 120 * time.Second,
		StreamTimeout:   900 * time.Second,
		TimeoutAdapter:  nil,
	}
	params := &ExecParams{IsStream: true}
	cand := provider.Candidate{BaseURL: "https://example.com"}

	got := e.selectUpstreamTimeout(params, cand, 1024)
	if got != 900*time.Second {
		t.Errorf("without adapter, streaming should use StreamTimeout=900s, got %v", got)
	}
}
