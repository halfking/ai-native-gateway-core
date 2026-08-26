package dispatch

import (
	"context"
	"errors"
	"testing"
	"time"
)

// ── v6 G-Ⅰ: adaptive executor count ─────────────────────────────────────

func TestAdaptiveWorkerCountBounds(t *testing.T) {
	n := AdaptiveWorkerCount()
	if n < adaptiveWorkerMin || n > adaptiveWorkerMax {
		t.Fatalf("AdaptiveWorkerCount out of bounds: %d", n)
	}
	cfg := DefaultConfig()
	if cfg.DispatcherWorkers != n || cfg.FailoverWorkers != n {
		t.Fatalf("DefaultConfig workers mismatch: %d/%d want %d", cfg.DispatcherWorkers, cfg.FailoverWorkers, n)
	}
	if cfg.DimensionTTLSeconds != DefaultDimensionTTLSeconds || cfg.DimensionCapacity != DefaultDimensionCapacity {
		t.Fatalf("dimension defaults not wired: %+v", cfg)
	}
}

// ── v6 G-Ⅱ: scheduled requests (定时请求) ───────────────────────────────

// TestScheduledRequestParksUntilDue verifies the executor pickup rule: a
// future DueAt parks the request (no forward before due) and executes at/after
// the due time.
func TestScheduledRequestParksUntilDue(t *testing.T) {
	forwardedAt := make(chan time.Time, 4)
	f := &fakeDeps{
		refsByModel: map[string][]CredentialRef{"gpt4": {cred(1, ModeConcurrency, 5)}},
		forwardFn: func(ctx context.Context, qr *QueuedRequest, c CredentialRef) ForwardOutcome {
			forwardedAt <- time.Now()
			return ForwardOutcome{}
		},
		forwardCalls: map[int]int{},
	}
	p := f.pipeline()
	p.Start()
	defer p.Stop()

	due := time.Now().Add(300 * time.Millisecond)
	qr := NewQueuedRequest("sched-1", "t", "gpt4", context.Background(), "payload")
	qr.DueAt = due
	notices := collectNotices(qr)

	res, err := p.Submit(context.Background(), qr)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res == nil {
		t.Fatalf("nil result")
	}
	// The acceptance notice must have fired at park time (before due).
	n := readNotice(t, notices, time.Second)
	if n.Kind != NoticeKindScheduled || n.RetryAt.IsZero() {
		t.Fatalf("expected scheduled accept notice, got %+v", n)
	}
	if qr.LastFailover.NextAction != NextActionScheduledWait {
		t.Fatalf("LastFailover.NextAction = %q, want scheduled_wait", qr.LastFailover.NextAction)
	}
	// Execution happens at/after due.
	select {
	case at := <-forwardedAt:
		if at.Before(due.Add(-50 * time.Millisecond)) {
			t.Fatalf("executed before due: %v < %v", at, due)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("request never executed after due time")
	}
	// Due notice follows.
	n = readNotice(t, notices, 2*time.Second)
	if n.Kind != NoticeKindScheduled {
		t.Fatalf("expected scheduled due notice, got %+v", n)
	}
}

// TestScheduledRequestRejectsFarFuture verifies the maxScheduleAhead guard.
func TestScheduledRequestRejectsFarFuture(t *testing.T) {
	f := &fakeDeps{
		refsByModel:  map[string][]CredentialRef{"gpt4": {cred(1, ModeConcurrency, 5)}},
		forwardCalls: map[int]int{},
	}
	p := f.pipeline()
	p.Start()
	defer p.Stop()

	qr := NewQueuedRequest("sched-far", "t", "gpt4", context.Background(), "payload")
	qr.DueAt = time.Now().Add(maxScheduleAhead + time.Hour)
	_, err := p.Submit(context.Background(), qr)
	if !errors.Is(err, ErrScheduleTooFar) {
		t.Fatalf("want ErrScheduleTooFar, got %v", err)
	}
}

// TestScheduledShutdownCompletesParked verifies Stop completes parked
// scheduled requests so Submit callers cannot block forever.
func TestScheduledShutdownCompletesParked(t *testing.T) {
	f := &fakeDeps{
		refsByModel:  map[string][]CredentialRef{"gpt4": {cred(1, ModeConcurrency, 5)}},
		forwardCalls: map[int]int{},
	}
	p := f.pipeline()
	p.Start()

	qr := NewQueuedRequest("sched-stop", "t", "gpt4", context.Background(), "payload")
	qr.DueAt = time.Now().Add(time.Hour)
	done := make(chan error, 1)
	go func() {
		_, err := p.Submit(context.Background(), qr)
		done <- err
	}()
	// Wait for the request to park in the due heap.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if p.dueScheduler != nil && p.dueScheduler.Len() == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if p.dueScheduler == nil || p.dueScheduler.Len() != 1 {
		t.Fatalf("request never parked")
	}
	p.Stop()
	select {
	case err := <-done:
		if !IsShutdown(err) {
			t.Fatalf("want shutdown error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Submit caller blocked through shutdown")
	}
}

// ── v6 G-Ⅲ: dispatch notices (think 通道) + G-Ⅴ 回队打标 ────────────────

func collectNotices(qr *QueuedRequest) chan DispatchNotice {
	out := make(chan DispatchNotice, 64)
	qr.OnDispatchNotice = func(n DispatchNotice) { out <- n }
	return out
}

// readNotice reads one notice or fails the test after timeout (never blocks
// the test forever).
func readNotice(t *testing.T, ch chan DispatchNotice, timeout time.Duration) DispatchNotice {
	t.Helper()
	select {
	case n := <-ch:
		return n
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for dispatch notice")
		return DispatchNotice{}
	}
}

// TestNoticeRetryAndNodeSwitch: a failing first credential (non-fatal) under
// RetryPerCredential=0 triggers an immediate node switch notice.
func TestNoticeRetryAndNodeSwitch(t *testing.T) {
	f := &fakeDeps{
		refsByModel: map[string][]CredentialRef{"gpt4": {
			cred(1, ModeConcurrency, 5),
			cred(2, ModeConcurrency, 5),
		}},
		forwardFn: func(ctx context.Context, qr *QueuedRequest, c CredentialRef) ForwardOutcome {
			if c.CredentialID == 1 {
				return ForwardOutcome{Err: errors.New("upstream 500"), ErrorKind: "upstream_error"}
			}
			return ForwardOutcome{}
		},
		forwardCalls: map[int]int{},
	}
	p := f.pipeline()
	p.Start()
	defer p.Stop()

	qr := NewQueuedRequest("n1", "t", "gpt4", context.Background(), "payload")
	qr.RetryPerCredential = 0 // switch immediately
	notices := collectNotices(qr)

	if _, err := p.Submit(context.Background(), qr); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	n := readNotice(t, notices, 2*time.Second)
	if n.Kind != NoticeKindNodeSwitch {
		t.Fatalf("expected node_switch notice, got %+v", n)
	}
	if n.FromCredentialID != 1 || n.ToCredentialID != 2 {
		t.Fatalf("notice cred transition wrong: %+v", n)
	}
	if n.ErrorKind == "" {
		t.Fatalf("notice missing error kind: %+v", n)
	}
	// 回队打标 (G-Ⅴ): marker must reflect the failed node and switch intent.
	if qr.LastFailover.NextAction != NextActionSwitchCred {
		t.Fatalf("LastFailover.NextAction = %q, want switch_cred", qr.LastFailover.NextAction)
	}
	if qr.LastFailover.CredentialID != 1 {
		t.Fatalf("LastFailover.CredentialID = %d, want 1", qr.LastFailover.CredentialID)
	}
	if qr.LastFailover.ErrorKind == "" {
		t.Fatalf("LastFailover.ErrorKind empty")
	}
}

// TestNoticeRetryScheduled: with the default retry budget and a scheduler,
// the same-credential retry fires a retry notice carrying retry_at.
func TestNoticeRetryScheduled(t *testing.T) {
	f := &fakeDeps{
		refsByModel: map[string][]CredentialRef{"gpt4": {cred(1, ModeConcurrency, 5)}},
		forwardFn: func(ctx context.Context, qr *QueuedRequest, c CredentialRef) ForwardOutcome {
			return ForwardOutcome{Err: errors.New("timeout"), ErrorKind: "timeout"}
		},
		forwardCalls: map[int]int{},
	}
	p := f.pipeline()
	sched := p.NewDefaultRetryScheduler()
	defer sched.Close()
	p.SetRetryScheduler(sched)
	p.Start()
	defer p.Stop()

	qr := NewQueuedRequest("n2", "t", "gpt4", context.Background(), "payload")
	notices := collectNotices(qr)
	go func() { _, _ = p.Submit(context.Background(), qr) }()

	n := readNotice(t, notices, 2*time.Second)
	if n.Kind != NoticeKindRetry {
		t.Fatalf("expected retry notice, got %+v", n)
	}
	if n.RetryAt.IsZero() {
		t.Fatalf("retry notice missing retry_at: %+v", n)
	}
	if qr.LastFailover.NextAction != NextActionRetrySameCred {
		t.Fatalf("LastFailover.NextAction = %q, want retry_same_cred", qr.LastFailover.NextAction)
	}
	p.complete(qr, ForwardOutcome{Err: errCapacitySaturated}) // unblock Submit
}

// TestNoticeCapacityWait drives scheduleCapacityRetry directly (the
// "所有节点并发/限流已满" queue-wait path) and asserts the queued notice + marker.
func TestNoticeCapacityWait(t *testing.T) {
	p := NewPipeline(Deps{
		RouteFunc: func(ctx context.Context, qr *QueuedRequest) ([]CredentialRef, error) { return nil, nil },
		ModelResolveFunc: func(ctx context.Context, requested string, tried []string) (string, []string, error) {
			return requested, nil, nil
		},
		ForwardFunc: func(ctx context.Context, qr *QueuedRequest, cred CredentialRef) ForwardOutcome {
			return ForwardOutcome{}
		},
	})
	sched := p.NewDefaultRetryScheduler()
	defer sched.Close()
	p.SetRetryScheduler(sched)
	p.Start()
	defer p.Stop()

	qr := NewQueuedRequest("cq1", "t", "gpt4", context.Background(), "payload")
	qr.ResolvedModel = "gpt4"
	notices := collectNotices(qr)

	p.scheduleCapacityRetry(qr)
	n := readNotice(t, notices, 2*time.Second)
	if n.Kind != NoticeKindQueued {
		t.Fatalf("expected queued notice, got %+v", n)
	}
	if n.RetryAt.IsZero() || n.WaitHint == "" {
		t.Fatalf("queued notice missing wait info: %+v", n)
	}
	if qr.LastFailover.NextAction != NextActionCapacityWait {
		t.Fatalf("LastFailover.NextAction = %q, want capacity_wait", qr.LastFailover.NextAction)
	}
	if qr.CapacityRetryCount != 1 {
		t.Fatalf("CapacityRetryCount = %d, want 1", qr.CapacityRetryCount)
	}
	p.complete(qr, ForwardOutcome{Err: errCapacitySaturated}) // unbook the parked request
}

// TestNoticeModelSwitch: exhausting the only credential under gpt4 (fatal,
// no retry) escalates to model-change with a model_switch notice.
func TestNoticeModelSwitch(t *testing.T) {
	f := &fakeDeps{
		refsByModel: map[string][]CredentialRef{
			"gpt4": {cred(1, ModeConcurrency, 5)},
			"m2":   {cred(2, ModeConcurrency, 5)},
		},
		altsByModel: map[string][]string{"gpt4": {"m2"}},
		allowChange: true,
		forwardFn: func(ctx context.Context, qr *QueuedRequest, c CredentialRef) ForwardOutcome {
			if c.CredentialID == 1 {
				return ForwardOutcome{Err: errors.New("quota dead"), ErrorKind: "quota", FatalCredential: true}
			}
			return ForwardOutcome{}
		},
		forwardCalls: map[int]int{},
	}
	p := f.pipeline()
	p.Start()
	defer p.Stop()

	qr := NewQueuedRequest("ms1", "t", "gpt4", context.Background(), "payload")
	qr.RetryPerCredential = 0
	qr.AllowModelChange = true
	notices := collectNotices(qr)

	if _, err := p.Submit(context.Background(), qr); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	n := readNotice(t, notices, 2*time.Second)
	if n.Kind != NoticeKindModelSwitch {
		t.Fatalf("expected model_switch notice, got %+v", n)
	}
	if n.FromModel != "gpt4" || n.ToModel != "m2" {
		t.Fatalf("model transition wrong: %+v", n)
	}
	if qr.LastFailover.NextAction != NextActionSwitchModel {
		t.Fatalf("LastFailover.NextAction = %q, want switch_model", qr.LastFailover.NextAction)
	}
}

// ── v6 G-Ⅳ: DimensionIndex (分维队列) ────────────────────────────────────

func TestDimensionIndexLifecycle(t *testing.T) {
	ix := NewDimensionIndex(DimensionIndexConfig{TTL: 50 * time.Millisecond, PerKeyCapacity: 4, MaxKeys: 16})
	now := time.Now()

	qr := NewQueuedRequest("d1", "tenant", "gpt4", context.Background(), "payload")
	ix.Track(qr, now)

	snap := ix.Snapshot(DimensionModel, "gpt4", 10)
	if snap.Total != 1 || len(snap.Entries) != 1 {
		t.Fatalf("model ring wrong: %+v", snap)
	}
	if snap.Entries[0].State != DimensionStatePending {
		t.Fatalf("entry state = %q, want pending", snap.Entries[0].State)
	}

	cred := CredentialRef{CredentialID: 7, ProviderID: 3, Vendor: "openai"}
	ix.MarkNode(qr, cred, now)
	if got := ix.Snapshot(DimensionCredential, "7", 10).Total; got != 1 {
		t.Fatalf("credential ring missing entry: %d", got)
	}
	if got := ix.Snapshot(DimensionProvider, "3", 10).Total; got != 1 {
		t.Fatalf("provider ring missing entry: %d", got)
	}

	ix.Complete(qr, ForwardOutcome{ErrorKind: "timeout", Err: errors.New("x")}, now)
	for _, check := range []struct {
		kind DimensionKind
		id   string
	}{
		{DimensionModel, "gpt4"},
		{DimensionCredential, "7"},
		{DimensionProvider, "3"},
	} {
		snap := ix.Snapshot(check.kind, check.id, 10)
		if snap.Total == 0 {
			t.Fatalf("%s ring empty after complete", check.kind)
		}
		if snap.Entries[0].State != DimensionStateCompleted {
			t.Fatalf("%s entry state = %q, want completed (完成后不移除)", check.kind, snap.Entries[0].State)
		}
		if snap.Entries[0].ErrorKind != "timeout" {
			t.Fatalf("error kind not stamped: %+v", snap.Entries[0])
		}
	}

	// TTL eviction: entries expire after the TTL window.
	time.Sleep(80 * time.Millisecond)
	if got := ix.Sweep(time.Now()); got < 3 {
		t.Fatalf("TTL sweep evicted %d entries, want >= 3", got)
	}
	if got := ix.Snapshot(DimensionCredential, "7", 10).Total; got != 0 {
		t.Fatalf("credential ring not empty after TTL: %d", got)
	}
}

func TestDimensionIndexCapacityEviction(t *testing.T) {
	ix := NewDimensionIndex(DimensionIndexConfig{TTL: time.Hour, PerKeyCapacity: 2, MaxKeys: 16})
	for i := 0; i < 5; i++ {
		qr := NewQueuedRequest("cap-"+string(rune('a'+i)), "t", "m", context.Background(), nil)
		ix.Track(qr, time.Now())
	}
	if got := ix.Snapshot(DimensionModel, "m", 10).Total; got != 2 {
		t.Fatalf("capacity eviction failed: %d entries, want 2", got)
	}
}

func TestDimensionIndexDisabled(t *testing.T) {
	ix := NewDimensionIndex(DimensionIndexConfig{PerKeyCapacity: -1})
	qr := NewQueuedRequest("x", "t", "m", context.Background(), nil)
	ix.Track(qr, time.Now())
	ix.MarkNode(qr, CredentialRef{CredentialID: 1}, time.Now())
	ix.Complete(qr, ForwardOutcome{}, time.Now())
	if _, ev, reqs := ix.Stats(); ev != 0 || reqs != 0 {
		t.Fatalf("disabled index tracked work: %v %v", ev, reqs)
	}
}

// TestPipelineDimensionIndexWiring: end-to-end through Submit/complete.
func TestPipelineDimensionIndexWiring(t *testing.T) {
	f := &fakeDeps{
		refsByModel: map[string][]CredentialRef{"gpt4": {cred(9, ModeConcurrency, 5)}},
		forwardFn: func(ctx context.Context, qr *QueuedRequest, c CredentialRef) ForwardOutcome {
			return ForwardOutcome{}
		},
		forwardCalls: map[int]int{},
	}
	p := f.pipeline()
	p.Start()
	defer p.Stop()

	qr := NewQueuedRequest("dim-1", "t", "gpt4", context.Background(), "payload")
	if _, err := p.Submit(context.Background(), qr); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	ix := p.DimensionIndex()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snap := ix.Snapshot(DimensionCredential, "9", 10)
		if snap.Total == 1 && len(snap.Entries) > 0 && snap.Entries[0].State == DimensionStateCompleted {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("credential dimension never reached completed state")
}
