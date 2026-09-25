package bg

import (
	"context"
	"strings"
	"testing"
)

// 2026-09-25 (对健康节点零探测): the URSM recovery signal is DIRECT-round
// driven, and the direct round is also the task verdict. A direct-verified
// node whose gateway round failed (`no_candidates` — the gateway's own
// routing state, not the node's health) settles terminal with the gateway
// anomaly as observability metadata; the old composite verdict marked the
// task Failed and re-armed the healthy node into another probe cycle.
func TestProbeServiceUsesDirectResultForURSMRecovery(t *testing.T) {
	recorder := &probeOutcomeRecorder{}
	service := newTestProbeService(
		nodeProbeRoundResult{ok: true, providerID: 7, latencyMs: 123},
		gatewayProbeResult{round: nodeProbeRoundResult{errCode: "no_candidates"}, pinned: true},
		recorder.apply,
	)
	sink := &recordingNodeProbeStateSink{}
	service.worker.stateSink = sink
	task := probeServiceTask(1)
	task.TenantID = "tenant-a"

	result, err := service.Run(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != ProbeQueueSuccess {
		t.Fatalf("result.Status = %q, want terminal success (node verdict is direct-only)", result.Status)
	}
	if result.NextRunAt != nil {
		t.Fatalf("next run = %v, want nil (gateway round must not re-arm a healthy node)", result.NextRunAt)
	}
	if result.ReasonCode != "gateway_round_degraded" {
		t.Fatalf("reason = %q, want gateway_round_degraded", result.ReasonCode)
	}
	if sink.calls != 1 || !sink.success || sink.tenant != "tenant-a" || sink.credential != 42 || sink.model != "nodehealth-test-model" {
		t.Fatalf("URSM recovery signal = %+v, want direct-round success", sink)
	}
	if !recorder.single(t).success {
		t.Fatal("direct-verified node was not recovered")
	}
}

func TestReconcileStaleNodeProbeStateSQLPreservesPausedNodes(t *testing.T) {
	sql := reconcileStaleNodeProbeStateSQL()
	if !strings.Contains(sql, "COALESCE(nps.paused, FALSE) = FALSE") {
		t.Fatalf("reconcile SQL must exclude paused node probes:\n%s", sql)
	}
	if strings.Contains(sql, "OR nps.paused = TRUE") {
		t.Fatalf("reconcile SQL must not auto-resume paused node probes:\n%s", sql)
	}
}
