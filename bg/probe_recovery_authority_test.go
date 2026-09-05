package bg

import (
	"context"
	"strings"
	"testing"
)

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
	if result.Status != ProbeQueueFailed {
		t.Fatalf("result.Status = %q, want failed gateway verification", result.Status)
	}
	if sink.calls != 1 || !sink.success || sink.tenant != "tenant-a" || sink.credential != 42 || sink.model != "nodehealth-test-model" {
		t.Fatalf("URSM recovery signal = %+v, want direct-round success", sink)
	}
	if recorder.single(t).success {
		t.Fatal("gateway failure must not mark the binding recovered")
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
