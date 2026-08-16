package streaming

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/durable"
	"github.com/kaixuan/llm-gateway-go/metrics"
)

// durable_stream.go — SR-12 流式 durable 前台绑定（doc 18 §10.1/§11.3）。
//
// 前台 Coordinator 与后台 RecoveryWorker 使用完全相同的 lease holder 语义：
// 流式 durable 请求在快照切点 CreateAndClaim 后由 SurvivalCoordinator 持
// 租约在连接内执行；commit gate 的每次语义状态推进先经 Checkpoint 落
// commit_state（write-ahead，DB 失败禁写网络）；租约由 renewal goroutine
// 周期续期。结算矩阵：
//   - 成功（有捕获正文）→ CommitTerminal(completed)；
//   - 成功（无正文可存）→ failed/durable_result_unavailable（不假装可轮询）；
//   - 客户端断连且尚未过语义检查点 → Reschedule 交给 worker 后台重执行；
//   - 客户端断连且已过检查点 → 停止续租弃置，safety reaper（带 lease
//     护栏）在租约到期后终态化 resume_safety_blocked（§10.3 禁自动重放）；
//   - 前台终态失败 → CommitTerminal(failed)。

// DurableForegroundStore is the durable repository surface the foreground
// streaming path needs on top of the handler surface. *durable.Store
// satisfies it.
type DurableForegroundStore interface {
	DurableHandlerStore
	RenewLease(context.Context, string, string, int64, time.Time) error
	CheckpointCommitState(context.Context, durable.CheckpointParams) error
	PersistSettlementIntent(context.Context, durable.TerminalCommit) error
	ClaimSettlementIntent(context.Context, string, string, time.Duration, time.Time) (*durable.ClaimedSettlement, error)
	ClaimSettlementIntents(context.Context, string, time.Duration, int, time.Time) ([]*durable.ClaimedSettlement, error)
	FinalizeSettlement(context.Context, durable.ClaimedSettlement) (*durable.TerminalProjection, error)
	RetrySettlementIntent(context.Context, durable.ClaimedSettlement, time.Time, error) error
}

// DurableStreamBinding is the foreground lease/checkpoint/settlement
// context for one streaming durable request.
type DurableStreamBinding struct {
	store DurableForegroundStore
	task  *durable.Task
	lease time.Duration

	mu        sync.Mutex
	lastRank  int
	stopRenew chan struct{}
	renewDone chan struct{}
	// renewInterval is the ticker period of the renewal loop; Stop bounds its
	// wait on it so it never times out before a pending tick can drain.
	renewInterval time.Duration
}

var settlementRetryDelays = [...]time.Duration{
	50 * time.Millisecond,
	100 * time.Millisecond,
	200 * time.Millisecond,
}

func newDurableStreamBinding(store DurableForegroundStore, task *durable.Task, lease time.Duration) *DurableStreamBinding {
	if lease <= 0 {
		lease = 15 * time.Second
	}
	return &DurableStreamBinding{store: store, task: task, lease: lease}
}

// Start launches the lease renewal loop (every lease/2) until Stop.
func (b *DurableStreamBinding) Start() {
	b.mu.Lock()
	if b.stopRenew != nil {
		b.mu.Unlock()
		return
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	b.stopRenew, b.renewDone = stop, done
	b.mu.Unlock()

	interval := b.lease / 2
	if interval <= 0 {
		interval = time.Second
	}
	b.mu.Lock()
	b.renewInterval = interval
	b.mu.Unlock()
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				err := b.store.RenewLease(ctx, b.task.ID, b.task.LeaseOwner, b.task.FencingToken, time.Now().Add(b.lease))
				cancel()
				if err != nil {
					// Fence lost or DB unavailable: stop renewing. Post-content
					// tasks are unclaimable (commit_state gate) and the safety
					// reaper owns them; pre-content tasks may be re-claimed by a
					// worker — the client keeps our stream either way.
					if err == durable.ErrLeaseLost {
						metrics.SurvivalLeaseConflictsTotal.Inc()
						metrics.DurableLeaseLostTotal.Inc()
					}
					slog.Warn("durable foreground lease renewal stopped", "task_id", b.task.ID, "error", err)
					return
				}
			}
		}
	}()
}

