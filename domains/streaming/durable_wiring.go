package streaming

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/durable"
)

// durable_wiring.go — SR-12 handler 侧 durable 装配（doc 18 §9.4/§11.2/§11.3）。
//
// 三协议 handler 在「鉴权、session 归属校验、tool_ids 展开、body 规范化之后、
// 候选解析之前」的快照切点调用 maybeStartDurable。非流式且客户端完成
// durable 握手（capability + respond-async）时构造冻结的
// DurableRequestSnapshotV1、CreateAndClaim 并立即 Reschedule 交接给
// RecoveryWorker（doc 18 §11.3「创建后直接后台执行」模式：Handler 不得
// 同时调用 attempt），随后渲染 §9.4 的 202 envelope。流式 durable 在
// checkpoint 绑定完成前 fail-closed（明确 unsupported）。零值 handler 完全
// 不进入本路径——SetDurableExecution 之前 durable 不存在。

const httpAccepted = http.StatusAccepted

// DurablePolicyVersionV1 identifies the durable/survival policy family that
// governed task creation; snapshot consumers compare it before rebuilding.
const DurablePolicyVersionV1 = "survival-policy-v1"

// DurableHandlerStore is the narrow durable repository surface the handler
// needs: create-and-claim at the snapshot cut point, then the worker
// handoff. *durable.Store satisfies it.
type DurableHandlerStore interface {
	CreateAndClaim(context.Context, durable.NewTask) (*durable.Task, error)
	Reschedule(context.Context, durable.RescheduleParams) error
}

// DurableExecutionOptions tunes the handler-side durable lifecycle. Zero
// values fall back to the doc 18 §13 defaults.
type DurableExecutionOptions struct {
	// Deadline is the task budget (config durable deadline, default 24h).
	Deadline time.Duration
	// ResultReadWindow is how long the terminal result stays readable
	// after the deadline; it seeds ExpiresAt (Redis projection TTL base).
	ResultReadWindow time.Duration
	// FrontendLease bounds the lease held between CreateAndClaim and the
	// worker handoff. If the handoff Reschedule fails, the worker still
	// recovers the task once this lease expires.
	FrontendLease time.Duration
	// RetryAfter is the 202 Retry-After hint in seconds.
	RetryAfter int
}

func (o DurableExecutionOptions) withDefaults() DurableExecutionOptions {
	if o.Deadline <= 0 {
		o.Deadline = 24 * time.Hour
	}
	if o.ResultReadWindow <= 0 {
		o.ResultReadWindow = time.Hour
	}
	if o.FrontendLease <= 0 {
		o.FrontendLease = 15 * time.Second
	}
	if o.RetryAfter <= 0 {
		o.RetryAfter = 5
	}
	return o
}

// DurableSnapshotInput carries everything the three protocol handlers have
// already resolved at the frozen snapshot cut point (doc 18 §11.2: after
// auth, session ownership, tool_ids expansion and normalization; before
// candidate resolution).
type DurableSnapshotInput struct {
	Protocol        string // executor ClientProtocol of the endpoint
	Endpoint        string // request path
	TenantID        string
	ApplicationID   int
	APIKeyID        int
	SessionID       string // real gateway session (empty → sync semantics)
	SessionSource   string // body | header | auto | resume
	ClientModel     string
	Body            []byte // normalized request body
	IdentityHash    string // full client identity hash
	ClientProfile   string
	RequestID       string
	ParentRequestID string
	ToolsRequested  bool
}

// durableDecision tells the calling handler whether maybeStartDurable owns
// the response.
type durableDecision int

const (
	// durableProceed: no durable handoff happened (or preconditions were
	// unmet); the handler continues on the normal sync path.
	durableProceed durableDecision = iota
	// durableHandled: the 202 envelope (or a fail-closed error) has been
	// written; the handler must return immediately.
	durableHandled
)

