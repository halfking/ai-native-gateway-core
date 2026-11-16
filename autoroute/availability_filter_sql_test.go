package autoroute

// availability_filter_sql_test.go —— 决策面 / 候选面**同闸**钉测
// （2026-11-17 auto 模式可用性轮 · 根修 6）。
//
// ★ 守的是什么：
//
// auto 决策选中的模型在候选面被**必然滤空** ⇒ 请求 503「原来的模型不可用」。
// 211 真机实证（2026-11-17 00:24）：qwen2.5-7b-instruct 的状态闸全部通过，
// 但 models_canonical.released_at = 2024-09-19，被候选面的 model lifecycle
// 闸（migration 966）丢弃 ⇒ 决策说能用、路由说不能用 ⇒ 判据分歧。
//
// ⇒ 决策面必须补齐候选面已有的三道闸。少一道，同类事故就会以另一种模型名
// 再现（这正是「同型事故会复发」的通用式）。

import (
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/probemode"
)

// TestAvailabilityFilterSQL_HasCandidateSideGates pins that the decision-side
// availability SQL carries every gate the candidate-side SQL carries.
func TestAvailabilityFilterSQL_HasCandidateSideGates(t *testing.T) {
	sql := availabilityFilterSQL()

	checks := []struct {
		name   string
		needle string
	}{
		{"models_canonical disabled gate", "COALESCE(mc.status, 'active') != 'disabled'"},
		{"model lifecycle gate", "mc.released_at IS NULL"},
		{"lifecycle age comparison", "age(now(), mc.released_at)"},
		{"stale threshold setting key", "model_lifecycle.stale_threshold"},
		{"manual priority exemption", "COALESCE(cmb.manual_priority, 0) > 0"},
		{"broken confirmed probe guard", "mps.state = 'broken_confirmed'"},
		{"probe pair key", "mps.raw_model_name = pm.raw_model_name"},
	}
	for _, c := range checks {
		if !strings.Contains(sql, c.needle) {
			t.Errorf("decision-side availability SQL is missing the %s gate (%q)", c.name, c.needle)
		}
	}
}

// TestAvailabilityFilterSQL_ProbeTableIsProbemodeGuarded pins that the probe
// gate reads the same table the candidate side reads — legacy vs new probe
// stack must not diverge between the two faces.
func TestAvailabilityFilterSQL_ProbeTableIsProbemodeGuarded(t *testing.T) {
	sql := availabilityFilterSQL()
	if !strings.Contains(sql, probemode.GuardStateTable()) {
		t.Errorf("availability SQL must read the guarded probe table %q", probemode.GuardStateTable())
	}
}

// TestAvailabilityFilterSQL_PlaceholderRendered pins that the probe guard is
// actually rendered — a leftover placeholder would silently drop the gate.
func TestAvailabilityFilterSQL_PlaceholderRendered(t *testing.T) {
	if strings.Contains(availabilityFilterSQL(), "/*BROKEN_PAIR_GUARD*/") {
		t.Error("BROKEN_PAIR_GUARD placeholder was not rendered into the SQL")
	}
}
