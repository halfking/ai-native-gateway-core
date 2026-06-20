package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/kaixuan/llm-gateway-go/audit"
	"github.com/kaixuan/llm-gateway-go/auth"
	"github.com/kaixuan/llm-gateway-go/autoroute"
	"github.com/kaixuan/llm-gateway-go/circuit"
	"github.com/kaixuan/llm-gateway-go/compressor"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/identity"
	"github.com/kaixuan/llm-gateway-go/internal/observability"
	"github.com/kaixuan/llm-gateway-go/limiter"
	"github.com/kaixuan/llm-gateway-go/maas"
	"github.com/kaixuan/llm-gateway-go/pool"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/ratelimit"
	"github.com/kaixuan/llm-gateway-go/resolve"
	"github.com/kaixuan/llm-gateway-go/routing"
	"github.com/kaixuan/llm-gateway-go/sessions"
	"github.com/kaixuan/llm-gateway-go/telemetry"
	"github.com/kaixuan/llm-gateway-go/transform"
	upstreampkg "github.com/kaixuan/llm-gateway-go/upstream"
	"go.opentelemetry.io/otel/trace"
)

const maxBodySize = 32 << 20

func MaxBodySize() int { return maxBodySize }

func sanitizeGwSessionHeader(v string) string {
	s := strings.TrimSpace(v)
	if s == "" {
		return ""
	}
	// V2 gateway sessions are always gw_<uuid>. Treat plain UUID-style
	// values as client metadata/session identifiers, not gateway session IDs.
	if !strings.HasPrefix(s, "gw_") {
		return ""
	}
	return s
}

// ServiceID maps an API key to a (providerID, credentialID) pair.
type ServiceID struct {
	ProviderID   int
	CredentialID int
}

// defaultService returns the default provider+credential from env vars.
func defaultService() ServiceID {
	pid, _ := strconv.Atoi(os.Getenv("LLM_GATEWAY_DEFAULT_PROVIDER"))
	cid, _ := strconv.Atoi(os.Getenv("LLM_GATEWAY_DEFAULT_CREDENTIAL"))
	if pid == 0 {
		pid = 1
	}
	if cid == 0 {
		cid = 1
	}
	return ServiceID{ProviderID: pid, CredentialID: cid}
}

type chatRequestBody struct {
	Model    string          `json:"model"`
	Stream   bool            `json:"stream"`
	Messages json.RawMessage `json:"messages,omitempty"`
	User     string          `json:"user,omitempty"`
	// Tools is the optional function/tool definitions array.
	// Used by autoroute (v2.0) to detect multi-tool agent requests.
	Tools json.RawMessage `json:"tools,omitempty"`
}

type chatResponseBody struct {
	Model string `json:"model"`
}

//-----------------------------------------------------------------------------
// Chat handler — integrates circuit breaker + concurrency limiter
//-----------------------------------------------------------------------------

type providerResolver interface {
	Enabled() bool
	GetCandidates(ctx context.Context, model, profile string) ([]provider.Candidate, *provider.Policy, error)
}

// ChatHandler handles chat completions with circuit breaker and concurrency control.
type ChatHandler struct {
	circuit         *circuit.Manager
	limiter         *limiter.Limiter
	matrix          *transform.Matrix
	pools           *pool.PoolManager
	resolver        *resolve.Resolver
	auditor         audit.Sink
	client          *upstreampkg.Client
	normalizer      *Normalizer
	executor        *routing.Executor
	provider        providerResolver
	sticky          *routing.StickyCache
	keyVerifier     *auth.KeyVerifier
	rateLimiter     ratelimit.RPMLimiter
	telemetryClient *telemetry.Client
	// decider (v2.0) is the optional autoroute.Decider. When non-nil,
	// requests with model="auto" trigger task classification + 6-dim
	// scoring. When nil, model="auto" falls back to default chat model.
	decider *autoroute.Decider
	// requestLogHook is an optional test sink.  When set, every
	// request_logs row the gateway emits is also passed to the hook
	// function so unit tests can assert on the safety-net coverage.
	// See SetRequestLogHook.
	requestLogHook func(*telemetry.RequestLogEntry)
	maasSvc        *maas.Service
	sessionGetter  interface {
		Get(ctx context.Context, id string) (*sessions.Session, error)
		Touch(ctx context.Context, id string) error
		CreateV2(ctx context.Context, apiKeyID int, tenantID, deviceSeed, taskID string) (*sessions.Session, error)
		BindAPIKey(ctx context.Context, sessionID string, apiKeyID int, tenantID string) error
	}
	// idempotentCache (Track C C5, 2026-06-18) deduplicates
	// re-sent requests within a 5-minute window. When a client
	// retries (network glitch, double-click), the handler
	// returns 202 + X-Gw-Pending immediately rather than
	// re-executing the full routing + vendor path. nil disables
	// the dedup (every request is treated as new).
	idempotentCache *IdempotentCache

	// sessionCompressor (v3, 2026-06-19) is the session-level
	// intelligent compressor. When non-nil, each request runs a
	// message-level LCS delta-append + optional proactive sliding-window
	// LLM summary before forwarding to the upstream. nil disables v3
	// (every request forwards the client body as-is, matching v7 behaviour).
	sessionCompressor *compressor.SessionCompressor

	// metaToolInterceptor (Phase 2, 2026-06-20) handles meta-tool calls
	// (list_categories, load_tools) locally without forwarding to upstream.
	// nil disables Phase 2 meta-tools.
	metaToolInterceptor *MetaToolInterceptor
}

func NewChatHandler(cm *circuit.Manager, l *limiter.Limiter, matrix *transform.Matrix, pools *pool.PoolManager, resolver *resolve.Resolver, auditor audit.Sink) *ChatHandler {
	if auditor == nil {
		auditor = &audit.LogSink{}
	}
	return &ChatHandler{circuit: cm, limiter: l, matrix: matrix, pools: pools, resolver: resolver, auditor: auditor, client: upstreampkg.New(), normalizer: NewNormalizer()}
}

func (h *ChatHandler) SetExecutor(exec *routing.Executor, prov providerResolver, sticky *routing.StickyCache) {
	h.executor = exec
	h.provider = prov
	h.sticky = sticky
}

// SetIdempotentCache (Track C C5, 2026-06-18) wires the
// duplicate-request detector. nil disables dedup; every
// request is treated as new. Production wiring in
// cmd/gateway/main.go calls this with a non-nil cache so
// that double-clicks and network retries get an instant
// 202 + X-Gw-Pending response.
func (h *ChatHandler) SetIdempotentCache(c *IdempotentCache) {
	h.idempotentCache = c
}

// SetSessionCompressor wires the v3 session-level intelligent compressor.
// When set, each request performs message-level delta-append + optional
// proactive sliding-window LLM summary before forwarding to the upstream.
func (h *ChatHandler) SetSessionCompressor(sc *compressor.SessionCompressor) {
	h.sessionCompressor = sc
}

// SetMetaToolInterceptor wires the Phase 2 meta-tool interceptor.
// When set, requests containing meta-tool calls (list_categories, load_tools)
// are handled locally without forwarding to upstream LLM providers.
func (h *ChatHandler) SetMetaToolInterceptor(i *MetaToolInterceptor) {
	h.metaToolInterceptor = i
}

func (h *ChatHandler) SetAuth(kv *auth.KeyVerifier, rl ratelimit.RPMLimiter) {
	h.keyVerifier = kv
	h.rateLimiter = rl
}

func (h *ChatHandler) SetTelemetry(tc *telemetry.Client) {
	h.telemetryClient = tc
}

func (h *ChatHandler) SetMaas(svc *maas.Service) {
	h.maasSvc = svc
}

// SetRequestLogHook installs an in-memory sink that records every
// request_logs row the safety-net (or the success path) emits.  It is
// used by unit tests in this package to assert that every error exit
// path still produces a row.  Passing nil clears the hook (the
// default is nil; production callers should never set a hook).
//
// The hook is best-effort: if it is set and the entry is nil, the
// hook does nothing.  Concurrent appends are guarded by a mutex so
// tests that fire many requests in parallel can inspect the
// collected slice without racing.
func (h *ChatHandler) SetRequestLogHook(hook func(*telemetry.RequestLogEntry)) {
	h.requestLogHook = hook
}

func (h *ChatHandler) SetSessionGetter(sg interface {
	Get(ctx context.Context, id string) (*sessions.Session, error)
	Touch(ctx context.Context, id string) error
	CreateV2(ctx context.Context, apiKeyID int, tenantID, deviceSeed, taskID string) (*sessions.Session, error)
	BindAPIKey(ctx context.Context, sessionID string, apiKeyID int, tenantID string) error
}) {
	h.sessionGetter = sg
}

// ServeHTTP handles /v1/chat/completions and /v1/completions.
func (h *ChatHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// ── requestAttempt safety-net: every request that reaches this
	//    handler must produce exactly one request_logs row, regardless
	//    of which early-return path it takes.  attemptErrCode is
	//    populated by the inner functions when they exit without
	//    writing a row themselves; the deferred block at the end
	//    of this function writes the row using those fields.  The
	//    *attemptLogged bool is shared with the inner functions via
	//    pointer so success / explicit-failure paths can mark the
	//    row as already-written to avoid double-logging.
	var logCtx *RequestLogContext
	requestID := r.Header.Get("X-Request-Id")
	if requestID == "" {
		requestID = generateRequestID()
		w.Header().Set("X-Request-Id", requestID)
	}
	startTime := time.Now()
	logCtx = h.NewRequestLogContext(r, requestID, startTime)
	if wt := strings.TrimSpace(r.Header.Get(autoWorkTypeHeader)); wt != "" {
		logCtx.SetWorkType(wt)
	}
	defer func() {
		slog.Info("safety_net_defer_fired",
			"request_id", requestID,
			"attempt_err_code", logCtx.ErrCode,
			"attempt_logged", logCtx.IsLogged())
		if rec := recover(); rec != nil {
			slog.Error("chat handler panic", "panic", rec, "request_id", requestID)
			logCtx.SetError("internal_panic", "internal server error")
			if len(logCtx.Body) == 0 {
				logCtx.EnsureCaptured()
			}
			if logCtx.ClientModel == "" {
				if len(logCtx.Body) > 0 {
					logCtx.SetClientModel(extractModelFromBody(logCtx.Body))
				}
				if logCtx.ClientModel == "" {
					logCtx.SetClientModel("<unknown>")
				}
			}
			logCtx.EmitFailure(logCtx.ErrCode, logCtx.ErrMsg, logCtx.ProviderID, logCtx.CredentialID)
			writeErrorJSON(w, http.StatusInternalServerError, requestID,
				"internal server error", "server_error", "internal_panic")
		} else if logCtx.ErrCode != "" && !logCtx.IsLogged() {
			slog.Info("safety_net: recording failed request",
				"request_id", requestID,
				"error_kind", logCtx.ErrCode,
				"client_model", logCtx.ClientModel)
			logCtx.EmitFailure(logCtx.ErrCode, logCtx.ErrMsg, logCtx.ProviderID, logCtx.CredentialID)
		}
	}()

	// GET probe — return 200 for client compatibility checks
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":  "ok",
			"message": "Chat completions endpoint is available. Use POST to send requests.",
		})
		return
	}
	if r.Method != http.MethodPost {
		logCtx.SetError("method_not_allowed", "method not allowed")
		// 2026-06-20 audit fix: capture the body + model so the
		// request_logs row records what the client actually sent
		// (not just "method not allowed"). Without this every
		// 405 row shows empty body + model="<unknown>" and the
		// operator can't tell which client / tool sent the
		// wrong method or which model it was trying to reach.
		// EmitFailure here (not relying on the safety net)
		// because the safety-net path runs only when the inner
		// pipeline never returned; an early 405 should produce
		// a single, fully-populated row.
		logCtx.EnsureCaptured()
		if logCtx.ClientModel == "" {
			if len(logCtx.Body) > 0 {
				logCtx.SetClientModel(extractModelFromBody(logCtx.Body))
			}
			if logCtx.ClientModel == "" {
				logCtx.SetClientModel("<unknown>")
			}
		}
		logCtx.EmitFailure(logCtx.ErrCode, logCtx.ErrMsg, nil, nil)
		logCtx.MarkLogged()
		http.Error(w, `{"error":{"message":"Method not allowed","type":"invalid_request","code":"method_not_allowed"}}`, http.StatusMethodNotAllowed)
		return
	}

	if h.executor != nil && h.provider != nil && h.provider.Enabled() {
		h.serveWithExecutor(w, r, logCtx)
		return
	}
	logCtx.SetError("executor_unavailable", "routing executor not available; database connection required")
	logCtx.EnsureCaptured()
	logCtx.EmitFailure(logCtx.ErrCode, logCtx.ErrMsg, nil, nil)
	logCtx.MarkLogged()
	h.serveFallback(w, r)
}

