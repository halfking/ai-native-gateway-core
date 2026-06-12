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
