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
// In production this will come from the Python control plane; for now
// it's configured via environment variables.
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
	circuit          *circuit.Manager
	limiter          *limiter.Limiter
	matrix           *transform.Matrix
	pools            *pool.PoolManager
	resolver         *resolve.Resolver
	auditor          audit.Sink
	client           *upstreampkg.Client
	normalizer       *Normalizer
	executor         *routing.Executor
	provider         *provider.Client
	sticky           *routing.StickyCache
	keyVerifier      *auth.KeyVerifier
	rateLimiter      ratelimit.RPMLimiter
	telemetryClient  *telemetry.Client
	sessionGetter    interface {
		Get(ctx context.Context, id string) (*sessions.Session, error)
		Touch(ctx context.Context, id string) error
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

func (h *ChatHandler) SetSessionGetter(sg interface {
	Get(ctx context.Context, id string) (*sessions.Session, error)
	Touch(ctx context.Context, id string) error
}) {
	h.sessionGetter = sg
}

// ServeHTTP handles /v1/chat/completions and /v1/completions.
func (h *ChatHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":{"message":"Method not allowed","type":"invalid_request","code":"method_not_allowed"}}`, http.StatusMethodNotAllowed)
		return
	}

	if h.executor != nil && h.provider != nil && h.provider.Enabled() {
		h.serveWithExecutor(w, r)
		return
	}
	h.serveFallback(w, r)
}

