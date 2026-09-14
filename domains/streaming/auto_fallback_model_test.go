package streaming

import (
	"testing"
)

// 2026-09-14 audit O5: the decider==nil fallback default must be a model
// that is actually routable in production. The old hardcoded default
// ("claude-sonnet-4.5") had no live credentials, turning every model="auto"
// request on a decider-less instance into a guaranteed 503 no_candidate.
func TestAutoFallbackModelDefault(t *testing.T) {
	t.Setenv("LLM_GATEWAY_AUTO_FALLBACK_MODEL", "")
	if got := autoFallbackModel(); got != "deepseek-v4-flash" {
		t.Fatalf("default fallback model = %q, want deepseek-v4-flash (routable workhorse)", got)
	}

	t.Setenv("LLM_GATEWAY_AUTO_FALLBACK_MODEL", " glm-5.2 ")
	if got := autoFallbackModel(); got != "glm-5.2" {
		t.Fatalf("env override = %q, want glm-5.2 (trimmed)", got)
	}
}
