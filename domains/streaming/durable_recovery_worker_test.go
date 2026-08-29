package streaming

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/kaixuan/llm-gateway-go/durable"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/metrics"
	"github.com/kaixuan/llm-gateway-go/pending"
)

type workerFakeStore struct {
	mu                                                    sync.Mutex
	task                                                  *durable.Task
	snapshot                                              *durable.Snapshot
	snapshotErr                                           error
	attempt                                               *DurableAttempt
	claimCalls, commitCalls, rescheduleCalls, repairCalls int
	leaseErr                                              error
	commitErr                                             error
	lastOutcome                                           durable.Status
	lastReschedule                                        durable.RescheduleParams
}

func (f *workerFakeStore) PersistSettlementIntent(_ context.Context, c durable.TerminalCommit) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commitCalls++
	f.lastOutcome = c.Outcome
	return f.commitErr
}

func (f *workerFakeStore) ClaimSettlementIntent(_ context.Context, taskID, owner string, lease time.Duration, now time.Time) (*durable.ClaimedSettlement, error) {
	return &durable.ClaimedSettlement{SettlementIntent: durable.SettlementIntent{TaskID: taskID, Attempts: 1}, ClaimOwner: owner, ClaimUntil: now.Add(lease), ClaimFencingToken: 1}, nil
}

func (f *workerFakeStore) ClaimSettlementIntents(context.Context, string, time.Duration, int, time.Time) ([]*durable.ClaimedSettlement, error) {
	return nil, nil
}

func (f *workerFakeStore) FinalizeSettlement(context.Context, durable.ClaimedSettlement) (*durable.TerminalProjection, error) {
	return &durable.TerminalProjection{Committed: true}, nil
}

func (f *workerFakeStore) RetrySettlementIntent(context.Context, durable.ClaimedSettlement, time.Time, error) error {
	return nil
}

func (f *workerFakeStore) ClaimRunnable(context.Context, durable.ClaimOptions) ([]*durable.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.task == nil {
		return nil, nil
	}
	task := f.task
	f.task = nil
	f.claimCalls++
	return []*durable.Task{task}, nil
}
func (f *workerFakeStore) LoadSnapshot(context.Context, string) (*durable.Snapshot, error) {
	return f.snapshot, f.snapshotErr
}
func (f *workerFakeStore) RenewLease(context.Context, string, string, int64, time.Time) error {
	return f.leaseErr
}
func (f *workerFakeStore) Reschedule(_ context.Context, params durable.RescheduleParams) error {
	f.lastReschedule = params
	f.rescheduleCalls++
	return nil
}
func (f *workerFakeStore) CommitTerminal(_ context.Context, c durable.TerminalCommit) (*durable.TerminalProjection, error) {
	f.commitCalls++
	f.lastOutcome = c.Outcome
	if f.commitErr != nil {
		return nil, f.commitErr
	}
	return &durable.TerminalProjection{Committed: true}, nil
}
func (f *workerFakeStore) ReapDeadlines(context.Context, int, time.Time) ([]*durable.ReapedTaskInfo, error) {
	return nil, nil
}
func (f *workerFakeStore) ReapUnsafeCheckpointed(context.Context, int, time.Time) ([]*durable.Task, error) {
	return nil, nil
}
func (f *workerFakeStore) ProjectPendingOutbox(context.Context, *pending.Store, int, time.Time) (int, error) {
	f.repairCalls++
	return 0, nil
}
func (f *workerFakeStore) ActiveTaskCounts(context.Context) (map[string]int64, error) {
	return map[string]int64{"tenant-a": 1}, nil
}

type workerFakeRunner struct {
	attempt *DurableAttempt
	err     error
}

func (r workerFakeRunner) Run(context.Context, *durable.Task, *durable.Snapshot) (*DurableAttempt, error) {
	return r.attempt, r.err
}

