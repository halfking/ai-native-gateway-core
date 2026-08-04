package bg

import (
	"context"
	"errors"
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

// TestNodeProbeWorkerBackfillsTenantFromResolver verifies the recovery
// branch: when the trigger omits the tenant ID (a periodic background tick
// that lost the in-memory trigger map across a restart), the worker
// resolves it from the injected tenantResolver before forwarding the
// probe outcome to URSM v2.
func TestNodeProbeWorkerBackfillsTenantFromResolver(t *testing.T) {
	sink := &recordingNodeProbeStateSink{}
	w := &NodeProbeWorker{stateSink: sink}
	w.SetTenantResolver(func(_ context.Context, credID int) (string, error) {
		if credID != 42 {
			t.Fatalf("resolver called for unexpected credential %d", credID)
		}
		return "tenant-b", nil
	})
	w.updateURSMv2ProbeState(context.Background(), "", 42, "model-z", false, 250)
	if sink.calls != 1 || sink.tenant != "tenant-b" || sink.credential != 42 || sink.model != "model-z" || sink.success || sink.latency != 250 {
		t.Fatalf("unexpected sink call: %+v", sink)
	}
}

// TestNodeProbeWorkerSkipsSinkOnTenantLookupFailure verifies the worker
// swallows resolver failures — the sink MUST NOT be called when the
// fallback lookup could not produce a tenant.
func TestNodeProbeWorkerSkipsSinkOnTenantLookupFailure(t *testing.T) {
	sink := &recordingNodeProbeStateSink{}
	w := &NodeProbeWorker{stateSink: sink}
	w.SetTenantResolver(func(_ context.Context, _ int) (string, error) {
		return "", errors.New("synthetic resolver failure")
	})
	w.updateURSMv2ProbeState(context.Background(), "", 99, "model-q", true, 11)
	if sink.calls != 0 {
		t.Fatalf("sink must not be invoked when tenant lookup failed: %+v", sink)
	}
}
