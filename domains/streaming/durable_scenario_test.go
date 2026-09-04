package streaming

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/durable"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/pending"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// durable_scenario_test.go — M4 故障注入场景 S30-S35（doc 18 §16.4）。
//
// 与决策级单测的区别：这里把**真实组件**（DurableRecoveryWorker、
// DurableAttemptRunnerImpl、SurvivalCoordinator、settleDurableStream 结算
// 矩阵）串成端到端链路，跑在一个单任务内存 store 上。该 store 逐条镜像
// durable SQL 语义（fencing 条件、commit_state 运行门禁、claim 互斥、
// 双 reaper 谓词），所以场景验证的是组件间协议，不是被测代码自己的复述。
// 部署态注入（真 PG/Redis/上游 429）属 deploy-test 轨道，本文件不宣称。

// memDurableStore mirrors the durable SQL lifecycle for ONE task. Each
// method documents the SQL predicate it mirrors; deviations would show up
// as scenario failures against the real worker.
type memDurableStore struct {
	mu          sync.Mutex
	task        durable.Task
	snapshot    []byte
	terminals   []durable.TerminalCommit
	projections int
	settlement  *durable.TerminalCommit
	claimed     bool
	finalizeErr error
	createErr   error
	history     json.RawMessage
}

func (s *memDurableStore) LoadDecisionHistory(_ context.Context, _ string) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.history, nil
}

func (s *memDurableStore) SaveDecisionHistory(_ context.Context, _ string, _ string, _ int64, h json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.history = h
	return nil
}

func (s *memDurableStore) snapshotTask() durable.Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.task
}

func (s *memDurableStore) status() durable.Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.task.Status
}

func (s *memDurableStore) terminalCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.terminals)
}