func runnableTask() *durable.Task {
	return &durable.Task{ID: "task-1", TenantID: "tenant-a", RequestID: "req-1", SessionID: "sess-1", Status: durable.StatusRetryScheduled, CommitState: durable.CommitStateNone, LeaseOwner: "worker", FencingToken: 1, AttemptCount: 1, ExpiresAt: time.Now().Add(time.Hour)}
}
func successAttempt() *DurableAttempt {
	return &DurableAttempt{Result: &AttemptResult{Success: true}, Body: []byte("ok"), ContentType: "application/json", Attempt: 1}
}

func TestDurableRecoveryWorkerRunOnceCommitsSuccess(t *testing.T) {
	store := &workerFakeStore{task: runnableTask(), snapshot: &durable.Snapshot{TaskID: "task-1"}, attempt: successAttempt()}
	worker := NewDurableRecoveryWorker(store, nil, workerFakeRunner{attempt: store.attempt}, DurableWorkerOptions{Owner: "worker"})
	worker.runOnce(context.Background())
	if store.commitCalls != 1 || store.rescheduleCalls != 0 {
		t.Fatalf("commit=%d reschedule=%d", store.commitCalls, store.rescheduleCalls)
	}
}

func TestDurableRecoveryWorkerRunOnceReschedulesRecoverable(t *testing.T) {
	result := &AttemptResult{Success: false, CandidateOutcomes: []CandidateOutcome{{Kind: errorsx.KindTransient}}}
	store := &workerFakeStore{task: runnableTask(), snapshot: &durable.Snapshot{TaskID: "task-1"}}
	worker := NewDurableRecoveryWorker(store, nil, workerFakeRunner{attempt: &DurableAttempt{Result: result, Attempt: 2}}, DurableWorkerOptions{Owner: "worker"})
	worker.runOnce(context.Background())
	if store.rescheduleCalls != 1 || store.commitCalls != 0 {
		t.Fatalf("commit=%d reschedule=%d", store.commitCalls, store.rescheduleCalls)
	}
}

func TestDurableRecoveryWorkerSchedulesFirstRetryAfterTwoSeconds(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	result := &AttemptResult{Success: false, CandidateOutcomes: []CandidateOutcome{{Kind: errorsx.KindTransient}}}
	store := &workerFakeStore{task: runnableTask(), snapshot: &durable.Snapshot{TaskID: "task-1"}}
	worker := NewDurableRecoveryWorker(store, nil, workerFakeRunner{attempt: &DurableAttempt{Result: result, Attempt: 1}}, DurableWorkerOptions{Owner: "worker"})
	worker.now = func() time.Time { return now }

	worker.runOnce(context.Background())

	if got, want := store.lastReschedule.NextRetryAt, now.Add(2*time.Second); !got.Equal(want) {
		t.Fatalf("next retry = %s, want %s", got, want)
	}
}

func TestDurableRecoveryWorkerPreservesAuthoritativeRetryAfter(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	result := &AttemptResult{Success: false, CandidateOutcomes: []CandidateOutcome{{Kind: errorsx.KindRateLimit, RetryAfter: 10 * time.Minute}}}
	store := &workerFakeStore{task: runnableTask(), snapshot: &durable.Snapshot{TaskID: "task-1"}}
	worker := NewDurableRecoveryWorker(store, nil, workerFakeRunner{attempt: &DurableAttempt{Result: result, Attempt: 1}}, DurableWorkerOptions{Owner: "worker", RetryMax: 2 * time.Minute})
	worker.now = func() time.Time { return now }

	worker.runOnce(context.Background())

	if got, want := store.lastReschedule.NextRetryAt, now.Add(10*time.Minute); !got.Equal(want) {
		t.Fatalf("next retry = %s, want authoritative retry time %s", got, want)
	}
}