// serveWithExecutor is the main chat-completions / completions pipeline.
// It receives pointers to the safety-net attempt state from ServeHTTP
// so that any exit path can populate them and the deferred logger in
// the caller will record exactly one request_logs row.  attemptLogged
// is set to true by any inner function that has already recorded the
// row (e.g. via recordFailedRequest or emitTelemetry on the success
// path) so the deferred safety net does not duplicate it.
func (h *ChatHandler) serveWithExecutor(
	w http.ResponseWriter,
	r *http.Request,
	logCtx *RequestLogContext,
) {
	defer r.Body.Close()

	requestID := logCtx.RequestID
	startTime := logCtx.StartTime
	logCtx.EnsureCaptured()

	markLogged := func() { logCtx.MarkLogged() }
	
	// 2026-06-20 audit fix helper: capture body + model + emit failure
	// for early-exit error paths. Without this every 405/401/400/503
	// row shows empty body + model="<unknown>" and the operator cannot
	// tell which client sent the bad request or which model it was
	// trying to reach (the symptom that triggered the comprehensive audit).
	captureAndEmitFailure := func(errCode, errMsg string, providerID, credentialID *int) {
		logCtx.SetError(errCode, errMsg)
		logCtx.EnsureCaptured()
		if logCtx.ClientModel == "" {
			if len(logCtx.Body) > 0 {
				logCtx.SetClientModel(extractModelFromBody(logCtx.Body))
			}
			if logCtx.ClientModel == "" {
				logCtx.SetClientModel("<unknown>")
			}
		}
		logCtx.EmitFailure(errCode, errMsg, providerID, credentialID)
		logCtx.MarkLogged()
	}

	// ── API key authentication ──────────────────────────────────────────
	var keyInfo *auth.KeyInfo
	if h.keyVerifier != nil && h.keyVerifier.Enabled() {
		rawKey := extractBearerToken(r)
		if rawKey == "" {
			captureAndEmitFailure("missing_key", "missing api key", nil, nil)
			writeErrorJSON(w, http.StatusUnauthorized, requestID, "Missing API key", "authentication_error", "missing_key")
			return
		}
		ki, verifyErr := h.keyVerifier.Verify(r.Context(), rawKey)
		if verifyErr != nil {
			if _, ok := verifyErr.(*auth.InvalidKeyError); ok {
				captureAndEmitFailure("invalid_key", "invalid or expired api key", nil, nil)
				w.Header().Set("WWW-Authenticate", "Bearer")
				writeErrorJSON(w, http.StatusUnauthorized, requestID, "Invalid or expired API key", "authentication_error", "invalid_key")
				return
			}
			slog.Error("key verification RPC failed, rejecting request", "error", verifyErr)
			captureAndEmitFailure("auth_unavailable", "authentication service temporarily unavailable", nil, nil)
			writeErrorJSON(w, http.StatusServiceUnavailable, requestID,
				"Authentication service temporarily unavailable", "server_error", "auth_unavailable")
			return
		}
		keyInfo = ki
		logCtx.SetKey(ki)

		// Round 38 (2026-06-16) — emit multi-tenant OTel span
		// attributes per docs/multi-tenant-otel-design.md §3.1.
		// llm-gateway-go is Pattern A (direct tenant_id from
		// auth.KeyInfo). Every authenticated request now carries
		// tenant.id so production debugging can filter Jaeger.
		span := trace.SpanFromContext(r.Context())
		observability.SetTenantAttrs(span, keyInfo.TenantID, "api_key",
			fmt.Sprintf("key_%d", keyInfo.ID))
	}

	// ── Status checks (throttled key → hard rate-limit) ────────────────
	if keyInfo != nil && keyInfo.Status == "throttled" {
		captureAndEmitFailure("key_throttled", "api key throttled due to anomalous usage", nil, nil)
		writeErrorJSON(w, http.StatusTooManyRequests, requestID,
			"Your API key has been throttled due to anomalous usage. Contact admin.",
			"rate_limit_error", "key_throttled")
		return
	}

	// ── RPM rate limit (unified via checkGatewayRateLimit) ──────────────
	if rlOutcome := checkGatewayRateLimit(keyInfo, h.rateLimiter); !rlOutcome.Skipped {
		writeRateLimitHeaders(w, rlOutcome)
		if rlOutcome.Blocked {
			captureAndEmitFailure("rate_limit_exceeded", "rate limit exceeded", nil, nil)
			writeErrorJSON(w, http.StatusTooManyRequests, requestID, "Rate limit exceeded", "rate_limit_error", "rate_limit_exceeded")
			return
		}
	}

	// ── Budget pre-check ─────────────────────────────────────────────────
	if keyInfo != nil && h.keyVerifier != nil {
		if budgetErr := h.keyVerifier.CheckBudget(r.Context(), keyInfo.ID); budgetErr != nil {
			if _, ok := budgetErr.(*auth.BudgetExceededError); ok {
				captureAndEmitFailure("budget_exhausted", "budget exhausted", nil, nil)
				writeErrorJSON(w, http.StatusPaymentRequired, requestID, "Budget exhausted. Contact admin to top up.", "insufficient_quota", "budget_exhausted")
				return
			}
		}
	}

	// ── MaaS credits pre-check (non-default tenants) ─────────────────────
	if keyInfo != nil && h.maasSvc != nil && keyInfo.TenantID != "" && keyInfo.TenantID != "default" {
		if err := h.maasSvc.PreCheckCredits(r.Context(), keyInfo.TenantID); err != nil {
			if _, ok := err.(*maas.InsufficientCreditsError); ok {
				captureAndEmitFailure("insufficient_credits", "insufficient credits", nil, nil)
				writeErrorJSON(w, http.StatusPaymentRequired, requestID, "Insufficient credits. Please subscribe or purchase a top-up package.", "insufficient_quota", "insufficient_credits")
				return
			}
		}
	}

	// ── Inject API Key info into context for session middleware ─────────
	ctx := r.Context()
	if keyInfo != nil {
		ctx = sessions.SetAPIKeyID(ctx, keyInfo.ID)
		ctx = sessions.SetTenantID(ctx, keyInfo.TenantID)
	}

	// ── Session validation (if X-Gw-Session-Id or X-Session-Id provided) ──
	var sessionInfo *sessions.Session
	sessionID := sanitizeGwSessionHeader(r.Header.Get("X-Gw-Session-Id"))
	if sessionID == "" {
		sessionID = r.Header.Get("X-Session-Id")
	}
	if sessionID != "" && h.sessionGetter != nil {
		si, err := h.sessionGetter.Get(ctx, sessionID)
		if err != nil {
			if err == sessions.ErrSessionNotFound && keyInfo != nil {
				deviceSeed := r.Header.Get("X-Device-Seed")
				if deviceSeed == "" {
					deviceSeed = r.Header.Get("X-Machine-Id")
				}
				if deviceSeed == "" {
					deviceSeed = "default"
				}
				taskID := r.Header.Get("X-Gw-Task-Id")
				newSession, createErr := h.sessionGetter.CreateV2(ctx, keyInfo.ID, keyInfo.TenantID, deviceSeed, taskID)
				if createErr != nil {
					slog.Error("session fallback create failed", "error", createErr, "session_id", sessionID)
				} else {
					sessionInfo = newSession
					sessionID = newSession.SessionID
					logCtx.SetSession(newSession)
					ctx = sessions.SessionFromContextWith(ctx, newSession)
					w.Header().Set("X-Gw-Session-Id-Resume", newSession.SessionID)
					if r.Header.Get("X-Session-Id") != "" {
						slog.Warn("legacy X-Session-Id used, fallback created; migrate to X-Gw-Session-Id",
							"original_session_id", r.Header.Get("X-Session-Id"),
							"new_session_id", newSession.SessionID,
						)
						w.Header().Set("Deprecation", "true")
					}
					slog.Info("session fallback created",
						"original_session_id", r.Header.Get("X-Gw-Session-Id"),
						"new_session_id", newSession.SessionID,
						"task_id", taskID,
					)
				}
			} else if err != sessions.ErrSessionNotFound {
				slog.Warn("session lookup failed", "error", err)
			}
		} else {
			sessionInfo = si
			logCtx.SetSession(si)
			if keyInfo != nil && si.APIKeyID != keyInfo.ID {
				if si.APIKeyID == 0 {
				if bindErr := h.sessionGetter.BindAPIKey(ctx, sessionID, keyInfo.ID, keyInfo.TenantID); bindErr != nil {
					slog.Warn("orphan session bind failed", "error", bindErr, "session_id", sessionID)
					captureAndEmitFailure("session_forbidden", "session not owned by this api key", nil, nil)
					writeErrorJSON(w, http.StatusForbidden, requestID, "session not owned by this API key", "session_error", "SESSION_FORBIDDEN")
					return
				}
				si.APIKeyID = keyInfo.ID
				si.TenantID = keyInfo.TenantID
				sessionInfo = si
			} else {
				captureAndEmitFailure("session_forbidden", "session not owned by this api key", nil, nil)
				writeErrorJSON(w, http.StatusForbidden, requestID, "session not owned by this API key", "session_error", "SESSION_FORBIDDEN")
				return
			}
			}
			go func() {
				touchCtx, touchCancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer touchCancel()
				h.sessionGetter.Touch(touchCtx, sessionID)
			}()
			ctx = sessions.SessionFromContextWith(ctx, sessionInfo)
		}
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, int64(maxBodySize)+1))
	if err != nil {
		logCtx.CapturePartialBody(bodyBytes)
		logCtx.SetError("body_read_error", fmt.Sprintf("failed to read request body: %v", err))
		slog.Warn("request body read failed",
			"request_id", requestID,
			"error", err,
			"content_length", r.ContentLength,
			"partial_bytes", len(bodyBytes),
			"client_model", logCtx.ClientModel,
			"latency_ms", time.Since(startTime).Milliseconds(),
		)
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{"message": "failed to read request body", "type": "invalid_request", "code": "body_read_error"},
		})
		return
	}
	if len(bodyBytes) > 0 {
		logCtx.Body = bodyBytes
	}
	if len(bodyBytes) > maxBodySize {
		// body_too_large already has body captured (it's in bodyBytes)
		// but we need to emit + mark to prevent safety net double-emit
		logCtx.SetError("body_too_large", "request body exceeds 32 MiB limit")
		if logCtx.ClientModel == "" {
			logCtx.SetClientModel(extractModelFromBody(bodyBytes[:maxBodySize]))
		}
		logCtx.EmitFailure("body_too_large", "request body exceeds 32 MiB limit", nil, nil)
		logCtx.MarkLogged()
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{
			"error": map[string]string{"message": "request body exceeds 32 MiB limit", "type": "invalid_request", "code": "body_too-large"},
		})
		return
	}

	var reqBody chatRequestBody
	if err := json.Unmarshal(bodyBytes, &reqBody); err != nil {
		// json_parse_error already has body captured (it's in bodyBytes)
		logCtx.SetError("json_parse_error", "invalid JSON in request body")
		if logCtx.ClientModel == "" {
			logCtx.SetClientModel(extractModelFromBody(bodyBytes))
		}
		logCtx.EmitFailure("json_parse_error", "invalid JSON in request body", nil, nil)
		logCtx.MarkLogged()
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{"message": "invalid JSON in request body", "type": "invalid_request", "code": "json_parse_error"},
		})
		return
	}

	clientModel := reqBody.Model
	logCtx.SetClientModel(clientModel)

	// ── v2.0 auto-route ────────────────────────────────────────────────
	// If the client requested model="auto", classify the task and pick
	// the best credential. Rewrites body model + sets X-Gw-Auto-Decision.
	if clientModel == autoRequestMagic {
		apiKeyID := 0
		if keyInfo != nil {
			apiKeyID = keyInfo.ID
		}
		newBody, wire, _ := h.maybeResolveAuto(&reqBody, bodyBytes, r, apiKeyID)
		if newBody != nil {
			bodyBytes = newBody
		}
		if wire != nil {
			writeAutoDecisionHeader(w, wire)
			logCtx.SetAutoDecision(wire)
		} else {
			logCtx.IsAutoRequest = true
		}
	clientModel = reqBody.Model
	logCtx.SetClientModel(clientModel)
	}

	// NOTE: v3 session-level compression runs AFTER candidate resolution
	// (below, once we know the target model's context window). See the
	// "v3 Session-level intelligent compression" block after GetCandidates.

	isStream := reqBody.Stream
	endUser := resolveEndUser(reqBody.User, r)
	clientID := identity.BuildIdentityFromRequest(r, tenant(keyInfo), appID(keyInfo), apiKeyIDPtr(keyInfo), clientProfileFromKey(keyInfo))
	identityHash := clientID.ShortID()
	// startTime is the outer-watcher time; executor tracks per-candidate
	// latency internally.  We re-use the safety-net's startTime (the
	// one from the function parameter) for the audit latency.
	_ = startTime

	auditBuilder := newAuditEvent(requestID).
		ClientModel(clientModel).
		IdentityHash(identityHash).
		ClientProfile(clientID.Fingerprint.ClientProfile).
		Stream(isStream).
		RequestChecksum(bodyBytes)

	var streamCapture *audit.StreamCapture
	if isStream {
		streamCapture = audit.NewStreamCapture()
	}
	defer func() {
		if streamCapture != nil {
			auditBuilder.StreamMetrics(streamCapture)
		}
		h.auditor.Emit(context.Background(), auditBuilder.Build())
	}()

	candidates, policy, err := h.provider.GetCandidates(r.Context(), clientModel, clientID.Fingerprint.ClientProfile)
	if err != nil {
		slog.Error("failed to get candidates from provider", "error", err)
		h.emitFailedDecisionLog(requestID, clientModel, keyInfo, clientID, 0, nil, nil, "no_candidate", nil, int(time.Since(startTime).Milliseconds()))
		logCtx.failAndMark("no_candidate",
			fmt.Sprintf("no available provider for model '%s'", clientModel), nil, nil)
		markLogged()
		writeErrorJSON(w, http.StatusServiceUnavailable, requestID, fmt.Sprintf("no available provider for model '%s'", clientModel), "server_error", "no_candidate")
		return
	}
	if len(candidates) == 0 {
		h.emitFailedDecisionLog(requestID, clientModel, keyInfo, clientID, 0, nil, nil, "no_candidate", nil, int(time.Since(startTime).Milliseconds()))
		logCtx.failAndMark("no_candidate",
			fmt.Sprintf("no available provider for model '%s'", clientModel), nil, nil)
		markLogged()
		writeErrorJSON(w, http.StatusServiceUnavailable, requestID, fmt.Sprintf("no available provider for model '%s'", clientModel), "server_error", "no_candidate")
		return
	}
	if len(candidates) > 0 {
		// Stash the first candidate so the safety net can attribute
		// the failure to a specific provider / credential when the
		// executor itself fails.
		pid := candidates[0].ProviderID
		cid := candidates[0].CredentialID
		logCtx.SetRoute(&pid, &cid)
	}

	var modelResolution *resolve.Resolution
	if h.resolver != nil {
		modelResolution = h.resolver.Resolve(r.Context(), clientModel, clientID.Fingerprint.ClientProfile)
	}

	var txResult *transform.TransformResult
	tCtx := &transform.TransformContext{
		RequestMode:   "chat",
		ClientProfile: clientID.Fingerprint.ClientProfile,
		ClientModel:   clientModel,
	}
	if modelResolution != nil && modelResolution.CanonicalName != nil {
		tCtx.CanonicalName = *modelResolution.CanonicalName
	}
	if h.matrix != nil {
		txResult = h.matrix.Resolve(tCtx)
	}
	explicitOutbound := ""
	if len(candidates) > 0 {
		explicitOutbound = renderOutboundFromTransform(txResult, candidates[0], tCtx.CanonicalName)
	}

	auditBuilder.OutboundModel(explicitOutbound).Provider(candidates[0].ProviderID).Credential(candidates[0].CredentialID)
	if modelResolution != nil {
		auditBuilder.ResolutionPath(modelResolution.ResolutionPath)
		if modelResolution.CanonicalName != nil {
			auditBuilder.CanonicalName(*modelResolution.CanonicalName)
		}
	}
	if txResult != nil {
		auditBuilder.TransformRule(txResult.MatchedRule)
	}

	egressProtocol := ""
	if len(candidates) > 0 {
		egressProtocol = candidates[0].Protocol
	}
	var canonicalID *int
	if modelResolution != nil {
		canonicalID = modelResolution.CanonicalID
	}
	gwSessionID, gwTaskID := gwSessionTaskFromRequest(r, sessionInfo)
	outboundForLog := explicitOutbound
	if len(candidates) > 0 {
		outboundForLog = outboundModelForLog(clientModel, explicitOutbound, candidates[0].RawModel)
	}

	// ── Phase 2 Meta-tool interception ─────────────────────────────────
	// Checks if the request contains meta-tool calls (list_categories,
	// load_tools) and handles them locally without forwarding to upstream.
	// This runs BEFORE session compression to avoid unnecessary processing.
	if h.metaToolInterceptor != nil {
		modified, intercepted, err := h.metaToolInterceptor.InterceptRequest(r.Context(), bodyBytes)
		if err != nil {
			captureAndEmitFailure("meta_tool_error", fmt.Sprintf("meta-tool interception failed: %v", err), nil, nil)
			writeErrorJSON(w, http.StatusInternalServerError, requestID, "Meta-tool processing failed", "internal_error", "meta_tool_error")
			return
		}
		if intercepted {
			// Meta-tool was handled locally, return the result directly
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write(modified)
			markLogged()
			return
		}
		// Not a meta-tool call, continue with normal processing
		bodyBytes = modified
	}

	// ── v3 Session-level intelligent compression ────────────────────────
	// Runs AFTER candidate resolution so we know the target model's context
	// window (B1 fix: previously passed 0, which disabled the TOKEN trigger
	// and the mechanical-trim fallback). The session compressor delta-appends
	// new turns to the compressed session history and, when the sliding
	// window fires, produces a lossless LLM summary (or trims as fallback).
	if h.sessionCompressor != nil && gwSessionID != "" {
		tenantForSC := "default"
		if keyInfo != nil {
			tenantForSC = keyInfo.TenantID
		}
		protocolForSC := "openai"
		if isAnthropicMessagesPath(r.URL.Path) {
			protocolForSC = "anthropic-messages"
		}
		// Resolve the target model context window from the first candidate.
		// 0 when unknown (TOKEN trigger then relies on msg_count / idle only).
		ctxWindow := 0
		if len(candidates) > 0 && candidates[0].ContextWindow != nil {
			ctxWindow = *candidates[0].ContextWindow
		}
		scResult := h.sessionCompressor.Prepare(
			r.Context(),
			bodyBytes,
			tenantForSC,
			gwSessionID,
			protocolForSC,
			ctxWindow,
			false, // not streaming yet at this point
		)
		if scResult != nil && len(scResult.OutboundBody) > 0 {
			bodyBytes = scResult.OutboundBody
			
			// ── Tools restoration (Phase 1 optimization) ──────────────────
			// If compressor cached tools (marked with "_tools_cached": true),
			// restore them from the original request body before forwarding
			// to upstream LLM provider.
			var outbound map[string]json.RawMessage
			if err := json.Unmarshal(bodyBytes, &outbound); err == nil {
				if cached := outbound["_tools_cached"]; string(cached) == "true" {
					// Tools were cached → restore from original reqBody
					if len(reqBody.Tools) > 0 {
						outbound["tools"] = reqBody.Tools
						delete(outbound, "_tools_cached")
						if restored, err := json.Marshal(outbound); err == nil {
							bodyBytes = restored
						}
					}
				}
			}
		}
		if scResult != nil && scResult.Degraded {
			w.Header().Set("X-Gw-Compression-Degraded", "sliding_window_collision")
		}
		if scResult != nil && scResult.CompressionStrategy != "" {
			mc := scResult.MsgCount
			te := scResult.TokenEst
			logCtx.OutboundBody = scResult.OutboundBody
			logCtx.OutboundMsgCount = &mc
			logCtx.OutboundTokenEst = &te
			logCtx.OutboundMsgHashes = []byte(scResult.MsgHashes)
			logCtx.OutboundStrategy = scResult.CompressionStrategy
			logCtx.OutboundSummaryMarker = scResult.SummaryMarker
			logCtx.OutboundWindowTriggered = scResult.WindowTriggered
		}
	}

	h.recordInitialRequestLog(
		requestID, clientModel, outboundForLog, endUser, "chat", keyInfo,
		clientID.Fingerprint.ClientProfile, identityHash,
		logCtx.ProviderID, logCtx.CredentialID, canonicalID,
		bodyBytes, txResult, egressProtocol, isStream,
		gwSessionID, gwTaskID,
		logCtx,
	)

	var sessionKey string
	if sessionInfo != nil {
		sessionKey = sessionInfo.SessionKey
	}

	// ── Idempotent dedup (Track C C5, 2026-06-18) ────────────────────────
	// When a client retries the same (sessionID, requestID) within
	// the 5-minute window — network glitch, double-click, mobile
	// background-then-foreground — we short-circuit to a 202 +
	// X-Gw-Pending response. The pending store (C3) already
	// deduplicates at the durable layer; this is the in-memory
	// fast path that avoids re-running routing + circuit +
	// limiter checks.
	//
	// The cache is "first-writer wins" — a hit is recorded as
	// a real attempt by the cache, so concurrent retries see
	// a hit. This is the desired behaviour: only the first
	// request does the work, all subsequent retries are
	// informed of the same pending response.
	if h.idempotentCache != nil && sessionID != "" && requestID != "" {
		if h.idempotentCache.CheckAndMark(sessionID, requestID) {
			w.Header().Set("X-Gw-Pending", sessionID)
			w.Header().Set("X-Gw-Pending-Request", requestID)
			w.Header().Set("X-Gw-Idempotent-Replay", "true")
			w.Header().Set("Retry-After", "2")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":      "in_progress",
				"session_id":  sessionID,
				"request_id":  requestID,
				"retry_after": 2,
				"idempotent":  true,
		})
		logCtx.SetError("idempotent_replay", "duplicate request, returning in_progress")
		// Body and model already captured (from earlier reqBody parse)
		logCtx.EmitFailure("idempotent_replay", "duplicate request, returning in_progress", nil, nil)
		markLogged()
		return
	}
}

