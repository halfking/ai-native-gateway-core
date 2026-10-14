package reqprobe

import (
	"strings"
	"testing"
)

// P0-B（2026-10-14 magpie 对标）：骨架按线格式——stream_options 只在
// openai-chat 线保护（usage 契约）；responses/anthropic/gemini 可剥。
// 判据纪律：前提在场配对 + 变异方向「恢复全局保护」必须红。

func TestP0BStreamOptionsStrippableOnResponsesWire(t *testing.T) {
	body := []byte(`{"model":"g","messages":[],"stream_options":{"include_usage":true},"thinking":{}}`)
	out, stripped := StripParamsForWire(body, "stream_options", "openai-responses")
	if len(stripped) != 1 || stripped[0] != "stream_options" {
		t.Fatalf("responses wire strip stream_options = %v, want [stream_options]", stripped)
	}
	if strings.Contains(string(out), "stream_options") {
		t.Fatalf("responses wire body still has stream_options: %s", out)
	}
}

func TestP0BStreamOptionsProtectedOnChatWire(t *testing.T) {
	body := []byte(`{"model":"g","messages":[],"stream_options":{"include_usage":true}}`)
	_, stripped := StripParamsForWire(body, "stream_options", "openai-chat")
	if len(stripped) != 0 {
		t.Fatalf("chat wire must protect stream_options, got stripped=%v", stripped)
	}
	// empty wire = conservative default (legacy callers) also protects
	_, stripped = StripParamsForWire(body, "stream_options", "")
	if len(stripped) != 0 {
		t.Fatalf("empty wire must protect stream_options (legacy default), got %v", stripped)
	}
}

func TestP0BStreamOptionsStrippableOnAnthropicAndGemini(t *testing.T) {
	body := []byte(`{"model":"g","messages":[],"stream_options":{"include_usage":true}}`)
	for _, wire := range []string{"anthropic-messages", "gemini"} {
		_, stripped := StripParamsForWire(body, "stream_options", wire)
		if len(stripped) != 1 || stripped[0] != "stream_options" {
			t.Fatalf("wire %s: stripped=%v, want [stream_options]", wire, stripped)
		}
	}
}

func TestP0BNamedOptionalStillStripsOnChat(t *testing.T) {
	// service_tier 不在骨架集——chat 线点名仍可剥（与 stream_options 对照）。
	body := []byte(`{"model":"g","messages":[],"service_tier":"default"}`)
	_, stripped := StripParamsForWire(body, "service_tier", "openai-chat")
	if len(stripped) != 1 || stripped[0] != "service_tier" {
		t.Fatalf("chat wire service_tier strip = %v, want [service_tier]", stripped)
	}
}

func TestP0BModelStillSkeletonOnAllWires(t *testing.T) {
	body := []byte(`{"model":"g","messages":[]}`)
	for _, wire := range []string{"", "openai-chat", "openai-responses", "anthropic-messages"} {
		_, stripped := StripParamsForWire(body, "model", wire)
		if len(stripped) != 0 {
			t.Fatalf("wire %s must never strip model, got %v", wire, stripped)
		}
	}
}

func TestP0BStripCandidatesFromNestedPath(t *testing.T) {
	got := StripCandidates("stream_options.include_usage")
	if len(got) == 0 || got[0] != "stream_options" {
		t.Fatalf("StripCandidates nested = %v, want leading stream_options", got)
	}
	got = StripCandidates(`body.service_tier`)
	found := false
	for _, g := range got {
		if g == "service_tier" {
			found = true
		}
	}
	if !found {
		t.Fatalf("StripCandidates body.service_tier = %v, want service_tier", got)
	}
	if Strippable("messages") {
		t.Fatal("messages must not be Strippable (core blacklist)")
	}
}