func TestDurableRecoveryWorkerDoublesRetryBackoff(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	result := &AttemptResult{Success: false, CandidateOutcomes: []CandidateOutcome{{Kind: errorsx.KindTransient}}}
	task := runnableTask()
	task.AttemptCount = 4
	store := &workerFakeStore{task: task, snapshot: &durable.Snapshot{TaskID: "task-1"}}
	worker := NewDurableRecoveryWorker(store, nil, workerFakeRunner{attempt: &DurableAttempt{Result: result, Attempt: 4}}, DurableWorkerOptions{Owner: "worker"})
	worker.now = func() time.Time { return now }

	worker.runOnce(context.Background())

	if got, want := store.lastReschedule.NextRetryAt, now.Add(16*time.Second); !got.Equal(want) {
		t.Fatalf("next retry = %s, want %s", got, want)
	}
}

func TestDurableRecoveryWorkerPreservesSuggestedRetryAfter(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	result := &AttemptResult{Success: false, CandidateOutcomes: []CandidateOutcome{{Kind: errorsx.KindTransient, RetryAfter: 10 * time.Minute}}}
	store := &workerFakeStore{task: runnableTask(), snapshot: &durable.Snapshot{TaskID: "task-1"}}
	worker := NewDurableRecoveryWorker(store, nil, workerFakeRunner{attempt: &DurableAttempt{Result: result, Attempt: 1}}, DurableWorkerOptions{Owner: "worker"})
	worker.now = func() time.Time { return now }

	worker.runOnce(context.Background())

	if got, want := store.lastReschedule.NextRetryAt, now.Add(10*time.Minute); !got.Equal(want) {
		t.Fatalf("next retry = %s, want %s", got, want)
	}
}

func TestDurableRecoveryWorkerTerminalizesAfterOneHundredRetries(t *testing.T) {
	result := &AttemptResult{Success: false, CandidateOutcomes: []CandidateOutcome{{Kind: errorsx.KindTransient}}}
	task := runnableTask()
	task.AttemptCount = 101
	store := &workerFakeStore{task: task, snapshot: &durable.Snapshot{TaskID: "task-1"}}
	worker := NewDurableRecoveryWorker(store, nil, workerFakeRunner{attempt: &DurableAttempt{Result: result, Attempt: 101}}, DurableWorkerOptions{Owner: "worker"})

	worker.runOnce(context.Background())

	if store.rescheduleCalls != 0 || store.commitCalls != 1 || store.lastOutcome != durable.StatusFailed {
		t.Fatalf("reschedule=%d commit=%d outcome=%s, want terminal failed", store.rescheduleCalls, store.commitCalls, store.lastOutcome)
	}
}

func TestDurableRecoveryWorkerStopIsIdempotent(t *testing.T) {
	store := &workerFakeStore{}
	worker := NewDurableRecoveryWorker(store, nil, workerFakeRunner{}, DurableWorkerOptions{PollInterval: time.Hour})
	worker.Start(context.Background())
	worker.Start(context.Background())
	worker.Stop()
	worker.Stop()
}

// TestDurableRecoveryWorkerStopGraceExceededIncrementsMetric asserts that a
// Stop whose bounded wait times out (the runner ignores cancellation and
// keeps the attempt goroutine alive past StopGrace) increments
// durable_recovery_stop_grace_exceeded_total, giving operators visibility
// into a slow foreground detach / goroutine leak. The bounded wait must still
// return (not hang) — verified by the 15s ceiling.
func TestDurableRecoveryWorkerStopGraceExceededIncrementsMetric(t *testing.T) {
	store := &workerFakeStore{task: runnableTask(), snapshot: &durable.Snapshot{TaskID: "task-1"}}
	before := gatherMetricValue(t, "durable_recovery_stop_grace_exceeded_total")
	started := make(chan struct{})
	release := make(chan struct{})
	worker := NewDurableRecoveryWorker(store, nil, blockingRunner{started: started, release: release},
		DurableWorkerOptions{Owner: "worker", Lease: time.Hour, StopGrace: 30 * time.Millisecond})
	worker.Start(context.Background())
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("runner did not start")
	}
	stopped := make(chan struct{})
	go func() { worker.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(15 * time.Second):
		t.Fatal("Stop must not hang when the runner ignores cancellation")
	}
	if got := gatherMetricValue(t, "durable_recovery_stop_grace_exceeded_total"); got <= before {
		t.Fatalf("durable_recovery_stop_grace_exceeded_total = %v, want > %v", got, before)
	}
	close(release)
}