// CreateAndClaim mirrors store.go: insert running + lease + fencing 1.
func (s *memDurableStore) CreateAndClaim(_ context.Context, n durable.NewTask) (*durable.Task, error) {
	if err := n.Validate(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.task.ID != "" {
		return nil, durable.ErrDuplicateTask
	}
	now := time.Now()
	s.task = durable.Task{
		ID: "task-s", TenantID: n.TenantID, RequestID: n.RequestID, SessionID: n.SessionID,
		Protocol: n.Protocol, Endpoint: n.Endpoint,
		Status: durable.StatusRunning, CommitState: durable.CommitStateNone,
		AttemptCount: 1, FencingToken: 1,
		LeaseOwner: n.LeaseOwner, LeaseUntil: n.LeaseUntil,
		NextRetryAt: now, DeadlineAt: n.DeadlineAt, ExpiresAt: n.ExpiresAt,
		RequestHash: n.RequestHash, SnapshotVersion: n.SnapshotVersion,
		CreatedAt: now, UpdatedAt: now,
	}
	s.snapshot = n.Snapshot
	return &s.task, nil
}

// ClaimRunnable mirrors claimSelectSQL: commit_state gate + runnable status
// + expired-or-absent lease + next_retry_at <= now + deadline > now; claim
// bumps fencing and takes a fresh lease.
func (s *memDurableStore) ClaimRunnable(_ context.Context, opts durable.ClaimOptions) ([]*durable.Task, error) {
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := &s.task
	runnable := durable.IsRunnableCommitState(t.CommitState) &&
		(t.Status == durable.StatusWaitingRecovery || t.Status == durable.StatusRetryScheduled || t.Status == durable.StatusRunning) &&
		!(t.Status == durable.StatusRunning && !t.LeaseUntil.Before(now)) &&
		!t.NextRetryAt.After(now) && t.DeadlineAt.After(now) &&
		(t.LeaseUntil.IsZero() || t.LeaseUntil.Before(now))
	if s.settlement != nil || !runnable {
		return nil, nil
	}
	t.Status = durable.StatusRunning
	t.LeaseOwner = opts.Owner
	t.LeaseUntil = now.Add(opts.Lease)
	t.FencingToken++
	cp := *t
	return []*durable.Task{&cp}, nil
}

// RenewLease mirrors UPDATE ... WHERE lease_owner AND fencing_token AND
// status='running'; 0 rows → ErrLeaseLost.
func (s *memDurableStore) RenewLease(_ context.Context, taskID, owner string, token int64, until time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := &s.task
	if t.ID != taskID || t.LeaseOwner != owner || t.FencingToken != token || t.Status != durable.StatusRunning {
		return durable.ErrLeaseLost
	}
	t.LeaseUntil = until
	return nil
}

// Reschedule mirrors RescheduleParams SQL: running + owner + token +
// commit_state gate → retry_scheduled with lease cleared.
func (s *memDurableStore) Reschedule(_ context.Context, p durable.RescheduleParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := &s.task
	if t.ID != p.TaskID || t.LeaseOwner != p.LeaseOwner || t.FencingToken != p.FencingToken ||
		t.Status != durable.StatusRunning || !durable.IsRunnableCommitState(t.CommitState) {
		return durable.ErrLeaseLost
	}
	t.Status = durable.StatusRetryScheduled
	t.NextRetryAt = p.NextRetryAt
	t.LeaseOwner, t.LeaseUntil = "", time.Time{}
	t.AttemptCount++
	return nil
}

// CheckpointCommitState mirrors the rank-advance UPDATE with owner+token.
func (s *memDurableStore) CheckpointCommitState(_ context.Context, p durable.CheckpointParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := &s.task
	rank := durable.CommitStateRank(p.State)
	if t.ID != p.TaskID || t.LeaseOwner != p.LeaseOwner || t.FencingToken != p.FencingToken ||
		t.Status != durable.StatusRunning || durable.CommitStateRank(t.CommitState) >= rank {
		return durable.ErrLeaseLost
	}
	t.CommitState = p.State
	if rank >= durable.CommitStateRank(durable.CommitStateContent) {
		t.SemanticContentCommitted = true
	}
	return nil
}

// CommitTerminal mirrors the fenced atomic terminal: only the current
// holder of the latest token commits; terminal-on-terminal is rejected.
func (s *memDurableStore) CommitTerminal(_ context.Context, c durable.TerminalCommit) (*durable.TerminalProjection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := &s.task
	if t.ID != c.Task.ID || t.LeaseOwner != c.Task.LeaseOwner || t.FencingToken != c.Task.FencingToken ||
		t.Status != durable.StatusRunning {
		return nil, durable.ErrLeaseLost
	}
	if !durable.IsTerminalStatus(c.Outcome) {
		return nil, durable.ErrLeaseLost
	}
	t.Status = c.Outcome
	t.ReasonCode = c.ReasonCode
	t.CompletedAt = time.Now()
	s.terminals = append(s.terminals, c)
	return &durable.TerminalProjection{
		Committed: true, TaskID: t.ID, TenantID: t.TenantID, RequestID: t.RequestID,
		SessionID: t.SessionID, Status: c.Outcome, Body: string(c.Body),
		ContentType: c.ContentType, FencingToken: t.FencingToken, CompletedAt: t.CompletedAt,
		ExpiresAt: t.ExpiresAt,
	}, nil
}

func (s *memDurableStore) PersistSettlementIntent(_ context.Context, c durable.TerminalCommit) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := &s.task
	if c.Task == nil || t.ID != c.Task.ID || t.LeaseOwner != c.Task.LeaseOwner || t.FencingToken != c.Task.FencingToken || durable.IsTerminalStatus(t.Status) {
		return durable.ErrLeaseLost
	}
	if s.settlement != nil {
		return nil
	}
	copy := c
	s.settlement = &copy
	return nil
}

