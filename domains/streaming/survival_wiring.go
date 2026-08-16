package streaming

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

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

	// A-P2-6 write-unification: the pre-stream keepalive goroutine writes
	// the connection directly; from here on the coordinator's serialized
	// writer is the single writer (recovery keepalives flow through it).
	// Stop the pre-stream sender first so the two can never interleave.
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
		Exec:     attemptExec,
		Protocol: protocol,
		Options:  h.survivalOptions,
		Refresh: func(ctx context.Context) {
			cands, _, _, err := resolveCandidatesForRequest(
				ctx, h.provider, params.ClientModel, params.ClientID.Fingerprint.ClientProfile,
				tenantID, params.BodyBytes,
			)
			if err == nil && len(cands) > 0 {
				params.Candidates = cands
			}
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
	slog.Info("request_survival_finished",
		"request_id", params.RequestID,
		"succeed", res.Succeed,
		"attempts", res.Attempts,
		"decision", res.Decision.Action.String(),
		"reason", res.Decision.Reason,
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
	return func(_ context.Context, state CommitState) error { return durable.Checkpoint(state) }
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
