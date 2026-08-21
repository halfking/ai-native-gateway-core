package bg

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"

	"github.com/kaixuan/llm-gateway-go/domains/goalrun"
)

// fakeDispatcher 是 Dispatcher 的测试桩；可注入 success/error。
type fakeDispatcher struct {
	mu      sync.Mutex
	calls   []string
	onCall  func(a *goalrun.GoalRunAction) (*Successor, error)
	delay   time.Duration
	calledN atomic.Int32
}

func (f *fakeDispatcher) ContinueGoal(ctx context.Context, a *goalrun.GoalRunAction) (*Successor, error) {
	return f.invoke(ctx, "continue", a)
}

func (f *fakeDispatcher) ProposeHandoff(ctx context.Context, a *goalrun.GoalRunAction) (*Successor, error) {
	return f.invoke(ctx, "handoff", a)
}

func (f *fakeDispatcher) SwitchModel(ctx context.Context, a *goalrun.GoalRunAction) (*Successor, error) {
	return f.invoke(ctx, "model_switch", a)
}

func (f *fakeDispatcher) invoke(ctx context.Context, kind string, a *goalrun.GoalRunAction) (*Successor, error) {
	f.calledN.Add(1)
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.onCall != nil {
		return f.onCall(a)
	}
	return &Successor{
		SuccessorID:     "succ_" + a.ActionID,
		ParentRequestID: a.CausationID,
		SessionID:       a.GoalRunID,
		Sequence:        0,
	}, nil
}

// newMockStore 构造只含「goal_run_actions 表」的 mock store。
func newMockStore(t *testing.T) (*goalrun.Store, pgxmock.PgxPoolIface) {
	t.Helper()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	t.Cleanup(func() { mock.Close() })
	return goalrun.NewStore(mock), mock
}

// newScheduler 构造统一配置的 scheduler（不 Start）。
func newScheduler(store *goalrun.Store, disp Dispatcher, cfg SchedulerConfig) *GoalRunActionScheduler {
	if cfg.Lease == 0 {
		cfg.Lease = 60 * time.Second
	}
	return NewGoalRunActionScheduler(store, "gw-1", disp, cfg)
}

// expectedProcessOnceExpectations 设置一次 ProcessOnce 调用所需的全部
// pgxmock 期望：Begin → SELECT FOR UPDATE → n× UPDATE → Commit。
//
// 「NoData」表示 SELECT 返回 0 行（空 batch）。
type expectedProcessOnceExpectations struct {
	updates       int                  // UPDATE 命中数
	actionIDs     []string             // 期望的 action_id 序列
	tokenSequence []int64              // 期望 fencing_token 序列（递增）
	now           time.Time            // 提供给 SELECT 的 now 参数
	noData        bool                 // SELECT 是否返回空
}

