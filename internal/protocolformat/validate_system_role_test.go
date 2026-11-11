package protocolformat

import (
	"strings"
	"testing"
)

// TestValidateAnthropicRequest_SystemRoleAccepted pins the 2026-11-11 fix
// (ZCode system-role incident): a body whose messages array carries a
// role=system entry MUST pass the gate.
//
// Why (211 production evidence, 2026-11-11): ZCode's /v1/messages traffic
// carries messages[1].role = "system". The gate used to reject it as a
// protocol violation ⇒ the request never left the gateway ⇒ the client saw
// 503 "All providers unavailable" (before the third-layer fix) or 400
// client_bug (after it). The rejection was self-inflicted: a direct probe
// against the real apiclaude credential showed the upstream answers 200 and
// the model actually reads the system content.
//
// The negative control matters just as much: role=system being allowed must
// NOT turn the gate into a rubber stamp — tool / developer / engine / empty
// roles stay rejected (those are genuinely outside the anthropic-messages
// role closure; anthropic has no "tool" role — FixAnthropicMessages turns
// that one into user + tool_result).
func TestValidateAnthropicRequest_SystemRoleAccepted(t *testing.T) {
	legal := []struct {
		name string
		body string
	}{
		{
			// ZCode's actual shape: a user turn carrying the harness
			// <system-reminder> block, followed by an explicit system turn.
			"zcode shape (user blocks + system turn)",
			`{"model":"claude-fable-5-2","max_tokens":128000,"messages":[` +
				`{"role":"user","content":[{"type":"text","text":"<system-reminder>\nproject instructions\n</system-reminder>"}]},` +
				`{"role":"system","content":"You are ZCode."}]}`,
		},
		{
			"system turn with plain string content",
			`{"model":"m","max_tokens":10,"messages":[{"role":"system","content":"s"},{"role":"user","content":"hi"}]}`,
		},
		{
			"system turn with text blocks",
			`{"model":"m","max_tokens":10,"messages":[{"role":"system","content":[{"type":"text","text":"s"}]},{"role":"user","content":"hi"}]}`,
		},
		{
			"system turn before an assistant turn",
			`{"model":"m","max_tokens":10,"messages":[{"role":"system","content":"s"},{"role":"assistant","content":"a"},{"role":"user","content":"u"}]}`,
		},
		{
			// Top-level system + a system turn: both shapes coexist.
			"top-level system plus system turn",
			`{"model":"m","max_tokens":10,"system":"top","messages":[{"role":"system","content":"s"},{"role":"user","content":"hi"}]}`,
		},
	}
	for _, tc := range legal {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateAnthropicRequest([]byte(tc.body)); err != nil {
				t.Fatalf("system-role body must pass the gate, got: %v", err)
			}
		})
	}
}

// TestValidateAnthropicRequest_RoleClosureStillClosed is the negative
// control for the change above: widening the closure to user|assistant|system
// must not widen it any further.
func TestValidateAnthropicRequest_RoleClosureStillClosed(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"tool role", `{"model":"m","max_tokens":10,"messages":[{"role":"tool","content":"x"}]}`},
		{"developer role (openai-only)", `{"model":"m","max_tokens":10,"messages":[{"role":"developer","content":"x"}]}`},
		{"engine role", `{"model":"m","max_tokens":10,"messages":[{"role":"engine","content":"x"}]}`},
		{"empty role", `{"model":"m","max_tokens":10,"messages":[{"role":"","content":"x"}]}`},
		{"function role", `{"model":"m","max_tokens":10,"messages":[{"role":"function","content":"x"}]}`},
		{"role not a string", `{"model":"m","max_tokens":10,"messages":[{"role":123,"content":"x"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateAnthropicRequest([]byte(tc.body))
			if err == nil {
				t.Fatalf("%s: accepted — the role closure must stay closed", tc.name)
			}
			if !strings.Contains(err.Error(), "role") {
				t.Fatalf("%s: error must name the role problem, got: %v", tc.name, err)
			}
		})
	}
}
