// domains/streaming/context_attrs_test.go — 侧表 is_probe 的口径一致门
//
// 本文件**不再硬编码任何清单** —— 2026-10-23 之前它硬编码了一份
// origin_stage 清单，那正是"4 处各写一份 ⇒ 必然漂移"的病根之一。
// 现在清单的唯一真值源是 internal/probekind，本门只验证
// **本包的 isProbeOriginStage 与 probekind 判定一致**。
//
// 同族门（各自包下，验证同一件事）：
//   - internal/probekind/probekind_test.go   真值源自检
//   - admin/probe_origin_alignment_test.go   实时拓扑 dashboard 口径
//   - middleware/origin_mw_test.go            DB CHECK 闸门口径
//   - domains/stats/probe_origin_alignment_test.go stats/board 口径
package streaming

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/probekind"
)

// TestIsProbeOriginStage_MatchesProbekind 验证侧表判定与真值源一致：
// probekind 认的**每一个** stage，侧表必须认（少认 = 侧表 is_probe 漏标）；
// probekind 不认的，侧表不许认（多认 = 与 DB CHECK 判定不一致）。
func TestIsProbeOriginStage_MatchesProbekind(t *testing.T) {
	for _, stage := range probekind.ProbeStages {
		if !isProbeOriginStage(stage) {
			t.Errorf("isProbeOriginStage(%q) = false, want true（probekind.ProbeStages 里有 ⇒ 侧表必须认）", stage)
		}
	}
	for _, stage := range probekind.ValidStages() {
		if probekind.IsProbeStage(stage) {
			continue
		}
		if isProbeOriginStage(stage) {
			t.Errorf("isProbeOriginStage(%q) = true, want false（probekind 不认 ⇒ 侧表不得认）", stage)
		}
	}
}

// TestIsProbeOriginStage_BusinessIsNotProbe 显式反例：
// "business" 是合法 origin_stage 但不是探测。
func TestIsProbeOriginStage_BusinessIsNotProbe(t *testing.T) {
	if isProbeOriginStage(probekind.BusinessStage) {
		t.Error("origin_stage='business' 不得被判为探测（反例）")
	}
}
