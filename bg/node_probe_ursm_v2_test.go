package bg

import (
	"context"
	"testing"
)

type recordingNodeProbeStateSink struct {
	tenant     string
	credential int
	model      string
	success    bool
	latency    int
	calls      int
}

func (s *recordingNodeProbeStateSink) ApplyProbeForTenant(_ context.Context, tenant string, credentialID int, rawModel string, success bool, latencyMs int) error {
	s.tenant = tenant
	s.credential = credentialID
	s.model = rawModel
	s.success = success
	s.latency = latencyMs
	s.calls++
	return nil
}

func TestNodeProbeWorkerWritesTenantScopedV2ProbeState(t *testing.T) {
	sink := &recordingNodeProbeStateSink{}
	w := &NodeProbeWorker{stateSink: sink}
	w.updateURSMv2ProbeState(context.Background(), "tenant-a", 17, "model-x", true, 123)
	if sink.calls != 1 || sink.tenant != "tenant-a" || sink.credential != 17 || sink.model != "model-x" || !sink.success || sink.latency != 123 {
		t.Fatalf("unexpected sink call: %+v", sink)
	}
}