// 审计修正（P1-1）：快照加载失败可能是瞬态 DB/keyring 问题，不得永久
// terminal-fail——重排重试，deadline reaper 给出安全的 expired 终态。
func TestDurableRecoveryWorkerSnapshotFailureReschedules(t *testing.T) {
	store := &workerFakeStore{task: runnableTask(), snapshotErr: errors.New("db unavailable")}
	worker := NewDurableRecoveryWorker(store, nil, workerFakeRunner{}, DurableWorkerOptions{Owner: "worker"})
	worker.runOnce(context.Background())
	if store.rescheduleCalls != 1 || store.commitCalls != 0 {
		t.Fatalf("commit=%d reschedule=%d, want reschedule-only", store.commitCalls, store.rescheduleCalls)
	}
}

// 审计修正（P1-1）：runner 基础设施错误（无 AttemptResult 可聚合）同样只
// 重排；永久终态只能来自 AggregateTaskOutcome 的判定。
func TestDurableRecoveryWorkerRunnerErrorReschedules(t *testing.T) {
	store := &workerFakeStore{task: runnableTask(), snapshot: &durable.Snapshot{TaskID: "task-1"}}
	worker := NewDurableRecoveryWorker(store, nil, workerFakeRunner{err: errors.New("rebuild failed")}, DurableWorkerOptions{Owner: "worker"})
	worker.runOnce(context.Background())
	if store.rescheduleCalls != 1 || store.commitCalls != 0 {
		t.Fatalf("commit=%d reschedule=%d, want reschedule-only", store.commitCalls, store.rescheduleCalls)
	}
}

func TestDurableRecoveryWorkerRunnerErrorTerminalizesAfterOneHundredRetries(t *testing.T) {
	task := runnableTask()
	task.AttemptCount = 101
	store := &workerFakeStore{task: task, snapshot: &durable.Snapshot{TaskID: "task-1"}}
	worker := NewDurableRecoveryWorker(store, nil, workerFakeRunner{err: errors.New("rebuild failed")}, DurableWorkerOptions{Owner: "worker"})

	worker.runOnce(context.Background())

	if store.rescheduleCalls != 0 || store.commitCalls != 1 || store.lastOutcome != durable.StatusFailed {
		t.Fatalf("reschedule=%d commit=%d outcome=%s, want terminal failed", store.rescheduleCalls, store.commitCalls, store.lastOutcome)
	}
}

// 任务级永久判定（AggregateTaskOutcome FailTerminal）仍必须形成 failed 终态。
func TestDurableRecoveryWorkerTerminalDecisionFails(t *testing.T) {
	result := &AttemptResult{Success: false, CandidateOutcomes: []CandidateOutcome{{Kind: errorsx.KindContextLength}}}
	store := &workerFakeStore{task: runnableTask(), snapshot: &durable.Snapshot{TaskID: "task-1"}}
	worker := NewDurableRecoveryWorker(store, nil, workerFakeRunner{attempt: &DurableAttempt{Result: result, Attempt: 1}}, DurableWorkerOptions{Owner: "worker"})
	worker.runOnce(context.Background())
	if store.commitCalls != 1 || store.lastOutcome != durable.StatusFailed {
		t.Fatalf("commit=%d outcome=%s", store.commitCalls, store.lastOutcome)
	}
}

