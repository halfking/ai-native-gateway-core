// bg/probe_service.go — unified queue-driven probe execution owner.
//
// ProbeService is the single execution+side-effect owner for self-check probes
// fed through the durable credential_probe_queue (需求 6). It replaces the
// ad-hoc per-worker HTTP executors with one place that:
//
//   - runs the two-round probe (direct upstream THEN credential-pinned gateway)
//     and restores routing-visible state only after both rounds succeed;
//   - applies all post-probe side effects (binding/credential/observed state,
//     circuit success, candidate-cache invalidate, URSM v2, model-IQ trigger,
//     pg_notify);
//   - writes the node_probe_runs audit row (errors are surfaced, not swallowed);
//   - keeps the queue lease alive via periodic heartbeat while the rounds and
//     side effects run, so a second worker cannot reclaim and double-settle
//     a task whose original owner is still applying side effects;
//   - returns a ProbeQueueResult so the queue's Complete records the outcome
//     and re-arms backoff in-place (the queue row is the primary state).
//
// It reuses NodeProbeWorker's battle-tested probeDirect + state-mutation
// helpers (same package) and ActiveProbeExecutor.RunGateway for the pinned
// gateway round (X-LLM-Pin-Credential, so the gateway round attributes to the
// exact node under test — 需求 6 bullet 5).
//
// node_probe_state is mirrored for backward compatibility with existing
// dashboard/recovery readers during the transition; the queue row is the
// source of truth for dedup/lease/backoff (C11 will drop the mirror).
package bg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/settings"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// ProbeQueueLeaseDefault is the default per-task lease for the unified
// credential_probe_queue (2026-08-18 Agent B, handoff §7 P1 fix).
//
// Background: the previous default was 30s. A node_probe task owns two
// serial HTTP rounds — direct upstream (≤ 15s timeout) and credential-pinned
// gateway (≤ 15s timeout) — plus URSM v2 / node_probe_state mirror / pg_notify
// / candidate-cache invalidate side effects. With any slow upstream or DB
// contention the total wall time easily exceeds 30s, so the queue's
// RequeueExpiredLeases can hand the same task to another worker that
// double-applies the side effects while the first owner is still finishing
// (lost / duplicate settlement — handoff §7 P1).
//
// 5 minutes is a deliberately conservative ceiling: it covers the realistic
// 99.9-percentile of (direct + gateway + side effects) and leaves wide margin
// for slow LLM upstreams. Concurrent ownership is prevented by the
// lease_token compare in Complete() and the heartbeat below, not by a tight
// lease window.
const ProbeQueueLeaseDefault = 5 * time.Minute

// ProbeQueueHeartbeatInterval is how often Run() extends the lease while the
// two-round probe + side effects are still running. Set well below
// ProbeQueueLeaseDefault so a single missed extension can never hand the task
// to another worker; multiple missed extensions just stretch the worst case
// before takeover, not the common case.
//
// Declared as a var (not const) so tests can swap in a shorter interval —
// 60s would force every heartbeat-cadence test to wait the full minute.
var ProbeQueueHeartbeatInterval = 60 * time.Second

var (
	// ErrProbeHeartbeatFailed means the lease extension could not be confirmed.
	// It is distinct from ErrProbeLeaseLost: a transient database failure must
	// stop the current owner, but does not prove that another owner reclaimed it.
	ErrProbeHeartbeatFailed = errors.New("probe service lease heartbeat failed")

	auditPersistFailedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llmgw_node_probe_audit_persist_failed_total",
			Help: "node_probe_runs audit INSERT failures (must be zero on healthy deployments).",
		},
		[]string{"queue_source"},
	)
	auditUnknownSourceTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llmgw_node_probe_audit_unknown_source_total",
			Help: "task.Source values not present in knownTriggerKind — operator should extend migration 536.",
		},
		[]string{"source"},
	)
	heartbeatExtendedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llmgw_node_probe_lease_heartbeat_extended_total",
			Help: "Number of lease heartbeat extensions that succeeded while ProbeService.Run was active.",
		},
		[]string{"caller"},
	)
	leaseLostTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llmgw_node_probe_lease_lost_total",
			Help: "Number of times ExtendLease / OwnsLease returned ErrProbeLeaseLost during a probe run.",
		},
		[]string{"caller"},
	)
)

