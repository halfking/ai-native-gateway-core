package main

import (
	"testing"
	"time"
)

// TestLoadPIAgentConfigTimeoutKnob 钉 2026-11-18 根修：分析超时旋钮
// LLMGatewayPIErrorAnalysisTimeoutSec（默认 150s、5..600 合法区间）。
// 生产实证：60s 硬编码下 glm-5.3-flash 大提示词 59s 贴线必超时。
func TestLoadPIAgentConfigTimeoutKnob(t *testing.T) {
	t.Setenv("LLMGatewayPIErrorCollectorEnabled", "true")
	t.Setenv("LLMGatewayPIErrorAnalysisEnabled", "true")

	if got := loadPIAgentConfig().timeoutSec; got != 150 {
		t.Fatalf("default timeout = %ds, want 150 (reasoning-model-safe)", got)
	}
	t.Setenv("LLMGatewayPIErrorAnalysisTimeoutSec", "240")
	if got := loadPIAgentConfig().timeoutSec; got != 240 {
		t.Fatalf("explicit timeout = %ds, want 240", got)
	}
	for _, bad := range []string{"0", "4", "601", "-1", "abc"} {
		t.Setenv("LLMGatewayPIErrorAnalysisTimeoutSec", bad)
		if got := loadPIAgentConfig().timeoutSec; got != 150 {
			t.Fatalf("invalid %q accepted as %ds, want fallback 150", bad, got)
		}
	}
	_ = time.Second
}
