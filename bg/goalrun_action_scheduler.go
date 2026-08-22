// bg/goalrun_action_scheduler.go — Durable Continuation Scheduler（设计 13 §6.2，Wave 3-A）
//
// 替代进程内 `go injectFollowUpRequest`：周期性扫描 goal_run_actions 表
// （status='pending' AND retry_at <= NOW()），CAS claim 后分发到相应执行器。
//
// 关键不变量：
//   - 一个 GoalRun 同时只有一个有效 successor（store.UpdateStatus 的 CAS 保证 +
//     GoalRunActionStore 的唯一性约束）；
//   - 迟到 worker 的副作用必须被 fence（ErrActionLeaseLost → 丢弃结果）；
//   - 终态 sticky：cancel/terminal 优先于迟到 worker。
//
// 架构（与 durable.ClaimRunnable 一致）：
//
//	┌─ Scan (every ScanInterval) ─────────────────────────┐
//	│ pgx: SELECT FOR UPDATE SKIP LOCKED, batch=Concurrency│
//	└──────────────────────────────────────────────────────┘
//	           │
//	           ▼
//	┌─ Claim (per action) ────────────────────────────────┐
//	│ pgx: UPDATE status='running' + lease + token+1     │
//	│ 0 rows → ErrActionLeaseLost（其它 worker 抢占）      │
//	└──────────────────────────────────────────────────────┘
//	           │
//	           ▼
//	┌─ Execute (actionType 路由) ──────────────────────────┐
//	│ continue    → Dispatcher.ContinueGoal(parent)       │
//	│ handoff     → HandoffProposer.Propose(...)          │
//	│ model_switch→ ModelSwitcher.Switch(...)             │
//	└──────────────────────────────────────────────────────┘
//	           │
//	           ▼
//	┌─ Complete / Requeue ─────────────────────────────────┐
//	│ success → CompleteAction(status='completed')        │
//	│ err retryable → RequeueAction(retry_at=now+backoff)  │
//	│ err fatal → CompleteAction(status='failed')          │
//	│ ErrActionLeaseLost → 丢弃；勿再做副作用             │
//	└──────────────────────────────────────────────────────┘
package bg

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/goalrun"
)

// Dispatcher 是 scheduler 调用的外部副作用入口（Wave 3-A 接口边界）。
//
// 所有方法必须：
//   - 接受 context 返回 error；
//   - 幂等（基于 request_id / proposal_id 等幂等键）；
//   - 不自行持久化状态（持久化由 scheduler + store 完成）。
//
// 真实实现由 request pipeline（Wave 2-D）提供；测试中可用 fake。
type Dispatcher interface {
	// ContinueGoal 创建 GoalRun 的 successor request（同 session 续接）。
	// 返回的 Successor 中 SuccessorID 是新生成的 request_id。
	ContinueGoal(ctx context.Context, action *goalrun.GoalRunAction) (*Successor, error)

	// ProposeHandoff 创建跨 session handoff proposal。
	ProposeHandoff(ctx context.Context, action *goalrun.GoalRunAction) (*Successor, error)

	// SwitchModel 触发 Goal loop 级别模型切换。
	SwitchModel(ctx context.Context, action *goalrun.GoalRunAction) (*Successor, error)
}

// Successor 是 action 执行产物（持久化在 goal_run_steps 中，作为 Step 关联）。
type Successor struct {
	SuccessorID     string // 新生成 request_id / proposal_id
	ParentRequestID string
	SessionID       string
	Sequence        int
}

// SchedulerConfig 是默认配置（与 durable.ClaimRunnable 对齐）。
type SchedulerConfig struct {
	// Concurrency 同时在飞的 action 数。默认 4，区间 2-4。
	Concurrency int
	// ScanInterval 扫描间隔。默认 5s，区间 5-10s。
	ScanInterval time.Duration
	// Lease claim 租约时长。默认 60s（与 durable.ClaimRunnable 一致）。
	Lease time.Duration
	// RenewInterval 续租间隔（= Lease / 3）。默认 20s。
	RenewInterval time.Duration
	// Batch 单次 batch 上限。默认 = Concurrency。
	Batch int
	// MaxAttempts 单 action 最大尝试次数。默认 5。
	MaxAttempts int
	// ReaperInterval lease 过期 reaper 周期。默认 = 5*ScanInterval。
	ReaperInterval time.Duration
	// ReaperBatch reaper 单次清理上限。默认 256。
	ReaperBatch int

	// RetryBackoff 失败重排退避基数（指数退避）。默认 2s。
	RetryBackoff time.Duration
	// RetryMaxBackoff 退避上限。默认 60s。
	RetryMaxBackoff time.Duration
}