// Stop ends the renewal loop. Idempotent; bounded wait so a stuck renewal
// call cannot pin the request goroutine.
func (b *DurableStreamBinding) Stop() {
	b.mu.Lock()
	stop, done := b.stopRenew, b.renewDone
	b.stopRenew, b.renewDone = nil, nil
	// Worst-case wait: a pending renewal tick may be mid-flight when we close
	// stop. The loop's select can defer up to one full interval before seeing
	// stop, then a RenewLease call can run up to its 5s timeout. Bound the
	// wait on that sum so Stop returns promptly but never times out early
	// (a short fixed grace < interval would spurious-timeout ~every stop).
	grace := b.renewInterval + 6*time.Second
	if grace < 3*time.Second {
		grace = 3 * time.Second
	}
	b.mu.Unlock()
	if stop == nil {
		return
	}
	close(stop)
	select {
	case <-done:
	case <-time.After(grace):
		slog.Warn("durable foreground renewal stop grace exceeded", "task_id", b.task.ID)
	}
}

// Checkpoint adapts the gate write-ahead hook. The durable SQL only accepts
// rank advances (equal rank reads as 0 rows → ErrLeaseLost), so retried
// attempts — whose gate state resets to none — must be deduped against the
// highest rank already persisted. Uses a detached bounded context: the
// checkpoint must outlive a disconnecting client.
func (b *DurableStreamBinding) Checkpoint(s CommitState) error {
	state := durable.CommitState(s.String())
	rank := durable.CommitStateRank(state)
	if rank < 0 {
		return fmt.Errorf("durable stream: unknown commit state %q", s)
	}
	b.mu.Lock()
	dupe := rank <= b.lastRank
	b.mu.Unlock()
	if dupe {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := b.store.CheckpointCommitState(ctx, durable.CheckpointParams{
		TaskID:       b.task.ID,
		LeaseOwner:   b.task.LeaseOwner,
		FencingToken: b.task.FencingToken,
		State:        state,
	}); err != nil {
		return err
	}
	b.mu.Lock()
	if rank > b.lastRank {
		b.lastRank = rank
	}
	b.mu.Unlock()
	return nil
}

// ContentCommitted reports whether semantic content was checkpointed —
// i.e. the resume-safety gate has closed (doc 18 §10.3).
func (b *DurableStreamBinding) ContentCommitted() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastRank >= durable.CommitStateRank(durable.CommitStateContent)
}

// Complete persists a terminal intent before attempting immediate finalization.
func (b *DurableStreamBinding) Complete(ctx context.Context, body []byte, contentType string) error {
	return b.settleTerminal(ctx, durable.TerminalCommit{
		Task:        b.task,
		Outcome:     durable.StatusCompleted,
		Body:        body,
		ContentType: contentType,
		Attempt:     b.task.AttemptCount,
	})
}

// FailTerminal persists a terminal intent before attempting immediate finalization.
func (b *DurableStreamBinding) FailTerminal(ctx context.Context, reason, kind string) error {
	return b.settleTerminal(ctx, durable.TerminalCommit{
		Task:       b.task,
		Outcome:    durable.StatusFailed,
		ReasonCode: reason,
		ErrorKind:  kind,
		Attempt:    b.task.AttemptCount,
	})
}

func (b *DurableStreamBinding) settleTerminal(ctx context.Context, c durable.TerminalCommit) error {
	if err := retrySettlement(ctx, func() error {
		return b.store.PersistSettlementIntent(ctx, c)
	}); err != nil {
		return err
	}
	claim, err := b.store.ClaimSettlementIntent(ctx, b.task.ID, b.task.LeaseOwner, b.lease, time.Now())
	if err != nil || claim == nil {
		return err
	}
	_, err = b.store.FinalizeSettlement(ctx, *claim)
	if err != nil && err != durable.ErrLeaseLost {
		if retryErr := b.store.RetrySettlementIntent(ctx, *claim, time.Now().Add(2*time.Second), err); retryErr != nil {
			return retryErr
		}
	}
	return err
}