func (s *memDurableStore) ClaimSettlementIntent(_ context.Context, taskID, owner string, lease time.Duration, now time.Time) (*durable.ClaimedSettlement, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settlement == nil || s.claimed || s.settlement.Task == nil || s.settlement.Task.ID != taskID {
		return nil, nil
	}
	c := s.settlement
	s.claimed = true
	return &durable.ClaimedSettlement{
		SettlementIntent: durable.SettlementIntent{
			TaskID: c.Task.ID, TenantID: c.Task.TenantID, RequestID: c.Task.RequestID, SessionID: c.Task.SessionID,
			RequestHash: c.Task.RequestHash, SourceOwner: c.Task.LeaseOwner, SourceFencingToken: c.Task.FencingToken,
			Outcome: c.Outcome, Body: c.Body, ContentType: c.ContentType, ReasonCode: c.ReasonCode, ErrorKind: c.ErrorKind, Attempt: c.Attempt,
		},
		ClaimOwner: owner, ClaimUntil: now.Add(lease), ClaimFencingToken: 1,
	}, nil
}

func (s *memDurableStore) ClaimSettlementIntents(_ context.Context, owner string, lease time.Duration, _ int, now time.Time) ([]*durable.ClaimedSettlement, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settlement == nil || s.claimed {
		return nil, nil
	}
	c := s.settlement
	s.claimed = true
	return []*durable.ClaimedSettlement{{
		SettlementIntent: durable.SettlementIntent{
			TaskID: c.Task.ID, TenantID: c.Task.TenantID, RequestID: c.Task.RequestID, SessionID: c.Task.SessionID,
			RequestHash: c.Task.RequestHash, SourceOwner: c.Task.LeaseOwner, SourceFencingToken: c.Task.FencingToken,
			Outcome: c.Outcome, Body: c.Body, ContentType: c.ContentType, ReasonCode: c.ReasonCode, ErrorKind: c.ErrorKind, Attempt: c.Attempt,
		},
		ClaimOwner: owner, ClaimUntil: now.Add(lease), ClaimFencingToken: 1,
	}}, nil
}

func (s *memDurableStore) FinalizeSettlement(_ context.Context, c durable.ClaimedSettlement) (*durable.TerminalProjection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settlement == nil || !s.claimed {
		return nil, durable.ErrLeaseLost
	}
	if s.finalizeErr != nil {
		return nil, s.finalizeErr
	}
	t := &s.task
	if t.ID != c.TaskID || t.LeaseOwner != c.SourceOwner || t.FencingToken != c.SourceFencingToken || t.Status != durable.StatusRunning {
		return nil, durable.ErrLeaseLost
	}
	t.Status, t.ReasonCode, t.CompletedAt = c.Outcome, c.ReasonCode, time.Now()
	t.CommitState, t.SemanticContentCommitted = durable.CommitStateTerminal, true
	s.terminals = append(s.terminals, *s.settlement)
	s.settlement, s.claimed = nil, false
	return &durable.TerminalProjection{Committed: true, TaskID: t.ID, TenantID: t.TenantID, RequestID: t.RequestID, SessionID: t.SessionID, Status: c.Outcome, Body: string(c.Body), ContentType: c.ContentType, FencingToken: t.FencingToken, CompletedAt: t.CompletedAt, ExpiresAt: t.ExpiresAt}, nil
}

func (s *memDurableStore) RetrySettlementIntent(_ context.Context, _ durable.ClaimedSettlement, _ time.Time, _ error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settlement == nil || !s.claimed {
		return durable.ErrLeaseLost
	}
	s.claimed = false
	return nil
}

// ReapDeadlines mirrors the deadline reaper: non-terminal past deadline →
// expired.
func (s *memDurableStore) ReapDeadlines(_ context.Context, _ int, now time.Time) ([]*durable.ReapedTaskInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := &s.task
	if s.settlement != nil {
		return nil, nil
	}
	if !durable.IsTerminalStatus(t.Status) && t.Status != durable.StatusResumeSafetyBlocked && !t.DeadlineAt.After(now) {
		t.Status = durable.StatusExpired
		t.FencingToken++
		t.LeaseOwner, t.LeaseUntil = "", time.Time{}
	}
	return nil, nil
}