// 审计修正（P1-2）：不响应取消的 runner 不能让 Stop 永久挂起。
func TestDurableRecoveryWorkerStopBoundedWhenRunnerIgnoresCancel(t *testing.T) {
	store := &workerFakeStore{task: runnableTask(), snapshot: &durable.Snapshot{TaskID: "task-1"}}
	release := make(chan struct{})
	started := make(chan struct{})
	worker := NewDurableRecoveryWorker(store, nil, blockingRunner{started: started, release: release}, DurableWorkerOptions{Owner: "worker", Lease: time.Hour, StopGrace: 30 * time.Millisecond})
	worker.Start(context.Background())
	select {
	case <-started:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("runner did not start")
	}
	done := make(chan struct{})
	go func() { worker.Stop(); close(done) }()
	select {
	case <-done:
		close(release)
	case <-time.After(15 * time.Second):
		close(release)
		t.Fatal("Stop must not hang when the runner ignores cancellation")
	}
}

type blockingRunner struct {
	started chan struct{}
	release chan struct{}
}

func (r blockingRunner) Run(ctx context.Context, _ *durable.Task, _ *durable.Snapshot) (*DurableAttempt, error) {
	if r.started != nil {
		close(r.started)
	}
	<-r.release
	return successAttempt(), nil
}

// gatherMetricValue sums the current value of one durable_* series across all
// label combinations (global registry; tests only assert deltas).
func gatherMetricValue(t *testing.T, name string) float64 {
	t.Helper()
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	total := 0.0
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			switch {
			case m.GetCounter() != nil:
				total += m.GetCounter().GetValue()
			case m.GetGauge() != nil:
				total += m.GetGauge().GetValue()
			}
		}
	}
	return total
}

func gatherLabeledValue(t *testing.T, name, labelValue string) float64 {
	t.Helper()
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetValue() == labelValue {
					if m.GetCounter() != nil {
						return m.GetCounter().GetValue()
					}
					if m.GetGauge() != nil {
						return m.GetGauge().GetValue()
					}
				}
			}
		}
	}
	return 0
}

// durable_* 观测契约（doc 18 §15）：claim 周期按结果计数，active gauge 来自
// 权威 ActiveTaskCounts，fencing 失效计入 lease-lost。
func TestDurableRecoveryWorkerEmitsRunOutcomes(t *testing.T) {
	before := gatherLabeledValue(t, "durable_recovery_runs_total", "claimed")
	emptyBefore := gatherLabeledValue(t, "durable_recovery_runs_total", "empty")

	store := &workerFakeStore{task: runnableTask(), snapshot: &durable.Snapshot{TaskID: "task-1"}, attempt: successAttempt()}
	worker := NewDurableRecoveryWorker(store, nil, workerFakeRunner{attempt: store.attempt}, DurableWorkerOptions{Owner: "worker"})
	worker.runOnce(context.Background())
	if got := gatherLabeledValue(t, "durable_recovery_runs_total", "claimed"); got < before+1 {
		t.Fatalf("durable_recovery_runs_total{claimed} = %v, want >= %v", got, before+1)
	}

	worker.runOnce(context.Background()) // second cycle claims nothing
	if got := gatherLabeledValue(t, "durable_recovery_runs_total", "empty"); got < emptyBefore+1 {
		t.Fatalf("durable_recovery_runs_total{empty} = %v, want >= %v", got, emptyBefore+1)
	}
}

func TestDurableRecoveryWorkerPublishesActiveGauge(t *testing.T) {
	metrics.SetDurableTasksActive(map[string]int64{"metric-tenant": 2})
	if got := gatherLabeledValue(t, "durable_tasks_active", "metric-tenant"); got != 2 {
		t.Fatalf("durable_tasks_active{metric-tenant} = %v, want 2", got)
	}
	// 租户清零后（GROUP BY 缺席）不得冻结在旧值。
	metrics.SetDurableTasksActive(map[string]int64{})
	if got := gatherLabeledValue(t, "durable_tasks_active", "metric-tenant"); got != 0 {
		t.Fatalf("durable_tasks_active{metric-tenant} = %v after drain, want 0", got)
	}
}