// knownTriggerKind maps every task.Source value that the unified queue can
// carry into the trigger_kind enum actually allowed by
// node_probe_runs_trigger_kind_check (migration 536). Anything outside this
// set is logged as "audit_unknown_source" and recorded under
// "request_failure" so the INSERT can never be rejected — losing the trigger
// attribution is strictly better than losing the whole audit row (and the
// duration/error/success evidence that goes with it). Keep this set in sync
// with migration 536.
var knownTriggerKind = map[string]struct{}{
	"request_failure":         {},
	"manual":                  {},
	"credential_recovery":     {},
	"sync_request":            {},
	"periodic":                {},
	"admin":                   {},
	"integrity_probe_planner": {},
	"selfcheck":               {},
	"external_async":          {},
}

// NormalizeTriggerKind returns a value guaranteed to be accepted by the
// node_probe_runs_trigger_kind_check constraint. Unknown values fall back to
// "request_failure" and increment a counter (audit_unknown_source_total) so
// the operator can detect a missing migration / typo / new source that needs
// to be added to the enum.
func NormalizeTriggerKind(source string) (canonical string, unknown bool) {
	if source == "" {
		return "request_failure", false
	}
	if _, ok := knownTriggerKind[source]; ok {
		return source, false
	}
	auditUnknownSourceTotal.WithLabelValues(source).Inc()
	slog.Warn("probe_service: unknown trigger_kind mapped to request_failure (migration 536?)", "source", source)
	return "request_failure", true
}

// ErrProbeAuditPersistFailed signals that the audit row INSERT into
// node_probe_runs failed. The probe itself (direct + gateway + side effects)
// may have already applied routing-visible state; callers must still call
// ProbeQueue.Complete so the task is released from its lease, but the error
// must be surfaced via metric / log so the operator can replay the audit row
// out-of-band and so the dashboards stop showing a silent gap.
var ErrProbeAuditPersistFailed = errors.New("probe_service: node_probe_runs audit insert failed")

// ProbeService is the queue-facing facade over NodeProbeWorker's probe logic.
type ProbeService struct {
	worker   *NodeProbeWorker
	executor *ActiveProbeExecutor

	// queue (2026-08-18, Agent B) lets Run() periodically extend the lease
	// while the two-round probe + side effects run. Optional — when nil
	// (tests), heartbeat is a no-op and the lease defaults are still safe
	// because the queue's Complete() guards on lease_token + status='running'.
	queue *ProbeQueue

	// Test seams keep Run behavior testable without an upstream, gateway, or DB.
	// Production construction leaves these nil and uses the worker methods below.
	directRoundFn  func(context.Context, int, string) nodeProbeRoundResult
	gatewayRoundFn func(context.Context, int, string) gatewayProbeResult
	applyOutcomeFn func(context.Context, probeOutcome)
	// heartbeatFn defaults to ProbeQueue.ExtendLease but can be overridden in
	// tests to assert the lease-extension cadence without touching the DB.
	heartbeatFn func(context.Context, ProbeQueueTask, time.Duration) error
	// leaseCheckFn defaults to ProbeQueue.OwnsLease but can be overridden in
	// tests so the audit-insert path is reachable without a live queue DB.
	leaseCheckFn func(context.Context, ProbeQueueTask) (bool, error)
}

// NewProbeService wires the service. worker supplies probeDirect + side-effect
// helpers; executor supplies the pinned gateway round. When executor is absent,
// the service builds the same pinned request from the worker's gateway config.
func NewProbeService(worker *NodeProbeWorker, executor *ActiveProbeExecutor) *ProbeService {
	return &ProbeService{worker: worker, executor: executor}
}

// SetProbeQueue wires the durable queue so Run() can periodically refresh the
// lease while the probe runs. Safe on a nil receiver and a nil queue.
func (s *ProbeService) SetProbeQueue(q *ProbeQueue) {
	if s != nil {
		s.queue = q
	}
}

// SetLeaseCheckFn wires a custom lease-ownership hook for tests. Production
// leaves it nil and falls through to ProbeQueue.OwnsLease.
func (s *ProbeService) SetLeaseCheckFn(fn func(context.Context, ProbeQueueTask) (bool, error)) {
	if s != nil {
		s.leaseCheckFn = fn
	}
}

type gatewayProbeResult struct {
	round  nodeProbeRoundResult
	pinned bool
}

type probeOutcome struct {
	credentialID int
	model        string
	direct       nodeProbeRoundResult
	gateway      nodeProbeRoundResult
	success      bool
	recoverAt    time.Time
}

