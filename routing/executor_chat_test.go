package routing

import (
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
)

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
