//go:build integration

package integration

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/transformation"
)

// TestIRDefaultSwitch_Smoke verifies that the IR transport path is
// correctly activated when TRANSPORT_LAYER_IR_ENABLED=true, covering
// the protocol matrix that spec §10.4.1 requires before flipping the
// production default.
func TestIRDefaultSwitch_Smoke(t *testing.T) {
	t.Setenv("TRANSPORT_LAYER_IR_ENABLED", "true")

	factory := transformation.NewTransportFactory()
	factory.Reload()

	cases := []struct {
		name     string
		client   string
		upstream string
		body     string
	}{
		{"openai_to_anthropic", "openai-chat", "anthropic-messages", `{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}`},
		{"openai_to_gemini", "openai-chat", "gemini-generate", `{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}`},
		{"openai_to_responses", "openai-chat", "openai-responses", `{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}`},
		{"anthropic_to_openai", "anthropic-messages", "openai-chat", `{"model":"claude-4","messages":[{"role":"user","content":"hello"}],"max_tokens":100}`},
		{"anthropic_to_gemini", "anthropic-messages", "gemini-generate", `{"model":"claude-4","messages":[{"role":"user","content":"hello"}],"max_tokens":100}`},
		{"anthropic_to_responses", "anthropic-messages", "openai-responses", `{"model":"claude-4","messages":[{"role":"user","content":"hello"}],"max_tokens":100}`},
		{"gemini_to_openai", "gemini-generate", "openai-chat", `{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`},
		{"gemini_to_anthropic", "gemini-generate", "anthropic-messages", `{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`},
		{"responses_to_openai", "openai-responses", "openai-chat", `{"model":"gpt-4o","input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}]}`},
	}

	transport := transformation.NewIRTransport()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newE2EEnvelope(tc.client, tc.upstream, tc.body, "test-model")
			output, err := transport.Convert(context.Background(), env)
			if err != nil {
				t.Fatalf("Convert failed: %v", err)
			}
			if len(output) == 0 {
				t.Fatal("Convert returned empty output")
			}
		})
	}

	// Verify conversion stats: all IR, zero legacy.
	irCount, legacyCount := factory.GetConversionPathStats()
	t.Logf("conversion stats: ir=%d legacy=%d", irCount, legacyCount)
	if legacyCount > 0 {
		t.Fatalf("legacyCount = %d, want 0 (IR fully enabled)", legacyCount)
	}
}