// applyDefaults 填充零值（必须保证 ScanInterval > 0；Concurrency 上限 4）。
func (c SchedulerConfig) applyDefaults() SchedulerConfig {
	if c.Concurrency <= 0 {
		c.Concurrency = 4
	}
	if c.Concurrency > 4 {
		c.Concurrency = 4
	}
	if c.Concurrency < 2 {
		c.Concurrency = 2
	}
	if c.ScanInterval <= 0 {
		c.ScanInterval = 5 * time.Second
	}
	if c.ScanInterval > 10*time.Second {
		c.ScanInterval = 10 * time.Second
	}
	if c.Lease <= 0 {
		c.Lease = 60 * time.Second
	}
	if c.RenewInterval <= 0 {
		c.RenewInterval = c.Lease / 3
	}
	if c.Batch <= 0 {
		c.Batch = c.Concurrency
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 5
	}
	if c.ReaperInterval <= 0 {
		c.ReaperInterval = 5 * c.ScanInterval
	}
	if c.ReaperBatch <= 0 {
		c.ReaperBatch = 256
	}
	if c.RetryBackoff <= 0 {
		c.RetryBackoff = 2 * time.Second
	}
	if c.RetryMaxBackoff <= 0 {
		c.RetryMaxBackoff = 60 * time.Second
	}
	return c
}

// schedulerMetrics 是内部可观测计数（轻量，不与 prometheus 强耦合）。
type schedulerMetrics struct {
	scanned       atomic.Int64
	claimed       atomic.Int64
	completed     atomic.Int64
	failed        atomic.Int64
	requeued      atomic.Int64
	fenced        atomic.Int64 // 迟到 worker 被拒
	leaseExpired  atomic.Int64
	dispatchFails atomic.Int64
}

// GoalRunActionScheduler 是 durable continuation scheduler。
//
// 生命周期：NewXxx → Start（启动后台 goroutine）→ Stop（join）。
// 多次 Start 是 no-op；Stop 在 Start 之前是 safe no-op。
type GoalRunActionScheduler struct {
	store *goalrun.Store
	owner string
	disp  Dispatcher
	cfg   SchedulerConfig
	m     schedulerMetrics

	jobCh chan *goalrun.GoalRunAction
	wg    sync.WaitGroup

	stopOnce sync.Once
	cancel   context.CancelFunc
	done     chan struct{}
	started  atomic.Bool
}

// NewGoalRunActionScheduler 构造 scheduler。
//
// owner 必须是稳定的实例标识（用于 lease_owner 与 fencing）；通常取
// gateway instance id。
func NewGoalRunActionScheduler(store *goalrun.Store, owner string, disp Dispatcher, cfg SchedulerConfig) *GoalRunActionScheduler {
	if store == nil {
		panic("bg: goalrun action scheduler requires non-nil store")
	}
	if disp == nil {
		panic("bg: goalrun action scheduler requires non-nil dispatcher")
	}
	if owner == "" {
		panic("bg: goalrun action scheduler requires non-empty owner")
	}
	return &GoalRunActionScheduler{
		store: store,
		owner: owner,
		disp:  disp,
		cfg:   cfg.applyDefaults(),
		jobCh: make(chan *goalrun.GoalRunAction, cfg.applyDefaults().Concurrency),
		done:  make(chan struct{}),
	}
}

// Start 启动 scanner goroutine + worker pool。多次调用安全（idempotent）。
func (s *GoalRunActionScheduler) Start(ctx context.Context) {
	if !s.started.CompareAndSwap(false, true) {
		return
	}
	ctx, s.cancel = context.WithCancel(ctx)

	// 启动 worker pool：每个 worker 处理一个 action；concurrency = cfg.Concurrency
	for i := 0; i < s.cfg.Concurrency; i++ {
		s.wg.Add(1)
		go s.workerLoop(ctx, i)
	}

	// 启动 scanner + reaper
	s.wg.Add(1)
	go s.scannerLoop(ctx)
	s.wg.Add(1)
	go s.reaperLoop(ctx)

	slog.Info("goalrun action scheduler started",
		"owner", s.owner,
		"concurrency", s.cfg.Concurrency,
		"scan_interval", s.cfg.ScanInterval.String(),
		"lease", s.cfg.Lease.String())
}

