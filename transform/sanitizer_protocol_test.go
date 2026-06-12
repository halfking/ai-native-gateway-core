package transform

import "testing"

func TestAllowListForProtocol_Anthropic(t *testing.T) {
	got := AllowListForProtocol("anthropic-messages")

	required := []string{
		"model", "messages", "system", "stream", "max_tokens",
		"temperature", "top_p", "top_k", "stop_sequences",
		"tools", "tool_choice", "metadata",
	}
	for _, f := range required {
		if !got[f] {
			t.Errorf("anthropic allow-list should include %q", f)
		}
	}
}

func TestAllowListForProtocol_OpenAI(t *testing.T) {
	got := AllowListForProtocol("openai-completions")

	required := []string{
		"model", "messages", "stream", "max_tokens",
		"temperature", "top_p", "n", "stop",
	}
	for _, f := range required {
		if !got[f] {
			t.Errorf("openai allow-list should include %q", f)
		}
	}

	for _, f := range []string{"system", "stop_sequences", "top_k", "metadata"} {
		if got[f] {
			t.Errorf("openai allow-list should NOT include %q", f)
		}
	}
}

func TestAllowListForProtocol_Empty(t *testing.T) {
	got := AllowListForProtocol("")
	if got == nil {
		t.Fatal("empty protocol should return a non-nil allow-list (default OpenAI)")
	}
	if !got["model"] {
		t.Error("default allow-list should include model")
	}
	if got["system"] {
		t.Error("default allow-list (OpenAI fallback) should NOT include system")
	}
}

func TestAllowListForProtocol_Unknown(t *testing.T) {
	got := AllowListForProtocol("some-unknown-protocol")
	if got == nil {
		t.Fatal("unknown protocol should return a non-nil allow-list (default OpenAI)")
	}
	if !got["model"] || !got["messages"] {
		t.Error("default allow-list should include model and messages")
	}
	if got["stop_sequences"] {
		t.Error("default allow-list (OpenAI fallback) should NOT include stop_sequences")
	}
}
