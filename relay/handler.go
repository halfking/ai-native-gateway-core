package relay

import (
	"bytes"
	"context"
	"encoding/json"
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
	"github.com/kaixuan/llm-gateway-go/circuit"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/identity"
	"github.com/kaixuan/llm-gateway-go/limiter"
	"github.com/kaixuan/llm-gateway-go/pool"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/ratelimit"
	"github.com/kaixuan/llm-gateway-go/resolve"
	"github.com/kaixuan/llm-gateway-go/routing"
	"github.com/kaixuan/llm-gateway-go/sessions"
	"github.com/kaixuan/llm-gateway-go/telemetry"
	"github.com/kaixuan/llm-gateway-go/transform"
	upstreampkg "github.com/kaixuan/llm-gateway-go/upstream"
)

const maxBodySize = 32 << 20

func MaxBodySize() int { return maxBodySize }

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
}

type chatResponseBody struct {
	Model string `json:"model"`
}

//-----------------------------------------------------------------------------
// Chat handler — integrates circuit breaker + concurrency limiter
//-----------------------------------------------------------------------------

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
	provider        *provider.Client
	sticky          *routing.StickyCache
	keyVerifier     *auth.KeyVerifier
	rateLimiter     ratelimit.RPMLimiter
	telemetryClient *telemetry.Client
	// requestLogHook is an optional test sink.  When set, every
	// request_logs row the gateway emits is also passed to the hook
	// function so unit tests can assert on the safety-net coverage.
	// See SetRequestLogHook.
	requestLogHook func(*telemetry.RequestLogEntry)
	sessionGetter   interface {
		Get(ctx context.Context, id string) (*sessions.Session, error)
		Touch(ctx context.Context, id string) error
		CreateV2(ctx context.Context, apiKeyID int, tenantID, deviceSeed, taskID string) (*sessions.Session, error)
	}
}

func NewChatHandler(cm *circuit.Manager, l *limiter.Limiter, matrix *transform.Matrix, pools *pool.PoolManager, resolver *resolve.Resolver, auditor audit.Sink) *ChatHandler {
	if auditor == nil {
		auditor = &audit.LogSink{}
	}
	return &ChatHandler{circuit: cm, limiter: l, matrix: matrix, pools: pools, resolver: resolver, auditor: auditor, client: upstreampkg.New(), normalizer: NewNormalizer()}
}

func (h *ChatHandler) SetExecutor(exec *routing.Executor, prov *provider.Client, sticky *routing.StickyCache) {
	h.executor = exec
	h.provider = prov
	h.sticky = sticky
}

func (h *ChatHandler) SetAuth(kv *auth.KeyVerifier, rl ratelimit.RPMLimiter) {
	h.keyVerifier = kv
	h.rateLimiter = rl
}

