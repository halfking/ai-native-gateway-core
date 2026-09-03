package streaming

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/internal/retryowner"
)

// survival_wiring.go — SR-07 (doc 18 §6 分层启用, §5.1 SurvivalCoordinator)
//
// Production wiring for the SR-W2 in-connection survival path: the handler
// branch in serveWithExecutor delegates to runSurvivalCoordinator when
// request survival is enabled for the tenant. Everything here is inert
// until SetRequestSurvival is called (main.go) — the flag-off path never
// constructs a coordinator.

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
	)
	if durable != nil {
		var body []byte
		contentType := ""
		if capture != nil {
			body, contentType = capture.result()
		}
		settleDurableStream(frozenCtx, durable, res, body, contentType, frozenCtx.Err() != nil)
	}
	if res.Succeed {
		return res.FinalAttempt.ExecResult, nil
	}
	var err error
	if res.FinalAttempt != nil {
		err = res.FinalAttempt.FinalError
	}
	if err == nil {
		err = fmt.Errorf("request survival ended: %s (%s)", res.Decision.Action, res.Decision.Reason)
	}
	return nil, err
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
		return durable.CheckpointContext(ctx, state)
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
	reasonJSON, _ := json.Marshal("gateway request survival ended: " + decision.Reason)
	actionJSON, _ := json.Marshal("gateway_survival_" + decision.Action.String())
	var frames string
	switch protocol {
	case ProtocolAnthropic:
		frames = fmt.Sprintf(
			"event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":%s}}\n\n",
			reasonJSON)
	case ProtocolOpenAIResponses:
		frames = fmt.Sprintf(
			"event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":%s,\"message\":%s}}}\n\n",
			actionJSON, reasonJSON)
	default: // OpenAI Chat
		frames = fmt.Sprintf(
			"data: {\"error\":{\"message\":%s,\"type\":\"server_error\",\"code\":%s}}\n\ndata: [DONE]\n\n",
			reasonJSON, actionJSON)
	}
	if _, err := sw.Write([]byte(frames)); err == nil {
		sw.Flush()
	}
}
