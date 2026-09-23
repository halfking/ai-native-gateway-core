package streaming

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/internal/retryowner"
)

// survival_wiring.go — SR-07 (doc 18 §6 分层启用, §5.1 SurvivalCoordinator)
//
// Production wiring for the SR-W2 in-connection survival path: the handler
// branch in serveWithExecutor delegates to runSurvivalCoordinator when
// request survival is enabled for the tenant. Everything here is inert
// until SetRequestSurvival is called (main.go) — the flag-off path never
// constructs a coordinator.

// errSurvivalTerminalRendered marks a failed survival outcome whose protocol
// terminal (error envelope + [DONE]) the coordinator already put on the wire
// — rendered by the Terminal seam itself, or latched earlier by the executor
// branch (the seam self-suppresses in that case, but a terminal is on the
// wire either way). Post-loop error handlers must not stack a second
// terminal after [DONE]; they consult errors.Is and keep bookkeeping only.
// Wraps errorsx.ErrProtocolTerminalRendered so both the survival sentinel
// and the dispatch-path wraps (executor_dispatch.go) match a single
// errors.Is target in the handler's blackhole guard.
var errSurvivalTerminalRendered = fmt.Errorf("%w (survival)", errorsx.ErrProtocolTerminalRendered)

// SetRequestSurvival arms the survival branch. tenantAllowed is consulted
// per request (global flag + tenant allowlist live in config; the handler
// only sees the verdict). opts zeroes fall back to the doc 18 §5.1/§8.4
// defaults. Passing a nil tenantAllowed (the zero state) keeps survival
// fully disabled.
func (h *ChatHandler) SetRequestSurvival(tenantAllowed func(tenantID string) bool, opts SurvivalOptions) {
	h.survivalTenantAllowed = tenantAllowed
	h.survivalOptions = opts
}

// survivalClientProtocol maps the request path onto the gate's client
// protocol so buffered frames classify correctly.
func survivalClientProtocol(r *http.Request) ClientProtocol {
	switch {
	case r == nil:
		return ProtocolOpenAIChat
	case isAnthropicMessagesPath(r.URL.Path):
		return ProtocolAnthropic
	case r.URL.Path == "/v1/responses" || len(r.URL.Path) > len("/v1/responses") && r.URL.Path[:len("/v1/responses")] == "/v1/responses":
		return ProtocolOpenAIResponses
	default:
		return ProtocolOpenAIChat
	}
}