stickyKey := buildRouteStickyKey(tenant(keyInfo), appID(keyInfo), apiKeyIDPtr(keyInfo), clientID.Fingerprint.ClientProfile, sessionID, endUser, clientID.Fingerprint.PrimarySeed(), clientModel)

upstreamBody, convErr := selectChatUpstreamBodyBytes(candidates, bodyBytes)
if convErr != nil {
	// Body already captured, just emit + mark
	logCtx.SetError("chat_to_anthropic_conversion_error", convErr.Error())
	logCtx.EmitFailure("chat_to_anthropic_conversion_error", convErr.Error(), nil, nil)
	logCtx.MarkLogged()
	writeErrorJSON(w, http.StatusBadRequest, requestID, convErr.Error(), "invalid_request", "chat_to_anthropic_conversion_error")
	return
}

	result, execErr := h.executor.Execute(&routing.ExecParams{
		W:              w,
		R:              r,
		BodyBytes:      upstreamBody,
		IsStream:       isStream,
		ClientProtocol: "openai-completions",
		ClientModel:    clientModel,
		OutboundModel:  outboundForLog,
		ClientID:       clientID,
		Transform:      txResult,
		Resolution:     modelResolution,
		Candidates:     candidates,
		Policy:         policy,
		AuditBuilder:   auditBuilder,
		Capture:        streamCapture,
		ToolsRequested: requestHasTools(bodyBytes),
		SessionKey:     sessionKey,
		StickyKey:      stickyKey,
		KeyID: func() int {
			if keyInfo != nil {
				return keyInfo.ID
			}
			return 0
		}(),
		KeyConcurrentLimit: func() int {
			if keyInfo != nil {
				return keyInfo.EffectiveConcurrent()
			}
			return 0
		}(),
		// Round 47 compression v7 T13: tenant-namespaced Memora user_id.
		TenantID: func() string {
			if keyInfo != nil {
				return keyInfo.TenantID
			}
			return ""
		}(),
	})

	if execErr != nil {
		slog.Error("executor failed",
			"error", execErr,
			"model", clientModel,
		)
		var providerID, credentialID *int
		var tried int
		var failTrace *routing.Trace
		if execErrTyped, ok := execErr.(*routing.ExecuteError); ok {
			tried = execErrTyped.Tried
			failTrace = execErrTyped.Trace
		}
		if len(candidates) > 0 {
			providerID = intPtr(candidates[0].ProviderID)
			credentialID = intPtr(candidates[0].CredentialID)
		}

		// Track C C4 (2026-06-18): the executor demoted a slow
		// request to async mode. Surface 202 + X-Gw-Pending so the
		// client knows to poll GET /v1/sessions/{id}/pending-response
		// (see sessions/handler.go C3). The body is a small JSON
		// status object; the real response lands in pending store
		// when the async goroutine completes.
		var asyncErr *routing.AsyncPendingError
		if errors.As(execErr, &asyncErr) {
			w.Header().Set("X-Gw-Pending", asyncErr.SessionID)
			w.Header().Set("X-Gw-Pending-Request", asyncErr.RequestID)
			w.Header().Set("Retry-After", "5")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":      "in_progress",
				"session_id":  asyncErr.SessionID,
				"request_id":  asyncErr.RequestID,
				"retry_after": 5,
				"started_at":  asyncErr.StartedAt.Format(time.RFC3339),
				"poll_url":    "/v1/sessions/" + asyncErr.SessionID + "/pending-response?request_id=" + asyncErr.RequestID,
			})
			slog.Info("async_pending_dispatched",
				"session_id", asyncErr.SessionID,
				"request_id", asyncErr.RequestID,
				"model", clientModel,
			)
			return
		}

		errCode := "provider_error"
		if execErrTyped, ok := execErr.(*routing.ExecuteError); ok && execErrTyped.Exhausted {
			// Step 6 (2026-06-18): preserve backward-compat error.code
			// = "model_not_found" but surface the REAL underlying
			// kind in error.kind + X-Gateway-Last-Kind header. Many
			// in-the-wild failures labeled model_not_found are
			// actually rate_limit / concurrent / unreachable, which
			// breaks downstream alerting that keys on the surface
			// code. The kind field is the SSoT for the real cause;
			// the legacy code is preserved for clients that pattern-
			// match on it.
			errCode = "model_not_found"
			realKind := mapExecuteErrorToKind(execErrTyped)
			logCtx.SetOutboundModel(explicitOutbound)
			// 2026-06-20: write the REAL underlying kind to
			// request_logs.error_kind (e.g. "rate_limit",
			// "concurrent", "upstream_down") instead of the
			// backward-compat "model_not_found". The HTTP
			// error.code stays "model_not_found" for old clients
			// (set below in writeErrorJSONWithKind); the new
			// error_kind column + error.kind JSON field carry the
			// precise cause. Operators can now filter on
			// error_kind='rate_limit' directly without parsing the
			// X-Gateway-Last-Kind header.
			logCtx.failAndMark(errorKindOrFallback(realKind),
				fmt.Sprintf("No available provider for model '%s'. All %d candidates failed.", clientModel, execErrTyped.Tried),
				providerID, credentialID)
			h.emitFailedDecisionLog(requestID, clientModel, keyInfo, clientID, tried, modelResolution, txResult, errCode, failTrace, int(time.Since(startTime).Milliseconds()))
			markLogged()
			// Step 6: surface real kind in response header so log
			// scrapers and debug dashboards can see it without
			// parsing JSON.
			if realKind != "" {
				w.Header().Set("X-Gateway-Last-Kind", realKind)
			}
			writeErrorJSONWithKind(w, http.StatusServiceUnavailable, requestID,
				fmt.Sprintf("No available provider for model '%s'. All %d candidates failed.", clientModel, execErrTyped.Tried),
				"server_error", "model_not_found", realKind, map[string]any{
					"stage":     "execution",
					"kind":      string(execErrTyped.LastKind),
					"attempts":  execErrTyped.Attempts,
					"tried":     execErrTyped.Tried,
					"retryable": errorsx.IsRetryable(execErrTyped.LastKind),
				})
			return
		}
		logCtx.SetOutboundModel(explicitOutbound)
		logCtx.failAndMark("provider_error", execErr.Error(), providerID, credentialID)
		h.emitFailedDecisionLog(requestID, clientModel, keyInfo, clientID, tried, modelResolution, txResult, errCode, failTrace, int(time.Since(startTime).Milliseconds()))
		markLogged()
		debugInfo := map[string]any{
			"stage":     "execution",
			"tried":     tried,
			"retryable": false,
		}
		if execErrTyped, ok := execErr.(*routing.ExecuteError); ok {
			debugInfo["kind"] = string(execErrTyped.LastKind)
			debugInfo["attempts"] = execErrTyped.Attempts
			debugInfo["retryable"] = errorsx.IsRetryable(execErrTyped.LastKind)
		}
		writeErrorJSONWithDebug(w, http.StatusBadGateway, requestID, "upstream request failed", "server_error", "provider_error", debugInfo)
		return
	}

	auditBuilder.Success(true).Latency(time.Duration(result.LatencyMs) * time.Millisecond)
	h.emitTelemetry(auditBuilder.Build(), result, endUser, keyInfo, streamCapture, "chat", txResult, result.RequestBody, result.ResponseBody, logCtx)
	markLogged()
}

