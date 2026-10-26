// admin/probe_origin_alignment_test.go — 实时拓扑 dashboard 的口径一致门
//
// 本文件**不再硬编码任何清单** —— 2026-10-23 之前它硬编码了一份
// origin_stage 清单，那正是"4 处各写一份 ⇒ 必然漂移"的病根之一。
// 现在清单的唯一真值源是 internal/probekind，本门只验证
// **本包的 extractProbeInfo 与 probekind 判定一致**。
//
// 同族门（各自包下，验证同一件事）：
//   - internal/probekind/probekind_test.go   真值源自检
//   - domains/streaming/context_attrs_test.go 侧表口径
//   - middleware/origin_mw_test.go            DB CHECK 闸门口径
//   - domains/stats/probe_origin_alignment_test.go stats/board 口径
package admin

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/internal/probekind"
)

// TestExtractProbeInfo_MatchesProbekindOnEveryStage 验证：对 probekind
// 认定的**每一个**探测 stage，dashboard 都必须判为探测。
//
// 这条断言的方向性很重要：以前 dashboard 的清单比 probekind 多 5 个 alias
// （direct / gateway / scheduled / probe_gateway / probe_scheduled），
// 现在它们从 dashboard 里删掉了（它们从来写不进 DB，是死防御代码）。
// 本门保证 dashboard 不会比真值源少认 —— 少认 = 漏标（用户报的现象）。
func TestExtractProbeInfo_MatchesProbekindOnEveryStage(t *testing.T) {
	for _, stage := range probekind.ProbeStages {
		entry := &telemetry.RequestLogEntry{OriginStage: strPtr(stage)}
		got := extractProbeInfo(entry)
		if !got.IsProbe {
			t.Errorf("origin_stage=%q：probekind 认它是探测（ProbeStages 里有），"+
				"但 extractProbeInfo 返回 IsProbe=false ⇒ 实时拓扑 pill 会漏标。", stage)
		}
	}
}

// TestExtractProbeInfo_MatchesProbekindOnNonProbeStages 验证：对 probekind
// **不**认的 stage，dashboard 也不许认。
//
// 这条防的是"反向漂移"：dashboard 认了某个写不进 DB 的值 ⇒ 同一行在
// dashboard 与侧表 / DB CHECK 给出不同判定。
func TestExtractProbeInfo_MatchesProbekindOnNonProbeStages(t *testing.T) {
	// 全集减去探测集。
	all := map[string]bool{}
	for _, s := range probekind.ValidStages() {
		all[s] = true
	}
	for s := range all {
		if probekind.IsProbeStage(s) {
			continue
		}
		entry := &telemetry.RequestLogEntry{OriginStage: strPtr(s)}
		got := extractProbeInfo(entry)
		if got.IsProbe {
			t.Errorf("origin_stage=%q：probekind 不认它是探测，但 extractProbeInfo 返回 IsProbe=true ⇒ "+
				"与侧表 / DB CHECK 判定不一致。", s)
		}
	}
}

// TestExtractProbeInfo_MatchesProbekindOnEveryActor 验证 actor 兜底路径：
// probekind 认定的每一个探测 actor，即使 origin_stage 丢失，dashboard
// 也必须判为探测。
//
// 2026-10-23 新增路径 4 就是为此 —— 此前 dashboard 只看 stage，
// stage 一丢就漏标；而 stats 端早就看 actor 了 ⇒ 两端口径不一致。
func TestExtractProbeInfo_MatchesProbekindOnEveryActor(t *testing.T) {
	for _, actor := range probekind.ProbeActors {
		entry := &telemetry.RequestLogEntry{OriginActor: strPtr(actor)}
		got := extractProbeInfo(entry)
		if !got.IsProbe {
			t.Errorf("origin_actor=%q：probekind 认它是探测（ProbeActors 里有），"+
				"但 extractProbeInfo 返回 IsProbe=false ⇒ origin_stage 丢失时实时拓扑会漏标。", actor)
		}
	}
}

// TestExtractProbeInfo_BusinessActorIsNotProbe 反例：业务 actor 不得被误认。
func TestExtractProbeInfo_BusinessActorIsNotProbe(t *testing.T) {
	entry := &telemetry.RequestLogEntry{
		OriginStage: strPtr(probekind.BusinessStage),
		OriginActor: strPtr("auto-title-generator"),
	}
	if got := extractProbeInfo(entry); got.IsProbe {
		t.Error("业务行（origin_stage='business' + 业务 actor）不得被判为探测")
	}
}

// TestExtractProbeInfo_NilEntryIsNotProbe 显式登记"缺 origin_stage 不是
// probe"的边界；防止有人为了"兜底"把空值也算探测。
func TestExtractProbeInfo_NilEntryIsNotProbe(t *testing.T) {
	entry := &telemetry.RequestLogEntry{} // 全 nil 指针
	got := extractProbeInfo(entry)
	if got.IsProbe {
		t.Errorf("全 nil 字段的 entry 应当是 not-probe，实际 IsProbe=true（兜底过头了）")
	}
}