// runSurvivalCoordinator drives the SR-W2 recovery loop for one streaming
// request and returns the executor-shaped outcome so the shared
// post-loop handling (telemetry, traces, session bookkeeping) runs
// unchanged. buildExecParams is the same factory the goal-retry loop uses.
// A non-nil durable binding switches the loop into the doc 18 §11.3
// foreground mode: write-ahead commit checkpoints, lease renewal and the
// terminal settlement matrix (durable_stream.go).
func (h *ChatHandler) runSurvivalCoordinator(
	r *http.Request,
	w http.ResponseWriter,
	buildExecParams func(streamWriter http.ResponseWriter) *executors.ExecParams,
	tenantID string,
	durable *DurableStreamBinding,
) (*executors.ExecuteResult, error) {
	protocol := survivalClientProtocol(r)

	// Owner freeze (SR-05/W0): mark the whole request survival-owned so the
	// streamretry wrapper (if still mounted) steps aside.
	frozenCtx := retryowner.FreezeOwner(r.Context(), retryowner.Survival)
	frozenReq := r.WithContext(frozenCtx)

	params := buildExecParams(w)
	params.R = frozenReq
	// The legacy handler factory pre-allocates its 100-call budget for the
	// goal-retry owner. Survival chooses the effective day/night retry budget
	// from its start-time snapshot, so let the coordinator allocate the shared
	// request-survival budget before the first upstream call.
	params.UpstreamAttempts = nil

	// The handler-owned StreamSession stays active through terminal completion.
	// OnStreamReady is retained for compatibility but no longer stops heartbeat;
	// both heartbeat and coordinator output share the serialized connection.
	if params.OnStreamReady != nil {
		params.OnStreamReady()
	}

	// Durable foreground: tee the committed wire bytes for the completed
	// terminal body, then start the lease renewal loop.
	var capture *durableWireCapture
	baseWriter := w
	if durable != nil {
		capture = newDurableWireCapture(w, 0)
		baseWriter = capture
		durable.Start()
	}

	sw := NewSerializedStreamWriter(baseWriter)
	attemptExec := h.survivalAttemptExec
	if attemptExec == nil {
		attemptExec = h.executor
	}
	// terminalOnWire latches once the coordinator settles a terminal
	// decision. The seam is invoked for every settled terminal — including
	// the self-suppressed case where the executor branch already latched
	// gate.TerminalRendered — so flag ≠ "this closure wrote frames"; it
	// means "a protocol terminal is on the wire, one way or the other".
	terminalOnWire := false
	coordinator := &SurvivalCoordinator{
		Exec:               attemptExec,
		Protocol:           protocol,
		Options:            h.survivalOptions,
		TransportHeartbeat: params.OnStreamHeartbeat,
		RetryNotice: func(ctx context.Context, attempt int, decision TaskDecision, wait time.Duration) error {
			if params.OnNodeJump == nil {
				return nil
			}
			params.OnNodeJump(fmt.Sprintf("正在等待可用节点并重试（第 %d 次，原因=%s，等待 %s）", attempt, decision.Reason, wait.Round(time.Second)))
			return nil
		},
		Refresh: func(ctx context.Context) {
			cands, policy, _, err := resolveCandidatesForRequest(
				ctx, h.provider, params.ClientModel, params.ClientID.Fingerprint.ClientProfile,
				tenantID, params.BodyBytes,
			)
			if err != nil {
				slog.Warn("survival candidate refresh failed", "request_id", params.RequestID, "error", err)
				return
			}
			// A successful empty refresh is meaningful: it prevents retrying a
			// stale route while the coordinator keeps the client connection alive.
			params.Candidates = cands
			params.Policy = policy
		},
		// NOTE: the coordinator's durable Reschedule seam stays unwired
		// here: store.Reschedule clears the lease while the coordinator
		// keeps executing in-connection after the wait — wiring it would
		// fork ownership between the foreground and the worker. Crash
		// during a wait is covered by frontend lease expiry instead
		// (ClaimRunnable re-claims expired running tasks).
		BeforeSemanticCommit: durableBeforeSemanticCommit(durable),
		Terminal: func(decision TaskDecision, committed bool) {
			terminalOnWire = true
			renderSurvivalTerminal(sw, protocol, decision, committed)
		},
	}

	res := coordinator.Run(frozenCtx, sw, params)
	// 2026-08-19: extend request_survival_finished with the correlation
	// context the survival loop couldn't see (parent_request_id,
	// session_id, tenant_id) plus the last-attempt provider/model/kinds so
	// an offline grep by request_id reproduces the failure shape without
	// joining the audit table.
	lastProviderID := 0
	lastRawModel := ""
	lastKinds := ""
	lastCommitState := ""
	attemptHistory := ""
	if res.FinalAttempt != nil {
		lastCommitState = res.FinalAttempt.CommitState.String()
		if res.FinalAttempt.ExecResult != nil {
			c := res.FinalAttempt.ExecResult.Candidate
			if c.RawModel != "" || c.ProviderID != 0 {
				lastRawModel = c.RawModel
			}
		}
		kinds := make([]string, 0, len(res.FinalAttempt.CandidateOutcomes))
		for _, co := range res.FinalAttempt.CandidateOutcomes {
			if co.Kind != "" {
				kinds = append(kinds, string(co.Kind))
			}
			if co.ProviderID != 0 {
				lastProviderID = co.ProviderID
			}
		}
		lastKinds = strings.Join(kinds, ",")
	}
	if len(res.History.PriorAttempts) > 0 {
		historyParts := make([]string, 0, len(res.History.PriorAttempts))
		for _, prior := range res.History.PriorAttempts {
			historyParts = append(historyParts, fmt.Sprintf("%d:%d/%d/%s/%s/%s", prior.AttemptNo, prior.ProviderID,
				prior.CredentialID, prior.Model, prior.Kind, prior.Action))
			if res.FinalAttempt == nil || res.FinalAttempt.ExecResult == nil {
				if prior.ProviderID != 0 {
					lastProviderID = prior.ProviderID
				}
				if prior.Model != "" {
					lastRawModel = prior.Model
				}
			}
		}
		attemptHistory = strings.Join(historyParts, ";")
	}
	slog.Info("request_survival_finished",
		"request_id", params.RequestID,
		"succeed", res.Succeed,
		"attempts", res.Attempts,
		"decision", res.Decision.Action.String(),
		"reason", res.Decision.Reason,
		"committed", res.FinalAttempt != nil && res.FinalAttempt.CommitState >= CommitStateContent,
		"commit_state", lastCommitState,
		"kinds", lastKinds,
		"provider_id", lastProviderID,
		"raw_model", lastRawModel,
		"client_model", params.Model,
		"attempt_history", attemptHistory,
	)
	if durable != nil {
		var body []byte
		contentType := ""
		if capture != nil {
			body, contentType = capture.result()
		}
		clientDisconnected := res.Decision.Reason == "client_disconnected"
		settleDurableStream(frozenCtx, durable, res, body, contentType, clientDisconnected)
	}

	if res.Succeed {
		return res.FinalAttempt.ExecResult, nil
	}
	// Cancellation exits (loop-top ctx check / client disconnect) return
	// from Run WITHOUT rendering a protocol terminal — the client is gone
	// or the request context died. Keep the historical raw error so the
	// handler's cancel classification (safety net, EventCancelled) still
	// applies and nothing is mislabeled as a provider failure.
	if ctxErr := frozenCtx.Err(); errors.Is(ctxErr, context.Canceled) || errors.Is(ctxErr, context.DeadlineExceeded) {
		var rawErr error
		if res.FinalAttempt != nil {
			rawErr = res.FinalAttempt.FinalError
		}
		if rawErr == nil {
			rawErr = fmt.Errorf("request survival ended: %s (%s)", res.Decision.Action, res.Decision.Reason)
		}
		return nil, rawErr
	}
	var cause error
	if res.FinalAttempt != nil {
		cause = res.FinalAttempt.FinalError
	}
	if cause == nil {
		cause = fmt.Errorf("request survival ended: %s (%s)", res.Decision.Action, res.Decision.Reason)
	}
	if terminalOnWire {
		// R58 fault injection (245, §11.6 committed-then-EOF): the client
		// already received the protocol terminal + [DONE]. Keep the sentinel
		// inside the wrapped chain so the shared post-loop handlers (handler
		// blackhole guard, errors.Is consumers) skip further wire writes
		// instead of stacking a second terminal after [DONE]. Multiple %w
		// keeps the errorsx.ExecuteError As-chain intact for the typed
		// branches.
		cause = fmt.Errorf("%w (%w)", cause, errSurvivalTerminalRendered)
	}
	return nil, &survivalTerminalError{
		action: res.Decision.Action,
		reason: res.Decision.Reason,
		kinds:  lastKinds,
		cause:  cause,
	}
}