func (h *ChatHandler) emitTelemetry(evt audit.Event, result *routing.ExecuteResult, endUser string, keyInfo *auth.KeyInfo, capture *audit.StreamCapture, requestMode string, txResult *transform.TransformResult, requestBody []byte, responseBody []byte, logCtx *RequestLogContext) {
	if h.telemetryClient == nil || !h.telemetryClient.Enabled() {
		return
	}

	var apiKeyID *int
	var tenantID string = "default"
	var applicationID *int
	keyPrefix, keyOwner, appCode := "", "", ""
	if keyInfo != nil {
		apiKeyID = &keyInfo.ID
		tenantID = keyInfo.TenantID
		applicationID = appID(keyInfo)
		keyPrefix, keyOwner, appCode = keyMetaFromKeyInfo(keyInfo)
	}

	dl := &telemetry.DecisionLogEntry{
		RequestID:          evt.RequestID,
		TenantID:           tenantID,
		APIKeyID:           apiKeyID,
		Model:              canonicalOrClient(evt.CanonicalName, evt.ClientModel),
		ChosenCredentialID: intPtr(result.Candidate.CredentialID),
		ChosenProviderID:   intPtr(result.Candidate.ProviderID),
		Tier:               intPtr(result.Candidate.Tier),
		CandidatesTried:    1,
		LatencyMs:          result.LatencyMs,
		Success:            true,
		ClientModel:        strPtr(evt.ClientModel),
		OutboundModel:      strPtr(evt.OutboundModel),
		ClientProfile:      strPtr(evt.ClientProfile),
		RequestMode:        strPtr(requestMode),
		IdentityHash:       strPtr(evt.IdentityHash),
		TransformRuleID:    strPtr(evt.TransformRule),
	}
	if evt.ResolutionPath != "" {
		dl.ResolutionPath = strPtr(evt.ResolutionPath)
	}
	if evt.CanonicalName != "" {
		dl.CanonicalModel = strPtr(evt.CanonicalName)
	}
	if result.Candidate.Protocol != "" {
		dl.EgressProtocol = strPtr(result.Candidate.Protocol)
	}
	if txResult != nil && txResult.OutboundModel != "" {
		dl.OutboundModel = strPtr(txResult.OutboundModel)
	}
	if result.Trace != nil {
		traceJSON, _ := json.Marshal(result.Trace)
		dl.DecisionTrace = traceJSON
	} else if evt.DecisionTrace != nil {
		traceJSON, _ := json.Marshal(evt.DecisionTrace)
		dl.DecisionTrace = traceJSON
	}
	if result.Candidate.RawModel != "" {
		dl.ResolvedRawModel = strPtr(result.Candidate.RawModel)
		dl.ResolutionRawModels = []string{result.Candidate.RawModel}
	}

	var requestBodyText *string
	if len(requestBody) > 0 {
		v := string(requestBody)
		requestBodyText = &v
	}
	var responseBodyText *string
	if len(responseBody) > 0 {
		v := string(responseBody)
		// For streaming responses, the last SSE chunk sent to the client (and captured
		// in result.ResponseBody) often does NOT include the usage block. Merge the
		// stream-captured usage values into the response_body JSON so the persisted
		// row contains a complete `usage` block for downstream auditors/queries.
		if capture != nil {
			m := capture.SummaryAsMap()
			var pt, ct, crt, cwt int
			if val, ok := m["prompt_tokens"].(int); ok {
				pt = val
			}
			if val, ok := m["completion_tokens"].(int); ok {
				ct = val
			}
			if val, ok := m["cache_read_tokens"].(int); ok {
				crt = val
			}
			if val, ok := m["cache_write_tokens"].(int); ok {
				cwt = val
			}
			if pt > 0 || ct > 0 {
				v = string(injectUsageIntoResponseBody([]byte(v), pt, ct, crt, cwt))
			}
		} else if len(responseBody) > 0 {
			// Non-streaming: ensure we always pull whatever usage is in the body
			// (this is the primary path; capture==nil branch below is a fallback).
			ept, ect, ecrt, ecwt := extractTokensFromResponseBody(responseBody)
			if ept > 0 || ect > 0 {
				v = string(injectUsageIntoResponseBody([]byte(v), ept, ect, ecrt, ecwt))
			}
		}
		responseBodyText = &v
	} else if capture != nil {
		m := capture.SummaryAsMap()
		var textContent string
		if v, ok := m["stream_text_content"].(string); ok && v != "" {
			textContent = v
		}
		if textContent != "" {
			var pt, ct int
			if v, ok := m["prompt_tokens"].(int); ok {
				pt = v
			}
			if v, ok := m["completion_tokens"].(int); ok {
				ct = v
			}
			pseudoBody := map[string]any{
				"choices": []map[string]any{
					{"message": map[string]any{"role": "assistant", "content": textContent}, "finish_reason": "stop"},
				},
			}
			if pt > 0 || ct > 0 {
				pseudoBody["usage"] = map[string]any{"prompt_tokens": pt, "completion_tokens": ct, "total_tokens": pt + ct}
			}
			if b, err := json.Marshal(pseudoBody); err == nil {
				v := string(b)
				responseBodyText = &v
			}
		} else if previewStr, ok := m["response_preview"].(string); ok && previewStr != "" {
			// Fallback: textContent is empty (e.g. function-calling responses
			// that only carry `delta.tool_calls` and no `delta.content`, or
			// request_logs that are stored for audit even when no parsed text
			// was collected). Persist the raw SSE preview as the body so the
			// row is non-empty and downstream auditors/queries can still
			// inspect the wire format.
			responseBodyText = strPtr(previewStr)
		}
	}
	requestPreviewText := requestPreview(requestBody)
	transformSummaryText := transformSummary(txResult, evt.OutboundModel)
	responsePreviewText := responsePreview(responseBody)
	var requestPreviewPtr *string
	if requestPreviewText != "" {
		requestPreviewPtr = strPtr(requestPreviewText)
	}
	var transformSummaryPtr *string
	if transformSummaryText != "" {
		transformSummaryPtr = strPtr(transformSummaryText)
	}
	var responsePreviewPtr *string
	if responsePreviewText != "" {
		responsePreviewPtr = strPtr(responsePreviewText)
	}

	loggedOutbound := outboundModelForLog(evt.ClientModel, evt.OutboundModel, result.Candidate.RawModel)

	reqLog := &telemetry.RequestLogEntry{
		RequestID:        evt.RequestID,
		TenantID:         tenantID,
		ApplicationID:    applicationID,
		APIKeyID:         apiKeyID,
		APIKeyPrefix:     strPtr(keyPrefix),
		APIKeyOwnerUser:  strPtr(keyOwner),
		ApplicationCode:  strPtr(appCode),
		EndUserID:        strPtr(endUser),
		ClientModel:      strPtr(evt.ClientModel),
		OutboundModel:    strPtr(loggedOutbound),
		CredentialID:     intPtr(result.Candidate.CredentialID),
		ProviderID:       intPtr(result.Candidate.ProviderID),
		ClientProfile:    strPtr(evt.ClientProfile),
		RequestMode:      strPtr(requestMode),
		LatencyMs:        intPtr(result.LatencyMs),
		Success:          true,
		RequestStatus:    strPtr(telemetry.RequestStatusSuccess),
		IdentityHash:     strPtr(evt.IdentityHash),
		RequestPreview:   requestPreviewPtr,
		TransformSummary: transformSummaryPtr,
		ResponsePreview:  responsePreviewPtr,
		RequestBody:      requestBodyText,
		ResponseBody:     responseBodyText,
		// Round 47 compression v7 T-NEW-3: write the compression event
		// captured by the executor's 4xx recovery (see
		// routing.context_summarize.handleContextLengthRecovery) into
		// request_logs.compression_*. Operators can then SQL-trace the
		// parent-child chain via parent_request_id.
		//
		// We only set these when the executor actually rewrote the body;
		// nil pointers → NULL in PG → the existing partial index on
		// parent_request_id stays cheap.
		CompressionReason:   result.CompressionReason,
		CompressionStrategy: result.CompressionStrategy,
		CompressionMeta:     result.CompressionMeta,
		ParentRequestID:     result.ParentRequestID,
	}
	// v3: if v7 compression_strategy is empty but a session compressor strategy
	// exists, prefer the session compressor value so the row is queryable.
	// (v7 and v3 strategies are mutually exclusive in a single request.)

	if capture != nil {
		m := capture.SummaryAsMap()
		// Only set pointers when the captured value is non-zero. Some providers
		// (e.g. minimax) include `"usage": null` in every SSE chunk, so the
		// stream summary may have the keys present with value 0. Setting a
		// non-nil *int to 0 would otherwise suppress the estimator fallback
		// below (because the nil-check would be false).
		if v, ok := m["prompt_tokens"].(int); ok && v > 0 {
			reqLog.PromptTokens = &v
		}
		if v, ok := m["completion_tokens"].(int); ok && v > 0 {
			reqLog.CompletionTokens = &v
		}
		if v, ok := m["cache_read_tokens"].(int); ok && v > 0 {
			reqLog.CacheReadTokens = &v
		}
		if v, ok := m["cache_write_tokens"].(int); ok && v > 0 {
			reqLog.CacheWriteTokens = &v
		}
		if v, ok := m["stream_first_chunk_ms"].(int); ok {
			reqLog.StreamFirstChunkMs = &v
		}
		if v, ok := m["stream_chunk_count"].(int); ok {
			reqLog.StreamChunkCount = &v
		}
		if v, ok := m["response_checksum"].(string); ok {
			reqLog.ResponseChecksum = &v
		}
		if v, ok := m["stream_done_received"].(bool); ok {
			reqLog.StreamDoneReceived = &v
		}
		if v, ok := m["stream_interrupted"].(bool); ok {
			reqLog.StreamInterrupted = &v
			if v {
				isErr, detailCode := classifyStreamInterruption(m)
				if isErr {
					reqLog.Success = false
					reqLog.RequestStatus = strPtr(telemetry.RequestStatusFailure)
					reqLog.ErrorKind = strPtr("stream_error")
					reqLog.FailureStage = strPtr("upstream")
				}
				if detailCode != "" {
					reqLog.FailureDetailCode = strPtr(detailCode)
				}
			}
		}
		// 2026-06-19 T-NEW-7: split the semantic overload of failure_detail_code.
		// audit/audit.go::SummaryAsMap now publishes the upstream finish_reason
		// under the new "upstream_finish_reason" key (for BOTH success and
		// failure rows). It only republishes the value as
		// "failure_detail_code" when the value is a known interruption code
		// (e.g. eof_without_done, stream_timeout). The block below mirrors
		// that discipline into the request_log row:
		//
		//   1. Read upstream_finish_reason → UpstreamFinishReason column.
		//   2. Fall back to m["failure_detail_code"] for UpstreamFinishReason
		//      ONLY if it wasn't set above (legacy pre-018 captures).
		//   3. Do NOT touch FailureDetailCode here — that is already set by
		//      the stream_interrupted branch above for real failures. For
		//      successful streams we leave it NULL.
		if v, ok := m["upstream_finish_reason"].(string); ok && v != "" {
			reqLog.UpstreamFinishReason = strPtr(v)
		}
		if reqLog.UpstreamFinishReason == nil {
			if v, ok := m["failure_detail_code"].(string); ok && v != "" {
				// Legacy pre-018 capture path — promotion of the old
				// "failure_detail_code == finish_reason" usage to the new
				// column. Keep the value in BOTH columns for now so the
				// admin UI does not regress before the next deploy
				// rewires the relay-side capture.
				reqLog.UpstreamFinishReason = strPtr(v)
			}
		}
		if v, ok := m["response_preview"].(string); ok && v != "" && reqLog.ResponsePreview == nil {
			reqLog.ResponsePreview = strPtr(v)
		}
		// 2026-06-19 quality fix mode: pull stream-collected quality
		// signals out of the capture summary. The stream reader
		// already pushed the running flag list into the capture;
		// the audit summary serialises it under "quality_flags".
		if v, ok := m["quality_flags"].([]string); ok && len(v) > 0 {
			reqLog.QualityFlags = v
		}
		if v, ok := m["quality_fix_actions"].(string); ok && v != "" {
			reqLog.QualityFixActions = []byte(v)
		}
		if v, ok := m["quality_score"].(float64); ok {
			reqLog.QualityScore = &v
		}
		// 2026-06-19 quality fix mode (017_quality_fix_mode.sql): propagate
		// the post-processed quality signals into the request_log row.
		// The non-stream path stores the result directly on
		// ExecuteResult; the stream path already pushed them into the
		// capture above (m["quality_flags"] etc.). For non-stream we
		// simply read the fields that the executor set.
		if len(result.QualityFlags) > 0 {
			reqLog.QualityFlags = result.QualityFlags
		}
		if len(result.QualityFixActions) > 0 {
			reqLog.QualityFixActions = result.QualityFixActions
		}
		if result.QualityScore != nil {
			reqLog.QualityScore = result.QualityScore
		}
		pt, ct, crt, cwt := extractTokensFromResponseBody(result.ResponseBody)
		if pt > 0 || ct > 0 {
			reqLog.PromptTokens = &pt
			reqLog.CompletionTokens = &ct
			if crt > 0 {
				reqLog.CacheReadTokens = &crt
			}
			if cwt > 0 {
				reqLog.CacheWriteTokens = &cwt
			}
		}
	}

	// Fallback: if upstream did not return a usage block (e.g. minimax, certain
	// volcengine pass-through responses), estimate tokens locally from the
	// request/response text and mark the row so the UI can distinguish the
	// estimated value from a real LLM-reported count.
	// Check both nil AND zero: providers like minimax emit `"usage": null` in
	// every SSE chunk, which results in stream-captured pointers to 0 that
	// would otherwise suppress this fallback.
	promptZero := reqLog.PromptTokens == nil || *reqLog.PromptTokens == 0
	completionZero := reqLog.CompletionTokens == nil || *reqLog.CompletionTokens == 0
	if promptZero && completionZero {
		estPrompt := estimatePromptTokens(result.RequestBody)
		estCompletion := estimateCompletionTokens(result.ResponseBody)
		if estPrompt > 0 || estCompletion > 0 {
			reqLog.PromptTokens = &estPrompt
			reqLog.CompletionTokens = &estCompletion
			reqLog.UsageSource = strPtr(UsageSourceEstimated)
		}
	} else if reqLog.UsageSource == nil {
		reqLog.UsageSource = strPtr(UsageSourceLLM)
	}

	if reqLog.PromptTokens != nil || reqLog.CompletionTokens != nil {
		cost := CalcCost(CostInput{
			PromptTokens:     floatPtrFromInt(reqLog.PromptTokens),
			CompletionTokens: floatPtrFromInt(reqLog.CompletionTokens),
			CacheReadTokens:  floatPtrFromInt(reqLog.CacheReadTokens),
			CacheWriteTokens: floatPtrFromInt(reqLog.CacheWriteTokens),
			PriceIn:          result.Candidate.PriceInPer1M,
			PriceOut:         result.Candidate.PriceOutPer1M,
			CacheReadPrice:   result.Candidate.CacheReadPricePer1M,
			CacheWritePrice:  result.Candidate.CacheWritePricePer1M,
		})
		reqLog.CostUSD = cost
		// For CNY-priced providers (cost_usd is intentionally nil) record the
		// native-currency value in cost_display so /request-logs can show it.
		if cost == nil && result.Candidate.Currency != "" && result.Candidate.Currency != "USD" {
			cnyCost := CalcCost(CostInput{
				PromptTokens:     floatPtrFromInt(reqLog.PromptTokens),
				CompletionTokens: floatPtrFromInt(reqLog.CompletionTokens),
				CacheReadTokens:  floatPtrFromInt(reqLog.CacheReadTokens),
				CacheWriteTokens: floatPtrFromInt(reqLog.CacheWriteTokens),
				PriceIn:          result.Candidate.PriceInPer1M,
				PriceOut:         result.Candidate.PriceOutPer1M,
				CacheReadPrice:   result.Candidate.CacheReadPricePer1M,
				CacheWritePrice:  result.Candidate.CacheWritePricePer1M,
			})
			reqLog.CostDisplay = cnyCost
			curr := result.Candidate.Currency
			reqLog.CostCurrency = &curr
		}
	}

	if h.maasSvc != nil && keyInfo != nil && keyInfo.TenantID != "" && keyInfo.TenantID != "default" {
		pt, ct, crt, cwt := 0, 0, 0, 0
		if reqLog.PromptTokens != nil {
			pt = *reqLog.PromptTokens
		}
		if reqLog.CompletionTokens != nil {
			ct = *reqLog.CompletionTokens
		}
		if reqLog.CacheReadTokens != nil {
			crt = *reqLog.CacheReadTokens
		}
		if reqLog.CacheWriteTokens != nil {
			cwt = *reqLog.CacheWriteTokens
		}
		if pt > 0 || ct > 0 || crt > 0 || cwt > 0 {
			canonical := evt.CanonicalName
			if canonical == "" {
				canonical = evt.ClientModel
			}
			chargeCtx, chargeCancel := context.WithTimeout(context.Background(), 5*time.Second)
			charged, err := h.maasSvc.ChargeRequest(chargeCtx, keyInfo.TenantID, evt.RequestID, canonical, pt, ct, crt, cwt)
			chargeCancel()
			if err == nil && charged > 0 {
				reqLog.CreditsCharged = &charged
			} else if err != nil {
				slog.Warn("maas charge failed", "request_id", evt.RequestID, "tenant_id", keyInfo.TenantID, "error", err)
			}
		}
	}

	dl.PromptTokens = reqLog.PromptTokens
	dl.CompletionTokens = reqLog.CompletionTokens
	dl.CostUSD = reqLog.CostUSD
	if len(requestBody) > 0 {
		rb := len(requestBody)
		dl.RequestBytes = &rb
	}
	if len(responseBody) > 0 {
		rsb := len(responseBody)
		dl.ResponseBytes = &rsb
	}
	if result.Trace != nil && len(result.Trace.PlannedCandidates) > 0 {
		dl.CandidatesTried = len(result.Trace.PlannedCandidates)
	}
	h.telemetryClient.EmitDecisionLog(dl)

	applyKeyInfoToRequestLog(reqLog, keyInfo)
	// v3: merge session compressor outbound fields into the log entry.
	applySessionCompressorFields(reqLog, logCtx)
	h.telemetryClient.EmitRequestLogUpdate(reqLog)
	if h.requestLogHook != nil {
		h.requestLogHook(reqLog)
	}

	// v2.1: emit implicit feedback signal for the auto-route tuning loop.
	// Best-effort async write via the dedicated tuning writer; never blocks
	// the request path on DB latency.
	if reqLog.IsAutoRequest != nil && *reqLog.IsAutoRequest && reqLog.TaskType != nil {
		latencyMs := 0
		if reqLog.LatencyMs != nil {
			latencyMs = *reqLog.LatencyMs
		}
		h.emitTuningSignal(reqLog, reqLog.Success, latencyMs)
	}
}