func (h *ChatHandler) serveWithExecutor(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	requestID := r.Header.Get("X-Request-Id")

	// ── API key authentication ──────────────────────────────────────────
	var keyInfo *auth.KeyInfo
	if h.keyVerifier != nil && h.keyVerifier.Enabled() {
		rawKey := extractBearerToken(r)
		if rawKey == "" {
			writeErrorJSON(w, http.StatusUnauthorized, requestID, "Missing API key", "authentication_error", "missing_key")
			return
		}
		ki, verifyErr := h.keyVerifier.Verify(r.Context(), rawKey)
		if verifyErr != nil {
			if _, ok := verifyErr.(*auth.InvalidKeyError); ok {
				w.Header().Set("WWW-Authenticate", "Bearer")
				writeErrorJSON(w, http.StatusUnauthorized, requestID, "Invalid or expired API key", "authentication_error", "invalid_key")
				return
			}
			slog.Warn("key verification RPC failed, proceeding without auth", "error", verifyErr)
		} else {
			keyInfo = ki
		}
	}

	// ── Status checks (throttled key → hard rate-limit) ────────────────
	if keyInfo != nil && keyInfo.Status == "throttled" {
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
			writeErrorJSON(w, http.StatusTooManyRequests, requestID, "Rate limit exceeded", "rate_limit_error", "rate_limit_exceeded")
			return
		}
	}

	// ── Budget pre-check ─────────────────────────────────────────────────
	if keyInfo != nil && h.keyVerifier != nil {
		if budgetErr := h.keyVerifier.CheckBudget(r.Context(), keyInfo.ID); budgetErr != nil {
			if _, ok := budgetErr.(*auth.BudgetExceededError); ok {
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

	// ── Session validation (if X-Session-Id provided) ──────────────────
	var sessionInfo *sessions.Session
	sessionID := r.Header.Get("X-Session-Id")
	if sessionID != "" && h.sessionGetter != nil {
		si, err := h.sessionGetter.Get(ctx, sessionID)
		if err != nil {
			if err == sessions.ErrSessionNotFound {
				writeErrorJSON(w, http.StatusBadRequest, requestID, "invalid session", "session_error", "SESSION_INVALID")
				return
			}
			slog.Warn("session lookup failed", "error", err)
		} else {
			sessionInfo = si
			if keyInfo != nil && si.APIKeyID != keyInfo.ID {
				writeErrorJSON(w, http.StatusForbidden, requestID, "session not owned by this API key", "session_error", "SESSION_FORBIDDEN")
				return
			}
			go h.sessionGetter.Touch(context.Background(), sessionID)
			ctx = sessions.SessionFromContextWith(ctx, sessionInfo)
		}
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, int64(maxBodySize)+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{"message": "failed to read request body", "type": "invalid_request", "code": "body_read_error"},
		})
		return
	}
	if len(bodyBytes) > maxBodySize {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{
			"error": map[string]string{"message": "request body exceeds 32 MiB limit", "type": "invalid_request", "code": "body_too_large"},
		})
		return
	}

	var reqBody chatRequestBody
	if err := json.Unmarshal(bodyBytes, &reqBody); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{"message": "invalid JSON in request body", "type": "invalid_request", "code": "json_parse_error"},
		})
		return
	}

	clientModel := reqBody.Model
	isStream := reqBody.Stream
	endUser := resolveEndUser(reqBody.User, r)
	clientID := identity.BuildIdentityFromRequest(r, "default", nil, nil, "")
	identityHash := clientID.ShortID()

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
		// Use background context — request context may be cancelled by now
		// for long-running streaming requests.
		h.auditor.Emit(context.Background(), auditBuilder.Build())
	}()

	candidates, policy, err := h.provider.GetCandidates(r.Context(), clientModel, clientID.Fingerprint.ClientProfile)
	if err != nil {
		slog.Error("failed to get candidates from provider", "error", err)
		writeErrorJSON(w, http.StatusServiceUnavailable, requestID, fmt.Sprintf("no available provider for model '%s'", clientModel), "server_error", "no_candidate")
		return
	}
	if len(candidates) == 0 {
		writeErrorJSON(w, http.StatusServiceUnavailable, requestID, fmt.Sprintf("no available provider for model '%s'", clientModel), "server_error", "no_candidate")
		return
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
	if txResult != nil && txResult.OutboundModel != "" {
		explicitOutbound = transform.RenderOutboundModel(
			txResult.OutboundModel, txResult.OutboundModel, clientModel, tCtx.CanonicalName,
		)
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

	var sessionKey string
	if sessionInfo != nil {
		sessionKey = sessionInfo.SessionKey
	}
	stickyKey := buildRouteStickyKey(tenant(keyInfo), appID(keyInfo), apiKeyIDPtr(keyInfo), clientID.Fingerprint.ClientProfile, sessionID, endUser, clientID.Fingerprint.PrimarySeed())

	result, execErr := h.executor.Execute(&routing.ExecParams{
		W:             w,
		R:             r,
		BodyBytes:     bodyBytes,
		IsStream:      isStream,
		ClientModel:   clientModel,
		OutboundModel: explicitOutbound,
		ClientID:      clientID,
		Transform:     txResult,
		Resolution:    modelResolution,
		Candidates:    candidates,
		Policy:        policy,
		AuditBuilder:  auditBuilder,
		Capture:       streamCapture,
		SessionKey:    sessionKey,
		StickyKey:     stickyKey,
	})

	if execErr != nil {
		slog.Error("executor failed",
			"error", execErr,
			"model", clientModel,
		)
		if execErr, ok := execErr.(*routing.ExecuteError); ok && execErr.Exhausted {
			writeErrorJSON(w, http.StatusServiceUnavailable, requestID,
				fmt.Sprintf("No available provider for model '%s'. All %d candidates failed.", clientModel, execErr.Tried),
				"server_error", "model_not_found")
			return
		}
		writeErrorJSON(w, http.StatusBadGateway, requestID, "upstream request failed", "server_error", "provider_error")
		return
	}

	auditBuilder.Success(true).Latency(time.Duration(result.LatencyMs) * time.Millisecond)
	h.emitTelemetry(auditBuilder.Build(), result, endUser, keyInfo, streamCapture, "chat", txResult, result.RequestBody, result.ResponseBody)
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

	h.telemetryClient.EmitDecisionLog(&telemetry.DecisionLogEntry{
		RequestID:          evt.RequestID,
		TenantID:           tenantID,
		APIKeyID:           apiKeyID,
		Model:              evt.ClientModel,
		ChosenCredentialID: intPtr(result.Candidate.CredentialID),
		ChosenProviderID:   intPtr(result.Candidate.ProviderID),
		CandidatesTried:    1,
		LatencyMs:          result.LatencyMs,
		Success:            true,
		ClientModel:        strPtr(evt.ClientModel),
		OutboundModel:      strPtr(evt.OutboundModel),
		ClientProfile:      strPtr(evt.ClientProfile),
		RequestMode:        strPtr(requestMode),
		IdentityHash:       strPtr(evt.IdentityHash),
		TransformRuleID:    strPtr(evt.TransformRule),
	})

	var requestBodyText *string
	if len(requestBody) > 0 {
		v := string(requestBody)
		requestBodyText = &v
	}
	var responseBodyText *string
	if len(responseBody) > 0 {
		v := string(responseBody)
		responseBodyText = &v
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
		if v, ok := m["prompt_tokens"].(int); ok {
			reqLog.PromptTokens = &v
		}
		if v, ok := m["completion_tokens"].(int); ok {
			reqLog.CompletionTokens = &v
		}
		if v, ok := m["cache_read_tokens"].(int); ok {
			reqLog.CacheReadTokens = &v
		}
		if v, ok := m["cache_write_tokens"].(int); ok {
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
		}
		if v, ok := m["response_preview"].(string); ok && v != "" && reqLog.ResponsePreview == nil {
			reqLog.ResponsePreview = strPtr(v)
		}
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
	}

	h.telemetryClient.EmitRequestLog(reqLog)
}

func (h *ChatHandler) serveFallback(w http.ResponseWriter, r *http.Request) {
	svc := defaultService()

	// ── Circuit breaker check ──────────────────────────────────────────
	if !h.circuit.Allow(svc.ProviderID, svc.CredentialID) {
		state := h.circuit.GetOrCreate(svc.ProviderID, svc.CredentialID).State()
		status := http.StatusServiceUnavailable
		code := "service_unavailable"
		message := "Service temporarily unavailable due to provider issues. Please retry later."
		if state == circuit.StateQuarantined {
			status = http.StatusBadGateway
			code = "invalid_api_key"
			message = "Provider credentials are invalid. Please check your configuration."
		}
		writeJSON(w, status, map[string]any{
			"error": map[string]string{
				"message": message,
				"type":    "server_error",
				"code":    code,
			},
		})
		return
	}

	// ── Read and buffer body (capped at 32 MiB) ──────────────────────
	defer r.Body.Close()
	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, maxBodySize+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{
				"message": "failed to read request body",
				"type":    "invalid_request",
				"code":    "body_read_error",
			},
		})
		return
	}
	if len(bodyBytes) > maxBodySize {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{
			"error": map[string]string{
				"message": "request body exceeds 32 MiB limit",
				"type":    "invalid_request",
				"code":    "body_too_large",
			},
		})
		return
	}

	// ── Parse request body for model + stream ──────────────────────────
	var reqBody chatRequestBody
	if err := json.Unmarshal(bodyBytes, &reqBody); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{
				"message": "invalid JSON in request body",
				"type":    "invalid_request",
				"code":    "json_parse_error",
			},
		})
		return
	}

	clientModel := reqBody.Model
	isStream := reqBody.Stream

	// ── Build identity from request ────────────────────────────────────
	clientID := identity.BuildIdentityFromRequest(r, "default", nil, nil, "")
	identityHash := clientID.ShortID()

	// ── Resolve model name via control plane ──────────────────────────
	var modelResolution *resolve.Resolution
	if h.resolver != nil {
		modelResolution = h.resolver.Resolve(r.Context(), clientModel, clientID.Fingerprint.ClientProfile)
		slog.Debug("model resolved",
			"client_model", clientModel,
			"resolution_path", modelResolution.ResolutionPath,
			"raw_models", modelResolution.RawModels,
			"canonical", modelResolution.CanonicalName,
		)
	}

	// ── Resolve transform rules ────────────────────────────────────────
	tCtx := &transform.TransformContext{
		RequestMode:   "chat",
		ClientProfile: clientID.Fingerprint.ClientProfile,
		ClientModel:   clientModel,
	}
	if modelResolution != nil && modelResolution.CanonicalName != nil {
		tCtx.CanonicalName = *modelResolution.CanonicalName
	}
	var txResult *transform.TransformResult
	if h.matrix != nil {
		txResult = h.matrix.Resolve(tCtx)
	}
	explicitOutbound := ""
	if txResult != nil && txResult.OutboundModel != "" {
		explicitOutbound = transform.RenderOutboundModel(
			txResult.OutboundModel, txResult.OutboundModel, clientModel, tCtx.CanonicalName,
		)
	}

	// ── Replace model in request body if transformed ───────────────────
	if explicitOutbound != "" && explicitOutbound != clientModel {
		bodyBytes = ReplaceModelInRequestBody(bodyBytes, explicitOutbound)
	}

	// ── Audit event builder ────────────────────────────────────────────
	auditBuilder := newAuditEvent(r.Header.Get("X-Request-Id")).
		ClientModel(clientModel).
		OutboundModel(explicitOutbound).
		IdentityHash(identityHash).
		ClientProfile(clientID.Fingerprint.ClientProfile).
		Stream(isStream).
		Provider(svc.ProviderID).
		Credential(svc.CredentialID).
		RequestChecksum(bodyBytes)
	if modelResolution != nil {
		auditBuilder.ResolutionPath(modelResolution.ResolutionPath)
		if modelResolution.CanonicalName != nil {
			auditBuilder.CanonicalName(*modelResolution.CanonicalName)
		}
	}
	if txResult != nil {
		auditBuilder.TransformRule(txResult.MatchedRule)
	}
	defer func() {
		h.auditor.Emit(context.Background(), auditBuilder.Build())
	}()

	// ── Concurrency limiter ────────────────────────────────────────────
	release, err := h.limiter.AcquireAll(r.Context(), svc.ProviderID, svc.CredentialID, identityHash)
	if err != nil {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error": map[string]string{
				"message": "concurrency limit reached",
				"type":    "rate_limit_exceeded",
				"code":    "concurrency_limit",
			},
		})
		return
	}

	// ── Build upstream request ─────────────────────────────────────────
	ChatCompletionsPhase3(w, r, bodyBytes, isStream, clientModel, explicitOutbound, clientID, txResult, svc, h.circuit, h.limiter, h.pools, h.client, h.normalizer, auditBuilder, release)
}