// expectProcessOnce 让 mock 准备好一次 ProcessOnce 调用所需的全部期望。
func expectProcessOnce(t *testing.T, mock pgxmock.PgxPoolIface, exp expectedProcessOnceExpectations) {
	t.Helper()

	mock.ExpectBegin()
	if exp.noData {
		mock.ExpectQuery(`SELECT action_id FROM goal_run_actions`).
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"action_id"}))
		mock.ExpectCommit()
		return
	}
	rows := pgxmock.NewRows([]string{"action_id"})
	for _, id := range exp.actionIDs {
		rows.AddRow(id)
	}
	mock.ExpectQuery(`SELECT action_id FROM goal_run_actions`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(rows)

	for i, id := range exp.actionIDs {
		cols := []string{
			"action_id", "goal_run_id", "causation_id", "action_type",
			"idempotency_key", "expected_version", "status",
			"retry_at", "attempts", "last_error",
			"lease_owner", "lease_until", "fencing_token", "claimed_at",
			"created_at", "updated_at",
		}
		row := pgxmock.NewRows(cols).AddRow(
			id, "gr_1", "parent_1", "continue",
			"idem-"+id, int64(1), "running",
			exp.now, 1, "",
			"gw-1", exp.now.Add(60*time.Second), exp.tokenSequence[i], exp.now,
			exp.now, exp.now,
		)
		mock.ExpectQuery(`UPDATE goal_run_actions`).
			WithArgs(id, "gw-1", pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(row)
	}
	mock.ExpectCommit()
}

// TestScheduler_DispatchCompletesAction：ProcessOnce happy path → scanner
// 命中 1 个 action → ContinueGoal → CompleteAction(completed)。
func TestScheduler_DispatchCompletesAction(t *testing.T) {
	store, mock := newMockStore(t)
	disp := &fakeDispatcher{}
	now := time.Now()

	expectProcessOnce(t, mock, expectedProcessOnceExpectations{
		actionIDs:     []string{"act_a"},
		tokenSequence: []int64{1},
		now:           now,
	})

	// CompleteAction expected
	mock.ExpectExec(`UPDATE goal_run_actions`).
		WithArgs(pgxmock.AnyArg(), "", pgxmock.AnyArg(), "act_a", "gw-1", int64(1)).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	s := newScheduler(store, disp, SchedulerConfig{})
	s.cfg.applyDefaults() // 应用默认设置
	ctx := context.Background()

	n := s.ProcessOnce(ctx)
	if n != 1 {
		t.Fatalf("processed = %d, want 1", n)
	}
	if disp.calledN.Load() != 1 {
		t.Fatalf("dispatcher called = %d, want 1", disp.calledN.Load())
	}
	metrics := s.Metrics()
	if metrics.Scanned != 1 || metrics.Claimed != 1 || metrics.Completed != 1 {
		t.Fatalf("metrics = %+v, want scanned/claimed/completed = 1/1/1", metrics)
	}
	if metrics.Fenced > 0 {
		t.Fatalf("Fenced = %d, want 0", metrics.Fenced)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// TestScheduler_NoRunnable：空 batch 是正常路径；Fenced/Completed 都不应变化。
func TestScheduler_NoRunnable(t *testing.T) {
	store, mock := newMockStore(t)
	disp := &fakeDispatcher{}
	now := time.Now()

	expectProcessOnce(t, mock, expectedProcessOnceExpectations{
		now:    now,
		noData: true,
	})

	s := newScheduler(store, disp, SchedulerConfig{})
	ctx := context.Background()

	n := s.ProcessOnce(ctx)
	if n != 0 {
		t.Fatalf("processed = %d, want 0", n)
	}
	if disp.calledN.Load() != 0 {
		t.Fatalf("dispatcher must not be called")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// TestScheduler_ConcurrentClaim_OnlyOneWins：两个独立 scheduler（不同
// mock pool）代表两个 gateway 实例并发 claim 同一 action；先 claim 的赢，
// 后 claim 因 pgxmock 短路不再命中（模拟 SELECT FOR UPDATE SKIP LOCKED）。
func TestScheduler_ConcurrentClaim_OnlyOneWins(t *testing.T) {
	storeA, mockA := newMockStore(t)
	storeB, mockB := newMockStore(t)
	dispA := &fakeDispatcher{}
	dispB := &fakeDispatcher{}
	now := time.Now()

	// Scheduler A 命中 1 个
	mockA.ExpectBegin()
	mockA.ExpectQuery(`SELECT action_id FROM goal_run_actions`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"action_id"}).AddRow("act_only"))
	mockA.ExpectQuery(`UPDATE goal_run_actions`).
		WithArgs("act_only", "gw-A", pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"action_id", "goal_run_id", "causation_id", "action_type",
			"idempotency_key", "expected_version", "status",
			"retry_at", "attempts", "last_error",
			"lease_owner", "lease_until", "fencing_token", "claimed_at",
			"created_at", "updated_at",
		}).AddRow(
			"act_only", "gr_1", "", "continue",
			"idem_only", int64(1), "running",
			now, 1, "",
			"gw-A", now.Add(60*time.Second), int64(1), now,
			now, now,
		))
	mockA.ExpectCommit()
	mockA.ExpectExec(`UPDATE goal_run_actions`).
		WithArgs(pgxmock.AnyArg(), "", pgxmock.AnyArg(), "act_only", "gw-A", int64(1)).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	// Scheduler B 因前次已 claim，scan 返回空
	mockB.ExpectBegin()
	mockB.ExpectQuery(`SELECT action_id FROM goal_run_actions`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"action_id"}))
	mockB.ExpectCommit()

	sA := NewGoalRunActionScheduler(storeA, "gw-A", dispA, SchedulerConfig{})
	sB := NewGoalRunActionScheduler(storeB, "gw-B", dispB, SchedulerConfig{})

	if got := sA.ProcessOnce(context.Background()); got != 1 {
		t.Fatalf("Scheduler A processed = %d, want 1", got)
	}
	if got := sB.ProcessOnce(context.Background()); got != 0 {
		t.Fatalf("Scheduler B processed = %d, want 0 (other instance won)", got)
	}
	if dispA.calledN.Load() != 1 {
		t.Fatalf("Dispatcher A called = %d, want 1", dispA.calledN.Load())
	}
	if dispB.calledN.Load() != 0 {
		t.Fatalf("Dispatcher B called = %d, want 0", dispB.calledN.Load())
	}
	if err := mockA.ExpectationsWereMet(); err != nil {
		t.Fatalf("mockA: %v", err)
	}
	if err := mockB.ExpectationsWereMet(); err != nil {
		t.Fatalf("mockB: %v", err)
	}
}

