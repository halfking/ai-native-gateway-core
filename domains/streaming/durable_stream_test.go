package streaming

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/durable"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// SR-12 streaming durable foreground binding (doc 18 §11.3 前台模式):
// lease renewal, rank-deduped write-ahead checkpoints and the terminal
// settlement matrix.

// fakeForegroundStore extends the handler fake with the foreground surface.
type fakeForegroundStore struct {
	fakeDurableHandlerStore
	renews         []durableRenewCall
	checks         []durable.CheckpointParams
	terminals      []durable.TerminalCommit
	renewErr       error
	checkErr       error
	intents        []durable.TerminalCommit
	intentErr      error
	intentFailures int
	intentCalls    int
	claims         []*durable.ClaimedSettlement
	finalizeErr    error
	retried        []durable.ClaimedSettlement
}

type durableRenewCall struct {
	taskID string
	owner  string
	token  int64
}

func (f *fakeForegroundStore) RenewLease(_ context.Context, taskID, owner string, token int64, _ time.Time) error {
	if f.renewErr != nil {
		return f.renewErr
	}
	f.renews = append(f.renews, durableRenewCall{taskID, owner, token})
	return nil
}

func (f *fakeForegroundStore) CheckpointCommitState(_ context.Context, p durable.CheckpointParams) error {
	if f.checkErr != nil {
		return f.checkErr
	}
	f.checks = append(f.checks, p)
	return nil
}

func (f *fakeForegroundStore) CommitTerminal(_ context.Context, c durable.TerminalCommit) (*durable.TerminalProjection, error) {
	f.terminals = append(f.terminals, c)
	return &durable.TerminalProjection{Committed: true}, nil
}

func (f *fakeForegroundStore) PersistSettlementIntent(_ context.Context, c durable.TerminalCommit) error {
	f.intentCalls++
	if f.intentFailures > 0 {
		f.intentFailures--
		return errors.New("temporary settlement intent failure")
	}
	if f.intentErr != nil {
		return f.intentErr
	}
	f.intents = append(f.intents, c)
	return nil
}

func (f *fakeForegroundStore) ClaimSettlementIntent(_ context.Context, taskID, owner string, lease time.Duration, now time.Time) (*durable.ClaimedSettlement, error) {
	for _, claim := range f.claims {
		if claim.TaskID == taskID {
			claim.ClaimOwner, claim.ClaimUntil = owner, now.Add(lease)
			return claim, nil
		}
	}
	return &durable.ClaimedSettlement{SettlementIntent: durable.SettlementIntent{TaskID: taskID, Attempts: 1}, ClaimOwner: owner, ClaimUntil: now.Add(lease), ClaimFencingToken: 1}, nil
}

func (f *fakeForegroundStore) ClaimSettlementIntents(_ context.Context, _ string, _ time.Duration, _ int, _ time.Time) ([]*durable.ClaimedSettlement, error) {
	if f.claims != nil {
		return f.claims, nil
	}
	if len(f.intents) == 0 {
		return nil, nil
	}
	c := f.intents[len(f.intents)-1]
	return []*durable.ClaimedSettlement{{SettlementIntent: durable.SettlementIntent{TaskID: c.Task.ID}}}, nil
}

func (f *fakeForegroundStore) FinalizeSettlement(_ context.Context, c durable.ClaimedSettlement) (*durable.TerminalProjection, error) {
	if f.finalizeErr != nil {
		return nil, f.finalizeErr
	}
	for i := len(f.intents) - 1; i >= 0; i-- {
		if f.intents[i].Task != nil && f.intents[i].Task.ID == c.TaskID {
			f.terminals = append(f.terminals, f.intents[i])
			break
		}
	}
	return &durable.TerminalProjection{Committed: true}, nil
}

func (f *fakeForegroundStore) RetrySettlementIntent(_ context.Context, c durable.ClaimedSettlement, _ time.Time, _ error) error {
	f.retried = append(f.retried, c)
	return nil
}

func newStreamBinding(store *fakeForegroundStore) *DurableStreamBinding {
	task := &durable.Task{ID: "task-7", TenantID: "tenant-1", RequestID: "req-9",
		SessionID: "sess-1", RequestHash: "hash-1", LeaseOwner: "gw-front", FencingToken: 1}
	return newDurableStreamBinding(store, task, 40*time.Millisecond)
}

