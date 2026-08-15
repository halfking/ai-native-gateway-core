package streaming

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/durable"
	"github.com/kaixuan/llm-gateway-go/errorsx"
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
func (f *workerFakeStore) Reschedule(context.Context, durable.RescheduleParams) error {
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
	return &durable.Task{ID: "task-1", TenantID: "tenant-a", RequestID: "req-1", SessionID: "sess-1", Status: durable.StatusRetryScheduled, CommitState: durable.CommitStateNone, LeaseOwner: "worker", FencingToken: 1, ExpiresAt: time.Now().Add(time.Hour)}
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

func TestDurableRecoveryWorkerStopIsIdempotent(t *testing.T) {
	store := &workerFakeStore{}
	worker := NewDurableRecoveryWorker(store, nil, workerFakeRunner{}, DurableWorkerOptions{PollInterval: time.Hour})
	worker.Start(context.Background())
	worker.Start(context.Background())
	worker.Stop()
	worker.Stop()
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
	worker := NewDurableRecoveryWorker(store, nil, blockingRunner{release: release}, DurableWorkerOptions{Owner: "worker", Lease: time.Hour})
	started := make(chan struct{})
	worker.Start(context.Background())
	go func() {
		close(started)
	}()
	<-started
	// runOnce 在 Start 的首圈内同步执行；给调度器一点时间进入 runTask。
	time.Sleep(50 * time.Millisecond)
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
	release chan struct{}
}

func (r blockingRunner) Run(ctx context.Context, _ *durable.Task, _ *durable.Snapshot) (*DurableAttempt, error) {
	<-r.release
	return successAttempt(), nil
}