// ReapUnsafeCheckpointed mirrors the safety reaper with the lease guard
// (doc 28): only abandoned (expired/absent lease) tasks past the semantic
// checkpoint move to resume_safety_blocked.
func (s *memDurableStore) ReapUnsafeCheckpointed(_ context.Context, _ int, now time.Time) ([]*durable.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := &s.task
	if !durable.IsTerminalStatus(t.Status) && t.Status != durable.StatusResumeSafetyBlocked &&
		durable.CommitStateRank(t.CommitState) >= durable.CommitStateRank(durable.CommitStateContent) &&
		(t.LeaseUntil.IsZero() || t.LeaseUntil.Before(now)) {
		t.Status = durable.StatusResumeSafetyBlocked
		t.FencingToken++
		t.LeaseOwner, t.LeaseUntil = "", time.Time{}
	}
	return nil, nil
}

// ProjectPendingOutbox counts terminal→pending projections.
func (s *memDurableStore) ProjectPendingOutbox(context.Context, *pending.Store, int, time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if durable.IsTerminalStatus(s.task.Status) && s.task.Status == durable.StatusCompleted {
		s.projections++
		return 1, nil
	}
	return 0, nil
}

func (s *memDurableStore) ActiveTaskCounts(context.Context) (map[string]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if durable.IsTerminalStatus(s.task.Status) || s.task.Status == durable.StatusResumeSafetyBlocked {
		return map[string]int64{}, nil
	}
	return map[string]int64{s.task.TenantID: 1}, nil
}

func (s *memDurableStore) LoadSnapshot(_ context.Context, taskID string) (*durable.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.task.ID != taskID {
		return nil, durable.ErrLeaseLost
	}
	return &durable.Snapshot{
		TaskID: s.task.ID, TenantID: s.task.TenantID, RequestID: s.task.RequestID,
		RequestHash: s.task.RequestHash, Version: s.task.SnapshotVersion,
		EncryptionKeyID: "mem", Body: s.snapshot,
	}, nil
}

// expireLease simulates the passage of time past the holder's lease.
func (s *memDurableStore) expireLease() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.task.LeaseUntil = time.Now().Add(-time.Minute)
}

// scenarioHarness assembles the real worker + real runner over the
// scenario store, with counting fake execution collaborators.
type scenarioHarness struct {
	store    *memDurableStore
	worker   *DurableRecoveryWorker
	runner   *DurableAttemptRunnerImpl
	exec     *countingExecutor
	verifier *durableRunnerVerifierFake
	resolver *durableRunnerResolverFake
}

// countingExecutor is the terminal fake: every upstream call is visible.
type countingExecutor struct {
	calls   int
	result  *executors.ExecuteResult
	execErr error
}

func (e *countingExecutor) Execute(params *executors.ExecParams) (*executors.ExecuteResult, error) {
	e.calls++
	copied := *params
	_ = copied
	if e.execErr != nil {
		return nil, e.execErr
	}
	if e.result == nil {
		e.result = &executors.ExecuteResult{
			ResponseBody: []byte(`{"choices":[{"message":{"content":"hi"}}]}`),
			Response:     &http.Response{Header: http.Header{"Content-Type": []string{"application/json"}}},
		}
	}
	return e.result, nil
}

func newScenarioHarness(t *testing.T) *scenarioHarness {
	t.Helper()
	h := &scenarioHarness{
		store: &memDurableStore{},
		exec:  &countingExecutor{},
	}
	profile := "p1"
	h.verifier = &durableRunnerVerifierFake{info: &authentication.KeyInfo{
		ID: 42, TenantID: "tenant-s", ApplicationID: 7, DefaultClientProfile: &profile,
	}}
	h.resolver = &durableRunnerResolverFake{cands: []provider.Candidate{{ProviderID: 3}}}
	h.runner = NewDurableAttemptRunner(h.exec, h.resolver, h.verifier)
	h.worker = NewDurableRecoveryWorker(h.store, nil, h.runner, DurableWorkerOptions{
		Owner: "worker-b", Lease: time.Minute, RetryBase: time.Millisecond,
	})
	return h
}