// Run executes one queued probe task end-to-end and returns the queue result.
// attempt comes from the task (incremented by ProbeQueue.Claim); the service
// does not consult node_probe_state for the attempt number.
//
// Side-effect / lease ownership invariants (2026-08-18, Agent B, handoff §7 P1):
//  1. A lease heartbeat goroutine starts before the first round and stops
//     after the audit row is written; while the heartbeat is alive the queue
//     will not RequeueExpiredLeases this task.
//  2. The audit INSERT into node_probe_runs MUST return its error to the
//     caller — silent swallow (the pre-fix behaviour at lines 373-398)
//     produced the 2026-08-17 13:45 audit freeze. The error is wrapped with
//     ErrProbeAuditPersistFailed so the worker can still complete the queue
//     row (side effects already applied) while logging / alerting the gap.
//  3. If the heartbeat discovers the lease was already lost (ExtendLease
//     returned ErrProbeLeaseLost) the function short-circuits with an error
//     so a second worker can take over without double-applying side effects.
func (s *ProbeService) Run(ctx context.Context, task ProbeQueueTask) (ProbeQueueResult, error) {
	credID := int(task.CredentialID)
	model := task.RawModel
	if s == nil || s.worker == nil {
		return ProbeQueueResult{Status: ProbeQueueFailed, ReasonCode: "probe_service_not_configured"},
			fmt.Errorf("probe service not configured")
	}
	triggerKind, _ := NormalizeTriggerKind(task.Source)
	trigger := nodeProbeTrigger{tenantID: task.TenantID, parentID: task.ParentReqID}
	attempt := task.Attempt
	if attempt <= 0 {
		attempt = 1
	}
	startedAt := time.Now()

	// Lease heartbeat (2026-08-18): refresh lease_until every
	// ProbeQueueHeartbeatInterval while Run() does its work. Without this, a
	// direct+gateway+side-effects run that takes >30s (the old lease window)
	// can be reclaimed by RequeueExpiredLeases and re-executed by another
	// worker — the second worker then double-applies URSM / circuit success
	// / node_probe_state mirror and double-spends the attempt budget.
	hbCtx, hbCancel := s.startLeaseHeartbeat(ctx, task)
	defer hbCancel()

	s.worker.publishProbeEvent(credID, model, "in-flight", "node_probe", triggerKind, attempt)
	if err := probeRunContextErr(ctx, hbCtx, task); err != nil {
		// Lease lost mid-run via heartbeat cancellation of hbCtx. Side effects
		// have not been applied yet; bail with a "settle-as-no-op" success so
		// the queue worker can release the lease without retrying or double
		// attributing. Parent ctx cancellation (request shutdown) propagates
		// as-is so the caller sees the real cause.
		if errors.Is(err, ErrProbeLeaseLost) && ctx.Err() == nil {
			return ProbeQueueResult{Status: ProbeQueueSuccess, ReasonCode: "lease_lost_during_run"}, nil
		}
		return ProbeQueueResult{}, err
	}

	// Round 1 is evidence only. A direct success must not restore routing-visible
	// state before the pinned gateway round has verified the same node.
	direct := s.directRound(hbCtx, credID, model)
	if err := probeRunContextErr(ctx, hbCtx, task); err != nil {
		if errors.Is(err, ErrProbeLeaseLost) && ctx.Err() == nil {
			return ProbeQueueResult{Status: ProbeQueueSuccess, ReasonCode: "lease_lost_during_run"}, nil
		}
		return ProbeQueueResult{}, err
	}

	// Missing-binding short-circuit: a (cred, model) with no binding row is a
	// config problem, not a health problem. Emit one audit row, drop the state,
	// and report success so the queue does not retry forever.
	if isMissingBindingErr(direct) {
		slog.Warn("probe_service: dropping probe for (cred, model) with no binding row",
			"credential_id", credID, "model", model)
		s.worker.emitProbe(hbCtx, credID, 0, model, model, "direct", attempt, trigger, direct)
		s.worker.deleteNodeProbeState(hbCtx, credID, model)
		// Still write the audit row so dashboards don't lose the signal.
		now := time.Now()
		if err := s.insertAuditRowWithLeaseCheck(hbCtx, hbCtx, task, credID, model, triggerKind, attempt, 0, direct, direct, false, startedAt, now, int(now.Sub(startedAt).Milliseconds())); err != nil {
			auditPersistFailedTotal.WithLabelValues(task.Source).Inc()
			return ProbeQueueResult{Status: ProbeQueueSuccess, ReasonCode: "missing_binding_dropped"}, fmt.Errorf("%w: %v", ErrProbeAuditPersistFailed, err)
		}
		return ProbeQueueResult{Status: ProbeQueueSuccess, ReasonCode: "missing_binding_dropped"}, nil
	}

	// Round 2 must be pinned to the same credential. A legacy unpinned gateway
	// response is retained as an explicit failure result and cannot restore state.
	gateway := s.gatewayRound(hbCtx, credID, model)
	if err := probeRunContextErr(ctx, hbCtx, task); err != nil {
		if errors.Is(err, ErrProbeLeaseLost) && ctx.Err() == nil {
			return ProbeQueueResult{Status: ProbeQueueSuccess, ReasonCode: "lease_lost_during_run"}, nil
		}
		return ProbeQueueResult{}, err
	}
	gw := gateway.round
	if gw.ok && !gateway.pinned {
		gw.ok = false
		gw.errCode = "gateway_pin_unsupported"
		gw.errDetail = "legacy gateway probe cannot prove credential attribution"
	}

	success := direct.ok && gw.ok && gateway.pinned
	if err := probeRunContextErr(ctx, hbCtx, task); err != nil {
		// Bail BEFORE side effects (URSM update + applyOutcome + mirror +
		// notify) so the other worker that reclaimed this task does not
		// see competing writes from us.
		if errors.Is(err, ErrProbeLeaseLost) && ctx.Err() == nil {
			return ProbeQueueResult{Status: ProbeQueueSuccess, ReasonCode: "lease_lost_during_run"}, nil
		}
		return ProbeQueueResult{}, err
	}
	// The direct upstream round is the recovery signal for authoritative URSM.
	// The pinned gateway round depends on that same URSM key and may fail while
	// the key is absent; using the composite result here would renew the lockout.
	s.worker.updateURSMv2ProbeState(hbCtx, trigger.tenantID, credID, model, direct.ok, direct.latencyMs)
	s.worker.emitProbe(hbCtx, credID, direct.providerID, model, direct.outboundModel, "direct", attempt, trigger, direct)
	s.worker.emitProbe(hbCtx, credID, direct.providerID, model, direct.outboundModel, "gateway", attempt, trigger, gw)
	now := time.Now()
	s.applyOutcome(hbCtx, probeOutcome{
		credentialID: credID,
		model:        model,
		direct:       direct,
		gateway:      gw,
		success:      success,
		recoverAt:    now.Add(5 * time.Minute),
	})
	durationMs := int(now.Sub(startedAt).Milliseconds())

	backoff := ChainBackoffIndex(attempt, NodeProbeBackoffChain)
	// 2026-08-13: 常用模型失败回退缩短（probe.featured_backoff_multiplier，默认
	// 50% → 更快重试恢复）；非常用按标准 7 步链。仅影响失败后的下次重试间隔，
	// 不降低探测深度（仍走 direct+gateway 双轮）。
	// Audit fix #6: clamp to ≥5s — at pct=1 the first rung (5s) would truncate
	// to 0s and produce a busy retry loop; the spec Min is 20 (enforced there),
	// but a defensive floor here guards against future chain rungs <5s.
	if globalIsFeaturedModel(model, "") {
		if pct := settings.GetPlatformInt("probe.featured_backoff_multiplier", 50); pct > 0 && pct < 100 {
			scaled := time.Duration(float64(backoff) * float64(pct) / 100.0)
			if scaled < 5*time.Second {
				scaled = 5 * time.Second
			}
			backoff = scaled
		}
	}
	nextSec := int(backoff.Seconds())
	if nextSec <= 0 {
		nextSec = 5 // defensive floor for sub-second backoff (audit #6)
	}

	// Mirror into node_probe_state for backward compat with existing dashboard /
	// recovery readers during the transition (C11 will drop this).
	if err := probeRunContextErr(ctx, hbCtx, task); err != nil {
		// applyOutcome already ran above. If lease was lost, skip mirror +
		// notify + audit (insertAuditRowWithLeaseCheck also guards). Bail
		// with a success-shaped no-op result.
		if errors.Is(err, ErrProbeLeaseLost) && ctx.Err() == nil {
			return ProbeQueueResult{Status: ProbeQueueSuccess, ReasonCode: "lease_lost_during_run"}, nil
		}
		return ProbeQueueResult{}, err
	}
	s.worker.mirrorNodeProbeState(hbCtx, credID, model, attempt, success, direct, gw, now, backoff)

	if success {
		// Drop node_probe_failed immediately. applyOutcome already invalidated the
		// candidate cache after committing both-round recovery side effects.
		s.worker.notifyAutoRouteRefresh(hbCtx, credID)
	} else {
		if s.worker.modelQualityTrigger != nil && attempt >= 2 {
			s.worker.modelQualityTrigger(credID, model, attempt)
		}
	}

	// Persist the audit row. Errors are surfaced (NOT swallowed) so the
	// operator can detect a missing migration / schema drift / DB outage
	// without staring at a frozen audit table. The worker still calls
	// ProbeQueue.Complete afterwards because side effects were already
	// applied; the error only escapes so it is logged + counted.
	auditErr := s.insertAuditRowWithLeaseCheck(ctx, hbCtx, task, credID, model, triggerKind, attempt, nextSec, direct, gw, success, startedAt, now, durationMs)
	if auditErr != nil {
		auditPersistFailedTotal.WithLabelValues(task.Source).Inc()
	}

	// Build the queue result. On success the task is terminal; on failure the
	// queue worker re-arms in-place up to max_attempts using next_run_at.
	if auditErr != nil {
		// Side effects already applied — still settle the queue row so the
		// lease is released and the task is not retried. AuditErr is returned
		// so the caller can emit metric + log + alert.
		//
		// Two distinct failure modes must be distinguishable to the caller:
		//   - errors.Is(auditErr, ErrProbeLeaseLost)  → another worker took
		//     over; drop queue Complete as a no-op. Bubble the error up
		//     unwrapped (still tagged with context) so the worker can
		//     errors.Is it without surprises.
		//   - everything else (pgx / CHECK rejection / DB outage) → wrap
		//     with ErrProbeAuditPersistFailed so the operator / alert
		//     path can detect the audit freeze.
		var wrappedErr error
		switch {
		case errors.Is(auditErr, ErrProbeLeaseLost):
			wrappedErr = fmt.Errorf("%w (queue_id=%d cred=%d model=%s)", ErrProbeLeaseLost, task.ID, credID, model)
		default:
			wrappedErr = fmt.Errorf("%w (queue_id=%d cred=%d model=%s): %v", ErrProbeAuditPersistFailed, task.ID, credID, model, auditErr)
		}
		if success {
			return ProbeQueueResult{Status: ProbeQueueSuccess, HTTPStatus: direct.httpStatus, LatencyMs: direct.latencyMs}, wrappedErr
		}
		nextRetryAt := now.Add(backoff)
		errCode := ""
		if c := firstErrCode(direct, gw); c != nil {
			errCode = *c
		}
		return ProbeQueueResult{
			Status:       ProbeQueueFailed,
			ReasonCode:   errCode,
			ReasonDetail: firstErrDetailString(direct, gw),
			HTTPStatus:   gw.httpStatus,
			LatencyMs:    gw.latencyMs,
			NextRunAt:    &nextRetryAt,
		}, wrappedErr
	}

	if success {
		return ProbeQueueResult{Status: ProbeQueueSuccess, HTTPStatus: direct.httpStatus, LatencyMs: direct.latencyMs}, nil
	}
	nextRetryAt := now.Add(backoff)
	errCode := ""
	if c := firstErrCode(direct, gw); c != nil {
		errCode = *c
	}
	return ProbeQueueResult{
		Status:       ProbeQueueFailed,
		ReasonCode:   errCode,
		ReasonDetail: firstErrDetailString(direct, gw),
		HTTPStatus:   gw.httpStatus,
		LatencyMs:    gw.latencyMs,
		NextRunAt:    &nextRetryAt,
	}, nil
}

