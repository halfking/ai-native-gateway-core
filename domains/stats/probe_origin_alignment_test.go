// domains/stats/probe_origin_alignment_test.go — stats/board 排除的对齐门
//
// 钉住 domains/stats/minute_entry.go::IsProbeTraffic 与其它 3 处
// （DB CHECK / 侧表 / dashboard）的关系。本测试在 stats 包下，
// 与 admin/domains/streaming/middleware 端的对齐门共同维护。
//
// 第 4 处的特殊地位（2026-10-23 R 报告新发现）：
//   IsProbeTraffic **故意不读 origin_stage**，按文件头注释自陈：
//   "we deliberately do NOT add an origin_stage arm — that would make
//    the live and rollup writers disagree about which rows are business
//    traffic"。它只走三条独立路径：quality_flags / task_type / origin_actor。
//
// 这造成它与 dashboard/侧表的口径**真分歧** ——
//   origin_stage='self_check' + origin_actor='self-check-worker' 的行：
//     - dashboard pill = 是（extractProbeInfo 看 origin_stage）
//     - 侧表 is_probe = true（isProbeOriginStage 看 origin_stage）
//     - stats/board 排除 = **否**（IsProbeTraffic 看 actor，self-check-worker 不在白名单）
//   ⇒ 板上"总请求数"会持续累计这类行，**虚高**。
//
// 本测试不修行为，只显式登记这一形态，供未来对齐修复时对位。
package stats

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

// TestIsProbeTraffic_SelfCheckWorkerIsProbe 钉住 B' 的修复成果：
// origin_actor='self-check-worker' / 'model-quality-worker' 的行
// **必须**被 IsProbeTraffic 认作探测流量。
//
// 历史（2026-10-23 审计前）：stats 端的 actor 白名单只有 4 个
// （node-probe-worker / active-probe-worker / probe-service /
// credential-selfcheck-worker），这两个 actor 不在其中 ⇒ 这两类 worker
// 写出的行 dashboard 认 probe、stats 不认 ⇒ 板上"总请求数"持续虚高。
//
// 本门在修复**之前**是登记"当前未修"的形态（期望红 ⇒ 属主已修）；
// 修复落地后已翻转为"必须认"。
func TestIsProbeTraffic_SelfCheckWorkerIsProbe(t *testing.T) {
	// 这两个 actor 在 middleware/origin_mw.go 的 systemOwnerFallbackStage
	// 里映射到 self_check ⇒ 是探测 stage ⇒ stats 端也必须排除。
	for _, actor := range []string{"self-check-worker", "model-quality-worker"} {
		entry := &telemetry.RequestLogEntry{OriginActor: strptr(actor)}
		if !IsProbeTraffic(entry) {
			t.Errorf("origin_actor=%q：stats/board 端必须认作探测（B' 修复已落地）。\n"+
				"  它写出的行 origin_stage='self_check'，dashboard 认 probe；stats 若不认，\n"+
				"  板上「总请求数」会把探测流量当业务流量累计。\n"+
				"  该 actor 必须出现在 internal/probekind.Writers 里（⇒ ProbeActors ⇒ 本表 + SQL 镜像）。", actor)
		}
	}
}

// TestIsProbeTraffic_KnownProbeActorStillProbe 反向校验：
// 已知白名单内的 actor 仍要被 IsProbeTraffic 识别为 probe，
// 防止未来某次"对齐修复"把这条也误改掉。
func TestIsProbeTraffic_KnownProbeActorStillProbe(t *testing.T) {
	for _, actor := range probeGatewayActors {
		entry := &telemetry.RequestLogEntry{OriginActor: strptr(actor)}
		if !IsProbeTraffic(entry) {
			t.Errorf("origin_actor=%q 应当被 stats 端识别为 probe（白名单内），但 IsProbeTraffic 返回 false", actor)
		}
	}
}