// Stop 取消并等待所有 goroutine 退出。Start 之前调用是 no-op。
func (s *GoalRunActionScheduler) Stop() {
	if !s.started.Load() {
		return
	}
	s.stopOnce.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		close(s.jobCh) // 通知 workers drain 完毕
	})
	s.wg.Wait()
	close(s.done)
}

// Done 返回在 Stop 完成时关闭的 channel（用于测试）。
func (s *GoalRunActionScheduler) Done() <-chan struct{} { return s.done }

// Metrics 返回只读指标快照（用于测试与可观测集成）。
func (s *GoalRunActionScheduler) Metrics() SchedulerMetricsSnapshot {
	return SchedulerMetricsSnapshot{
		Scanned:       s.m.scanned.Load(),
		Claimed:       s.m.claimed.Load(),
		Completed:     s.m.completed.Load(),
		Failed:        s.m.failed.Load(),
		Requeued:      s.m.requeued.Load(),
		Fenced:        s.m.fenced.Load(),
		LeaseExpired:  s.m.leaseExpired.Load(),
		DispatchFails: s.m.dispatchFails.Load(),
	}
}

// SchedulerMetricsSnapshot 是 scheduler 的指标快照。
type SchedulerMetricsSnapshot struct {
	Scanned       int64
	Claimed       int64
	Completed     int64
	Failed        int64
	Requeued      int64
	Fenced        int64
	LeaseExpired  int64
	DispatchFails int64
}

// scannerLoop 周期性调用 ClaimRunnableActions 并把 claim 到的 action
// 投递到 jobCh。jobCh 已满时 block（背压）。
func (s *GoalRunActionScheduler) scannerLoop(ctx context.Context) {
	defer s.wg.Done()

	// 启动时立即扫一次，避免冷启积压
	s.scanAndDispatch(ctx)

	ticker := time.NewTicker(s.cfg.ScanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.scanAndDispatch(ctx)
		}
	}
}

// scanAndDispatch claim + 投递到 jobCh（异步由 workerLoop 取出执行）。
func (s *GoalRunActionScheduler) scanAndDispatch(ctx context.Context) {
	scanCtx, cancel := context.WithTimeout(ctx, s.cfg.ScanInterval)
	defer cancel()

	actions, err := s.store.ClaimRunnableActions(scanCtx, goalrun.ClaimActionOptions{
		Owner: s.owner,
		Lease: s.cfg.Lease,
		Batch: s.cfg.Batch,
	})
	if err != nil {
		slog.Warn("goalrun scheduler claim failed", "error", err, "owner", s.owner)
		return
	}

	for _, a := range actions {
		s.m.scanned.Add(1)
		s.m.claimed.Add(1)
		select {
		case <-ctx.Done():
			return
		case s.jobCh <- a:
		}
	}
}

// ProcessOnce 是测试 / 一次性执行入口：单次 claim + 同步处理直到 jobCh
// 排空，返回本次处理的 action 数量。
//
// 该方法不依赖 Start() 启动的 worker pool，可用于单元测试与冷启 path。
// bg 循环里实际工作由 scannerLoop + workerLoop 完成，这里只是把
// ClaimRunnableActions → processAction 串成同步链路。
func (s *GoalRunActionScheduler) ProcessOnce(ctx context.Context) int {
	scanCtx, cancel := context.WithTimeout(ctx, s.cfg.ScanInterval)
	defer cancel()

	actions, err := s.store.ClaimRunnableActions(scanCtx, goalrun.ClaimActionOptions{
		Owner: s.owner,
		Lease: s.cfg.Lease,
		Batch: s.cfg.Batch,
	})
	if err != nil {
		slog.Warn("goalrun scheduler claim failed", "error", err, "owner", s.owner)
		return 0
	}

	processed := 0
	for _, a := range actions {
		s.m.scanned.Add(1)
		s.m.claimed.Add(1)
		s.processAction(ctx, a, 0)
		processed++
	}
	return processed
}