// recordFailedRequest writes a request_logs row for any non-success
// request exit (auth, rate-limit, budget, validation, candidate,
// executor, panic, …).  It is the safety net that guarantees
// every request that reaches any of the three handlers
// (chat completions, anthropic messages, openai responses) shows
// up in the admin request-logs UI, even when the request never
// makes it as far as the routing executor.
//
// Callers may set keyInfo to attach api_key_id / tenant_id; the
// rest of the row is filled in from the supplied error metadata.
// The caller is expected to call EmitRequestLog exactly once;
// recordFailedRequest never duplicates the entry.
func (h *ChatHandler) recordFailedRequest(requestID, clientModel, outboundModel string, providerID, credentialID *int, errCode, errMessage string, latencyMs int, requestBody []byte) {
	h.recordFailedRequestWithKey(requestID, clientModel, outboundModel, providerID, credentialID, errCode, errMessage, latencyMs, requestBody, nil, nil)
}

// recordFailedRequestWithKey records a failure via the unified RequestLogContext pipeline.
func (h *ChatHandler) recordFailedRequestWithKey(requestID, clientModel, outboundModel string, providerID, credentialID *int, errCode, errMessage string, latencyMs int, requestBody []byte, keyInfo *auth.KeyInfo, r *http.Request) {
	ctx := &RequestLogContext{
		handler:       h,
		RequestID:     requestID,
		StartTime:     time.Now().Add(-time.Duration(latencyMs) * time.Millisecond),
		Request:       r,
		KeyInfo:       keyInfo,
		Body:          requestBody,
		ClientModel:   clientModel,
		OutboundModel: outboundModel,
	}
	if r != nil {
		if session := sessions.SessionFromContext(r.Context()); session != nil {
			ctx.Session = session
		}
		ctx.refreshMeta()
	}
	ctx.EmitFailure(errCode, errMessage, providerID, credentialID)
}