// ReleaseToWorker hands the uncommitted task to the recovery worker.
func (b *DurableStreamBinding) ReleaseToWorker(ctx context.Context, reason string) error {
	return b.store.Reschedule(ctx, durable.RescheduleParams{
		TaskID:       b.task.ID,
		LeaseOwner:   b.task.LeaseOwner,
		FencingToken: b.task.FencingToken,
		NextRetryAt:  time.Now(),
		Reason:       reason,
		Attempt:      b.task.AttemptCount,
	})
}

// releaseDurableBeforeSurvival hands a foreground-claimed task to the worker
// when a handler fails after the snapshot cut point but before the coordinator
// can own its lease. No semantic bytes reached the client, so recovery is safe
// and must not wait for the frontend lease to expire.
func releaseDurableBeforeSurvival(b *DurableStreamBinding, reason string) {
	if b == nil {
		return
	}
	ctx, cancel := settleCtx()
	defer cancel()
	if err := b.ReleaseToWorker(ctx, reason); err != nil {
		logSettleError("release_before_survival", b, err)
	}
	b.Stop()
}

// settleCtx bounds settlement store writes: they run after the survival
// loop ends and must survive a disconnected client, but must not pin the
// handler forever.
func settleCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

// settleDurableStream finalizes the foreground task after the survival loop
// ends. clientDisconnected reports whether the connection dropped (the
// coordinator's client_disconnected verdict). body is the captured wire
// output (may be nil when capture is unavailable or oversize).
//
// Worst-case failure window (doc 18 §10.3 / SR-W3):
//
//   - PersistSettlementIntent exhausted -> task stays `running`, lease
//     eventually expires -> ReapDeadlines owns it (default 15s lease).
//   - ClaimSettlementIntent exhausted -> an intent row exists; the worker
//     drainSettlementIntents loop picks it up on its next tick (5s).
//   - FinalizeSettlement non-lease-loss error -> the intent row is parked
//     via RetrySettlementIntent; the worker retries with bounded backoff.
//   - ReleaseToWorker (reschedule) failure -> task stays `running`; safety
//     reaper / lease expiry own it; never re-execute, never replay.
//
// None of these branches invents a foreground fall-back write — re-execution
// safety depends on the safety reaper so the post-content
// "resume_safety_blocked" / no-replay contract is preserved end-to-end.
// Stage failures are surfaced via
// durable_settlement_stage_failures_total{stage=...} so an alert can catch
// the worst-case window before the reaper closes it.
func settleDurableStream(_ context.Context, b *DurableStreamBinding, res SurvivalResult, body []byte, contentType string, clientDisconnected bool) {
	if b == nil {
		return
	}
	ctx, cancel := settleCtx()
	defer cancel()
	// Keep the lease until the settlement write lands, then stop renewing.
	defer b.Stop()

	switch {
	case res.Succeed:
		if len(body) > 0 {
			if err := b.Complete(ctx, body, contentType); err != nil {
				logSettleError("complete", b, err)
			}
			return
		}
		// The client consumed the stream, but there is no honest body to
		// persist for pollers — record the outcome instead of a
		// completed-with-empty-body lie.
		if err := b.FailTerminal(ctx, "durable_result_unavailable", "durable_result"); err != nil {
			logSettleError("result_unavailable", b, err)
		}
	case clientDisconnected && !b.ContentCommitted():
		// The durable promise: the worker re-executes detached and the
		// client may poll for the result.
		if err := b.ReleaseToWorker(ctx, "client_disconnected_pre_content"); err != nil {
			logSettleError("release_to_worker", b, err)
		}
	case clientDisconnected:
		// Content already reached the client; transparent re-execution
		// would duplicate side effects. Stop renewing and let the safety
		// reaper (lease guard) terminalize resume_safety_blocked.
		slog.Warn("durable foreground abandoned after content; safety reaper will terminalize",
			"task_id", b.task.ID)
	default:
		kind := ""
		if res.FinalAttempt != nil {
			kind = string(res.FinalAttempt.LastKind())
		}
		if err := b.FailTerminal(ctx, res.Decision.Reason, kind); err != nil {
			logSettleError("fail_terminal", b, err)
		}
	}
}