// createScenarioTask inserts a durable task exactly like the handler's
// non-stream handoff would (CreateAndClaim then worker-ready retry row).
func scenarioSnapshotJSON(t *testing.T) []byte {
	t.Helper()
	body, err := MarshalDurableSnapshotV1(DurableRequestSnapshotV1{
		Version: DurableSnapshotVersionV1, Endpoint: "/v1/chat/completions",
		ClientProtocol: "openai-completions", ClientModel: "gpt-4o",
		NormalizedBody: []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`),
		RequestHash:    "hash-s", APIKeyID: 42, TenantID: "tenant-s", ApplicationID: 7,
		SessionID: "sess-s", ClientIdentityHash: "identityhash",
		PolicyVersion: DurablePolicyVersionV1, RequestID: "req-s", TaskCorrelationID: "req-s",
	})
	if err != nil {
		t.Fatalf("marshal scenario snapshot: %v", err)
	}
	return body
}

func (h *scenarioHarness) createTask(t *testing.T, owner string) *durable.Task {
	t.Helper()
	task, err := h.store.CreateAndClaim(context.Background(), durable.NewTask{
		TenantID: "tenant-s", RequestID: "req-s", SessionID: "sess-s",
		Protocol: "openai-completions", Endpoint: "/v1/chat/completions",
		Snapshot: scenarioSnapshotJSON(t), SnapshotVersion: DurableSnapshotVersionV1,
		RequestHash: "hash-s",
		DeadlineAt:  time.Now().Add(time.Hour), ExpiresAt: time.Now().Add(2 * time.Hour),
		LeaseOwner: owner, LeaseUntil: time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	return task
}

// S31_survival_disconnect_durable: the client disconnects before any
// semantic checkpoint; the settlement releases the task and the REAL
// recovery worker + runner complete it detached. One execution, one
// terminal, one projection.
func TestS31_DisconnectDurableCompletesDetached(t *testing.T) {
	h := newScenarioHarness(t)
	task := h.createTask(t, "gw-front")
	binding := newDurableStreamBinding(h.store, task, time.Minute)

	// Stream starts, client drops before the first semantic frame.
	settleDurableStream(context.Background(), binding,
		SurvivalResult{Decision: TaskDecision{Action: TaskActionFailClosed, Reason: "client_disconnected"}},
		nil, "", true)

	if got := h.store.status(); got != durable.StatusRetryScheduled {
		t.Fatalf("post-disconnect status = %v, want retry_scheduled", got)
	}

	h.worker.runOnce(context.Background())

	if got := h.store.status(); got != durable.StatusCompleted {
		t.Fatalf("worker status = %v, want completed", got)
	}
	if h.exec.calls != 1 {
		t.Fatalf("upstream executions = %d, want exactly 1", h.exec.calls)
	}
	if h.store.terminalCount() != 1 {
		t.Fatalf("terminals = %d, want 1 (no duplicate final commits)", h.store.terminalCount())
	}
	// The completed row must be projectable to the pending cache (the
	// redis-enabled worker tick); worker skips it with nil redis by design.
	if n, _ := h.store.ProjectPendingOutbox(context.Background(), nil, 8, time.Now()); n != 1 {
		t.Fatalf("pending projections = %d, want 1", n)
	}
	if h.store.projections != 1 {
		t.Fatalf("projection counter = %d, want 1", h.store.projections)
	}
}

// S32_survival_gateway_restart: the frontend dies right after
// CreateAndClaim with no settlement at all. While its lease is alive the
// worker must NOT touch the task; after expiry the worker claims it
// (fencing bumped) and completes.
func TestS32_GatewayRestartRecoversAfterLeaseExpiry(t *testing.T) {
	h := newScenarioHarness(t)
	h.createTask(t, "gw-front-A")

	// Restart: instance B's worker runs while A's lease is still valid.
	h.worker.runOnce(context.Background())
	if h.exec.calls != 0 {
		t.Fatalf("worker must not claim a live-lease task, exec = %d", h.exec.calls)
	}
	if got := h.store.status(); got != durable.StatusRunning {
		t.Fatalf("status = %v, want untouched running", got)
	}

	// Lease expires; B claims and completes.
	h.store.expireLease()
	h.worker.runOnce(context.Background())

	if got := h.store.status(); got != durable.StatusCompleted {
		t.Fatalf("status = %v, want completed", got)
	}
	if h.exec.calls != 1 {
		t.Fatalf("upstream executions = %d, want 1", h.exec.calls)
	}
	if tk := h.store.snapshotTask().FencingToken; tk != 2 {
		t.Fatalf("fencing token = %d, want 2 (claim bumped)", tk)
	}
}

// S33_survival_multi_instance_fencing: after B reclaims the abandoned
// task, the stale frontend holder (old owner+token) must be fenced off —
// its terminal commit is rejected and B's single result stands.
func TestS33_MultiInstanceFencingRejectsStaleHolder(t *testing.T) {
	h := newScenarioHarness(t)
	stale := h.createTask(t, "gw-front-A")
	bindingA := newDurableStreamBinding(h.store, stale, time.Minute)

	// A dies; lease expires; B reclaims and completes.
	h.store.expireLease()
	h.worker.runOnce(context.Background())
	if got := h.store.status(); got != durable.StatusCompleted {
		t.Fatalf("worker status = %v, want completed", got)
	}

	// A wakes up with its stale owner+token and tries to complete.
	if err := bindingA.Complete(context.Background(), []byte("stale bytes"), "text/event-stream"); err == nil {
		t.Fatal("stale holder commit must be rejected (fencing)")
	}
	if h.store.terminalCount() != 1 {
		t.Fatalf("terminals = %d, want exactly 1", h.store.terminalCount())
	}
	h.worker.runOnce(context.Background())
	if h.exec.calls != 1 {
		t.Fatalf("executions = %d, want 1 (no re-execution of a terminal task)", h.exec.calls)
	}
}

// S34_survival_permanent_error_fast_fail: a revoked API key re-verified by
// the runner is a permanent terminal — the worker fails the task on the
// first pass and never calls the upstream executor.
func TestS34_PermanentErrorFailsFastWithoutUpstreamCall(t *testing.T) {
	h := newScenarioHarness(t)
	h.verifier.err = &authentication.InvalidKeyError{Message: "revoked"}
	h.createTask(t, "gw-front")
	h.store.expireLease()

	h.worker.runOnce(context.Background())

	if got := h.store.status(); got != durable.StatusFailed {
		t.Fatalf("status = %v, want failed", got)
	}
	if h.exec.calls != 0 {
		t.Fatalf("upstream executions = %d, want 0 (revoked key must not reach upstream)", h.exec.calls)
	}
}

// S35_survival_tool_call_replay_blocked: tool-call output was committed to
// the client before the disconnect. The settlement abandons the task and
// the safety reaper (lease guard) terminalizes resume_safety_blocked; the
// worker NEVER re-executes it.
func TestS35_ToolCallCommitBlocksReplay(t *testing.T) {
	h := newScenarioHarness(t)
	task := h.createTask(t, "gw-front")
	binding := newDurableStreamBinding(h.store, task, time.Minute)

	// The stream committed tool-call output (write-ahead checkpoint).
	if err := binding.Checkpoint(CommitStateToolCall); err != nil {
		t.Fatalf("tool_call checkpoint: %v", err)
	}

	// Client disconnects after the semantic commit: settlement writes
	// NOTHING (safety reaper owns the terminal).
	settleDurableStream(context.Background(), binding,
		SurvivalResult{Decision: TaskDecision{Action: TaskActionFailClosed, Reason: "client_disconnected"}},
		nil, "", true)
	if h.store.terminalCount() != 0 {
		t.Fatal("post-content disconnect must not write a terminal itself")
	}

	// Lease still alive: neither reaper nor claim may touch it.
	h.worker.runOnce(context.Background())
	if h.exec.calls != 0 || h.store.status() != durable.StatusRunning {
		t.Fatalf("live-lease task must stay untouched (exec=%d status=%v)", h.exec.calls, h.store.status())
	}

	// Abandoned: safety reaper terminalizes; claim gate refuses.
	h.store.expireLease()
	h.worker.runOnce(context.Background())
	if got := h.store.status(); got != durable.StatusResumeSafetyBlocked {
		t.Fatalf("status = %v, want resume_safety_blocked", got)
	}
	if h.exec.calls != 0 {
		t.Fatalf("replay blocked, but upstream called %d times", h.exec.calls)
	}
}

// S30_survival_quota_wait_same_connection: a quota 429 on the first
// attempt waits in-connection (keepalive visible, no terminal frame) and
// the second attempt delivers the stream — byte-level with the real
// coordinator + gate + serialized writer.
func TestS30_QuotaWaitRecoversInSameConnection(t *testing.T) {
	f := &trackingFlusher{}
	exec := &quotaThenSuccessExecutor{}
	c := &SurvivalCoordinator{
		Exec:     exec,
		Protocol: ProtocolOpenAIChat,
		Options:  SurvivalOptions{Deadline: 30 * time.Second, RetryBase: time.Millisecond, RetryMax: 2 * time.Millisecond},
		Sleep: func(ctx context.Context, d time.Duration) error {
			return nil // wait-recovery window elapses instantly
		},
	}
	res := c.Run(context.Background(), NewSerializedStreamWriter(f), &executors.ExecParams{})
	if !res.Succeed {
		t.Fatalf("expected in-connection recovery, got %+v", res.Decision)
	}
	if res.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2 (429 wait then success)", res.Attempts)
	}
	wire := f.buf.String()
	if !strings.Contains(wire, "gw-survival-keepalive") {
		t.Fatalf("wire missing recovery keepalive: %q", wire)
	}
	if !strings.Contains(wire, "text_delta") {
		t.Fatalf("wire missing recovered content: %q", wire)
	}
	if n := strings.Count(wire, "gateway_survival_"); n != 0 {
		t.Fatalf("no error frames may reach a recovered connection, found %d", n)
	}
}

func TestS36_TerminalSettlementRetriesWithoutReplay(t *testing.T) {
	h := newScenarioHarness(t)
	task := h.createTask(t, "front")
	binding := newDurableStreamBinding(h.store, task, 40*time.Millisecond)
	binding.Start()
	defer binding.Stop()

	h.store.finalizeErr = errors.New("temporary terminal store outage")
	settleDurableStream(context.Background(), binding, SurvivalResult{Succeed: true}, []byte("data: {\"ok\":true}\n\n"), "text/event-stream", false)
	if got := h.store.terminalCount(); got != 0 {
		t.Fatalf("terminal count after transient finalize failure = %d, want 0", got)
	}
	if h.store.settlement == nil {
		t.Fatal("terminal settlement intent was lost after transient finalize failure")
	}
	if h.store.status() != durable.StatusRunning {
		t.Fatalf("task status = %s, want running while settlement is recoverable", h.store.status())
	}

	h.store.finalizeErr = nil
	h.worker.runOnce(context.Background())
	if got := h.store.terminalCount(); got != 1 {
		t.Fatalf("terminal count after repair = %d, want 1", got)
	}
	if got := h.store.status(); got != durable.StatusCompleted {
		t.Fatalf("task status after repair = %s, want completed", got)
	}
	if got := h.exec.calls; got != 0 {
		t.Fatalf("settlement repair must not replay upstream execution, calls = %d", got)
	}
}

// quotaThenSuccessExecutor fails the first attempt with a rate-limit
// (wait-recovery window), then streams content through the gate writer.
type quotaThenSuccessExecutor struct{ calls int }

func (e *quotaThenSuccessExecutor) Execute(params *executors.ExecParams) (*executors.ExecuteResult, error) {
	e.calls++
	if e.calls == 1 {
		return nil, &executors.ExecuteError{LastKind: errorsx.KindRateLimit, LastErr: context.DeadlineExceeded}
	}
	if _, err := params.W.Write([]byte("data: {\"choices\":[{\"delta\":{\"type\":\"text_delta\",\"content\":\"hi\"}}]}\n\n")); err != nil {
		return nil, err
	}
	return &executors.ExecuteResult{}, nil
}
