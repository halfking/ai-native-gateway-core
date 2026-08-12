package bg

import (
	"testing"
)

// TestProbeQueue_SetProbeSink_NilSafe and the publishProbeTask helper tests
// pin the 2026-08-11 durable-queue lifecycle hook: Enqueue publishes pending,
// Claim publishes in-flight, Scheduled is derived from the source. The sink is
// optional; a nil probeSink must never panic.
func TestProbeQueue_PublishProbeTask_NilSafe(t *testing.T) {
	q := &ProbeQueue{} // probeSink nil
	// Must not panic.
	q.publishProbeTask(ProbeQueueTask{ID: 1, CredentialID: 5, RawModel: "m", Source: "request_failure"}, "pending")
}

func TestProbeQueue_PublishProbeTask_FiresOnSink(t *testing.T) {
	sink := &captureSink{}
	q := &ProbeQueue{probeSink: sink}
	q.publishProbeTask(ProbeQueueTask{
		ID: 7, CredentialID: 9, ProviderID: 3, RawModel: "gpt-5.6",
		Source: "no_candidates", Attempt: 2,
	}, "in-flight")
	if sink.count() != 1 {
		t.Fatalf("sink must fire once, got %d", sink.count())
	}
	evt := sink.last()
	if evt.Status != "in-flight" {
		t.Errorf("status = %q, want in-flight", evt.Status)
	}
	// 2026-08-13: Source now propagates the task's own source (previously
	// hardcoded "integrity"), so node_probe / integrity_verify / selfcheck
	// tasks render with their real origin on the 自检 stream.
	if evt.Source != "no_candidates" {
		t.Errorf("source = %q, want no_candidates (task source)", evt.Source)
	}
	if evt.CredentialID != 9 || evt.RawModel != "gpt-5.6" {
		t.Errorf("credential/model not propagated: %+v", evt)
	}
	// request_failure / no_candidates are NOT scheduled.
	if evt.Scheduled {
		t.Errorf("request-triggered source must not be Scheduled=true")
	}
}

func TestProbeQueue_PublishProbeTask_ScheduledFlag(t *testing.T) {
	sink := &captureSink{}
	q := &ProbeQueue{probeSink: sink}
	// A periodic/scheduled source (e.g. integrity planner) sets Scheduled=true
	// so the 自检 tab can distinguish 定时自检 from on-demand probes.
	q.publishProbeTask(ProbeQueueTask{
		ID: 1, CredentialID: 2, RawModel: "m", Source: "periodic_integrity",
	}, "pending")
	evt := sink.last()
	if !evt.Scheduled {
		t.Errorf("scheduled source must set Scheduled=true")
	}
}

// TestNodeProbeWorker_PublishProbeEvent_NilSafe pins the node-probe lifecycle
// hook nil-safety.
func TestNodeProbeWorker_PublishProbeEvent_NilSafe(t *testing.T) {
	w := &NodeProbeWorker{} // probeSink nil
	w.publishProbeEvent(42, "gpt-5.6", "pending", "node_probe", "request_failure", 0)
}

func TestNodeProbeWorker_PublishProbeEvent_FiresOnSink(t *testing.T) {
	sink := &captureSink{}
	w := &NodeProbeWorker{probeSink: sink}
	w.publishProbeEvent(42, "gpt-5.6", "in-flight", "node_probe", "request_failure", 3)
	if sink.count() != 1 {
		t.Fatalf("sink must fire once, got %d", sink.count())
	}
	evt := sink.last()
	if evt.Status != "in-flight" {
		t.Errorf("status = %q, want in-flight", evt.Status)
	}
	if evt.Source != "node_probe" {
		t.Errorf("source = %q, want node_probe", evt.Source)
	}
	if evt.Attempt != 3 {
		t.Errorf("attempt = %d, want 3", evt.Attempt)
	}
	if evt.ID == "" {
		t.Errorf("ID must be populated")
	}
}

// TestCredentialSelfcheckWorker_PublishSelfcheck_NilSafe pins the self-check
// lifecycle hook nil-safety.
func TestCredentialSelfcheckWorker_PublishSelfcheck_NilSafe(t *testing.T) {
	w := &CredentialSelfcheckWorker{} // probeSink nil
	w.publishSelfcheck(1, 100, "in-flight")
}

func TestCredentialSelfcheckWorker_PublishSelfcheck_ScheduledFlag(t *testing.T) {
	sink := &captureSink{}
	w := &CredentialSelfcheckWorker{probeSink: sink}
	w.publishSelfcheck(11, 200, "in-flight")
	evt := sink.last()
	if evt.Source != "selfcheck" {
		t.Errorf("source = %q, want selfcheck", evt.Source)
	}
	// Daily self-check is scheduled → always Scheduled=true so the tab can
	// distinguish 定时自检 from request-triggered probes.
	if !evt.Scheduled {
		t.Errorf("daily self-check must set Scheduled=true")
	}
	// 2026-08-12: runID is part of the stable task ID so the in-flight and
	// terminal transitions for the same daily run collapse into one tile.
	if evt.ID != "selfcheck:11:200" {
		t.Errorf("ID = %q, want selfcheck:11:200", evt.ID)
	}
}