func TestDurableStreamBindingCheckpointRankDedupe(t *testing.T) {
	store := &fakeForegroundStore{}
	b := newStreamBinding(store)

	// Attempt 1 advances to metadata.
	if err := b.Checkpoint(CommitStateMetadata); err != nil {
		t.Fatalf("metadata checkpoint: %v", err)
	}
	// Attempt 2 (fresh gate, state reset) advances to metadata again —
	// the DB rank guard would read this as a regression (0 rows →
	// ErrLeaseLost); the binding must dedupe instead.
	if err := b.Checkpoint(CommitStateMetadata); err != nil {
		t.Fatalf("duplicate metadata checkpoint must dedupe, got %v", err)
	}
	if len(store.checks) != 1 || store.checks[0].State != durable.CommitStateMetadata {
		t.Fatalf("checks = %+v, want one metadata", store.checks)
	}
	// Content always writes (rank advance).
	if err := b.Checkpoint(CommitStateContent); err != nil {
		t.Fatalf("content checkpoint: %v", err)
	}
	if len(store.checks) != 2 {
		t.Fatalf("checks = %d, want 2", len(store.checks))
	}
	if store.checks[1].LeaseOwner != "gw-front" || store.checks[1].FencingToken != 1 {
		t.Fatalf("checkpoint must carry owner+token: %+v", store.checks[1])
	}
	if !b.ContentCommitted() {
		t.Fatal("ContentCommitted must reflect the content checkpoint")
	}
}

func TestDurableStreamBindingCheckpointErrorPropagates(t *testing.T) {
	store := &fakeForegroundStore{checkErr: errors.New("db down")}
	b := newStreamBinding(store)
	if err := b.Checkpoint(CommitStateContent); err == nil {
		t.Fatal("checkpoint error must propagate (gate will block the network write)")
	}
}

func TestDurableStreamBindingRenewalLoopStops(t *testing.T) {
	store := &fakeForegroundStore{}
	b := newStreamBinding(store) // 40ms lease → ~20ms renew interval
	b.Start()
	time.Sleep(120 * time.Millisecond)
	b.Stop()
	got := len(store.renews)
	if got == 0 {
		t.Fatal("renewal loop never renewed")
	}
	time.Sleep(80 * time.Millisecond)
	if after := len(store.renews); after > got+1 {
		t.Fatalf("renewals continued after Stop: %d → %d", got, after)
	}
	// Stop is idempotent.
	b.Stop()
}

// TestDurableStreamBindingStopWaitsForRenewalExit proves that Stop returns
// only after the renewal goroutine has actually exited, never on a premature
// timeout. With the default 15s lease the renew interval is 7.5s; a fixed
// grace shorter than that (the old 3s) would spuriously time out on nearly
// every stop. Here Stop is observed to return promptly once the goroutine
// exits and must not block for the full grace.
func TestDurableStreamBindingStopWaitsForRenewalExit(t *testing.T) {
	store := &fakeForegroundStore{}
	b := newDurableStreamBinding(store,
		&durable.Task{ID: "task-7", TenantID: "tenant-1", RequestID: "req-9",
			SessionID: "sess-1", RequestHash: "hash-1", LeaseOwner: "gw-front", FencingToken: 1},
		200*time.Millisecond)

	b.mu.Lock()
	stopCh := make(chan struct{})
	doneCh := make(chan struct{})
	b.stopRenew, b.renewDone = stopCh, doneCh
	b.mu.Unlock()
	// Simulate the renewal loop: exit only when Stop closes stopCh.
	go func() {
		defer close(doneCh)
		<-stopCh
	}()

	stopStart := time.Now()
	b.Stop()
	elapsed := time.Since(stopStart)
	// If the grace fired we would see ~6s; a correct wait returns in well
	// under a second once the goroutine exits.
	if elapsed > 2*time.Second {
		t.Fatalf("Stop blocked for %v; renewal goroutine did not exit promptly (grace fired?)", elapsed)
	}
	select {
	case <-doneCh:
	default:
		t.Fatal("renewal goroutine still running after Stop returned")
	}
}

