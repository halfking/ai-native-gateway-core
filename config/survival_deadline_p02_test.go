package config

// survival_deadline_p02_test.go — P0-2（2026-10-14，
// docs/research/2026-10-14-magpie对比与优化方案.md §P0-2）：
// 交互等待预算默认 5h → 240s 的配置层契约。
//
// 154 生产事故形态：候选全灭后 wait_recovery 轮询 + 5h 交互 deadline，
// 客户端拿到 200+keepalive 后被无限挂着（p99=889s、max 85min）。
// magpie 的 Patience 是 60-240s 有界等待——本契约把默认钉在 240s，
// 同时保持 18000（5h）作为合法上限（专用长等待部署仍可显式配置）。

import (
	"testing"
)

// TestRequestSurvivalInteractiveDeadlineDefaultIsBounded pins BOTH default
// surfaces: the Defaults struct handed out by Load() and the
// NormalizeRequestSurvival fallback for zero/invalid values.
func TestRequestSurvivalInteractiveDeadlineDefaultIsBounded(t *testing.T) {
	t.Setenv("LLM_GATEWAY_REQUEST_SURVIVAL_INTERACTIVE_DEADLINE_SECONDS", "")
	cfg := Load()
	if cfg.RequestSurvivalInteractiveDeadlineSeconds != 240 {
		t.Fatalf("Load() interactive deadline = %d, want 240 (P0-2: the 5h default is what hung clients for 85min)", cfg.RequestSurvivalInteractiveDeadlineSeconds)
	}
	// Normalize fallback: zero struct.
	n := &Config{}
	n.NormalizeRequestSurvival()
	if n.RequestSurvivalInteractiveDeadlineSeconds != 240 {
		t.Fatalf("normalize zero → %d, want 240", n.RequestSurvivalInteractiveDeadlineSeconds)
	}
	// Normalize fallback: negative.
	n = &Config{RequestSurvivalInteractiveDeadlineSeconds: -1}
	n.NormalizeRequestSurvival()
	if n.RequestSurvivalInteractiveDeadlineSeconds != 240 {
		t.Fatalf("normalize -1 → %d, want 240", n.RequestSurvivalInteractiveDeadlineSeconds)
	}
	// Normalize fallback: above the 18000 ceiling falls back to 240, not 5h.
	n = &Config{RequestSurvivalInteractiveDeadlineSeconds: 18001}
	n.NormalizeRequestSurvival()
	if n.RequestSurvivalInteractiveDeadlineSeconds != 240 {
		t.Fatalf("normalize 18001 → %d, want 240 (out-of-range falls back to the bounded default)", n.RequestSurvivalInteractiveDeadlineSeconds)
	}
}

// TestRequestSurvivalInteractiveDeadlineKeepsHistoricalCeiling pins that the
// whole historical range (0, 18000] stays legal — the downgrade changes the
// DEFAULT, not the valid interval.
func TestRequestSurvivalInteractiveDeadlineKeepsHistoricalCeiling(t *testing.T) {
	for _, legal := range []int{1, 240, 600, 17999, 18000} {
		n := &Config{RequestSurvivalInteractiveDeadlineSeconds: legal}
		n.NormalizeRequestSurvival()
		if n.RequestSurvivalInteractiveDeadlineSeconds != legal {
			t.Fatalf("legal value %d normalized to %d, want unchanged", legal, n.RequestSurvivalInteractiveDeadlineSeconds)
		}
	}
}

// TestRequestSurvivalDurableDeadlineUnchanged pins the durable deadline at
// 86400: durable tasks legitimately outlive the connection (client
// disconnect / gateway restart included) and must NOT inherit the P0-2 cut.
func TestRequestSurvivalDurableDeadlineUnchanged(t *testing.T) {
	n := &Config{}
	n.NormalizeRequestSurvival()
	if n.RequestSurvivalDurableDeadlineSeconds != 86400 {
		t.Fatalf("durable deadline = %d, want 86400 (durable semantics are out of P0-2 scope)", n.RequestSurvivalDurableDeadlineSeconds)
	}
}