// ExpireLeasesOnce 暴露一次 reaper pass（测试用）。
func (s *GoalRunActionScheduler) ExpireLeasesOnce(ctx context.Context) int64 {
	n, err := s.store.ExpireLeases(ctx, time.Time{}, s.cfg.ReaperBatch)
	if err != nil {
		slog.Warn("goalrun scheduler reaper failed", "error", err)
		return 0
	}
	if n > 0 {
		s.m.leaseExpired.Add(n)
	}
	return n
}

// RenewLeaseOnce 暴露一次续租（测试用）。fencing 命中返回 false。
func (s *GoalRunActionScheduler) RenewLeaseOnce(ctx context.Context, a *goalrun.GoalRunAction) bool {
	err := s.store.RenewActionLease(ctx, a.ActionID, a.LeaseOwner, a.FencingToken, time.Now().Add(s.cfg.Lease))
	if err != nil {
		if errors.Is(err, goalrun.ErrActionLeaseLost) {
			s.m.fenced.Add(1)
			return false
		}
		slog.Warn("goalrun scheduler renew failed", "action_id", a.ActionID, "error", err)
		return false
	}
	return true
}

// workerLoop 处理单 action 直到 ctx 取消或 jobCh 关闭。
func (s *GoalRunActionScheduler) workerLoop(ctx context.Context, workerID int) {
	defer s.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case a, ok := <-s.jobCh:
			if !ok {
				return
			}
			s.processAction(ctx, a, workerID)
		}
	}
}

// processAction 处理单 action：执行 + complete/requeue。
//
// 续租：每 RenewInterval 重置 lease_until，直到 processAction 结束。
func (s *GoalRunActionScheduler) processAction(ctx context.Context, a *goalrun.GoalRunAction, workerID int) {
	// 启动续租循环
	renewCtx, cancelRenew := context.WithCancel(ctx)
	defer cancelRenew()
	s.wg.Add(1)
	go s.renewLoop(renewCtx, a)

	defer func() {
		// 续租退出
		cancelRenew()
	}()

	// 调用 dispatcher
	succ, dispatchErr := s.dispatch(ctx, a)

	// 续租在 CAS 完成前必须已停止（避免晚到 renew 越过 complete CAS）
	cancelRenew()
	<-renewCtx.Done()
	_ = renewCtx

	if dispatchErr != nil {
		s.m.dispatchFails.Add(1)
		s.handleDispatchError(ctx, a, dispatchErr)
		return
	}

	// Dispatch 成功 → CompleteAction
	err := s.store.CompleteAction(ctx, goalrun.CompleteActionParams{
		ActionID:     a.ActionID,
		LeaseOwner:   a.LeaseOwner,
		FencingToken: a.FencingToken,
		NewStatus:    goalrun.ActionStatusCompleted,
	})
	if err != nil {
		if errors.Is(err, goalrun.ErrActionLeaseLost) {
			// 迟到 worker：副作用已发生但 CAS 失败。
			// 关键不变量：丢弃结果，绝不重做。
			// 在「已完成 successor」与「未记录 action completed」之间存在短暂
			// 不一致窗口，必须由后续 reaper / 对账路径发现；这不是 hot path。
			s.m.fenced.Add(1)
			slog.Warn("goalrun scheduler complete fenced (late worker, side effects kept)",
				"action_id", a.ActionID,
				"successor_id", succ.SuccessorID,
				"worker", workerID)
			return
		}
		slog.Warn("goalrun scheduler complete failed",
			"action_id", a.ActionID, "error", err)
		return
	}
	s.m.completed.Add(1)

	if succ != nil {
		slog.Info("goalrun action completed",
			"action_id", a.ActionID,
			"action_type", string(a.ActionType),
			"goal_run_id", a.GoalRunID,
			"successor_id", succ.SuccessorID,
			"worker", workerID)
	}
}

// renewLoop 周期性续租直到 ctx 取消。
//
// 续租失败（ErrActionLeaseLost）说明另一个 worker 已抢占 → 立即退出。
func (s *GoalRunActionScheduler) renewLoop(ctx context.Context, a *goalrun.GoalRunAction) {
	defer s.wg.Done()

	ticker := time.NewTicker(s.cfg.RenewInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			until := time.Now().Add(s.cfg.Lease)
			err := s.store.RenewActionLease(ctx, a.ActionID, a.LeaseOwner, a.FencingToken, until)
			if err != nil {
				if errors.Is(err, goalrun.ErrActionLeaseLost) {
					s.m.fenced.Add(1)
					return
				}
				slog.Warn("goalrun scheduler renew failed",
					"action_id", a.ActionID, "error", err)
				return
			}
		}
	}
}