func (h *ChatHandler) SetTelemetry(tc *telemetry.Client) {
	h.telemetryClient = tc
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
	var (
		attemptLoggedFlag   bool
		attemptKeyInfo      *auth.KeyInfo
		attemptClientModel  string
		attemptErrCode      string
		attemptErrMsg       string
		attemptProviderID   *int
		attemptCredentialID *int
	)
	attemptLogged := &attemptLoggedFlag
	requestID := r.Header.Get("X-Request-Id")
	if requestID == "" {
		requestID = generateRequestID()
		w.Header().Set("X-Request-Id", requestID)
	}
	startTime := time.Now()
	defer func() {
		slog.Info("safety_net_defer_fired",
			"request_id", requestID,
			"attempt_err_code", attemptErrCode,
			"attempt_logged", *attemptLogged)
		if rec := recover(); rec != nil {
			slog.Error("chat handler panic", "panic", rec, "request_id", requestID)
			attemptErrCode = "internal_panic"
			attemptErrMsg = "internal server error"
			if attemptClientModel == "" {
				attemptClientModel = "<unknown>"
			}
			h.recordFailedRequestWithKey(requestID, attemptClientModel, "",
				attemptProviderID, attemptCredentialID,
				attemptErrCode, attemptErrMsg,
				int(time.Since(startTime).Milliseconds()),
				nil, attemptKeyInfo, r)
			if !*attemptLogged {
				writeErrorJSON(w, http.StatusInternalServerError, requestID,
					"internal server error", "server_error", "internal_panic")
			}
		} else if attemptErrCode != "" && !*attemptLogged {
			slog.Info("safety_net: recording failed request",
				"request_id", requestID,
				"error_kind", attemptErrCode,
				"client_model", attemptClientModel)
			latency := int(time.Since(startTime).Milliseconds())
			h.recordFailedRequestWithKey(requestID, attemptClientModel, "",
				attemptProviderID, attemptCredentialID,
				attemptErrCode, attemptErrMsg, latency, nil, attemptKeyInfo, r)
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
		attemptErrCode = "method_not_allowed"
		attemptErrMsg = "method not allowed"
		http.Error(w, `{"error":{"message":"Method not allowed","type":"invalid_request","code":"method_not_allowed"}}`, http.StatusMethodNotAllowed)
		return
	}

	if h.executor != nil && h.provider != nil && h.provider.Enabled() {
		// serveWithExecutor shares the outer-scope attempt state via
		// Go's closure capture: any field it writes is visible to the
		// deferred safety-net below.  attemptLogged is passed as
		// *bool so the inner function can mark the row as already
		// written and avoid a duplicate emit.
		h.serveWithExecutor(w, r, attemptLogged, &attemptKeyInfo, &attemptClientModel,
			&attemptErrCode, &attemptErrMsg, &attemptProviderID, &attemptCredentialID,
			requestID, startTime)
		return
	}
	// No executor / provider — record a 503 row so the request still
	// shows up in the admin request-logs UI.
	attemptErrCode = "executor_unavailable"
	attemptErrMsg = "routing executor not available; database connection required"
	h.recordFailedRequestWithKey(requestID, attemptClientModel, "",
		attemptProviderID, attemptCredentialID,
		attemptErrCode, attemptErrMsg,
		int(time.Since(startTime).Milliseconds()),
		nil, attemptKeyInfo, r)
	*attemptLogged = true
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
	attemptLogged *bool,
	attemptKeyInfo **auth.KeyInfo,
	attemptClientModel *string,
	attemptErrCode *string,
	attemptErrMsg *string,
	attemptProviderID **int,
	attemptCredentialID **int,
	requestID string,
	startTime time.Time,
) {
	defer r.Body.Close()

	// Helper to mark this request as already-recorded, so the deferred
	// safety net in ServeHTTP does not write a duplicate row.
	markLogged := func() { *attemptLogged = true }

	// ── API key authentication ──────────────────────────────────────────
	var keyInfo *auth.KeyInfo
	if h.keyVerifier != nil && h.keyVerifier.Enabled() {
		rawKey := extractBearerToken(r)
		if rawKey == "" {
			*attemptErrCode = "missing_key"
			*attemptErrMsg = "missing api key"
			writeErrorJSON(w, http.StatusUnauthorized, requestID, "Missing API key", "authentication_error", "missing_key")
			return
		}
		ki, verifyErr := h.keyVerifier.Verify(r.Context(), rawKey)
		if verifyErr != nil {
			if _, ok := verifyErr.(*auth.InvalidKeyError); ok {
				*attemptErrCode = "invalid_key"
				*attemptErrMsg = "invalid or expired api key"
				w.Header().Set("WWW-Authenticate", "Bearer")
				writeErrorJSON(w, http.StatusUnauthorized, requestID, "Invalid or expired API key", "authentication_error", "invalid_key")
				return
			}
			// Auth RPC failed — fail-closed. Silently downgrading to anonymous would
			// bypass rate limiting, budget checks, and user isolation.
			slog.Error("key verification RPC failed, rejecting request", "error", verifyErr)
			*attemptErrCode = "auth_unavailable"
			*attemptErrMsg = "authentication service temporarily unavailable"
			writeErrorJSON(w, http.StatusServiceUnavailable, requestID,
				"Authentication service temporarily unavailable", "server_error", "auth_unavailable")
			return
		}
		keyInfo = ki
		*attemptKeyInfo = ki
	}

	// ── Status checks (throttled key → hard rate-limit) ────────────────
	if keyInfo != nil && keyInfo.Status == "throttled" {
		*attemptErrCode = "key_throttled"
		*attemptErrMsg = "api key throttled due to anomalous usage"
		writeErrorJSON(w, http.StatusTooManyRequests, requestID,
			"Your API key has been throttled due to anomalous usage. Contact admin.",
			"rate_limit_error", "key_throttled")
		return
	}

	// ── RPM rate limit ──────────────────────────────────────────────────
	if keyInfo != nil && h.rateLimiter != nil && !keyInfo.IsInternal {
		rpmLimit := keyInfo.EffectiveRPM()
		if !h.rateLimiter.CheckRPM(keyInfo.ID, rpmLimit) {
			_, remaining := h.rateLimiter.RPMStatus(keyInfo.ID, rpmLimit)
			w.Header().Set("X-RateLimit-Limit", fmt.Sprintf("%d", rpmLimit))
			w.Header().Set("X-RateLimit-Remaining", fmt.Sprintf("%d", remaining))
			w.Header().Set("Retry-After", "60")
			*attemptErrCode = "rate_limit_exceeded"
			*attemptErrMsg = "rate limit exceeded"
			writeErrorJSON(w, http.StatusTooManyRequests, requestID, "Rate limit exceeded", "rate_limit_error", "rate_limit_exceeded")
			return
		}
	}

	// ── Budget pre-check ─────────────────────────────────────────────────
	if keyInfo != nil && h.keyVerifier != nil {
		if budgetErr := h.keyVerifier.CheckBudget(r.Context(), keyInfo.ID); budgetErr != nil {
			if _, ok := budgetErr.(*auth.BudgetExceededError); ok {
				*attemptErrCode = "budget_exhausted"
				*attemptErrMsg = "budget exhausted"
				writeErrorJSON(w, http.StatusPaymentRequired, requestID, "Budget exhausted. Contact admin to top up.", "insufficient_quota", "budget_exhausted")
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
	sessionID := r.Header.Get("X-Gw-Session-Id")
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
			if keyInfo != nil && si.APIKeyID != keyInfo.ID {
				*attemptErrCode = "session_forbidden"
				*attemptErrMsg = "session not owned by this api key"
				writeErrorJSON(w, http.StatusForbidden, requestID, "session not owned by this API key", "session_error", "SESSION_FORBIDDEN")
				return
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
		*attemptErrCode = "body_read_error"
		*attemptErrMsg = "failed to read request body"
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{"message": "failed to read request body", "type": "invalid_request", "code": "body_read_error"},
		})
		return
	}
	if len(bodyBytes) > maxBodySize {
		*attemptErrCode = "body_too_large"
		*attemptErrMsg = "request body exceeds 32 MiB limit"
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{
			"error": map[string]string{"message": "request body exceeds 32 MiB limit", "type": "invalid_request", "code": "body_too-large"},
		})
		return
	}

	var reqBody chatRequestBody
	if err := json.Unmarshal(bodyBytes, &reqBody); err != nil {
		*attemptErrCode = "json_parse_error"
		*attemptErrMsg = "invalid JSON in request body"
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{"message": "invalid JSON in request body", "type": "invalid_request", "code": "json_parse_error"},
		})
		return
	}

	clientModel := reqBody.Model
	*attemptClientModel = clientModel
	isStream := reqBody.Stream
	endUser := resolveEndUser(reqBody.User, r)
	clientID := identity.BuildIdentityFromRequest(r, "default", nil, nil, "")
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
		h.recordFailedRequestWithKey(requestID, clientModel, "", nil, nil, "no_candidate",
			fmt.Sprintf("no available provider for model '%s'", clientModel),
			int(time.Since(startTime).Milliseconds()), bodyBytes, keyInfo, r)
		markLogged()
		writeErrorJSON(w, http.StatusServiceUnavailable, requestID, fmt.Sprintf("no available provider for model '%s'", clientModel), "server_error", "no_candidate")
		return
	}
	if len(candidates) == 0 {
		h.emitFailedDecisionLog(requestID, clientModel, keyInfo, clientID, 0, nil, nil, "no_candidate", nil, int(time.Since(startTime).Milliseconds()))
		h.recordFailedRequestWithKey(requestID, clientModel, "", nil, nil, "no_candidate",
			fmt.Sprintf("no available provider for model '%s'", clientModel),
			int(time.Since(startTime).Milliseconds()), bodyBytes, keyInfo, r)
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
		*attemptProviderID = &pid
		*attemptCredentialID = &cid
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
	h.recordInitialRequestLog(
		requestID, clientModel, explicitOutbound, endUser, "chat", keyInfo,
		clientID.Fingerprint.ClientProfile, identityHash,
		*attemptProviderID, *attemptCredentialID, canonicalID,
		bodyBytes, txResult, egressProtocol, isStream,
		gwSessionID, gwTaskID,
	)

	var sessionKey string
	if sessionInfo != nil {
		sessionKey = sessionInfo.SessionKey
	}
	stickyKey := buildRouteStickyKey(tenant(keyInfo), appID(keyInfo), apiKeyIDPtr(keyInfo), clientID.Fingerprint.ClientProfile, sessionID, endUser, clientID.Fingerprint.PrimarySeed())

	upstreamBody, convErr := selectChatUpstreamBodyBytes(candidates, bodyBytes)
	if convErr != nil {
		*attemptErrCode = "chat_to_anthropic_conversion_error"
		*attemptErrMsg = convErr.Error()
		writeErrorJSON(w, http.StatusBadRequest, requestID, convErr.Error(), "invalid_request", "chat_to_anthropic_conversion_error")
		return
	}

	result, execErr := h.executor.Execute(&routing.ExecParams{
		W:             w,
		R:             r,
		BodyBytes:     upstreamBody,
		IsStream:      isStream,
		ClientProtocol: "openai-completions",
		ClientModel:   clientModel,
		OutboundModel: explicitOutbound,
		ClientID:      clientID,
		Transform:     txResult,
		Resolution:    modelResolution,
		Candidates:    candidates,
		Policy:        policy,
		AuditBuilder:  auditBuilder,
		Capture:       streamCapture,
		ToolsRequested: requestHasTools(bodyBytes),
		SessionKey:    sessionKey,
		StickyKey:     stickyKey,
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
		errCode := "provider_error"
		if execErrTyped, ok := execErr.(*routing.ExecuteError); ok && execErrTyped.Exhausted {
			errCode = "model_not_found"
			h.recordFailedRequestWithKey(requestID, clientModel, explicitOutbound, providerID, credentialID,
				"model_not_found",
				fmt.Sprintf("No available provider for model '%s'. All %d candidates failed.", clientModel, execErrTyped.Tried),
				int(time.Since(startTime).Milliseconds()), bodyBytes, keyInfo, r)
			h.emitFailedDecisionLog(requestID, clientModel, keyInfo, clientID, tried, modelResolution, txResult, errCode, failTrace, int(time.Since(startTime).Milliseconds()))
			markLogged()
			writeErrorJSONWithDebug(w, http.StatusServiceUnavailable, requestID,
				fmt.Sprintf("No available provider for model '%s'. All %d candidates failed.", clientModel, execErrTyped.Tried),
				"server_error", "model_not_found", map[string]any{
					"stage":      "execution",
					"kind":       string(execErrTyped.LastKind),
					"attempts":   execErrTyped.Attempts,
					"tried":      execErrTyped.Tried,
					"retryable":  errorsx.IsRetryable(execErrTyped.LastKind),
				})
			return
		}
		h.recordFailedRequestWithKey(requestID, clientModel, explicitOutbound, providerID, credentialID,
			"provider_error", execErr.Error(),
			int(time.Since(startTime).Milliseconds()), bodyBytes, keyInfo, r)
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
	h.emitTelemetry(auditBuilder.Build(), result, endUser, keyInfo, streamCapture, "chat", txResult, result.RequestBody, result.ResponseBody)
	markLogged()
}

func (h *ChatHandler) emitTelemetry(evt audit.Event, result *routing.ExecuteResult, endUser string, keyInfo *auth.KeyInfo, capture *audit.StreamCapture, requestMode string, txResult *transform.TransformResult, requestBody []byte, responseBody []byte) {
	if h.telemetryClient == nil || !h.telemetryClient.Enabled() {
		return
	}

	var apiKeyID *int
	var tenantID string = "default"
	if keyInfo != nil {
		apiKeyID = &keyInfo.ID
		tenantID = keyInfo.TenantID
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

	reqLog := &telemetry.RequestLogEntry{
		RequestID:        evt.RequestID,
		TenantID:         tenantID,
		APIKeyID:         apiKeyID,
		EndUserID:        strPtr(endUser),
		ClientModel:      strPtr(evt.ClientModel),
		OutboundModel:    strPtr(evt.OutboundModel),
		CredentialID:     intPtr(result.Candidate.CredentialID),
		ProviderID:       intPtr(result.Candidate.ProviderID),
		ClientProfile:    strPtr(evt.ClientProfile),
		RequestMode:      strPtr(requestMode),
		LatencyMs:        intPtr(result.LatencyMs),
		Success:          true,
		IdentityHash:     strPtr(evt.IdentityHash),
		RequestPreview:   requestPreviewPtr,
		TransformSummary: transformSummaryPtr,
		ResponsePreview:  responsePreviewPtr,
		RequestBody:      requestBodyText,
		ResponseBody:     responseBodyText,
	}

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
					reqLog.ErrorKind = strPtr("stream_error")
				}
				if detailCode != "" {
					reqLog.FailureDetailCode = strPtr(detailCode)
				}
			}
		}
		// Persist the finish reason for successful streams too. The capture
		// summary reuses "failure_detail_code" for the upstream finish_reason
		// regardless of success/failure ("stop", "length", "tool_calls",
		// "end_turn" for success; "eof_without_done", "stream_timeout" for
		// interruption). Without this, the column is empty for normal 200s.
		if reqLog.FailureDetailCode == nil {
			if dc, ok := m["failure_detail_code"].(string); ok && dc != "" {
				reqLog.FailureDetailCode = strPtr(dc)
			}
		}
		if v, ok := m["response_preview"].(string); ok && v != "" && reqLog.ResponsePreview == nil {
			reqLog.ResponsePreview = strPtr(v)
		}
	} else {
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
			CacheWriteTokens:  floatPtrFromInt(reqLog.CacheWriteTokens),
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
				CacheWriteTokens:  floatPtrFromInt(reqLog.CacheWriteTokens),
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

	h.telemetryClient.EmitRequestLogUpdate(reqLog)
	if h.requestLogHook != nil {
		h.requestLogHook(reqLog)
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

// recordFailedRequestWithKey is the full-fat version.  When keyInfo is
// non-nil we attach api_key_id + tenant_id so the row is queryable
// from the admin UI in the same way as success rows.
// It updates the early request_logs row when one exists (see
// recordInitialRequestLog); otherwise it inserts a complete row.
func (h *ChatHandler) recordFailedRequestWithKey(requestID, clientModel, outboundModel string, providerID, credentialID *int, errCode, errMessage string, latencyMs int, requestBody []byte, keyInfo *auth.KeyInfo, r *http.Request) {
	gwSessionID, gwTaskID := "", ""
	if r != nil {
		gwSessionID, gwTaskID = gwSessionTaskFromRequest(r, sessions.SessionFromContext(r.Context()))
	}
	h.recordFailedRequestDetailed(requestID, clientModel, outboundModel, providerID, credentialID, errCode, errMessage, latencyMs, requestBody, keyInfo, "", "", "", gwSessionID, gwTaskID)
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
) {
	if clientModel == "" && len(requestBody) > 0 {
		clientModel = extractModelFromBody(requestBody)
	}
	var requestBodyText *string
	if len(requestBody) > 0 {
		v := string(requestBody)
		requestBodyText = &v
	}
	latency := latencyMs
	tenantID := "default"
	var apiKeyID *int
	if keyInfo != nil {
		tenantID = keyInfo.TenantID
		kid := keyInfo.ID
		apiKeyID = &kid
	}
	var requestPreviewPtr *string
	if preview := requestPreview(requestBody); preview != "" {
		requestPreviewPtr = strPtr(preview)
	}
	reqLog := &telemetry.RequestLogEntry{
		RequestID:     requestID,
		TenantID:      tenantID,
		APIKeyID:      apiKeyID,
		ClientModel:   strPtr(clientModel),
		OutboundModel: strPtr(outboundModel),
		ProviderID:    providerID,
		CredentialID:  credentialID,
		ClientProfile: strPtr(clientProfile),
		IdentityHash:  strPtr(identityHash),
		RequestMode:   strPtr(requestMode),
		GwSessionID:   strPtr(gwSessionID),
		GwTaskID:      strPtr(gwTaskID),
		LatencyMs:     &latency,
		Success:       false,
		ErrorKind:     strPtr(errCode),
		RequestBody:   requestBodyText,
		RequestPreview: requestPreviewPtr,
	}
	// Test hook fires regardless of whether the production telemetry
	// sink is configured, so unit tests can assert the safety net
	// without standing up a database.
	if h.requestLogHook != nil {
		h.requestLogHook(reqLog)
	}
	if h.telemetryClient == nil || !h.telemetryClient.Enabled() {
		return
	}
	h.telemetryClient.EmitRequestLogUpdate(reqLog)
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
	if keyInfo != nil {
		tenantID = keyInfo.TenantID
		kid := keyInfo.ID
		apiKeyID = &kid
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
		RequestID:        requestID,
		TenantID:           tenantID,
		APIKeyID:           apiKeyID,
		EndUserID:          strPtr(endUser),
		ClientModel:        strPtr(clientModel),
		OutboundModel:      strPtr(outboundModel),
		ProviderID:         providerID,
		CredentialID:       credentialID,
		CanonicalID:        canonicalID,
		ClientProfile:      strPtr(clientProfile),
		IdentityHash:       strPtr(identityHash),
		RequestMode:        strPtr(requestMode),
		GwSessionID:        strPtr(gwSessionID),
		GwTaskID:           strPtr(gwTaskID),
		Success:            false,
		RequestBody:        requestBodyText,
		RequestPreview:     requestPreviewPtr,
		TransformSummary:   transformSummaryPtr,
		TransformRuleID:    transformRuleID,
		EgressProtocol:     strPtr(egressProtocol),
		StreamInterrupted:  &streamInterrupted,
	}
	if isStream {
		zero := 0
		reqLog.StreamChunkCount = &zero
	}
	if h.requestLogHook != nil {
		h.requestLogHook(reqLog)
	}
	h.telemetryClient.EmitRequestLogInsert(reqLog)
}

func extractModelFromBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var parsed struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ""
	}
	return strings.TrimSpace(parsed.Model)
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
		RequestID:        requestID,
		TenantID:         tenantID,
		APIKeyID:         apiKeyID,
		Model:            canonicalOrClient(canonical, clientModel),
		CandidatesTried:  candidatesTried,
		LatencyMs:        latencyMs,
		Success:          false,
		ErrorClass:       strPtr(errCode),
		FailureDetailCode: strPtr(errCode),
		ClientModel:      strPtr(clientModel),
		IdentityHash:     strPtr(clientID.IdentityHash),
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