func TestDurableStreamBindingRenewalLeaseLossCountsBothMetricFamilies(t *testing.T) {
	beforeSurvival := gatherMetricValue(t, "gateway_survival_lease_conflicts_total")
	beforeDurable := gatherMetricValue(t, "durable_lease_lost_total")
	store := &fakeForegroundStore{renewErr: durable.ErrLeaseLost}
	b := newStreamBinding(store)
	b.Start()
	defer b.Stop()
	time.Sleep(80 * time.Millisecond)
	if got := gatherMetricValue(t, "gateway_survival_lease_conflicts_total"); got < beforeSurvival+1 {
		t.Fatalf("survival lease conflicts = %v, want >= %v", got, beforeSurvival+1)
	}
	if got := gatherMetricValue(t, "durable_lease_lost_total"); got < beforeDurable+1 {
		t.Fatalf("durable lease lost = %v, want >= %v", got, beforeDurable+1)
	}
}

func TestReleaseDurableBeforeSurvivalHandsTaskToWorker(t *testing.T) {
	store := &fakeForegroundStore{}
	b := newStreamBinding(store)

	releaseDurableBeforeSurvival(b, "candidate_resolution_failed")

	if len(store.resched) != 1 {
		t.Fatalf("reschedules = %d, want 1", len(store.resched))
	}
	got := store.resched[0]
	if got.TaskID != b.task.ID || got.LeaseOwner != b.task.LeaseOwner || got.FencingToken != b.task.FencingToken {
		t.Fatalf("reschedule fence = %+v, want task fence %+v", got, b.task)
	}
	if got.Reason != "candidate_resolution_failed" {
		t.Fatalf("reschedule reason = %q, want candidate_resolution_failed", got.Reason)
	}
	if len(store.terminals) != 0 {
		t.Fatalf("pre-survival release must not terminalize task, got %+v", store.terminals)
	}
}

func TestSettleDurableStreamLeaseLossCountsBothMetricFamilies(t *testing.T) {
	beforeSurvival := gatherMetricValue(t, "gateway_survival_lease_conflicts_total")
	beforeDurable := gatherMetricValue(t, "durable_lease_lost_total")
	store := &fakeForegroundStore{finalizeErr: durable.ErrLeaseLost}
	b := newStreamBinding(store)
	settleDurableStream(context.Background(), b,
		SurvivalResult{Succeed: true, Decision: TaskDecision{Action: TaskActionSucceed, Reason: "success"}},
		[]byte("data: hi\\n\\n"), "text/event-stream", false)
	if got := gatherMetricValue(t, "gateway_survival_lease_conflicts_total"); got < beforeSurvival+1 {
		t.Fatalf("survival lease conflicts = %v, want >= %v", got, beforeSurvival+1)
	}
	if got := gatherMetricValue(t, "durable_lease_lost_total"); got < beforeDurable+1 {
		t.Fatalf("durable lease lost = %v, want >= %v", got, beforeDurable+1)
	}
}

