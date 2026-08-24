package dispatch

import (
	"context"
	"testing"
	"time"
)

func TestWaterfallRingNewestFirstAndCap(t *testing.T) {
	r := newWaterfallRing(3)
	for i := 1; i <= 5; i++ {
		r.push(WaterfallRequest{RequestID: itoa(i), Model: "m"})
	}
	got := r.snapshot(10, "", 0, "")
	if len(got) != 3 {
		t.Fatalf("len=%d want 3", len(got))
	}
	// Newest first: 5,4,3
	if got[0].RequestID != "5" || got[1].RequestID != "4" || got[2].RequestID != "3" {
		t.Fatalf("order=%v,%v,%v", got[0].RequestID, got[1].RequestID, got[2].RequestID)
	}
}

func TestWaterfallRingFilter(t *testing.T) {
	r := newWaterfallRing(10)
	r.push(WaterfallRequest{RequestID: "a", Model: "claude", Credential: 1})
	r.push(WaterfallRequest{RequestID: "b", Model: "gpt", Credential: 2})
	r.push(WaterfallRequest{RequestID: "c", Model: "claude", Credential: 2})

	byModel := r.snapshot(50, "claude", 0, "")
	if len(byModel) != 2 {
		t.Fatalf("model filter len=%d", len(byModel))
	}
	byCred := r.snapshot(50, "", 2, "")
	if len(byCred) != 2 {
		t.Fatalf("cred filter len=%d", len(byCred))
	}
	both := r.snapshot(50, "claude", 2, "")
	if len(both) != 1 || both[0].RequestID != "c" {
		t.Fatalf("both filter=%+v", both)
	}
}

func TestWaterfallRingTenantFilter(t *testing.T) {
	r := newWaterfallRing(10)
	r.push(WaterfallRequest{RequestID: "a", TenantID: "t1", Model: "m"})
	r.push(WaterfallRequest{RequestID: "b", TenantID: "t2", Model: "m"})
	r.push(WaterfallRequest{RequestID: "c", TenantID: "t1", Model: "m"})

	scoped := r.snapshot(50, "", 0, "t1")
	if len(scoped) != 2 {
		t.Fatalf("tenant t1 len=%d want 2", len(scoped))
	}
	for _, item := range scoped {
		if item.TenantID != "t1" {
			t.Fatalf("leaked tenant %q in %+v", item.TenantID, item)
		}
	}
	all := r.snapshot(50, "", 0, "")
	if len(all) != 3 {
		t.Fatalf("all tenants len=%d want 3", len(all))
	}
}

func TestBuildWaterfallRequestDurations(t *testing.T) {
	qr := NewQueuedRequest("req-1", "t", "claude", nil, nil)
	qr.ResolvedModel = "claude-sonnet-4"
	qr.SelectedCred = CredentialRef{CredentialID: 587, Vendor: "anthropic"}
	t0 := qr.ReqStageTime(ReqStageArrived)
	t1 := t0.Add(10 * time.Millisecond)
	t2 := t1.Add(40 * time.Millisecond)
	t3 := t2.Add(2 * time.Millisecond)
	t4 := t3.Add(8 * time.Millisecond)
	t5 := t4.Add(2 * time.Millisecond)
	t6 := t5.Add(38 * time.Millisecond)
	t7 := t6.Add(2 * time.Millisecond)
	t8 := t7.Add(48 * time.Millisecond)
	t9 := t8.Add(1850 * time.Millisecond)
	qr.SetReqStageTime(ReqStageTotalEnqueued, t1)
	qr.SetReqStageTime(ReqStageTotalDequeued, t2)
	qr.SetReqStageTime(ReqStageModelEnqueued, t3)
	qr.SetReqStageTime(ReqStageModelDequeued, t4)
	qr.SetReqStageTime(ReqStageCredEnqueued, t5)
	qr.SetReqStageTime(ReqStageCredDequeued, t6)
	qr.SetReqStageTime(ReqStageForwardStart, t7)
	qr.SetReqStageTime(ReqStageResponseStart, t8)
	qr.SetReqStageTime(ReqStageResponseEnd, t9)

	item := buildWaterfallRequest(qr, ForwardOutcome{})
	if item.RequestID != "req-1" || item.Model != "claude-sonnet-4" || item.Credential != 587 {
		t.Fatalf("identity=%+v", item)
	}
	if item.Result != "success" {
		t.Fatalf("result=%s", item.Result)
	}
	if item.WaitingInTotalMS < 35 || item.WaitingInTotalMS > 45 {
		t.Fatalf("waiting_in_total_ms=%d", item.WaitingInTotalMS)
	}
	if item.UpstreamLatencyMS < 40 || item.UpstreamLatencyMS > 55 {
		t.Fatalf("upstream=%d", item.UpstreamLatencyMS)
	}
	if item.StreamingDurationMS < 1800 {
		t.Fatalf("streaming=%d", item.StreamingDurationMS)
	}
	if item.ArrivedAt == "" || item.ResponseEndAt == "" {
		t.Fatalf("timestamps missing: %+v", item)
	}
}

func TestPipelineSnapshotWaterfall(t *testing.T) {
	p := NewPipeline(Deps{
		RouteFunc: func(ctx context.Context, qr *QueuedRequest) ([]CredentialRef, error) { return nil, nil },
		ModelResolveFunc: func(ctx context.Context, requested string, tried []string) (string, []string, error) {
			return requested, nil, nil
		},
		ForwardFunc: func(ctx context.Context, qr *QueuedRequest, cred CredentialRef) ForwardOutcome {
			return ForwardOutcome{}
		},
	})
	qr := NewQueuedRequest("wf-1", "t", "m", nil, nil)
	end := qr.ReqStageTime(ReqStageArrived).Add(100 * time.Millisecond)
	qr.SetReqStageTime(ReqStageResponseEnd, end)
	p.recordWaterfall(qr, ForwardOutcome{})

	snap := p.SnapshotWaterfall(10, "", 0, "")
	if !snap.Wired {
		t.Fatal("wired=false")
	}
	if len(snap.Requests) != 1 || snap.Requests[0].RequestID != "wf-1" {
		t.Fatalf("requests=%+v", snap.Requests)
	}
	if snap.BottleneckDiagnosis.Bottleneck != "none" {
		t.Fatalf("diagnosis=%+v", snap.BottleneckDiagnosis)
	}
}

func TestDiagnoseBottleneck(t *testing.T) {
	d := diagnoseBottleneck(
		[]QueueSnapshot{{Model: "a", Depth: 5}},
		[]QueueSnapshot{{Credential: 1, Depth: 2}},
	)
	if d.Bottleneck != "none" {
		t.Fatalf("want none got %s", d.Bottleneck)
	}
	d = diagnoseBottleneck(
		[]QueueSnapshot{{Model: "a", Depth: 5}},
		[]QueueSnapshot{},
	)
	// totalDepth=5 < 50 → none
	if d.Bottleneck != "none" {
		t.Fatalf("low depth want none got %s", d.Bottleneck)
	}
	d = diagnoseBottleneck(
		[]QueueSnapshot{{Model: "hot", Depth: 40}},
		[]QueueSnapshot{},
	)
	if d.Bottleneck != "model_capacity" {
		t.Fatalf("want model_capacity got %s", d.Bottleneck)
	}
	d = diagnoseBottleneck(
		[]QueueSnapshot{{Model: "a", Depth: 1}},
		[]QueueSnapshot{{Credential: 9, Depth: 25}},
	)
	if d.Bottleneck != "node_concurrency" {
		t.Fatalf("want node_concurrency got %s", d.Bottleneck)
	}
}