func retrySettlement(ctx context.Context, write func() error) error {
	var err error
	for attempt, delay := range settlementRetryDelays {
		err = write()
		if err == nil || errors.Is(err, durable.ErrLeaseLost) {
			return err
		}
		if attempt == len(settlementRetryDelays)-1 {
			break
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return fmt.Errorf("durable settlement failed after %d attempts: %w", attempt+1, err)
		case <-timer.C:
		}
	}
	return fmt.Errorf("durable settlement failed after %d attempts: %w", len(settlementRetryDelays), err)
}

// logSettleError is the single funnel for foreground settlement write
// failures. It records the durable_settlement_stage_failures_total{stage}
// signal so an alert can detect a task that entered the worst-case window
// (stuck in `running` until the safety reaper / lease-expiry owns it).
// Lease-loss is counted in both observability families and does not
// increment the stage failure metric — losing the lease is a benign
// ownership change, not a stuck write.
func logSettleError(stage string, b *DurableStreamBinding, err error) {
	if err == durable.ErrLeaseLost {
		metrics.SurvivalLeaseConflictsTotal.Inc()
		metrics.DurableLeaseLostTotal.Inc()
		slog.Warn("durable foreground settlement step fenced off", "stage", stage, "task_id", b.task.ID, "error", err)
		return
	}
	metrics.DurableSettlementStageFailuresTotal.WithLabelValues(stage).Inc()
	slog.Warn("durable foreground settlement step failed; task left to safety reaper",
		"stage", stage, "task_id", b.task.ID, "error", err)
}

// durableWireCapture tees the committed wire bytes of one durable stream so
// the completed terminal can persist an honest pollable body. Bounded: on
// overflow the capture reports nil (the settlement then records
// durable_result_unavailable instead of storing a truncated lie).
type durableWireCapture struct {
	w http.ResponseWriter

	mu         sync.Mutex
	buf        []byte
	overflowed bool
	limit      int
}

// DefaultDurableResultCaptureLimit bounds the durable stream result body.
const DefaultDurableResultCaptureLimit = 1 << 20 // 1 MiB

func newDurableWireCapture(w http.ResponseWriter, limit int) *durableWireCapture {
	if limit <= 0 {
		limit = DefaultDurableResultCaptureLimit
	}
	return &durableWireCapture{w: w, limit: limit}
}

func (c *durableWireCapture) Header() http.Header { return c.w.Header() }

func (c *durableWireCapture) WriteHeader(status int) {
	if hw, ok := c.w.(interface{ WriteHeader(int) }); ok {
		hw.WriteHeader(status)
	}
}

func (c *durableWireCapture) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.mu.Lock()
	if err != nil {
		// A failed downstream write means bytes may not have reached the
		// client — persisting them as the durable pollable body would lie
		// about reachability. Mark the capture as overflowed so the
		// settlement path records durable_result_unavailable instead.
		c.overflowed = true
		c.buf = nil
	} else if !c.overflowed {
		if len(c.buf)+n > c.limit {
			c.overflowed = true
			c.buf = nil
		} else {
			c.buf = append(c.buf, p[:n]...)
		}
	}
	c.mu.Unlock()
	return n, err
}

// Flush delegates to the wrapped writer when it supports flushing.
func (c *durableWireCapture) Flush() {
	if f, ok := c.w.(interface{ Flush() }); ok {
		f.Flush()
	}
}

// result returns the captured wire bytes and content type, or nil when the
// capture overflowed (never a truncated body).
func (c *durableWireCapture) result() ([]byte, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.overflowed || len(c.buf) == 0 {
		return nil, ""
	}
	return c.buf, "text/event-stream"
}