// survivalTerminalError marks a request whose survival coordinator reached a
// terminal decision AND already rendered the protocol terminal envelope on
// the serialized stream writer (renderSurvivalTerminal — resume_blocked /
// fail_terminal / fail_closed all emit their own SSE error frame).
//
// 2026-09-23 (forensics round, design resume-blocked-long-stream-recovery
// §四.2 "error_kind 补齐"): pre-fix, the handler's generic provider_error /
// Exhausted fallthroughs recorded these requests with no survival-specific
// detail code AND could render a SECOND client error frame ("No available
// provider..." / "upstream request failed") on top of the already-written
// survival envelope, degrading the signal agent clients act on. The handler
// now pattern-matches this type: persist the survival decision verbatim,
// render nothing more. When the R58 terminalOnWire latch fired, Unwrap also
// exposes errSurvivalTerminalRendered so errors.Is keeps working.
type survivalTerminalError struct {
	action TaskAction
	reason string
	// kinds is the comma-joined errorsx kind list of the final attempt's
	// candidate outcomes (e.g. "upstream_down,network"), carried for
	// telemetry/logging.
	kinds string
	// cause is the final attempt's underlying error (usually an
	// *executors.ExecuteError, possibly %w-joined with
	// errSurvivalTerminalRendered); Unwrap keeps errors.Is/As chains intact.
	cause error
}