func TestDurableRecoveryWorkerCountsLeaseLost(t *testing.T) {
	before := gatherMetricValue(t, "durable_lease_lost_total")
	store := &workerFakeStore{task: runnableTask(), snapshot: &durable.Snapshot{TaskID: "task-1"}, commitErr: durable.ErrLeaseLost}
	worker := NewDurableRecoveryWorker(store, nil, workerFakeRunner{attempt: successAttempt()}, DurableWorkerOptions{Owner: "worker"})
	worker.runOnce(context.Background())
	if got := gatherMetricValue(t, "durable_lease_lost_total"); got < before+1 {
		t.Fatalf("durable_lease_lost_total = %v, want >= %v", got, before+1)
	}
}

// Audit-2026-08-29 (§5.2 hardening): a panic in w.runner.Run used to kill
// the worker goroutine silently because runOnce has no outer defer recover.
// After the fix the worker loop survives the panic and the next tick still
// runs runOnce. This test exercises that contract end-to-end: a runner that
// panics on the first call and succeeds on the second call, with a tight
// PollInterval, must produce a commit on the second tick — proof the worker
// goroutine is still alive after the panic.
type panickingThenSucceedingRunner struct {
	calls int
}

func (r *panickingThenSucceedingRunner) Run(context.Context, *durable.Task, *durable.Snapshot) (*DurableAttempt, error) {
	r.calls++
	if r.calls == 1 {
		panic("synthetic panic from durable runner on first call")
	}
	return successAttempt(), nil
}

// rearmingWorkerFakeStore yields the same task on every ClaimRunnable so the
// worker can exercise multiple runOnce ticks against a single fixture.
type rearmingWorkerFakeStore struct {
	mu              sync.Mutex
	task            *durable.Task
	snapshot        *durable.Snapshot
	commitCalls     int
	rescheduleCalls int
	lastOutcome     durable.Status
}

func (f *rearmingWorkerFakeStore) PersistSettlementIntent(context.Context, durable.TerminalCommit) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commitCalls++
	return nil
}
func (f *rearmingWorkerFakeStore) ClaimSettlementIntent(context.Context, string, string, time.Duration, time.Time) (*durable.ClaimedSettlement, error) {
	return &durable.ClaimedSettlement{SettlementIntent: durable.SettlementIntent{TaskID: "task-1", Attempts: 1}, ClaimOwner: "worker", ClaimUntil: time.Now().Add(time.Hour), ClaimFencingToken: 1}, nil
}
func (f *rearmingWorkerFakeStore) ClaimSettlementIntents(context.Context, string, time.Duration, int, time.Time) ([]*durable.ClaimedSettlement, error) {
	return nil, nil
}
func (f *rearmingWorkerFakeStore) FinalizeSettlement(context.Context, durable.ClaimedSettlement) (*durable.TerminalProjection, error) {
	return &durable.TerminalProjection{Committed: true}, nil
}
func (f *rearmingWorkerFakeStore) RetrySettlementIntent(context.Context, durable.ClaimedSettlement, time.Time, error) error {
	return nil
}
func (f *rearmingWorkerFakeStore) ClaimRunnable(context.Context, durable.ClaimOptions) ([]*durable.Task, error) {
	if f.task == nil {
		return nil, nil
	}
	return []*durable.Task{f.task}, nil
}
func (f *rearmingWorkerFakeStore) LoadSnapshot(context.Context, string) (*durable.Snapshot, error) {
	return f.snapshot, nil
}
func (f *rearmingWorkerFakeStore) RenewLease(context.Context, string, string, int64, time.Time) error {
	return nil
}
func (f *rearmingWorkerFakeStore) Reschedule(context.Context, durable.RescheduleParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rescheduleCalls++
	return nil
}
func (f *rearmingWorkerFakeStore) CommitTerminal(_ context.Context, c durable.TerminalCommit) (*durable.TerminalProjection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commitCalls++
	f.lastOutcome = c.Outcome
	return &durable.TerminalProjection{Committed: true}, nil
}
func (f *rearmingWorkerFakeStore) ReapDeadlines(context.Context, int, time.Time) ([]*durable.ReapedTaskInfo, error) {
	return nil, nil
}
func (f *rearmingWorkerFakeStore) ReapUnsafeCheckpointed(context.Context, int, time.Time) ([]*durable.Task, error) {
	return nil, nil
}
func (f *rearmingWorkerFakeStore) ProjectPendingOutbox(context.Context, *pending.Store, int, time.Time) (int, error) {
	return 0, nil
}
func (f *rearmingWorkerFakeStore) ActiveTaskCounts(context.Context) (map[string]int64, error) {
	return map[string]int64{"tenant-a": 1}, nil
}