// SetDurableExecution arms the handler-side durable path. A nil store (the
// zero state) keeps durable fully disabled — requests carrying the
// capability header are served with normal sync semantics.
func (h *ChatHandler) SetDurableExecution(store DurableHandlerStore, tenantAllowed func(tenantID string) bool, opts DurableExecutionOptions) {
	h.durableStore = store
	h.durableTenantAllowed = tenantAllowed
	h.durableExecOptions = opts.withDefaults()
}

// maybeStartDurable evaluates the durable handoff at the snapshot cut point.
// Non-stream requests that completed the handshake (capability header +
// Prefer: respond-async) create + claim the task, hand it to the recovery
// worker via Reschedule (doc 18 §11.3「创建后直接后台执行」mode — the handler
// never calls attempt) and render the frozen §9.4 202 envelope. Streaming
// durable requests fail closed until checkpoint binding lands.
func (h *ChatHandler) maybeStartDurable(w http.ResponseWriter, r *http.Request, in DurableSnapshotInput, isStream bool) durableDecision {
	if h.durableStore == nil || !DurableRequested(r, isStream) {
		return durableProceed
	}
	if h.durableTenantAllowed != nil && !h.durableTenantAllowed(in.TenantID) {
		// Tenant not authorized for durable: capability unmet, sync
		// terminal semantics (§6 — never auto-durable without handshake).
		return durableProceed
	}
	if isStream {
		writeErrorJSON(w, http.StatusNotImplemented, in.RequestID,
			"streaming durable recovery is not enabled on this gateway",
			"api_error", "streaming_durable_unsupported")
		return durableHandled
	}
	// Real-session / DB-auth preconditions: without them no durable task
	// can be built — keep sync semantics rather than half-honoring.
	if in.SessionID == "" || in.TenantID == "" || in.APIKeyID <= 0 {
		return durableProceed
	}

	opts := h.durableExecOptions
	now := time.Now()
	snapshot := DurableRequestSnapshotV1{
		Version:            DurableSnapshotVersionV1,
		Endpoint:           in.Endpoint,
		ClientProtocol:     in.Protocol,
		ClientModel:        in.ClientModel,
		NormalizedBody:     json.RawMessage(in.Body),
		RequestHash:        hashBody(in.Body),
		APIKeyID:           in.APIKeyID,
		TenantID:           in.TenantID,
		ApplicationID:      in.ApplicationID,
		SessionID:          in.SessionID,
		SessionSource:      in.SessionSource,
		ClientIdentityHash: in.IdentityHash,
		ToolsRequested:     in.ToolsRequested,
		PolicyVersion:      DurablePolicyVersionV1,
		RequestID:          in.RequestID,
		TaskCorrelationID:  in.RequestID,
		ParentRequestID:    in.ParentRequestID,
	}
	if modality := detectRequestModality(in.Body); modality != "" && modality != modalityText {
		snapshot.HasMultimodalContent = true
		snapshot.ResponseFormat = durableResponseFormat(in.Body)
	}
	marshaled, err := MarshalDurableSnapshotV1(snapshot)
	if err != nil {
		// Validation failure is a handler-side assembly bug: fail closed
		// rather than silently degrading an explicitly-async request.
		writeErrorJSON(w, http.StatusServiceUnavailable, in.RequestID,
			"durable request snapshot rejected", "api_error", "durable_snapshot_invalid")
		return durableHandled
	}

	deadline := now.Add(opts.Deadline)
	// Store operations must survive a client disconnect between sending the
	// request and receiving the 202: the async handoff exists precisely so
	// the task survives the connection. Bounded so a stuck DB cannot pin
	// the handler forever.
	storeCtx, cancelStore := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Second)
	defer cancelStore()
	task, err := h.durableStore.CreateAndClaim(storeCtx, durable.NewTask{
		TenantID:        in.TenantID,
		RequestID:       in.RequestID,
		SessionID:       in.SessionID,
		Protocol:        in.Protocol,
		Endpoint:        in.Endpoint,
		Snapshot:        marshaled,
		SnapshotVersion: DurableSnapshotVersionV1,
		RequestHash:     snapshot.RequestHash,
		DeadlineAt:      deadline,
		ExpiresAt:       deadline.Add(opts.ResultReadWindow),
		LeaseOwner:      "gateway-frontend",
		LeaseUntil:      now.Add(opts.FrontendLease),
	})
	if err != nil {
		if errors.Is(err, durable.ErrDuplicateTask) {
			// Same (tenant, request) task already exists — idempotent
			// replay: point the client at the existing pending response.
			renderDurableAccepted(w, in.SessionID, in.RequestID, "", opts.RetryAfter)
			return durableHandled
		}
		// §11.2 fail closed: the create transaction failed, so the gateway
		// must not claim durable ownership — and must not silently turn an
		// explicitly-async request into a sync one either.
		writeErrorJSON(w, http.StatusServiceUnavailable, in.RequestID,
			"durable task store unavailable", "api_error", "durable_unavailable")
		return durableHandled
	}

	// Hand the claimed task to the recovery worker（创建后直接后台执行）.
	// A failed handoff is not fatal: the frontend lease expires and
	// ClaimRunnable recovers the task on its own.
	if err := h.durableStore.Reschedule(storeCtx, durable.RescheduleParams{
		TaskID:       task.ID,
		LeaseOwner:   task.LeaseOwner,
		FencingToken: task.FencingToken,
		NextRetryAt:  now,
		Reason:       "handoff_to_worker",
		Attempt:      task.AttemptCount,
	}); err != nil && !errors.Is(err, durable.ErrLeaseLost) {
		slog.Warn("durable handoff to worker failed; lease expiry will recover",
			"task_id", task.ID, "error", err)
	}

	renderDurableAccepted(w, in.SessionID, in.RequestID, task.ID, opts.RetryAfter)
	return durableHandled
}