// probeRunContextErr translates cancellation of the heartbeat context into a
// typed error while preserving the caller's cancellation. The heartbeat context
// is the context used by all probe and side-effect operations.
func probeRunContextErr(parent, heartbeat context.Context, task ProbeQueueTask) error {
	if err := parent.Err(); err != nil {
		return err
	}
	if err := heartbeat.Err(); err != nil {
		cause := context.Cause(heartbeat)
		if cause == nil {
			cause = err
		}
		if errors.Is(cause, ErrProbeLeaseLost) {
			return fmt.Errorf("%w (queue_id=%d)", ErrProbeLeaseLost, task.ID)
		}
		return fmt.Errorf("%w (queue_id=%d): %v", ErrProbeHeartbeatFailed, task.ID, cause)
	}
	return nil
}

// startLeaseHeartbeat launches a goroutine that refreshes the lease window
// every ProbeQueueHeartbeatInterval. The returned context is cancelled by
// the returned cancel function so the heartbeat stops as soon as Run() is
// about to return. If no queue is wired (tests), this returns ctx and a
// no-op cancel.
func (s *ProbeService) startLeaseHeartbeat(parent context.Context, task ProbeQueueTask) (context.Context, context.CancelFunc) {
	if s == nil || s.queue == nil || task.LeaseToken == "" {
		return parent, func() {}
	}
	hbCtx, cancelCause := context.WithCancelCause(parent)
	cancel := func() { cancelCause(nil) }
	extend := s.heartbeatFn
	if extend == nil {
		extend = s.queue.ExtendLease
	}
	go func() {
		ticker := time.NewTicker(ProbeQueueHeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-ticker.C:
				if err := extend(hbCtx, task, ProbeQueueLeaseDefault); err != nil {
					// ErrProbeLeaseLost means another worker has already
					// reclaimed this task. Stop heartbeating immediately and
					// cancel the run context so Run() bails before writing
					// side effects that the new owner may also be writing.
					if errors.Is(err, ErrProbeLeaseLost) {
						leaseLostTotal.WithLabelValues("probe_service").Inc()
					} else {
						slog.Warn("probe_service: lease heartbeat failed", "queue_id", task.ID, "error", err)
					}
					slog.Warn("probe_service: lease heartbeat stopped — bailing to avoid double-settlement",
						"queue_id", task.ID, "error", err)
					if errors.Is(err, ErrProbeLeaseLost) {
						cancelCause(ErrProbeLeaseLost)
					} else {
						cancelCause(fmt.Errorf("%w: %v", ErrProbeHeartbeatFailed, err))
					}
					return
				}
				heartbeatExtendedTotal.WithLabelValues("probe_service").Inc()
			}
		}
	}()
	return hbCtx, cancel
}