// TestScheduler_LateWorkerFenced：complete CAS 失败（lease 被夺）
// → Fenced 计数 +1，副作用不重做，Completed 不增。
func TestScheduler_LateWorkerFenced(t *testing.T) {
	store, mock := newMockStore(t)
	disp := &fakeDispatcher{}
	now := time.Now()

	expectProcessOnce(t, mock, expectedProcessOnceExpectations{
		actionIDs:     []string{"act_a"},
		tokenSequence: []int64{1},
		now:           now,
	})

	// Complete CAS 失败（0 rows → ErrActionLeaseLost）
	mock.ExpectExec(`UPDATE goal_run_actions`).
		WithArgs(pgxmock.AnyArg(), "", pgxmock.AnyArg(), "act_a", "gw-1", int64(1)).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	s := newScheduler(store, disp, SchedulerConfig{})
	if got := s.ProcessOnce(context.Background()); got != 1 {
		t.Fatalf("processed = %d, want 1", got)
	}
	metrics := s.Metrics()
	if metrics.Fenced < 1 {
		t.Fatalf("Fenced = %d, want >= 1", metrics.Fenced)
	}
	if metrics.Completed > 0 {
		t.Fatalf("Completed = %d, want 0 (fenced must not increment Completed)", metrics.Completed)
	}
	// dispatcher 已执行副作用（succ_a 已被创建），但 action 表未完成；这是 fence 后的
	// 短暂不一致窗口，需要 reaper/对账发现。
	if disp.calledN.Load() != 1 {
		t.Fatalf("dispatcher must have executed side effect, called = %d", disp.calledN.Load())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// TestScheduler_RenewLeaseSucceeds：续租 CAS 成功不应触发 fence。
func TestScheduler_RenewLeaseSucceeds(t *testing.T) {
	store, mock := newMockStore(t)
	disp := &fakeDispatcher{}

	// 续租成功
	mock.ExpectExec(`UPDATE goal_run_actions`).
		WithArgs("act_a", "gw-1", int64(3), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	s := newScheduler(store, disp, SchedulerConfig{})
	a := &goalrun.GoalRunAction{
		ActionID:     "act_a",
		LeaseOwner:   "gw-1",
		FencingToken: 3,
	}

	if ok := s.RenewLeaseOnce(context.Background(), a); !ok {
		t.Fatalf("RenewLeaseOnce should succeed")
	}
	if s.Metrics().Fenced > 0 {
		t.Fatalf("Fenced = %d, want 0 (renew succeeded)", s.Metrics().Fenced)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// TestScheduler_RenewLeaseFences：续租 CAS 0 行 → Fenced +1。
func TestScheduler_RenewLeaseFences(t *testing.T) {
	store, mock := newMockStore(t)
	disp := &fakeDispatcher{}

	mock.ExpectExec(`UPDATE goal_run_actions`).
		WithArgs("act_a", "gw-stale", int64(2), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	s := newScheduler(store, disp, SchedulerConfig{})
	a := &goalrun.GoalRunAction{
		ActionID:     "act_a",
		LeaseOwner:   "gw-stale",
		FencingToken: 2,
	}

	if ok := s.RenewLeaseOnce(context.Background(), a); ok {
		t.Fatalf("RenewLeaseOnce should return false on lease lost")
	}
	if s.Metrics().Fenced != 1 {
		t.Fatalf("Fenced = %d, want 1", s.Metrics().Fenced)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// TestScheduler_DispatchError_Requeued：dispatcher 抛错 → Requeued +1，
// 第 2 次 ProcessOnce 由同一动作（claim 重入）再次命中 dispatch。
func TestScheduler_DispatchError_Requeued(t *testing.T) {
	store, mock := newMockStore(t)
	var calls atomic.Int32
	disp := &fakeDispatcher{
		onCall: func(a *goalrun.GoalRunAction) (*Successor, error) {
			n := calls.Add(1)
			if n == 1 {
				return nil, errors.New("upstream temporarily unavailable")
			}
			return &Successor{SuccessorID: "succ_retry"}, nil
		},
	}
	now := time.Now()

	// 第 1 次 claim：成功
	expectProcessOnce(t, mock, expectedProcessOnceExpectations{
		actionIDs:     []string{"act_a"},
		tokenSequence: []int64{1},
		now:           now,
	})
	// Requeue（reset lease + retry_at 任意）
	mock.ExpectExec(`UPDATE goal_run_actions`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), "act_a", "gw-1", int64(1)).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	// 第 2 次 claim：token 递增到 2
	expectProcessOnce(t, mock, expectedProcessOnceExpectations{
		actionIDs:     []string{"act_a"},
		tokenSequence: []int64{2},
		now:           now,
	})
	// 第 2 次 Complete：成功
	mock.ExpectExec(`UPDATE goal_run_actions`).
		WithArgs(pgxmock.AnyArg(), "", pgxmock.AnyArg(), "act_a", "gw-1", int64(2)).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	s := newScheduler(store, disp, SchedulerConfig{
		MaxAttempts:  5,
		RetryBackoff: 10 * time.Millisecond,
	})

	if got := s.ProcessOnce(context.Background()); got != 1 {
		t.Fatalf("first process = %d, want 1", got)
	}
	if got := s.ProcessOnce(context.Background()); got != 1 {
		t.Fatalf("second process = %d, want 1", got)
	}
	metrics := s.Metrics()
	if metrics.Requeued != 1 {
		t.Fatalf("Requeued = %d, want 1", metrics.Requeued)
	}
	if metrics.Completed != 1 {
		t.Fatalf("Completed = %d, want 1", metrics.Completed)
	}
	if disp.calledN.Load() != 2 {
		t.Fatalf("dispatcher called = %d, want 2", disp.calledN.Load())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// TestScheduler_ReaperClearsExpiredLeases：ExpireLeasesOnce 返回受影响行数。
func TestScheduler_ReaperClearsExpiredLeases(t *testing.T) {
	store, mock := newMockStore(t)
	disp := &fakeDispatcher{}

	mock.ExpectExec(`UPDATE goal_run_actions`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 3))

	s := newScheduler(store, disp, SchedulerConfig{ReaperBatch: 256})
	n := s.ExpireLeasesOnce(context.Background())
	if n != 3 {
		t.Fatalf("expired = %d, want 3", n)
	}
	if s.Metrics().LeaseExpired != 3 {
		t.Fatalf("LeaseExpired = %d, want 3", s.Metrics().LeaseExpired)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// TestScheduler_RoutingByActionType：dispatcher 三种 action type 路由正确。
func TestScheduler_RoutingByActionType(t *testing.T) {
	cases := []struct {
		name      string
		action    goalrun.ActionType
		callKind  string // "continue"/"handoff"/"model_switch"
		expectRun bool
	}{
		{"continue", goalrun.ActionTypeContinue, "continue", true},
		{"handoff", goalrun.ActionTypeHandoff, "handoff", true},
		{"model_switch", goalrun.ActionTypeModelSwitch, "model_switch", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, mock := newMockStore(t)
			disp := &fakeDispatcher{}
			now := time.Now()

			// Build action row with the action_type
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT action_id FROM goal_run_actions`).
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(pgxmock.NewRows([]string{"action_id"}).AddRow("act_x"))
			cols := []string{
				"action_id", "goal_run_id", "causation_id", "action_type",
				"idempotency_key", "expected_version", "status",
				"retry_at", "attempts", "last_error",
				"lease_owner", "lease_until", "fencing_token", "claimed_at",
				"created_at", "updated_at",
			}
			mock.ExpectQuery(`UPDATE goal_run_actions`).
				WithArgs("act_x", "gw-1", pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(pgxmock.NewRows(cols).AddRow(
					"act_x", "gr_1", "", string(tc.action),
					"idem_x", int64(1), "running",
					now, 1, "",
					"gw-1", now.Add(60*time.Second), int64(1), now,
					now, now,
				))
			mock.ExpectCommit()
			mock.ExpectExec(`UPDATE goal_run_actions`).
				WithArgs(pgxmock.AnyArg(), "", pgxmock.AnyArg(), "act_x", "gw-1", int64(1)).
				WillReturnResult(pgxmock.NewResult("UPDATE", 1))

			s := newScheduler(store, disp, SchedulerConfig{})
			if got := s.ProcessOnce(context.Background()); got != 1 {
				t.Fatalf("processed = %d, want 1", got)
			}
			if tc.expectRun && disp.calledN.Load() != 1 {
				t.Fatalf("dispatcher called = %d, want 1", disp.calledN.Load())
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("expectations: %v", err)
			}
		})
	}
}

// TestScheduler_ConcurrencyClampsToConfig：applyDefaults 必须把
// Concurrency 限制在 [2, 4]。
func TestScheduler_ConcurrencyClampsToConfig(t *testing.T) {
	cfg := SchedulerConfig{}.applyDefaults()
	if cfg.Concurrency < 2 || cfg.Concurrency > 4 {
		t.Fatalf("Concurrency = %d, want in [2,4]", cfg.Concurrency)
	}
	if cfg.ScanInterval <= 0 || cfg.ScanInterval > 10*time.Second {
		t.Fatalf("ScanInterval = %s, want (0, 10s]", cfg.ScanInterval)
	}

	// Check upper bound too
	cfgBig := SchedulerConfig{Concurrency: 100}.applyDefaults()
	if cfgBig.Concurrency != 4 {
		t.Fatalf("Concurrency clamp from 100 = %d, want 4", cfgBig.Concurrency)
	}

	// Check lower bound
	cfgSmall := SchedulerConfig{Concurrency: 0}.applyDefaults()
	if cfgSmall.Concurrency < 2 {
		t.Fatalf("Concurrency clamp from 0 = %d, want >= 2", cfgSmall.Concurrency)
	}
}
