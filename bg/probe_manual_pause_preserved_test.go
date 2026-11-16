// bg/probe_manual_pause_preserved_test.go — 2026-11-17 节点状态漂移轮。
//
// 钉住「**探针成功不得撤销运维的人工下线**」这条契约。
//
// 成因（三处成功写面都会犯）：
//
//	运维手工下线 ⇒ node_probe_state.paused=TRUE + last_err_code='manual_offline'
//	（admin/credential_monitor.go 的 manual_offline 分支；兼容视图
//	 v_node_probe_state_compat 把这一支映射成 state='unknown'）
//	⇒ 探针成功镜像（队列臂 bg/probe_service.go 的 mirrorNodeProbeState 成功
//	   分支 / legacy 臂 bg/node_probe.go 的 runOne 成功分支）此前写
//	   paused=FALSE + last_err_code=NULL
//	⇒ 该行重新满足 pumpDueStatesSQL 的 paused=FALSE 条件 ⇒ **运维的下线被
//	   一次在途探测任务悄悄撤销**，且视图从 'unknown' 翻成 'healthy_confirmed'
//	   ⇒ 运维面看到的是「这个节点健康」，与人工意图相反。
//
// ★ 业务成功腿（MarkNodeProbeHealthy）本来就保留 paused ⇒ 探针腿对齐同一
// 口径；last_err_code / last_err_detail 的 manual_offline 标记同样必须保留
// （视图那一支依赖它）。
package bg

import (
	"strings"
	"testing"
)

// manualPauseGuardMarkers 是「保留人工下线」必须出现的三个形态。
func manualPauseGuardMarkers() []string {
	return []string{
		"paused = node_probe_state.paused",
		"node_probe_state.last_err_code = 'manual_offline'",
	}
}

func TestQueueProbeSuccessKeepsManualOfflinePause(t *testing.T) {
	body := sourceBetween(t, probeServiceSource(t),
		"func (w *NodeProbeWorker) mirrorNodeProbeState",
		"func (w *NodeProbeWorker) insertNodeProbeRun")
	if !strings.Contains(body, "execMirrorWrite(ctx, \"success\"") {
		t.Fatal("success mirror arm not found")
	}
	// 只看成功臂：失败臂的 paused 语义不同（它要推进失败梯子）。
	successArm := body[strings.Index(body, "execMirrorWrite(ctx, \"success\""):]
	successArm = successArm[:strings.Index(successArm, "execMirrorWrite(ctx, \"failure\"")]
	for _, marker := range manualPauseGuardMarkers() {
		if !strings.Contains(successArm, marker) {
			t.Errorf("queue success mirror missing %q — 一次探针成功会撤销运维的人工下线", marker)
		}
	}
	if strings.Contains(successArm, "paused = FALSE") {
		t.Error("queue success mirror still clears paused — 人工下线会被在途探测任务撤销")
	}
}

func TestLegacyProbeSuccessKeepsManualOfflinePause(t *testing.T) {
	body := sourceBetween(t, nodeProbeSource(t),
		"func (w *NodeProbeWorker) runOne",
		"func isMissingBindingErr")
	successArm := body[strings.Index(body, "if success {"):]
	for _, marker := range manualPauseGuardMarkers() {
		if !strings.Contains(successArm, marker) {
			t.Errorf("legacy success mirror missing %q — 一次探针成功会撤销运维的人工下线", marker)
		}
	}
	if strings.Contains(successArm, "paused = FALSE") {
		t.Error("legacy success mirror still clears paused — 人工下线会被在途探测任务撤销")
	}
}

func TestBusinessSuccessKeepsManualOfflineMarker(t *testing.T) {
	body := sourceBetween(t, nodeProbeSource(t),
		"func MarkNodeProbeHealthy(ctx context.Context",
		"func (w *NodeProbeWorker) finishProbe")
	for _, marker := range []string{
		"paused = node_probe_state.paused",
		"node_probe_state.last_err_code = 'manual_offline'",
	} {
		if !strings.Contains(body, marker) {
			t.Errorf("business success leg missing %q — 人工下线的标记被清掉后视图会显示 healthy_confirmed", marker)
		}
	}
}