// insertAuditRowWithLeaseCheck writes the audit row, but first verifies that
// the worker still holds the lease (cheap SELECT). If the lease was already
// reclaimed, returns ErrProbeLeaseLost without writing the audit row so we
// don't double-attribute (cred, model) outcomes to two workers. Otherwise the
// pgx Exec error is returned to the caller, which logs + alerts.
//
// Why the lease check first: a heartbeat that fired in the gap between the
// last round and the audit insert can already be late, in which case the
// second owner may be writing its own audit row. Inserting ours would
// produce two rows for the same (queue_id, attempt) which downstream joins
// (v_probe_queue_snapshot) cannot disambiguate.
func (s *ProbeService) insertAuditRowWithLeaseCheck(ctx, hbCtx context.Context, task ProbeQueueTask, credID int, model, triggerKind string,
	attempt, nextSec int, direct, gw nodeProbeRoundResult, success bool, startedAt, now time.Time, durationMs int) error {
	if s == nil || s.worker == nil {
		return nil
	}
	if s.queue != nil && task.LeaseToken != "" {
		check := s.leaseCheckFn
		if check == nil {
			check = s.queue.OwnsLease
		}
		owned, err := check(hbCtx, task)
		if err != nil {
			slog.Warn("probe_service: lease ownership check failed — skipping audit insert",
				"queue_id", task.ID, "error", err)
			return fmt.Errorf("lease check: %w", err)
		}
		if !owned {
			leaseLostTotal.WithLabelValues("probe_service_audit").Inc()
			slog.Warn("probe_service: lease no longer owned — skipping audit insert to avoid double attribution",
				"queue_id", task.ID, "credential_id", credID, "model", model)
			return ErrProbeLeaseLost
		}
	}
	return s.worker.insertNodeProbeRun(ctx, credID, model, triggerKind, attempt, nextSec,
		direct, gw, success, startedAt, now, durationMs)
}