// dispatch 路由到对应 Dispatcher 方法。
func (s *GoalRunActionScheduler) dispatch(ctx context.Context, a *goalrun.GoalRunAction) (*Successor, error) {
	switch a.ActionType {
	case goalrun.ActionTypeContinue:
		return s.disp.ContinueGoal(ctx, a)
	case goalrun.ActionTypeHandoff:
		return s.disp.ProposeHandoff(ctx, a)
	case goalrun.ActionTypeModelSwitch:
		return s.disp.SwitchModel(ctx, a)
	default:
		return nil, fmt.Errorf("goalrun scheduler: unsupported action type %q", a.ActionType)
	}
}

// handleDispatchError 根据 attempts 与错误决定重排 vs 失败。
//
// 简单重试策略：
//   - attempts >= MaxAttempts → CompleteAction(failed)
//   - 否则 RequeueAction(retry_at = now + backoff(attempts))
//
// 重试 backoff: RetryBackoff * 2^(attempts-1) ± 抖动，上限 RetryMaxBackoff。
func (s *GoalRunActionScheduler) handleDispatchError(ctx context.Context, a *goalrun.GoalRunAction, dispatchErr error) {
	if a.Attempts >= s.cfg.MaxAttempts {
		err := s.store.CompleteAction(ctx, goalrun.CompleteActionParams{
			ActionID:     a.ActionID,
			LeaseOwner:   a.LeaseOwner,
			FencingToken: a.FencingToken,
			NewStatus:    goalrun.ActionStatusFailed,
			LastError:    truncateError(dispatchErr.Error(), 1024),
		})
		if err != nil {
			s.m.fenced.Add(1)
			slog.Warn("goalrun scheduler final-fail complete fenced",
				"action_id", a.ActionID, "error", err)
			return
		}
		s.m.failed.Add(1)
		slog.Info("goalrun action failed permanently",
			"action_id", a.ActionID,
			"attempts", a.Attempts,
			"error", dispatchErr)
		return
	}

	// 指数退避 + ±20% 抖动
	base := s.cfg.RetryBackoff * (1 << minUint(uint(a.Attempts-1), 6))
	if base > s.cfg.RetryMaxBackoff {
		base = s.cfg.RetryMaxBackoff
	}
	jitter := time.Duration((rand.Float64()*0.4 - 0.2) * float64(base))
	retryAt := time.Now().Add(base + jitter)

	err := s.store.RequeueAction(ctx, goalrun.RequeueActionParams{
		ActionID:     a.ActionID,
		LeaseOwner:   a.LeaseOwner,
		FencingToken: a.FencingToken,
		RetryAt:      retryAt,
		LastError:    truncateError(dispatchErr.Error(), 1024),
	})
	if err != nil {
		s.m.fenced.Add(1)
		slog.Warn("goalrun scheduler requeue fenced",
			"action_id", a.ActionID, "error", err)
		return
	}
	s.m.requeued.Add(1)
	slog.Info("goalrun action requeued",
		"action_id", a.ActionID,
		"attempts", a.Attempts,
		"retry_at", retryAt.Format(time.RFC3339Nano))
}

// reaperLoop 周期性 ExpireLeases（清理租约过期的 running action）。
func (s *GoalRunActionScheduler) reaperLoop(ctx context.Context) {
	defer s.wg.Done()

	ticker := time.NewTicker(s.cfg.ReaperInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := s.store.ExpireLeases(ctx, time.Time{}, s.cfg.ReaperBatch)
			if err != nil {
				slog.Warn("goalrun scheduler reaper failed", "error", err)
				continue
			}
			if n > 0 {
				s.m.leaseExpired.Add(n)
				slog.Info("goalrun scheduler reaped expired leases", "count", n)
			}
		}
	}
}

// minUint 返回 a 与 0 中的较大者；用于位移上限保护。
func minUint(a, b uint) uint {
	if a < b {
		return a
	}
	return b
}

// truncateError 把 s 截断到 n 字节，避免 last_error 列被异常信息填爆。
func truncateError(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated)"
}