func (e *survivalTerminalError) Error() string {
	// Message stays byte-identical to the historical fmt.Errorf so
	// log-grepping tests and dashboards keep matching.
	return fmt.Sprintf("request survival ended: %s (%s)", e.action, e.reason)
}

func (e *survivalTerminalError) Unwrap() error { return e.cause }

// detailCode is the request_logs failure_detail_code / error_kind value.
func (e *survivalTerminalError) detailCode() string {
	return "gateway_survival_" + e.action.String()
}

// durableBeforeSemanticCommit adapts the binding into the coordinator's
// write-ahead seam; nil binding → nil hook (plain survival, no durable
// checkpoints). The binding uses its own detached context internally, so a
// disconnecting client cannot cancel the write-ahead.
func durableBeforeSemanticCommit(durable *DurableStreamBinding) func(context.Context, CommitState) error {
	if durable == nil {
		return nil
	}
	return func(ctx context.Context, state CommitState) error {
		// Durable ownership outlives the client connection; fencing and the
		// binding's own timeout still bound this write.
		return durable.CheckpointContext(context.WithoutCancel(ctx), state)
	}
}

// renderSurvivalTerminal writes the final protocol frame(s) for a task the
// coordinator gave up on (doc 18 §9.3 — the coordinator owns final
// rendering; bridges already returned outcome-only). committed requests get
// the same error frame so the stream ends well-formed instead of hanging.
func renderSurvivalTerminal(sw *SerializedStreamWriter, protocol ClientProtocol, decision TaskDecision, committed bool) {
	if sw == nil {
		return
	}
	retryable := decision.Action == TaskActionRetryNow || decision.Action == TaskActionWaitRecovery
	// 2026-09-23 (strategy fix, user-report class #0/#2/#3/#8/#10): a
	// resume_blocked verdict only ever fires for RETRYABLE failure kinds
	// (errorsx.DecideNextAction arms it exclusively when CommitState >=
	// CommitStateContent AND the kind is recoverable — transient / network /
	// timeout / rate_limit / overloaded). The in-connection transparent retry
	// stays blocked (the client owns the committed prefix; resuming would
	// splice duplicate bytes), but that is a GATEWAY-side constraint, not a
	// verdict on the failure itself. The pre-fix envelope told agent clients
	// (ZCode et al.) retryable=false, so they hard-failed the whole turn even
	// though their own "discard partial output and re-send" machinery
	// recovers it cleanly. Mark the class client-retryable: the failure is
	// transient by construction and a full-turn regeneration is the designed
	// L4 fallback (design resume-blocked-long-stream-recovery §四.1
	// AllowVisibleRestart without the protocol event).
	if decision.Action == TaskActionResumeBlocked {
		retryable = true
	}
	code := "gateway_survival_" + decision.Action.String()
	message := "gateway request survival ended: " + decision.Reason
	reasonJSON, _ := json.Marshal(message)
	actionJSON, _ := json.Marshal(code)
	retryJSON, _ := json.Marshal(retryable)
	reasonCodeJSON, _ := json.Marshal(decision.Reason)
	var frames string
	switch protocol {
	case ProtocolAnthropic:
		frames = fmt.Sprintf(
			"event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":%s,\"reason\":%s,\"retryable\":%s,\"code\":%s}}\n\n",
			reasonJSON, reasonCodeJSON, retryJSON, actionJSON)
	case ProtocolOpenAIResponses:
		frames = fmt.Sprintf(
			"event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":%s,\"message\":%s,\"reason\":%s,\"retryable\":%s}}}\n\n",
			actionJSON, reasonJSON, reasonCodeJSON, retryJSON)
	default: // OpenAI Chat
		frames = fmt.Sprintf(
			"data: {\"error\":{\"message\":%s,\"type\":\"server_error\",\"code\":%s,\"reason\":%s,\"retryable\":%s}}\n\ndata: [DONE]\n\n",
			reasonJSON, actionJSON, reasonCodeJSON, retryJSON)
	}
	if _, err := sw.Write([]byte(frames)); err == nil {
		sw.Flush()
	}
}