func (s *ProbeService) directRound(ctx context.Context, credID int, model string) nodeProbeRoundResult {
	if s.directRoundFn != nil {
		return s.directRoundFn(ctx, credID, model)
	}
	return s.worker.probeDirect(ctx, credID, model)
}

// gatewayRound runs the gateway probe round, preferring the executor's pinned
// round (X-LLM-Pin-Credential -> exact node) when configured. The legacy result
// is explicit about its lack of attribution so it can never recover the node.
func (s *ProbeService) gatewayRound(ctx context.Context, credID int, model string) gatewayProbeResult {
	if s.gatewayRoundFn != nil {
		return s.gatewayRoundFn(ctx, credID, model)
	}
	target := &ProbeTarget{CredentialID: credID, RawModel: model}
	if s.executor != nil && s.executor.GatewayEnabled() {
		pr := s.executor.RunGateway(ctx, target)
		return gatewayProbeResult{round: probeResultToRound(model, pr), pinned: true}
	}
	if s.worker != nil && s.worker.client != nil && s.worker.baseURL != "" {
		fallback := NewActiveProbeExecutor(nil, nil, nil, 0)
		fallback.SetGateway(s.worker.baseURL, s.worker.apiKey, s.worker.client)
		pr := fallback.RunGateway(ctx, target)
		return gatewayProbeResult{round: probeResultToRound(model, pr), pinned: true}
	}
	return gatewayProbeResult{round: nodeProbeRoundResult{
		errCode:   "gateway_pin_unsupported",
		errDetail: "legacy gateway probe cannot prove credential attribution",
	}, pinned: false}
}