//-----------------------------------------------------------------------------
// ChatCompletionsPhase3 — full pipeline with transform, pool, streaming
//-----------------------------------------------------------------------------

func ChatCompletionsPhase3(
	w http.ResponseWriter,
	r *http.Request,
	bodyBytes []byte,
	isStream bool,
	clientModel string,
	explicitOutbound string,
	clientID identity.ClientIdentity,
	txResult *transform.TransformResult,
	svc ServiceID,
	cm *circuit.Manager,
	lim *limiter.Limiter,
	pools *pool.PoolManager,
	upClient *upstreampkg.Client,
	norm *Normalizer,
	auditBuilder *audit.EventBuilder,
	release limiter.ReleaseFunc,
) {
	var released bool
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("panic in chat handler", "panic", rec)
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": map[string]string{
					"message": "internal server error",
					"type":    "server_error",
					"code":    "panic",
				},
			})
			if !released {
				release()
				released = true
			}
			cm.RecordFailure(svc.ProviderID, svc.CredentialID, circuit.KindTransient)
		}
	}()

	rid := r.Header.Get("X-Request-Id")
	slog.Info("chat completions",
		"request_id", rid,
		"client_model", clientModel,
		"outbound_model", explicitOutbound,
		"stream", isStream,
		"identity", clientID.ShortID(),
		"upstream", upstream.String(),
	)

	// ── Build upstream request ─────────────────────────────────────────
	upstreamReq, err := http.NewRequestWithContext(r.Context(), r.Method, upstream.String()+r.URL.Path, bytes.NewReader(bodyBytes))
	if err != nil {
		slog.Error("failed to create upstream request", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": map[string]string{
				"message": "failed to create upstream request",
				"type":    "server_error",
				"code":    "upstream_error",
			},
		})
		released = true
		release()
		cm.RecordFailure(svc.ProviderID, svc.CredentialID, circuit.KindTransient)
		return
	}

	// Copy only safe headers (H4: strip hop-by-hop and auth headers)
	safeHeaders := copySafeHeaders(r.Header)
	upstreamReq.Header = safeHeaders
	upstreamReq.Header.Set("X-Request-Id", rid)
	upstreamReq.Header.Set("Content-Type", "application/json")

	// ── Inject identity headers ────────────────────────────────────────
	upstreamReq.Header.Set("X-Virtual-Client-Id", clientID.VirtualClientID)
	upstreamReq.Header.Set("X-Virtual-IP", clientID.VirtualIP)
	upstreamReq.Header.Set("X-Virtual-MAC", clientID.VirtualMAC)

	// ── Apply transform header rules ───────────────────────────────────
	if txResult != nil {
		for _, h := range txResult.StripHeaders {
			upstreamReq.Header.Del(h)
		}
		for k, v := range txResult.InjectHeaders {
			upstreamReq.Header.Set(k, v)
		}
	}

	// ── Get HTTP client (identity-bound pool or default) ───────────────
	var httpClient *http.Client
	var reqPool *pool.Pool
	if pools != nil {
		poolKey := pool.PoolKey{
			IdentityHash: clientID.IdentityHash,
			ProviderID:   svc.ProviderID,
			CredentialID: svc.CredentialID,
		}
		p := pools.GetOrCreate(poolKey, "")
		if p != nil && p.State() == pool.PoolActive {
			reqPool = p
			httpClient = p.Client()
		}
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	} else if err := reqPool.Acquire(r.Context()); err != nil {
		writeErrorJSON(w, http.StatusTooManyRequests, r.Header.Get("X-Request-Id"), "connection pool busy", "rate_limit_exceeded", "pool_busy")
		return
	}
	if reqPool != nil {
		defer reqPool.Release()
	}

	timeout := UpstreamTimeout()
	if isStream {
		timeout = StreamTimeout()
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	upstreamReq = upstreamReq.WithContext(ctx)

	var resp *http.Response
	var uErr *upstreampkg.Error
	if upClient != nil {
		resp, uErr = upClient.Do(upstreamReq)
	} else {
		var doErr error
		resp, doErr = httpClient.Do(upstreamReq)
		if doErr != nil {
			uErr = &upstreampkg.Error{Kind: errorsx.ClassifyError(doErr, nil), Message: doErr.Error(), Err: doErr}
		}
	}

	if uErr != nil && (resp == nil || resp.StatusCode >= 500) {
		slog.Error("upstream request failed", "error", uErr)
		errKind := uErr.Kind
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": map[string]string{
				"message": "Provider service is currently unavailable. Please try again later.",
				"type":    "server_error",
				"code":    "provider_error",
			},
		})
		released = true
		release()
		if errKind == errorsx.KindCanceled {
			// Client disconnect — don't affect circuit breaker state
		} else if errKind == "" {
			// Empty kind means the error wasn't classified — don't silently
			// record as success. Only unretryable upstream 4xx with no
			// rate-limit/auth/quota semantics should be no-ops.
		} else {
			cm.RecordFailure(svc.ProviderID, svc.CredentialID, errKind)
			if errKind == circuit.KindRateLimit {
				lim.Shrink(svc.ProviderID, svc.CredentialID)
			}
		}
		return
	}

	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		body := make([]byte, 4096)
		n, _ := resp.Body.Read(body)
		errKind := errorsx.ClassifyError(nil, resp)

		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		if n > 0 {
			w.Write(body[:n])
		}

		released = true
		release()
		if resp.StatusCode >= 400 && resp.StatusCode < 500 &&
			resp.StatusCode != 429 && resp.StatusCode != 401 &&
			resp.StatusCode != 403 && resp.StatusCode != 402 {
			cm.RecordSuccess(svc.ProviderID, svc.CredentialID)
		} else {
			cm.RecordFailure(svc.ProviderID, svc.CredentialID, errKind)
			if errKind == circuit.KindRateLimit {
				lim.Shrink(svc.ProviderID, svc.CredentialID)
			}
		}
		return
	}

	// ── Success — proxy the response ───────────────────────────────────
	toolsRequested := requestHasTools(bodyBytes)
	if isStream {
		outcome := StreamChatWithCaptureAndToolFallback(w, resp, clientModel, explicitOutbound, norm, nil, toolsRequested)
		released = true
		release()
		if outcome.Interrupted && outcome.Reason != "client_cancel" {
			cm.RecordFailure(svc.ProviderID, svc.CredentialID, errorsx.KindStreamTimeout)
			auditBuilder.Success(false)
			slog.Warn("stream interrupted, recording failure",
				"provider_id", svc.ProviderID,
				"credential_id", svc.CredentialID,
				"reason", outcome.Reason,
			)
		} else {
			cm.RecordSuccess(svc.ProviderID, svc.CredentialID)
			auditBuilder.Success(true)
		}
	} else {
		defer resp.Body.Close()
		respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize+1))
		if err != nil {
			slog.Error("failed to read upstream response", "error", err)
			writeJSON(w, http.StatusBadGateway, map[string]any{
				"error": map[string]string{
					"message": "failed to read upstream response",
					"type":    "upstream_error",
					"code":    "read_error",
				},
			})
			release()
			released = true
			cm.RecordFailure(svc.ProviderID, svc.CredentialID, circuit.KindTransient)
			return
		}
		if len(respBody) > maxBodySize {
			slog.Warn("upstream response truncated", "size", len(respBody))
			respBody = respBody[:maxBodySize]
		}

		// Replace model in non-streaming response
		if clientModel != "" {
			respBody = ReplaceModelInResponseBody(respBody, clientModel)
		}

		respBody = coerceXMLToolCallsInChatResponse(respBody, toolsRequested)

		if norm != nil {
			respBody = norm.NormalizeChunk(respBody, false)
		}

		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		w.Write(respBody)
		released = true
		release()
		cm.RecordSuccess(svc.ProviderID, svc.CredentialID)
		auditBuilder.Success(true)
	}
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

// ChatCompletionsWithHooks proxies a chat completion request and calls the
// done callback with success/failure after the request completes.
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
	Status      string `json:"status"`
	Version     string `json:"version"`
	Circuit     any    `json:"circuit,omitempty"`
	Concurrency any    `json:"concurrency,omitempty"`
}

// HealthHandler returns health information including circuit breaker and limiter stats.
type HealthHandler struct {
	circuit *circuit.Manager
	limiter *limiter.Limiter
}

// NewHealthHandler creates a new health handler.
func NewHealthHandler(cm *circuit.Manager, l *limiter.Limiter) *HealthHandler {
	return &HealthHandler{circuit: cm, limiter: l}
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
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return ""
	}
	if strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	if strings.HasPrefix(auth, "bearer ") {
		return strings.TrimPrefix(auth, "bearer ")
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

func writeErrorJSON(w http.ResponseWriter, status int, requestID, msg, errType, code string) {
	w.Header().Set("Content-Type", "application/json")
	if requestID != "" {
		w.Header().Set("X-Request-Id", requestID)
	}
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{
			"message":    msg,
			"type":       errType,
			"code":       code,
			"request_id": requestID,
		},
	})
}
