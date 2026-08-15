// durable_wiring.go — SR-W3 request-entry durable execution wiring (doc 18 §6/§12).
//
// When armed (SetDurableExecution from main.go, behind LLM_GATEWAY_DURABLE_EXECUTION),
// every survival-coordinated request creates its durable task at entry:
// the encrypted snapshot + initial lease land in PostgreSQL before the first
// upstream attempt; the lease rides the request context; every per-attempt
// gate's BeforeSemanticCommit persists the PostgreSQL write-ahead checkpoint
// before semantic bytes flush; and the coordinator's verdict is mapped onto
// the terminal Complete/Fail transitions that enqueue the PendingStore
// projection outbox row.
package streaming

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/durabletask"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/metrics"
	"github.com/kaixuan/llm-gateway-go/secret"
)

// SetDurableExecution arms durable request persistence. A nil store (the
// default) keeps every request non-durable — zero behavior change.
func (h *ChatHandler) SetDurableExecution(store *durabletask.Store, kr *secret.Keyring, cfg durabletask.ForegroundConfig) {
	h.durableStore = store
	h.durableKeyring = kr
	h.durableConfig = cfg
}

// durableClientProtocolName maps the gate protocol onto the snapshot's
// client_protocol field (same values protocolMetricLabel emits).
func durableClientProtocolName(p ClientProtocol) string {
	switch p {
	case ProtocolOpenAIResponses:
		return "openai_responses"
	case ProtocolAnthropic:
		return "anthropic"
	default:
		return "openai_chat"
	}
}

// beginDurable creates and claims the durable task for one request. Failure
// to begin durable execution is fail-open with an error log: the request
// still serves, just without durable recovery (the feature is opt-in per
// deployment and gated by config).
func (h *ChatHandler) beginDurable(r *http.Request, params *executors.ExecParams, protocol ClientProtocol, tenantID string) *durabletask.Foreground {
	if h.durableStore == nil {
		return nil
	}
	if tenantID == "" {
		tenantID = "default" // single-tenant legacy mode
	}
	snapshot := durabletask.DurableRequestSnapshotV1{
		Version:        durabletask.SnapshotVersionV1,
		RequestID:      params.RequestID,
		RequestHash:    durabletask.HashRequestBody(params.BodyBytes),
		TenantID:       tenantID,
		SessionID:      params.SessionID,
		Endpoint:       r.URL.Path,
		ClientProtocol: durableClientProtocolName(protocol),
		ClientModel:    params.ClientModel,
		NormalizedBody: json.RawMessage(params.BodyBytes),
		Safety: durabletask.RequestSafetyV1{
			HasTools: params.ToolsRequested,
		},
	}
	if params.AppID != nil {
		snapshot.ApplicationID = strconv.Itoa(*params.AppID)
	}
	if params.KeyID > 0 {
		snapshot.APIKeyID = strconv.Itoa(params.KeyID)
	}
	if params.Policy != nil {
		if raw, err := json.Marshal(params.Policy); err == nil {
			snapshot.Policy = raw
		}
	}
	fg, err := durabletask.BeginForeground(r.Context(), h.durableStore, snapshot, h.durableConfig)
	if err != nil {
		slog.Error("durable: begin failed; serving without durable execution",
			"request_id", params.RequestID, "error", err)
		return nil
	}
	slog.Info("durable: task created",
		"task_id", fg.Lease.TaskID, "request_id", params.RequestID,
		"fencing_token", fg.Lease.FencingToken, "deadline_at", fg.DeadlineAt)
	return fg
}

// durableCheckpointHook adapts the gate's commit state onto the durable
// write-ahead checkpoint. Unknown states fail closed as content; a lost
// lease aborts the flush (the attempt is fenced off).
func durableCheckpointHook(ctx context.Context, fg *durabletask.Foreground) func(CommitState) error {
	if fg == nil {
		return nil
	}
	return func(state CommitState) error {
		durable, ok := durabletask.CommitStateFromString(state.String())
		if !ok {
			durable = durabletask.CommitContent
		}
		if err := fg.Checkpoint(ctx, durable); err != nil {
			if errors.Is(err, durabletask.ErrLeaseLost) {
				metrics.DurableLeaseLostTotal.Inc()
				slog.Warn("durable: checkpoint fenced off; aborting semantic flush",
					"task_id", fg.Lease.TaskID, "state", state.String())
			}
			return err
		}
		return nil
	}
}

// finishDurable maps the coordinator verdict onto the terminal durable
// transition. Wave 2 maps every failure to permanent_failed; Wave 3's replay
// executor will reschedule recoverable outcomes instead.
func (h *ChatHandler) finishDurable(ctx context.Context, fg *durabletask.Foreground, res SurvivalResult, requestID string) {
	if fg == nil {
		return
	}
	if res.Succeed {
		body, contentType := durableResultBody(res.FinalAttempt, fg, requestID)
		if _, err := fg.Complete(ctx, body, contentType); err != nil {
			logDurableTerminalError("complete", fg, err)
		}
		return
	}
	reason := res.Decision.Reason
	if reason == "" {
		reason = "survival_failed"
	}
	if _, err := fg.Fail(ctx, durabletask.FailureParams{
		Status:     durabletask.StatusPermanentFailed,
		ReasonCode: reason,
		ErrorKind:  "survival_terminal",
	}); err != nil {
		logDurableTerminalError("fail", fg, err)
	}
}

// durableResultBody extracts the persisted result body. Non-stream captures
// carry ResponseBody; streamed responses are not yet byte-captured end-to-end
// (Wave 3: gate-owned capture), so a status stub is persisted instead — the
// reconnect path still learns the task completed and can consult the
// PendingStore projection.
func durableResultBody(attempt *AttemptResult, fg *durabletask.Foreground, requestID string) ([]byte, string) {
	if attempt != nil && attempt.ExecResult != nil && len(attempt.ExecResult.ResponseBody) > 0 {
		return attempt.ExecResult.ResponseBody, "application/json"
	}
	stub, err := json.Marshal(map[string]any{
		"status":     "completed",
		"task_id":    fg.Lease.TaskID,
		"request_id": requestID,
		"note":       "streamed body not captured (Wave 3)",
	})
	if err != nil {
		stub = []byte(`{"status":"completed"}`)
	}
	return stub, "application/json"
}

func logDurableTerminalError(stage string, fg *durabletask.Foreground, err error) {
	if errors.Is(err, durabletask.ErrLeaseLost) {
		metrics.DurableLeaseLostTotal.Inc()
		slog.Warn("durable: terminal transition fenced off", "task_id", fg.Lease.TaskID, "stage", stage)
		return
	}
	slog.Error("durable: terminal transition failed", "task_id", fg.Lease.TaskID, "stage", stage, "error", err)
}

// durableForegroundLeaseRenewal cadence for the foreground keeper loop (doc
// 18 §13.1 worker lease 60s; foreground renews at a third of its lease).
const durableForegroundRenewInterval = 30 * time.Second

// durableRenewLoop keeps the foreground lease alive for the duration of the
// request so a long streaming attempt is never stolen by the recovery worker
// mid-flight. A renew failure ends the loop — the worker may take over; the
// fenced writes keep the takeover safe.
func durableRenewLoop(ctx context.Context, fg *durabletask.Foreground) {
	ticker := time.NewTicker(durableForegroundRenewInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := fg.RenewNow(ctx); err != nil {
				if !errors.Is(err, durabletask.ErrLeaseLost) {
					slog.Warn("durable: foreground renew errored", "task_id", fg.Lease.TaskID, "error", err)
				}
				return
			}
		}
	}
}