// clientProfileFromKey returns the API key / application default client profile.
func clientProfileFromKey(keyInfo *auth.KeyInfo) string {
	if keyInfo != nil && keyInfo.DefaultClientProfile != nil {
		return strings.TrimSpace(*keyInfo.DefaultClientProfile)
	}
	return ""
}

// failedRequestIdentity builds client_profile + identity_hash from request
// headers and key anchors without requiring a parsed request body.
func failedRequestIdentity(r *http.Request, keyInfo *auth.KeyInfo) (clientProfile, identityHash string) {
	if r == nil {
		return "", ""
	}
	cp := clientProfileFromKey(keyInfo)
	clientID := identity.BuildIdentityFromRequest(r, tenant(keyInfo), appID(keyInfo), apiKeyIDPtr(keyInfo), cp)
	return clientID.Fingerprint.ClientProfile, clientID.ShortID()
}

func requestModeFromPath(path string) string {
	switch {
	case strings.Contains(path, "/messages"):
		return "anthropic"
	case strings.Contains(path, "/responses"):
		return "responses"
	default:
		return "chat"
	}
}

// isAnthropicMessagesPath returns true when the request targets the
// Anthropic Messages API (/v1/messages), so the session compressor
// knows which wire format to use when rebuilding the body.
func isAnthropicMessagesPath(path string) bool {
	return strings.Contains(path, "/messages")
}

// capturePartialBodyOnReadError keeps bytes already received when io.ReadAll
// fails mid-stream (timeout, client disconnect). model is usually near the
// start of JSON bodies, so partial data is enough for request_logs preview.
func capturePartialBodyOnReadError(body []byte, attemptRequestBody *[]byte, attemptClientModel *string) {
	if attemptRequestBody == nil || len(body) == 0 {
		return
	}
	*attemptRequestBody = body
	if attemptClientModel != nil && *attemptClientModel == "" {
		*attemptClientModel = extractModelFromBody(body)
	}
}

func (h *ChatHandler) recordFailedRequestDetailed(
	requestID, clientModel, outboundModel string,
	providerID, credentialID *int,
	errCode, errMessage string,
	latencyMs int,
	requestBody []byte,
	keyInfo *auth.KeyInfo,
	clientProfile, identityHash, requestMode string,
	gwSessionID, gwTaskID string,
	meta *requestAttemptMeta,
) {
	// Deprecated: kept for binary compatibility in tests; delegates to pipeline.
	ctx := &RequestLogContext{
		handler:       h,
		RequestID:     requestID,
		StartTime:     time.Now().Add(-time.Duration(latencyMs) * time.Millisecond),
		KeyInfo:       keyInfo,
		Body:          requestBody,
		ClientModel:   clientModel,
		OutboundModel: outboundModel,
	}
	if meta != nil {
		ctx.meta = *meta
	}
	ctx.EmitFailure(errCode, errMessage, providerID, credentialID)
}

// mapGatewayErrorToDetail returns a machine-readable sub-classification for
// the given early-exit error code.  Gateway-side errors are prefixed with
// "gw_" so that request_log consumers can immediately distinguish these from
// upstream provider errors (which keep their classified kind, e.g. "rate_limit",
// "concurrent", "timeout").
//
// The mapping:
//
//	gateway RPM limit   → "gw_rpm_exceeded"
//	gateway concurrent  → "gw_concurrent_exceeded"
//	gateway TPM         → "gw_tpm_exceeded"
//	key throttled       → "gw_key_throttled"
//	budget exhausted    → "gw_budget_exhausted"
//	upstream 429        → "rate_limit"        (unchanged)
//	upstream 429/503    → "concurrent"        (unchanged)
//	other early-exits   → errCode passthrough
func mapGatewayErrorToDetail(errCode string) string {
	switch errCode {
	case "rate_limit_exceeded":
		return "gw_rpm_exceeded"
	case "concurrent_limit_exceeded":
		return "gw_concurrent_exceeded"
	case "tpm_limit_exceeded":
		return "gw_tpm_exceeded"
	case "key_throttled":
		return "gw_key_throttled"
	case "budget_exhausted":
		return "gw_budget_exhausted"
	case "missing_key", "invalid_key":
		return "gw_" + errCode
	case "auth_unavailable":
		return "gw_auth_unavailable"
	case "method_not_allowed":
		return "gw_method_not_allowed"
	case "executor_unavailable":
		return "gw_executor_unavailable"
	case "no_candidate":
		return "gw_no_candidate"
	case "body_too_large", "body_read_error", "json_parse_error":
		return "gw_" + errCode
	case "missing_model", "missing_max_tokens":
		return "gw_" + errCode
	case "conversion_error":
		return "gw_conversion_error"
	case "session_forbidden":
		return "gw_session_forbidden"
	case "internal_panic":
		return "gw_internal_panic"
	case "chat_to_anthropic_conversion_error":
		return "gw_chat_to_anthropic_conversion_error"
	default:
		return errCode
	}
}

// classifyFailureStage returns where in the request lifecycle the
// failure happened so the request_log UI can group and filter.  Two
// possible values:
//
//	"gateway"  — the request never reached an upstream provider
//	             (auth/rate-limit/budget/validation/panics/...)
//	"upstream" — the request was dispatched to a provider and failed
//	             during or after the provider call
//	             (provider_error, model_not_found, stream_error, ...)
//
// Any error code that is NOT in the gateway early-exit list is
// assumed to be upstream.  This mirrors the rule used in
// mapGatewayErrorToDetail: the codes that get a "gw_" prefix are
// gateway; everything else is upstream.
func classifyFailureStage(errCode string) string {
	switch errCode {
	case "rate_limit_exceeded",
		"concurrent_limit_exceeded",
		"tpm_limit_exceeded",
		"key_throttled",
		"budget_exhausted",
		"insufficient_credits",
		"missing_key",
		"invalid_key",
		"auth_unavailable",
		"method_not_allowed",
		"executor_unavailable",
		"no_candidate",
		"body_too_large",
		"body_read_error",
		"json_parse_error",
		"missing_model",
		"missing_max_tokens",
		"conversion_error",
		"session_forbidden",
		"internal_panic",
		"chat_to_anthropic_conversion_error":
		return "gateway"
	default:
		return "upstream"
	}
}

// recordInitialRequestLog writes the base request metadata as soon as routing
// is resolved and before the upstream call starts.  Streaming requests then
// appear immediately in /request-logs; completion paths update tokens, bodies,
// and final success/error state via EmitRequestLogUpdate.
func (h *ChatHandler) recordInitialRequestLog(
	requestID, clientModel, outboundModel, endUser, requestMode string,
	keyInfo *auth.KeyInfo,
	clientProfile, identityHash string,
	providerID, credentialID, canonicalID *int,
	requestBody []byte,
	txResult *transform.TransformResult,
	egressProtocol string,
	isStream bool,
	gwSessionID, gwTaskID string,
	autoCtx *RequestLogContext,
) {
	if h.telemetryClient == nil || !h.telemetryClient.Enabled() {
		return
	}
	if clientModel == "" && len(requestBody) > 0 {
		clientModel = extractModelFromBody(requestBody)
	}
	if outboundModel == "" && clientModel != "" {
		outboundModel = clientModel
	}
	var requestBodyText *string
	if len(requestBody) > 0 {
		v := string(requestBody)
		requestBodyText = &v
	}
	tenantID := "default"
	var apiKeyID *int
	var applicationID *int
	keyPrefix, keyOwner, appCode := "", "", ""
	if keyInfo != nil {
		tenantID = keyInfo.TenantID
		kid := keyInfo.ID
		apiKeyID = &kid
		applicationID = appID(keyInfo)
		keyPrefix, keyOwner, appCode = keyMetaFromKeyInfo(keyInfo)
	}
	var requestPreviewPtr *string
	if preview := requestPreview(requestBody); preview != "" {
		requestPreviewPtr = strPtr(preview)
	}
	var transformSummaryPtr *string
	if summary := transformSummary(txResult, outboundModel); summary != "" {
		transformSummaryPtr = strPtr(summary)
	}
	var transformRuleID *string
	if txResult != nil && txResult.MatchedRule != "" {
		transformRuleID = strPtr(txResult.MatchedRule)
	}
	streamInterrupted := false
	reqLog := &telemetry.RequestLogEntry{
		RequestID:         requestID,
		TenantID:          tenantID,
		ApplicationID:     applicationID,
		APIKeyID:          apiKeyID,
		APIKeyPrefix:      strPtr(keyPrefix),
		APIKeyOwnerUser:   strPtr(keyOwner),
		ApplicationCode:   strPtr(appCode),
		EndUserID:         strPtr(endUser),
		ClientModel:       strPtr(clientModel),
		OutboundModel:     strPtr(outboundModel),
		ProviderID:        providerID,
		CredentialID:      credentialID,
		CanonicalID:       canonicalID,
		ClientProfile:     strPtr(clientProfile),
		IdentityHash:      strPtr(identityHash),
		RequestMode:       strPtr(requestMode),
		GwSessionID:       strPtr(gwSessionID),
		GwTaskID:          strPtr(gwTaskID),
		Success:           false,
		RequestStatus:     strPtr(telemetry.RequestStatusInProgress),
		RequestBody:       requestBodyText,
		RequestPreview:    requestPreviewPtr,
		TransformSummary:  transformSummaryPtr,
		TransformRuleID:   transformRuleID,
		EgressProtocol:    strPtr(egressProtocol),
		StreamInterrupted: &streamInterrupted,
	}
	if isStream {
		zero := 0
		reqLog.StreamChunkCount = &zero
	}
	applyAutoRouteFields(reqLog, autoCtx)
	if h.requestLogHook != nil {
		h.requestLogHook(reqLog)
	}
	applyKeyInfoToRequestLog(reqLog, keyInfo)
	h.telemetryClient.EmitRequestLogInsert(reqLog)
}

func extractModelFromBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var parsed struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil {
		return strings.TrimSpace(parsed.Model)
	}
	return extractModelFieldLoose(body)
}

// extractModelFieldLoose reads "model":"..." from truncated or invalid JSON.
func extractModelFieldLoose(body []byte) string {
	pattern := []byte(`"model"`)
	idx := bytes.Index(body, pattern)
	if idx < 0 {
		return ""
	}
	after := body[idx+len(pattern):]
	colonIdx := bytes.IndexByte(after, ':')
	if colonIdx < 0 {
		return ""
	}
	rest := bytes.TrimLeft(after[colonIdx+1:], " \t\n\r")
	if len(rest) == 0 || rest[0] != '"' {
		return ""
	}
	endIdx := bytes.IndexByte(rest[1:], '"')
	if endIdx < 0 {
		return ""
	}
	return strings.TrimSpace(string(rest[1 : endIdx+1]))
}

func (h *ChatHandler) emitFailedDecisionLog(requestID, clientModel string, keyInfo *auth.KeyInfo, clientID identity.ClientIdentity, candidatesTried int, modelResolution *resolve.Resolution, txResult *transform.TransformResult, errCode string, failTrace *routing.Trace, latencyMs int) {
	if h.telemetryClient == nil || !h.telemetryClient.Enabled() {
		return
	}
	var apiKeyID *int
	var tenantID string = "default"
	if keyInfo != nil {
		apiKeyID = &keyInfo.ID
		tenantID = keyInfo.TenantID
	}
	var canonical string
	if modelResolution != nil && modelResolution.CanonicalName != nil {
		canonical = *modelResolution.CanonicalName
	}
	dl := &telemetry.DecisionLogEntry{
		RequestID:         requestID,
		TenantID:          tenantID,
		APIKeyID:          apiKeyID,
		Model:             canonicalOrClient(canonical, clientModel),
		CandidatesTried:   candidatesTried,
		LatencyMs:         latencyMs,
		Success:           false,
		ErrorClass:        strPtr(errCode),
		FailureDetailCode: strPtr(errCode),
		ClientModel:       strPtr(clientModel),
		IdentityHash:      strPtr(clientID.IdentityHash),
	}
	if failTrace != nil {
		traceJSON, _ := json.Marshal(failTrace)
		dl.DecisionTrace = traceJSON
	}
	if modelResolution != nil {
		dl.ResolutionPath = strPtr(modelResolution.ResolutionPath)
		if modelResolution.CanonicalName != nil {
			dl.CanonicalModel = strPtr(*modelResolution.CanonicalName)
		}
		if len(modelResolution.RawModels) > 0 {
			dl.ResolutionRawModels = modelResolution.RawModels
		}
	}
	if txResult != nil {
		dl.OutboundModel = strPtr(txResult.OutboundModel)
		if txResult.MatchedRule != "" {
			dl.TransformRuleID = strPtr(txResult.MatchedRule)
		}
	}
	h.telemetryClient.EmitDecisionLog(dl)
}

func (h *ChatHandler) serveFallback(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusServiceUnavailable, map[string]any{
		"error": map[string]string{
			"message": "Routing executor not available. Database connection required.",
			"type":    "server_error",
			"code":    "executor_unavailable",
		},
	})
}

// ReplaceModelInRequestBody replaces the "model" field in a JSON body.
func ReplaceModelInRequestBody(body []byte, newModel string) []byte {
	quotedOld := bytes.Contains(body, []byte(`"model"`))
	if !quotedOld {
		return body
	}
	pattern := []byte(`"model"`)
	idx := bytes.Index(body, pattern)
	if idx < 0 {
		return body
	}
	after := body[idx+len(pattern):]
	colonIdx := bytes.IndexByte(after, ':')
	if colonIdx < 0 {
		return body
	}
	rest := after[colonIdx+1:]
	rest = bytes.TrimLeft(rest, " \t\n\r")
	if len(rest) == 0 || rest[0] != '"' {
		return body
	}
	endIdx := bytes.IndexByte(rest[1:], '"')
	if endIdx < 0 {
		return body
	}
	oldValue := rest[1 : endIdx+1]
	if string(oldValue) == newModel {
		return body
	}
	var buf bytes.Buffer
	prefix := body[:idx+len(pattern)+colonIdx+1]
	suffix := rest[endIdx+2:]
	buf.Write(prefix)
	buf.WriteString(" \"")
	buf.WriteString(newModel)
	buf.WriteByte('"')
	buf.Write(suffix)
	return buf.Bytes()
}

// ReplaceModelInResponseBody replaces whatever model is in the response with clientModel.
func ReplaceModelInResponseBody(body []byte, clientModel string) []byte {
	pattern := []byte(`"model"`)
	idx := bytes.Index(body, pattern)
	if idx < 0 {
		return body
	}
	after := body[idx+len(pattern):]
	colonIdx := bytes.IndexByte(after, ':')
	if colonIdx < 0 {
		return body
	}
	rest := after[colonIdx+1:]
	rest = bytes.TrimLeft(rest, " \t\n\r")
	if len(rest) < 2 || rest[0] != '"' {
		return body
	}
	endIdx := bytes.IndexByte(rest[1:], '"')
	if endIdx < 0 {
		return body
	}
	oldValue := rest[1 : endIdx+1]
	if string(oldValue) == clientModel {
		return body
	}
	var buf bytes.Buffer
	prefix := body[:idx+len(pattern)+colonIdx+1]
	suffix := rest[endIdx+2:]
	buf.Write(prefix)
	buf.WriteString(`"` + clientModel + `"`)
	buf.Write(suffix)
	return buf.Bytes()
}

// selectChatUpstreamBodyBytes is kept for backward compatibility but
// now always returns originalBody unchanged. The Q1/Q3 dispatch has
// moved into the executor (per-candidate) to avoid the candidates[0]
// mismatch bug where the body format didn't match the candidate the
// executor actually routed to.
func selectChatUpstreamBodyBytes(candidates []provider.Candidate, originalBody []byte) ([]byte, error) {
	return originalBody, nil
}

func requestHasTools(body []byte) bool {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return false
	}
	toolsRaw, ok := obj["tools"]
	if !ok || len(toolsRaw) == 0 || string(toolsRaw) == "null" {
		return false
	}
	var tools []any
	return json.Unmarshal(toolsRaw, &tools) == nil && len(tools) > 0
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

//-----------------------------------------------------------------------------
// Health handler
//-----------------------------------------------------------------------------

// HealthResponse represents the health check response.
type HealthResponse struct {
	Status      string         `json:"status"`
	Version     string         `json:"version"`
	Circuit     any            `json:"circuit,omitempty"`
	Concurrency any            `json:"concurrency,omitempty"`
	Proxy       map[string]any `json:"proxy,omitempty"`
}

// HealthHandler returns health information including circuit breaker and limiter stats.
type HealthHandler struct {
	circuit *circuit.Manager
	limiter *limiter.Limiter
	proxy   *upstreampkg.ProxyResolver
}

// NewHealthHandler creates a new health handler.
func NewHealthHandler(cm *circuit.Manager, l *limiter.Limiter, proxy *upstreampkg.ProxyResolver) *HealthHandler {
	return &HealthHandler{circuit: cm, limiter: l, proxy: proxy}
}

func (h *HealthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	resp := HealthResponse{
		Status:  "ok",
		Version: "0.2.0",
	}

	if r.URL.Query().Get("full") == "true" {
		resp.Circuit = h.circuit.Stats()
		resp.Concurrency = h.limiter.Stats()
	}

	if h.proxy != nil {
		if status := h.proxy.Status(); status != nil {
			resp.Proxy = status
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

var hopByHopHeaders = map[string]bool{
	"Connection":          true,
	"Keep-Alive":          true,
	"Proxy-Authenticate":  true,
	"Proxy-Authorization": true,
	"TE":                  true,
	"Trailers":            true,
	"Transfer-Encoding":   true,
	"Upgrade":             true,
	"Authorization":       true,
	"Cookie":              true,
	"Host":                true,
}

func copySafeHeaders(src http.Header) http.Header {
	dst := make(http.Header, len(src))
	for k, vs := range src {
		if hopByHopHeaders[http.CanonicalHeaderKey(k)] {
			continue
		}
		dst[k] = append([]string(nil), vs...)
	}
	return dst
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if s, err := strconv.Atoi(v); err == nil && s > 0 {
			return time.Duration(s) * time.Second
		}
	}
	return def
}

func extractBearerToken(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); auth != "" {
		if strings.HasPrefix(auth, "Bearer ") {
			return strings.TrimPrefix(auth, "Bearer ")
		}
		if strings.HasPrefix(auth, "bearer ") {
			return strings.TrimPrefix(auth, "bearer ")
		}
	}
	if key := r.Header.Get("x-api-key"); key != "" {
		return key
	}
	return ""
}

func resolveEndUser(bodyUser string, r *http.Request) string {
	if bodyUser != "" {
		return bodyUser
	}
	if header := r.Header.Get("X-End-User-Id"); header != "" {
		return strings.TrimSpace(header)
	}
	return "anonymous"
}

func extractTokensFromResponseBody(body []byte) (promptTokens, completionTokens, cacheRead, cacheWrite int) {
	if len(body) == 0 {
		return 0, 0, 0, 0
	}
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return 0, 0, 0, 0
	}
	usageRaw, ok := data["usage"]
	if !ok {
		// Fallback: some providers (e.g. minimax) may return usage at top level
		usageRaw = data
	}
	usage, ok := usageRaw.(map[string]any)
	if !ok {
		return 0, 0, 0, 0
	}
	// prompt_tokens / input_tokens (Anthropic native)
	if v, ok := usage["prompt_tokens"].(float64); ok {
		promptTokens = int(v)
	} else if v, ok := usage["input_tokens"].(float64); ok {
		promptTokens = int(v)
	}
	// completion_tokens / output_tokens (Anthropic native)
	if v, ok := usage["completion_tokens"].(float64); ok {
		completionTokens = int(v)
	} else if v, ok := usage["output_tokens"].(float64); ok {
		completionTokens = int(v)
	}
	// cache_read: try 4 variants
	if v, ok := usage["cache_read_input_tokens"].(float64); ok {
		cacheRead = int(v)
	} else if v, ok := usage["cache_read_tokens"].(float64); ok {
		cacheRead = int(v)
	} else if pt := usage["prompt_tokens_details"]; pt != nil {
		if details, ok := pt.(map[string]any); ok {
			if v, ok := details["cached_tokens"].(float64); ok && cacheRead == 0 {
				cacheRead = int(v)
			}
		}
	} else if pt := usage["input_token_details"]; pt != nil {
		if details, ok := pt.(map[string]any); ok {
			if v, ok := details["cache_read"].(float64); ok && cacheRead == 0 {
				cacheRead = int(v)
			}
		}
	}
	// cache_write: try 3 variants
	if v, ok := usage["cache_creation_input_tokens"].(float64); ok {
		cacheWrite = int(v)
	} else if v, ok := usage["cache_write_tokens"].(float64); ok {
		cacheWrite = int(v)
	} else if pt := usage["input_token_details"]; pt != nil {
		if details, ok := pt.(map[string]any); ok {
			if v, ok := details["cache_creation"].(float64); ok && cacheWrite == 0 {
				cacheWrite = int(v)
			}
		}
	}
	// total_tokens fallback: if we have total but missing prompt/completion, infer them
	if promptTokens == 0 || completionTokens == 0 {
		if total, ok := usage["total_tokens"].(float64); ok && int(total) > 0 {
			totalInt := int(total)
			if promptTokens == 0 && completionTokens > 0 && totalInt > completionTokens {
				promptTokens = totalInt - completionTokens
			} else if completionTokens == 0 && promptTokens > 0 && totalInt > promptTokens {
				completionTokens = totalInt - promptTokens
			}
		}
	}
	return
}