func TestSettleDurableStreamMatrix(t *testing.T) {
	meta := "event: message_start\ndata: {\"message\":{}}\n\n"
	_ = meta

	t.Run("success with captured body completes", func(t *testing.T) {
		store := &fakeForegroundStore{}
		b := newStreamBinding(store)
		settleDurableStream(context.Background(), b,
			SurvivalResult{Succeed: true, Decision: TaskDecision{Action: TaskActionSucceed, Reason: "success"}},
			[]byte("data: hi\n\n"), "text/event-stream", false)
		if len(store.terminals) != 1 || store.terminals[0].Outcome != durable.StatusCompleted {
			t.Fatalf("terminals = %+v, want completed", store.terminals)
		}
		if string(store.terminals[0].Body) != "data: hi\n\n" || store.terminals[0].ContentType != "text/event-stream" {
			t.Fatalf("completed terminal body/ct wrong: %+v", store.terminals[0])
		}
	})

	t.Run("success without body fails terminal honestly", func(t *testing.T) {
		store := &fakeForegroundStore{}
		b := newStreamBinding(store)
		settleDurableStream(context.Background(), b,
			SurvivalResult{Succeed: true, Decision: TaskDecision{Action: TaskActionSucceed, Reason: "success"}},
			nil, "", false)
		if len(store.terminals) != 1 || store.terminals[0].Outcome != durable.StatusFailed ||
			store.terminals[0].ReasonCode != "durable_result_unavailable" {
			t.Fatalf("terminals = %+v, want failed/durable_result_unavailable", store.terminals)
		}
	})

	t.Run("client disconnect before content releases to worker", func(t *testing.T) {
		store := &fakeForegroundStore{}
		b := newStreamBinding(store)
		settleDurableStream(context.Background(), b,
			SurvivalResult{Decision: TaskDecision{Action: TaskActionFailClosed, Reason: "client_disconnected"}},
			nil, "", true)
		if len(store.resched) != 1 {
			t.Fatalf("reschedules = %d, want 1 (worker takeover)", len(store.resched))
		}
		if store.resched[0].Reason != "client_disconnected_pre_content" {
			t.Fatalf("release reason = %q", store.resched[0].Reason)
		}
		if len(store.terminals) != 0 {
			t.Fatalf("no terminal expected, got %+v", store.terminals)
		}
	})

	t.Run("client disconnect after content is abandoned to safety reaper", func(t *testing.T) {
		store := &fakeForegroundStore{}
		b := newStreamBinding(store)
		if err := b.Checkpoint(CommitStateContent); err != nil {
			t.Fatalf("content checkpoint: %v", err)
		}
		settleDurableStream(context.Background(), b,
			SurvivalResult{Decision: TaskDecision{Action: TaskActionFailClosed, Reason: "client_disconnected"}},
			nil, "", true)
		if len(store.terminals) != 0 || len(store.resched) != 0 {
			t.Fatalf("post-content disconnect must write nothing (reaper owns it): %+v %+v",
				store.terminals, store.resched)
		}
	})

	t.Run("foreground failure terminates the task", func(t *testing.T) {
		store := &fakeForegroundStore{}
		b := newStreamBinding(store)
		settleDurableStream(context.Background(), b,
			SurvivalResult{Decision: TaskDecision{Action: TaskActionFailTerminal, Reason: "terminal_candidate"},
				FinalAttempt: &AttemptResult{CandidateOutcomes: []CandidateOutcome{{Kind: errorsx.KindModelNotFound}}}},
			nil, "", false)
		if len(store.terminals) != 1 || store.terminals[0].Outcome != durable.StatusFailed ||
			store.terminals[0].ReasonCode != "terminal_candidate" {
			t.Fatalf("terminals = %+v, want failed/terminal_candidate", store.terminals)
		}
	})
}

func TestSettleDurableStreamRetriesTransientTerminalFailure(t *testing.T) {
	store := &fakeForegroundStore{intentFailures: 2}
	b := newStreamBinding(store)
	settleDurableStream(context.Background(), b,
		SurvivalResult{Succeed: true, Decision: TaskDecision{Action: TaskActionSucceed, Reason: "success"}},
		[]byte("data: hi\n\n"), "text/event-stream", false)
	if len(store.terminals) != 1 {
		t.Fatalf("terminals = %d, want 1 after transient failures", len(store.terminals))
	}
	if store.intentCalls != 3 {
		t.Fatalf("settlement intent calls = %d, want 3", store.intentCalls)
	}
}

func TestSettleDurableStreamStopsOnLeaseLoss(t *testing.T) {
	store := &fakeForegroundStore{intentErr: durable.ErrLeaseLost}
	b := newStreamBinding(store)
	settleDurableStream(context.Background(), b,
		SurvivalResult{Succeed: true, Decision: TaskDecision{Action: TaskActionSucceed, Reason: "success"}},
		[]byte("data: hi\n\n"), "text/event-stream", false)
	if len(store.terminals) != 0 {
		t.Fatalf("lease loss must not finalize terminal, got %+v", store.terminals)
	}
	if store.intentCalls != 1 {
		t.Fatalf("lease loss settlement intent calls = %d, want 1", store.intentCalls)
	}
}

// frameWritingExecutor writes protocol frames through the attempt writer
// (the coordinator wraps it in a gate writer) and succeeds.
type frameWritingExecutor struct{ frames []string }

func (e *frameWritingExecutor) Execute(params *executors.ExecParams) (*executors.ExecuteResult, error) {
	for _, f := range e.frames {
		if _, err := params.W.Write([]byte(f)); err != nil {
			return nil, err
		}
	}
	return &executors.ExecuteResult{}, nil
}