func (s *ProbeService) applyOutcome(ctx context.Context, outcome probeOutcome) {
	if s.applyOutcomeFn != nil {
		s.applyOutcomeFn(ctx, outcome)
		return
	}
	if outcome.success {
		s.worker.updateBindingAvailability(ctx, outcome.credentialID, outcome.model, true, "")
		if s.worker.db != nil {
			s.worker.updateCredentialHealth(ctx, outcome.credentialID)
		}
		s.worker.updateObservedState(ctx, outcome.credentialID, outcome.model, true, "", time.Now())
		if s.worker.recordCircuitSuccess != nil {
			s.worker.recordCircuitSuccess(outcome.direct.providerID, outcome.credentialID)
		}
	} else {
		errCode := "gateway_probe_failed"
		if code := firstErrCode(outcome.direct, outcome.gateway); code != nil && *code != "" {
			errCode = *code
		}
		s.worker.updateBindingAvailability(ctx, outcome.credentialID, outcome.model, false, errCode)
		s.worker.updateObservedState(ctx, outcome.credentialID, outcome.model, false, errCode, outcome.recoverAt)
	}
	if s.worker.invalidateCandidateCache != nil {
		s.worker.invalidateCandidateCache(outcome.credentialID)
	}
}

// probeResultToRound converts an executor ProbeResult into the worker's round
// shape so the audit/side-effect code stays unified across both gateway paths.
func probeResultToRound(model string, pr *ProbeResult) nodeProbeRoundResult {
	r := nodeProbeRoundResult{
		ok:            pr != nil && pr.Status == ProbeStatusSuccess,
		httpStatus:    0,
		errCode:       "none",
		outboundModel: model,
	}
	if pr == nil {
		r.errCode = "gateway_not_configured"
		return r
	}
	r.httpStatus = pr.HTTPStatus
	r.latencyMs = pr.LatencyMs
	r.requestURL = pr.RequestURL
	r.requestBody = pr.RequestBody
	r.responseBody = pr.ResponseBody
	r.timedOut = pr.Status == ProbeStatusTimeout
	if !r.ok {
		if pr.ErrCode != "" {
			r.errCode = pr.ErrCode
		} else {
			r.errCode = string(pr.Status)
		}
		r.errDetail = pr.ErrMsg
	}
	return r
}

// firstErrDetailString is the string form of firstErrDetail (nil-safe).
func firstErrDetailString(a, b nodeProbeRoundResult) string {
	if d := firstErrDetail(a, b); d != nil {
		return *d
	}
	return ""
}

// --- NodeProbeWorker helpers extracted from runOne for reuse by ProbeService ---
// These are thin wrappers so the side-effect/audit SQL lives in one place
// (node_probe.go) and both the legacy cycle() path and the queue path share it.

func (w *NodeProbeWorker) deleteNodeProbeState(ctx context.Context, credID int, model string) {
	if w.db == nil {
		return
	}
	if _, err := w.db.Exec(ctx, `DELETE FROM node_probe_state WHERE credential_id = $1 AND raw_model_name = $2`,
		credID, model); err != nil {
		slog.Warn("probe_service: failed to drop orphan state row",
			"credential_id", credID, "model", model, "error", err)
	}
}

func (w *NodeProbeWorker) notifyAutoRouteRefresh(ctx context.Context, credID int) {
	if w.db == nil {
		return
	}
	bgCtx, bgCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer bgCancel()
	if _, err := w.db.Exec(bgCtx, "SELECT pg_notify('auto_route_refresh', $1)", fmt.Sprintf("credentials:UPDATE:%d", credID)); err != nil {
		slog.Warn("probe_service: pg_notify auto_route_refresh failed", "credential_id", credID, "error", err)
	}
}