// injectUsageIntoResponseBody augments a response body JSON with usage data extracted
// from the stream capture. This ensures request_logs.response_body always contains a
// `usage` block even when the upstream's last SSE chunk does not include one.
func injectUsageIntoResponseBody(body []byte, pt, ct, crt, cwt int) []byte {
	if pt <= 0 && ct <= 0 {
		return body
	}
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return body
	}
	usageRaw, ok := data["usage"]
	if !ok {
		usageRaw = map[string]any{}
	}
	usage, ok := usageRaw.(map[string]any)
	if !ok {
		usage = map[string]any{}
	}
	if pt > 0 {
		usage["prompt_tokens"] = pt
	}
	if ct > 0 {
		usage["completion_tokens"] = ct
	}
	if crt > 0 {
		usage["cache_read_tokens"] = crt
	}
	if cwt > 0 {
		usage["cache_write_tokens"] = cwt
	}
	if pt > 0 && ct > 0 {
		usage["total_tokens"] = pt + ct
	}
	data["usage"] = usage
	result, err := json.Marshal(data)
	if err != nil {
		return body
	}
	return result
}

func intPtr(v int) *int { return &v }
func floatPtrFromInt(p *int) *float64 {
	if p == nil {
		return nil
	}
	v := float64(*p)
	return &v
}
func strPtr(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func boolPtr(v bool) *bool {
	return &v
}

// classifyStreamInterruption determines whether a stream interruption captured
// in the summary map represents a real gateway error that should mark the
// request log as failed. It returns (isError, detailCode).
//
// Benign cases that do NOT mark the request as failed:
//   - "eof_without_done" with chunk_count > 0: upstream closed without [DONE]
//     but content was already delivered (e.g. MiniMax). The gateway synthesises
//     [DONE] for the client. This mirrors executor_chat.go's isBenignEOF.
//   - "client_cancel" / "client_disconnected": the client went away; not a
//     gateway or upstream error.
func classifyStreamInterruption(m map[string]any) (isError bool, detailCode string) {
	detailCode, _ = m["failure_detail_code"].(string)
	chunkCount, _ := m["stream_chunk_count"].(int)

	isBenignEOF := detailCode == "eof_without_done" && chunkCount > 0
	isClientCancel := detailCode == "client_cancel" || detailCode == "client_disconnected"

	if isBenignEOF || isClientCancel {
		return false, detailCode
	}
	return true, detailCode
}

// canonicalOrClient prefers the canonical name (standardised model key from the
// routing table). When the resolution did not yield a canonical entry (direct
// passthrough), it falls back to whatever the client supplied.
func canonicalOrClient(canonical, client string) string {
	if canonical != "" {
		return canonical
	}
	return client
}

// generateRequestID returns a stable per-request UUID used both as the
// X-Request-Id response header and as the request_logs row's request_id
// column.  Always non-empty so the safety-net logger can find a row.
func generateRequestID() string {
	return uuid.NewString()
}

func writeErrorJSON(w http.ResponseWriter, status int, requestID, msg, errType, code string) {
	writeErrorJSONWithDebug(w, status, requestID, msg, errType, code, nil)
}

func writeErrorJSONWithDebug(w http.ResponseWriter, status int, requestID, msg, errType, code string, debug map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	if requestID != "" {
		w.Header().Set("X-Request-Id", requestID)
	}
	w.WriteHeader(status)
	errObj := map[string]any{
		"message":    msg,
		"type":       errType,
		"code":       code,
		"request_id": requestID,
	}
	if debug != nil {
		errObj["gateway_debug"] = debug
	}
	json.NewEncoder(w).Encode(map[string]any{
		"error": errObj,
	})
}

// writeErrorJSONWithKind (Step 6, 2026-06-18) is like
// writeErrorJSONWithDebug but additionally surfaces a "kind" field in
// the error object. The kind is the SSoT for the underlying failure
// cause (rate_limit, concurrent, model_not_found, ...). It is always
// emitted even when kind == code, so clients that learn the new shape
// never have to null-check.
//
// Backward compat: the legacy "code" field is unchanged ("model_not_found"
// is still surfaced there even when the real kind is "rate_limit"). New
// clients should read "kind"; old clients keep working.
func writeErrorJSONWithKind(w http.ResponseWriter, status int, requestID, msg, errType, code, kind string, debug map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	if requestID != "" {
		w.Header().Set("X-Request-Id", requestID)
	}
	w.WriteHeader(status)
	errObj := map[string]any{
		"message":    msg,
		"type":       errType,
		"code":       code,
		"request_id": requestID,
		"kind":       kind,
	}
	if debug != nil {
		errObj["gateway_debug"] = debug
	}
	json.NewEncoder(w).Encode(map[string]any{
		"error": errObj,
	})
}

// mapExecuteErrorToKind (Step 6, 2026-06-18) maps an exhausted
// ExecuteError to the client-visible "kind" field. The logic prefers
// the executor's recorded LastKind when set, and falls back to a small
// lookup table for the cases where LastKind is empty (e.g. no
// candidates returned at all from the router).
//
// Returns "" when no kind can be determined (caller should omit the
// header and the field).
func mapExecuteErrorToKind(err *routing.ExecuteError) string {
	if err == nil {
		return ""
	}
	if err.LastKind != "" {
		return string(err.LastKind)
	}
	if err.Tried == 0 {
		return "no_candidates"
	}
	return "unknown"
}

// errorKindOrFallback (2026-06-20) returns the real underlying error
// kind for request_logs.error_kind. Falls back to "model_not_found"
// when the kind is empty or "unknown" so we never write a misleading
// empty/garbage value to the database. The HTTP error.code is handled
// separately (see serveWithExecutor's Exhausted branch) and stays
// "model_not_found" for backward compatibility.
func errorKindOrFallback(kind string) string {
	if kind == "" || kind == "unknown" {
		return "model_not_found"
	}
	return kind
}

// captureAttemptBody reads the request body (capped at 1MB) into bodyOut
// and extracts the client_model from the JSON.  It does NOT close the
// body — the caller (serveWithExecutor) owns that responsibility via
// its own defer.
//
// 2026-06-20 audit fix v2: When the body is captured but has no
// "model" field (e.g. /v1/messages client omitted model, or body is
// `{}`), set client_model to "<unknown>" so request_logs never shows
// a blank client_model alongside a non-empty request_body. Without
// this, the operator cannot tell whether the body was empty OR the
// client simply forgot the model field — both look like an empty
// client_model. Setting "<unknown>" makes it explicit that the body
// was received but model extraction failed.
func captureAttemptBody(r *http.Request, bodyOut *[]byte, modelOut *string) {
	if bodyOut == nil || r == nil || r.Body == nil {
		return
	}
	if len(*bodyOut) > 0 {
		return
	}
	const maxBody = 1 << 20 // 1MB
	buf, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil || len(buf) == 0 {
		return
	}
	*bodyOut = buf
	if modelOut == nil {
		return
	}
	// Only attempt model extraction if modelOut is still empty
	if *modelOut != "" {
		return
	}
	var probe struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(buf, &probe)
	if probe.Model != "" {
		*modelOut = probe.Model
		return
	}
	// Body captured but no model field found — record as <unknown>
	// so request_logs.client_model is never blank when body is set.
	// This distinguishes "empty body" from "body present but no
	// model field" — both look the same otherwise.
	*modelOut = "<unknown>"
}

// emitTuningSignal computes the implicit feedback signal for an auto-route
// request and enqueues it for async batched write to tuning_signals.
//
// Only called for auto-route requests (model="auto"). All scoring is
// done in-process (no DB lookup on the hot path) to keep latency <1ms.
// The DB insert happens asynchronously in the tuning writer goroutine.
func (h *ChatHandler) emitTuningSignal(reqLog *telemetry.RequestLogEntry, success bool, latencyMs int) {
	if h == nil || h.telemetryClient == nil {
		return
	}

	classifier := "heuristic"
	if reqLog.AutoDecision != nil {
		var d struct {
			Classifier string `json:"classifier"`
		}
		if err := json.Unmarshal([]byte(*reqLog.AutoDecision), &d); err == nil && d.Classifier != "" {
			classifier = d.Classifier
		}
	}

	taskType := ""
	if reqLog.TaskType != nil {
		taskType = *reqLog.TaskType
	}
	chosenModel := ""
	if reqLog.OutboundModel != nil {
		chosenModel = *reqLog.OutboundModel
	}
	confidence := 0.0
	if reqLog.AutoConfidence != nil {
		confidence = *reqLog.AutoConfidence
	}

	latencyScore := 0.5
	if latencyMs > 0 && latencyMs < 30000 {
		ratio := float64(latencyMs) / 30000.0
		if ratio > 1 {
			ratio = 1
		}
		latencyScore = 1.0 - ratio
	}

	costScore := 0.5
	costUSD := 0.0
	if reqLog.CostUSD != nil {
		costUSD = *reqLog.CostUSD
	}
	if costUSD > 0 {
		ratio := costUSD / 0.01
		if ratio > 1 {
			ratio = 1
		}
		costScore = 1.0 - ratio
	}

	drift := false
	quality := telemetry.ComputeTuningSignalQuality(success, latencyMs, 0, costUSD, 0, drift)

	sessionID := ""
	if reqLog.GwSessionID != nil {
		sessionID = *reqLog.GwSessionID
	}

	var payload []byte
	if reqLog.AutoDecision != nil {
		payload = []byte(*reqLog.AutoDecision)
	}

	promptTokens, completionTokens := 0, 0
	if reqLog.PromptTokens != nil {
		promptTokens = *reqLog.PromptTokens
	}
	if reqLog.CompletionTokens != nil {
		completionTokens = *reqLog.CompletionTokens
	}

	sig := telemetry.TuningSignal{
		RequestID:        reqLog.RequestID,
		SessionID:        sessionID,
		TaskType:         taskType,
		Classifier:       classifier,
		Confidence:       confidence,
		ChosenModel:      chosenModel,
		SuccessScore:     boolToFloat(success),
		LatencyScore:     latencyScore,
		CostScore:        costScore,
		DriftFlag:        drift,
		QualityScore:     quality,
		LatencyMs:        latencyMs,
		CostUSD:          costUSD,
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		SignalPayload:    payload,
		Strategy:         string(autoroute.AssignStrategy(reqLog.RequestID)),
	}
	telemetry.WriteTuningSignal(sig)
}

func boolToFloat(b bool) float64 {
	if b {
		return 1.0
	}
	return 0.0
}