// The coordinator must hand its Checkpoint seam to the attempt gate: the
// content frame triggers the write-ahead checkpoint before it reaches the
// wire (doc 18 §11.3 foreground mode).
func TestSurvivalCoordinatorPropagatesCheckpointSeam(t *testing.T) {
	exec := &frameWritingExecutor{frames: []string{
		"event: message_start\ndata: {\"message\":{}}\n\n",
		"event: content_block_delta\ndata: {\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n",
	}}
	f := &trackingFlusher{}
	var calls []CommitState
	c := &SurvivalCoordinator{
		Exec:     exec,
		Protocol: ProtocolAnthropic,
		Options:  SurvivalOptions{Deadline: time.Second},
		BeforeSemanticCommit: func(_ context.Context, s CommitState) error {
			calls = append(calls, s)
			return nil
		},
	}
	res := c.Run(context.Background(), NewSerializedStreamWriter(f), &executors.ExecParams{})
	if !res.Succeed {
		t.Fatalf("expected success, got %+v", res.Decision)
	}
	if len(calls) != 1 || calls[0] != CommitStateContent {
		t.Fatalf("checkpoint calls = %v, want [content]", calls)
	}
	if !strings.Contains(f.buf.String(), "content_block_delta") {
		t.Fatal("committed frame must reach the wire")
	}
}

// A failed write-ahead checkpoint must fail the attempt (禁写网络) and the
// coordinator fail-closes the task instead of retrying.
func TestSurvivalCoordinatorCheckpointFailureFailsClosed(t *testing.T) {
	exec := &frameWritingExecutor{frames: []string{
		"event: content_block_delta\ndata: {\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n",
	}}
	f := &trackingFlusher{}
	c := &SurvivalCoordinator{
		Exec:                 exec,
		Protocol:             ProtocolAnthropic,
		Options:              SurvivalOptions{Deadline: time.Second},
		BeforeSemanticCommit: func(context.Context, CommitState) error { return errors.New("checkpoint down") },
	}
	res := c.Run(context.Background(), NewSerializedStreamWriter(f), &executors.ExecParams{})
	if res.Succeed {
		t.Fatal("checkpoint failure must not succeed")
	}
	// The gate state advanced past content while the DB outcome is unknown,
	// so the aggregator fail-closes the task (resume_blocked: no transparent
	// retry) instead of looping.
	if res.Decision.Action != TaskActionResumeBlocked && res.Decision.Action != TaskActionFailClosed {
		t.Fatalf("decision = %+v, want resume_blocked or fail_closed", res.Decision)
	}
	if res.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1 (no retry after a failed checkpoint)", res.Attempts)
	}
	if strings.Contains(f.buf.String(), "content_block_delta") {
		t.Fatal("no semantic bytes may reach the wire after a failed checkpoint")
	}
}

func TestDurableWireCaptureDiscardsBodyOnWriteError(t *testing.T) {
	failed := newDurableWireCapture(errorResponseWriter{}, 64)
	if _, err := failed.Write([]byte("data: incomplete\n\n")); err == nil {
		t.Fatal("write error must propagate")
	}
	if body, _ := failed.result(); body != nil {
		t.Fatalf("failed write must not produce durable body, got %q", body)
	}
}

type errorResponseWriter struct{}

func (errorResponseWriter) Header() http.Header       { return make(http.Header) }
func (errorResponseWriter) Write([]byte) (int, error) { return 0, errors.New("client disconnected") }
func (errorResponseWriter) WriteHeader(int)           {}

func TestDurableWireCaptureTeesAndOverflows(t *testing.T) {
	inner := httptest.NewRecorder()
	cap := newDurableWireCapture(inner, 16)
	if _, err := cap.Write([]byte("data: hello\n\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	body, ct := cap.result()
	if string(body) != "data: hello\n\n" || ct != "text/event-stream" {
		t.Fatalf("capture = %q %q", body, ct)
	}
	if inner.Body.String() != "data: hello\n\n" {
		t.Fatalf("tee must pass bytes through, got %q", inner.Body.String())
	}

	over := newDurableWireCapture(httptest.NewRecorder(), 4)
	if _, err := over.Write([]byte("data: way too long frame\n\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if body, _ := over.result(); body != nil {
		t.Fatalf("overflow must report nil (never a truncated body), got %q", body)
	}
}
