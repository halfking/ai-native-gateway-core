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
//   - writes the node_probe_runs audit row;
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
	"fmt"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// ProbeService is the queue-facing facade over NodeProbeWorker's probe logic.
type ProbeService struct {
	worker   *NodeProbeWorker
	executor *ActiveProbeExecutor

	// Test seams keep Run behavior testable without an upstream, gateway, or DB.
	// Production construction leaves these nil and uses the worker methods below.
	directRoundFn  func(context.Context, int, string) nodeProbeRoundResult
	gatewayRoundFn func(context.Context, int, string) gatewayProbeResult
	applyOutcomeFn func(context.Context, probeOutcome)
}

// NewProbeService wires the service. worker supplies probeDirect + side-effect
// helpers; executor supplies the pinned gateway round. When executor is absent,
// the service builds the same pinned request from the worker's gateway config.
func NewProbeService(worker *NodeProbeWorker, executor *ActiveProbeExecutor) *ProbeService {
	return &ProbeService{worker: worker, executor: executor}
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
func (s *ProbeService) Run(ctx context.Context, task ProbeQueueTask) (ProbeQueueResult, error) {
	credID := int(task.CredentialID)
	model := task.RawModel
	if s == nil || s.worker == nil {
		return ProbeQueueResult{Status: ProbeQueueFailed, ReasonCode: "probe_service_not_configured"},
			fmt.Errorf("probe service not configured")
	}
	triggerKind := task.Source
	if triggerKind == "" {
		triggerKind = "request_failure"
	}
	trigger := nodeProbeTrigger{tenantID: task.TenantID, parentID: task.ParentReqID}
	attempt := task.Attempt
	if attempt <= 0 {
		attempt = 1
	}
	startedAt := time.Now()

	s.worker.publishProbeEvent(credID, model, "in-flight", "node_probe", triggerKind, attempt)

	// Round 1 is evidence only. A direct success must not restore routing-visible
	// state before the pinned gateway round has verified the same node.
	direct := s.directRound(ctx, credID, model)

	// Missing-binding short-circuit: a (cred, model) with no binding row is a
	// config problem, not a health problem. Emit one audit row, drop the state,
	// and report success so the queue does not retry forever.
	if isMissingBindingErr(direct) {
		slog.Warn("probe_service: dropping probe for (cred, model) with no binding row",
			"credential_id", credID, "model", model)
		s.worker.emitProbe(ctx, credID, 0, model, model, "direct", attempt, trigger, direct)
		s.worker.deleteNodeProbeState(ctx, credID, model)
		return ProbeQueueResult{Status: ProbeQueueSuccess, ReasonCode: "missing_binding_dropped"}, nil
	}

	// Round 2 must be pinned to the same credential. A legacy unpinned gateway
	// response is retained as an explicit failure result and cannot restore state.
	gateway := s.gatewayRound(ctx, credID, model)
	gw := gateway.round
	if gw.ok && !gateway.pinned {
		gw.ok = false
		gw.errCode = "gateway_pin_unsupported"
		gw.errDetail = "legacy gateway probe cannot prove credential attribution"
	}

	success := direct.ok && gw.ok && gateway.pinned
	s.worker.updateURSMv2ProbeState(ctx, trigger.tenantID, credID, model, success, direct.latencyMs)
	s.worker.emitProbe(ctx, credID, direct.providerID, model, direct.outboundModel, "direct", attempt, trigger, direct)
	s.worker.emitProbe(ctx, credID, direct.providerID, model, direct.outboundModel, "gateway", attempt, trigger, gw)
	now := time.Now()
	s.applyOutcome(ctx, probeOutcome{
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
	s.worker.mirrorNodeProbeState(ctx, credID, model, attempt, success, direct, gw, now, backoff)

	if success {
		// Drop node_probe_failed immediately. applyOutcome already invalidated the
		// candidate cache after committing both-round recovery side effects.
		s.worker.notifyAutoRouteRefresh(ctx, credID)
	} else {
		if s.worker.modelQualityTrigger != nil && attempt >= 2 {
			s.worker.modelQualityTrigger(credID, model, attempt)
		}
	}

	// Persist the audit row (same shape as runOne).
	s.worker.insertNodeProbeRun(ctx, credID, model, triggerKind, attempt, nextSec,
		direct, gw, success, startedAt, now, durationMs)

	// Build the queue result. On success the task is terminal; on failure the
	// queue worker re-arms in-place up to max_attempts using next_run_at.
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
func (w *NodeProbeWorker) insertNodeProbeRun(ctx context.Context, credID int, model, triggerKind string,
	attempt, nextSec int, direct, gw nodeProbeRoundResult, success bool, startedAt, now time.Time, durationMs int) {
	if w.db == nil {
		return
	}
	var requestHeadersJSON []byte
	if len(direct.requestHeaders) > 0 {
		requestHeadersJSON, _ = json.Marshal(direct.requestHeaders)
	}
	timeoutAtMs := 0
	if direct.errCode == "network_error" && direct.latencyMs >= 14900 {
		timeoutAtMs = direct.latencyMs
	}
	_, _ = w.db.Exec(ctx, `
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
}