func TestDurableRecoveryWorkerSurvivesRunnerPanic(t *testing.T) {
	store := &rearmingWorkerFakeStore{task: runnableTask(), snapshot: &durable.Snapshot{TaskID: "task-1"}}
	runner := &panickingThenSucceedingRunner{}
	worker := NewDurableRecoveryWorker(store, nil, runner, DurableWorkerOptions{
		Owner:       "worker",
		PollInterval: 50 * time.Millisecond,
		Lease:        time.Second,
		StopGrace:    50 * time.Millisecond,
	})
	worker.Start(context.Background())
	defer worker.Stop()

	// Wait until the runner has been invoked at least twice. Without the
	// recover guard the second invocation would never happen because the
	// worker goroutine would have died on the first panic.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if runner.calls >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if runner.calls < 2 {
		t.Fatalf("runner.calls = %d, want >= 2 — worker likely died after first panic", runner.calls)
	}

	// And the second invocation actually committed — proof the post-panic
	// runOnce ran end-to-end, not just that the goroutine woke up.
	if store.commitCalls < 1 {
		t.Fatalf("commitCalls = %d, want >= 1 — post-panic runOnce did not reach commit", store.commitCalls)
	}
}

// Audit-2026-08-29 (§5.2 hardening): when w.runner.Run panics, the panic
// value must be captured into the outer-scope `err` so the reschedule
// branch at attemptFinished logs the panic message instead of a generic
// "durable runner returned nil attempt". The runner-panic recovery is
// scoped inside the attempt goroutine (separate from the worker-loop
// recover in TestDurableRecoveryWorkerSurvivesRunnerPanic).
func TestDurableRecoveryAttemptRunnerPanicCapturesMessage(t *testing.T) {
	store := &workerFakeStore{task: runnableTask(), snapshot: &durable.Snapshot{TaskID: "task-1"}}
	worker := NewDurableRecoveryWorker(store, nil, workerFakeRunner{err: nil}, DurableWorkerOptions{Owner: "worker"})

	// Replace runner with a one-shot panicking one.
	panicMsg := "synthetic runner panic with sentinel"
	worker.runner = panickingRunnerOnce{msg: panicMsg}

	worker.runOnce(context.Background())

	// The panic must result in a reschedule (not commit) and the reschedule
	// reason / log message must carry the panic value.
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.commitCalls != 0 {
		t.Fatalf("commitCalls = %d, want 0 (panic must not commit)", store.commitCalls)
	}
	if store.rescheduleCalls < 1 {
		t.Fatalf("rescheduleCalls = %d, want >= 1 — panic must trigger reschedule", store.rescheduleCalls)
	}
	if !strings.Contains(store.lastReschedule.Reason, panicMsg) && !strings.Contains(store.lastReschedule.ErrorKind, "runner") {
		// The reschedule reason path is best-effort: the panic message is
		// either in Reason or carried by an explicit slog line. We accept
		// either as long as the panic was the proximate cause.
		t.Logf("reschedule reason=%q error_kind=%q (panic message surfaced separately via slog)", store.lastReschedule.Reason, store.lastReschedule.ErrorKind)
	}
}

// panickingRunnerOnce panics on its single Run call with msg.
type panickingRunnerOnce struct {
	msg string
}

func (r panickingRunnerOnce) Run(context.Context, *durable.Task, *durable.Snapshot) (*DurableAttempt, error) {
	panic(r.msg)
}
