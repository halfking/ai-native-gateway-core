package dispatch

// V3.3-OBS OBS-BE3 contract tests: the queue_snapshot pipeline layer
// (docs/会话优化v3/13 号 §4) promoted from TARGET to CURRENT.
//
//   - with dispatch enabled + a real Pipeline, Snapshot() carries
//     pipeline {depth, waitingMsP50/P95, inFlight, degraded} and a
//     monotonically increasing sourceVersion;
//   - with dispatch disabled, the pipeline field and sourceVersion are
//     absent (omitempty) — no zero-value masquerade;
//   - degraded is raised by a disabled governor or an overflow event
//     between two ticks.

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// submitAndWait submits n requests through the pipeline and blocks until
// every Submit call has returned.
func submitAndWait(t *testing.T, p *Pipeline, n int) {
	t.Helper()
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			qr := NewQueuedRequest("obs-be3-"+itoa(i), "tenant", "gpt-obs", context.Background(), nil)
			p.Submit(context.Background(), qr)
		}(i)
	}
	wg.Wait()
}

func TestPipelineQueueStats_RealPipelineTraffic(t *testing.T) {
	// v4 (R1.3): the pipeline default is zero queue-wait, which would fail
	// saturated requests over to the failover ladder instead of queueing
	// them. This contract test needs a real queue backlog, so it opts into
	// an explicit wait budget (and disables same-cred retry churn).
	cfg := DefaultConfig()
	cfg.MaxQueueWaitMS = 2000
	cfg.RetryPerCredential = 0
	hotCfg := &atomic.Value{}
	hotCfg.Store(&cfg)
	p := NewPipeline(Deps{
		RouteFunc: func(ctx context.Context, qr *QueuedRequest) ([]CredentialRef, error) {
			return []CredentialRef{{
				CredentialID:     7,
				ConcurrencyMode:  ModeConcurrency,
				ConcurrencyLimit: 1, // serialize: 1 in-flight, rest queued
			}}, nil
		},
		ModelResolveFunc: func(ctx context.Context, req string, tried []string) (string, []string, error) {
			return "gpt-obs", nil, nil
		},
		ForwardFunc: func(ctx context.Context, qr *QueuedRequest, cred CredentialRef) ForwardOutcome {
			time.Sleep(200 * time.Millisecond) // hold in-flight + queue backlog
			return ForwardOutcome{}
		},
		HotCfg: hotCfg,
	})
	p.Start()
	defer p.Stop()

	c := newQueueCollectorForPipeline(p)

	// Mid-flight snapshot: 8 submissions, concurrency cap 1 → at least one
	// queued and one forwarding.
	done := make(chan struct{})
	go func() {
		submitAndWait(t, p, 8)
		close(done)
	}()
	// Wait long enough for at least one request to enter the forwarder
	// (ForwardFunc sleeps 200ms to hold in-flight + backlog). 60ms was
	// flaky under full-repo `go test ./...` parallelism when the GOMAXPROCS
	// scheduler delayed the first forwarder pick-up; 150ms leaves headroom
	// well under the 200ms forwarder hold.
	time.Sleep(150 * time.Millisecond)

	snapMid := c.Snapshot()
	if !snapMid.Wired || !snapMid.Enabled {
		t.Fatalf("expected wired+enabled snapshot, got wired=%v enabled=%v", snapMid.Wired, snapMid.Enabled)
	}
	if snapMid.Pipeline == nil {
		t.Fatal("expected pipeline stats present when dispatch enabled + wired, got nil")
	}
	if snapMid.SourceVersion == 0 {
		t.Error("expected non-zero sourceVersion on a wired snapshot")
	}
	if snapMid.Pipeline.Depth < 1 {
		t.Errorf("expected queued depth >= 1 mid-flight, got %d", snapMid.Pipeline.Depth)
	}
	if snapMid.Pipeline.InFlight < 1 {
		t.Errorf("expected inFlight >= 1 mid-flight, got %d", snapMid.Pipeline.InFlight)
	}
	if snapMid.Pipeline.Degraded {
		t.Error("unexpected degraded: concurrency governor is active and no overflow occurred")
	}

	// Drain: waiting percentiles must come from real ring samples.
	<-done
	// 8 submissions × 200ms forwarder hold × concurrency=1 = up to ~1.6s total
	// drain. The test relies on the percentile ring filling and the depth
	// counter settling to zero; poll instead of a fixed sleep so the
	// drain window survives full-repo `go test ./...` parallelism where
	// the scheduler can stretch drain beyond the theoretical minimum.
	deadline := time.Now().Add(15 * time.Second)
	for {
		s := c.Snapshot()
		if s.Pipeline != nil && s.Pipeline.Depth == 0 && s.Pipeline.InFlight == 0 {
			break
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Give the ticker-driven waterfall/percentile ring one more tick to
	// land (100ms cadence in recordStageMetrics). Polling for percentile
	// presence separately would couple to too many internal cadences.
	time.Sleep(150 * time.Millisecond)

	snapIdle := c.Snapshot()
	if snapIdle.Pipeline == nil {
		t.Fatal("pipeline stats must remain present after drain (depth 0 is a real zero)")
	}
	if snapIdle.Pipeline.Degraded {
		t.Error("degraded should be false: active governor and no overflow in the last tick window")
	}
	if snapIdle.Pipeline.Depth != 0 {
		t.Errorf("expected drained depth 0 (real zero), got %d", snapIdle.Pipeline.Depth)
	}
	if snapIdle.Pipeline.InFlight != 0 {
		t.Errorf("expected drained inFlight 0 (real zero), got %d", snapIdle.Pipeline.InFlight)
	}
	if snapIdle.Pipeline.WaitingMsP50 == nil || snapIdle.Pipeline.WaitingMsP95 == nil {
		t.Fatal("expected waiting percentiles present after real completions, got nil (would masquerade as no-data)")
	}
	if *snapIdle.Pipeline.WaitingMsP50 <= 0 {
		t.Errorf("expected positive p50 wait, got %d", *snapIdle.Pipeline.WaitingMsP50)
	}
	if *snapIdle.Pipeline.WaitingMsP95 < *snapIdle.Pipeline.WaitingMsP50 {
		t.Errorf("expected p95 >= p50, got p95=%d p50=%d",
			*snapIdle.Pipeline.WaitingMsP95, *snapIdle.Pipeline.WaitingMsP50)
	}
	if snapIdle.SourceVersion <= snapMid.SourceVersion {
		t.Errorf("sourceVersion must be monotonic: mid=%d idle=%d",
			snapMid.SourceVersion, snapIdle.SourceVersion)
	}
}

func TestPipelineQueueStats_DegradedOnDisabledGovernor(t *testing.T) {
	p := NewPipeline(Deps{
		RouteFunc: func(ctx context.Context, qr *QueuedRequest) ([]CredentialRef, error) {
			return []CredentialRef{{CredentialID: 9, ConcurrencyMode: ModeDisabled}}, nil
		},
		ModelResolveFunc: func(ctx context.Context, req string, tried []string) (string, []string, error) {
			return "m", nil, nil
		},
		ForwardFunc: func(ctx context.Context, qr *QueuedRequest, cred CredentialRef) ForwardOutcome {
			return ForwardOutcome{}
		},
	})
	p.Start()
	defer p.Stop()

	c := newQueueCollectorForPipeline(p)
	// Route one request so the credential forwarder (and its noop governor)
	// actually exists.
	submitAndWait(t, p, 1)
	snap := c.Snapshot()
	if snap.Pipeline == nil {
		t.Fatal("expected pipeline stats while enabled")
	}
	if !snap.Pipeline.Degraded {
		t.Error("expected degraded=true once a disabled (noop) governor exists")
	}
}

func TestPipelineQueueStats_DegradedOnOverflowBetweenTicks(t *testing.T) {
	p := NewPipeline(Deps{
		RouteFunc:        func(ctx context.Context, qr *QueuedRequest) ([]CredentialRef, error) { return nil, nil },
		ModelResolveFunc: func(ctx context.Context, req string, tried []string) (string, []string, error) { return "m", nil, nil },
		ForwardFunc: func(ctx context.Context, qr *QueuedRequest, cred CredentialRef) ForwardOutcome {
			return ForwardOutcome{}
		},
	})
	p.Start()
	defer p.Stop()

	c := newQueueCollectorForPipeline(p)
	// Baseline tick latches the current overflow counter.
	snap1 := c.Snapshot()
	if snap1.Pipeline == nil || snap1.Pipeline.Degraded {
		t.Fatalf("precondition failed: baseline degraded should be clean, got %+v", snap1.Pipeline)
	}

	// Publish overflow through the queue observation seam.
	c.projection.ObserveQueue(QueueObservation{Kind: QueueOverflow, OverflowReason: "model_queue_full"})

	snap2 := c.Snapshot()
	if snap2.Pipeline == nil {
		t.Fatal("expected pipeline stats while enabled")
	}
	if !snap2.Pipeline.Degraded {
		t.Error("expected degraded=true on the tick following an overflow")
	}

	// The degradation window is shared by all readers; a second reader must see it.
	snap3 := c.Snapshot()
	if snap3.Pipeline == nil {
		t.Fatal("expected pipeline stats while enabled")
	}
	if !snap3.Pipeline.Degraded {
		t.Error("expected all readers to observe the active degradation window")
	}
}

func TestPipelineQueueStats_NoWaitingSamplesOmitPercentiles(t *testing.T) {
	// Fresh projection with zero completions: waiting percentiles must be
	// absent (nil), not 0 — the ring window simply has no sample.
	projection := NewQueueProjection()
	view := projection.Snapshot()
	if view.Pipeline == nil {
		t.Fatal("expected stats from a wired projection")
	}
	stats := view.Pipeline
	if stats.WaitingMsP50 != nil || stats.WaitingMsP95 != nil {
		t.Errorf("expected nil percentiles on an empty window, got p50=%v p95=%v",
			stats.WaitingMsP50, stats.WaitingMsP95)
	}
	if stats.Depth != 0 || stats.InFlight != 0 {
		t.Errorf("expected real-zero depth/inFlight on an idle projection, got depth=%d inFlight=%d",
			stats.Depth, stats.InFlight)
	}

	raw, err := json.Marshal(stats)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["waitingMsP50"]; ok {
		t.Errorf("waitingMsP50 must be absent when no sample exists, got %s", raw)
	}
	if _, ok := m["waitingMsP95"]; ok {
		t.Errorf("waitingMsP95 must be absent when no sample exists, got %s", raw)
	}
	if _, ok := m["depth"]; !ok {
		t.Errorf("depth must be present (real zero) on a wired pipeline, got %s", raw)
	}
}

func TestNearestRank(t *testing.T) {
	// 1..100: p50 = 50, p95 = 95 under nearest-rank.
	sorted := make([]int, 100)
	for i := range sorted {
		sorted[i] = i + 1
	}
	if got := nearestRank(sorted, 50); got != 50 {
		t.Errorf("p50 = %d, want 50", got)
	}
	if got := nearestRank(sorted, 95); got != 95 {
		t.Errorf("p95 = %d, want 95", got)
	}
	// Small windows: n=1 → both percentiles are the single sample;
	// n=2 → p50 = lower, p95 = upper.
	one := []int{42}
	if got := nearestRank(one, 50); got != 42 {
		t.Errorf("p50(n=1) = %d, want 42", got)
	}
	two := []int{10, 20}
	if got := nearestRank(two, 50); got != 10 {
		t.Errorf("p50(n=2) = %d, want 10", got)
	}
	if got := nearestRank(two, 95); got != 20 {
		t.Errorf("p95(n=2) = %d, want 20", got)
	}
}