// durableResponseFormat extracts the response_format type flag (a §10.3
// replay-safety marker) from a normalized body.
func durableResponseFormat(body []byte) string {
	var probe struct {
		ResponseFormat struct {
			Type string `json:"type"`
		} `json:"response_format"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return ""
	}
	return probe.ResponseFormat.Type
}

func hashBody(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// deriveSessionSource reports where the resolved session came from, without
// touching the resolution branches themselves: body declaration beats
// header, anything else (assignment/auto-create) is "auto".
func deriveSessionSource(body []byte, r *http.Request) string {
	if extractSessionIDFromBody(body) != "" {
		return "body"
	}
	if extractSessionIDFromHeaders(r) != "" {
		return "header"
	}
	return "auto"
}

func appIDValue(ki *authentication.KeyInfo) int {
	if v := appID(ki); v != nil {
		return *v
	}
	return 0
}

func apiKeyIDValue(ki *authentication.KeyInfo) int {
	if v := apiKeyIDPtr(ki); v != nil {
		return *v
	}
	return 0
}

// renderDurableAccepted writes the frozen §9.4 gateway async envelope. The
// three endpoints share this shape; it must not masquerade as a standard
// Chat/Responses/Anthropic success object.
func renderDurableAccepted(w http.ResponseWriter, sessionID, requestID, taskID string, retryAfterSeconds int) {
	if retryAfterSeconds <= 0 {
		retryAfterSeconds = 5
	}
	pollURL := "/v1/sessions/" + sessionID + "/pending-response?request_id=" + requestID
	h := w.Header()
	h.Set("Preference-Applied", "respond-async")
	h.Set("Location", pollURL)
	h.Set("Retry-After", strconv.Itoa(retryAfterSeconds))
	h.Set("X-Gw-Pending", sessionID)
	h.Set("X-Gw-Pending-Request", requestID)
	if taskID != "" {
		h.Set("X-Gw-Task-Id", taskID)
	}
	h.Set("Content-Type", "application/json")
	w.WriteHeader(httpAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":      "in_progress",
		"task_id":     taskID,
		"session_id":  sessionID,
		"request_id":  requestID,
		"retry_after": retryAfterSeconds,
		"poll_url":    pollURL,
	})
}
