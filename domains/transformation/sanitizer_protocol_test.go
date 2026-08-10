package transformation

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

// TestAllowListForProtocol_OpenAI verifies the OpenAI allow-list behaviour.
//
// 2026-08-11 (P7 fix): With paramreg enabled, the list is generated from the
// full registry and is much wider than the legacy 8-field set.  We verify that
// critical fields are present. The old assertions that Anthropic-only fields
// must be absent are removed — the paramreg base deliberately contains cross-
// dialect fields to prevent the "8-field landmine" bug (any provider with a
// passthrough_fields config would previously lose tools/tool_choice etc.).
func TestAllowListForProtocol_OpenAI(t *testing.T) {
	got := AllowListForProtocol("openai-chat")

	required := []string{
		"model", "messages", "stream", "max_tokens",
		"temperature", "top_p", "n", "stop",
		// These were previously missing from the narrow 8-field list:
		"tools", "tool_choice", "response_format", "seed",
		"reasoning_effort", "parallel_tool_calls", "stream_options",
	}
	for _, f := range required {
		if !got[f] {
			t.Errorf("openai allow-list should include %q", f)
		}
	}
}

func TestAllowListForProtocol_Empty(t *testing.T) {
	got := AllowListForProtocol("")
	if got == nil {
		t.Fatal("empty protocol should return a non-nil allow-list")
	}
	if !got["model"] {
		t.Error("default allow-list should include model")
	}
	if !got["messages"] {
		t.Error("default allow-list should include messages")
	}
}

func TestAllowListForProtocol_Unknown(t *testing.T) {
	got := AllowListForProtocol("some-unknown-protocol")
	if got == nil {
		t.Fatal("unknown protocol should return a non-nil allow-list")
	}
	if !got["model"] || !got["messages"] {
		t.Error("default allow-list should include model and messages")
	}
}