// mirrorNodeProbeState writes the success-reset / failure-backoff UPDATE to
// node_probe_state so legacy dashboard + recovery readers stay consistent
// during the transition (queue row remains the primary state).
func (w *NodeProbeWorker) mirrorNodeProbeState(ctx context.Context, credID int, model string, attempt int, success bool,
	direct, gw nodeProbeRoundResult, now time.Time, backoff time.Duration) {
	if w.db == nil {
		return
	}
	if success {
		_, _ = w.db.Exec(ctx, `
			UPDATE node_probe_state SET
				consecutive_failures = 0,
				consecutive_successes = consecutive_successes + 1,
				last_attempt_at = now(),
				next_retry_at = now() + interval '1 hour',
				next_retry_seconds = 3600,
				paused = FALSE,
				last_direct_ok = TRUE,
				last_gateway_ok = TRUE,
				last_err_code = NULL,
				last_err_detail = NULL,
				in_flight_until = NULL,
				updated_at = now()
			WHERE credential_id = $1 AND raw_model_name = $2`, credID, model)
		return
	}
	nextRetryAt := now.Add(backoff)
	nextSec := int(backoff.Seconds())
	_, _ = w.db.Exec(ctx, `
		UPDATE node_probe_state SET
			consecutive_failures = $3,
			consecutive_successes = 0,
			last_attempt_at = now(),
			next_retry_at = $4,
			next_retry_seconds = $5,
			last_direct_ok = $6,
			last_gateway_ok = $7,
			last_err_code = $8,
			last_err_detail = $9,
			in_flight_until = NULL,
			updated_at = now()
		WHERE credential_id = $1 AND raw_model_name = $2`,
		credID, model, attempt, nextRetryAt, nextSec,
		direct.ok, gw.ok, firstErrCode(direct, gw), firstErrDetail(direct, gw))
}

// insertNodeProbeRun writes the forensic audit row (mirrors runOne's INSERT).
// The pre-fix version swallowed the pgx Exec error (_, _ = ...). 2026-08-18
// Agent B: errors are returned to the caller so the audit gap shows up in
// metrics + logs + dashboards (the 2026-08-17 13:45 freeze was caused by this
// silent swallow + a CHECK constraint that rejected unified-queue sources).
func (w *NodeProbeWorker) insertNodeProbeRun(ctx context.Context, credID int, model, triggerKind string,
	attempt, nextSec int, direct, gw nodeProbeRoundResult, success bool, startedAt, now time.Time, durationMs int) error {
	if w.db == nil {
		return nil
	}
	var requestHeadersJSON []byte
	if len(direct.requestHeaders) > 0 {
		requestHeadersJSON, _ = json.Marshal(direct.requestHeaders)
	}
	timeoutAtMs := 0
	if direct.errCode == "network_error" && direct.latencyMs >= 14900 {
		timeoutAtMs = direct.latencyMs
	}
	_, err := w.db.Exec(ctx, `
		INSERT INTO node_probe_runs (
			credential_id, raw_model_name, trigger_kind, attempt, next_retry_seconds,
			direct_ok, direct_http_status, direct_err_code, direct_latency_ms, direct_err_detail,
			gateway_ok, gateway_http_status, gateway_err_code, gateway_latency_ms, gateway_err_detail,
			success, started_at, completed_at, duration_ms,
			api_model, outbound_model, provider_id,
			request_url, request_headers, request_body, response_body,
			timeout_at_ms, via_proxy
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9, $10,
			$11, $12, $13, $14, $15,
			$16, $17, $18, $19,
			$20, $21, $22,
			$23, $24, $25, $26,
			$27, $28
		)`,
		credID, model, triggerKind, attempt, nextSec,
		direct.ok, direct.httpStatus, direct.errCode, direct.latencyMs, direct.errDetail,
		gw.ok, gw.httpStatus, gw.errCode, gw.latencyMs, gw.errDetail,
		success, startedAt, now, durationMs,
		model, direct.outboundModel, direct.providerID,
		direct.requestURL, requestHeadersJSON, direct.requestBody, direct.responseBody,
		timeoutAtMs, direct.viaProxy,
	)
	if err != nil {
		// Caller already increments the metric and wraps with
		// ErrProbeAuditPersistFailed; here we just surface the raw pgx error
		// with enough context for the operator to triage (likely CHECK
		// rejection / DB outage / unique violation).
		slog.Error("probe_service: node_probe_runs audit insert failed",
			"credential_id", credID, "model", model, "trigger_kind", triggerKind, "error", err)
		return err
	}
	return nil
}
