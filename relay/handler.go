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
	sessionGetter   interface {
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
	// GET probe — return 200 for client compatibility checks
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":  "ok",
			"message": "Chat completions endpoint is available. Use POST to send requests.",
		})
		return
	}

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
			// Auth RPC failed — fail-closed. Silently downgrading to anonymous would
			// bypass rate limiting, budget checks, and user isolation.
			slog.Error("key verification RPC failed, rejecting request", "error", verifyErr)
			writeErrorJSON(w, http.StatusServiceUnavailable, requestID,
				"Authentication service temporarily unavailable", "server_error", "auth_unavailable")
			return
		}
		keyInfo = ki
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
			go func() {
				// Use a bounded context so a slow DB/cache call cannot leak this
				// goroutine indefinitely.
				touchCtx, touchCancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer touchCancel()
				h.sessionGetter.Touch(touchCtx, sessionID)
			}()
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
	startTime := time.Now()

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
		h.recordFailedRequest(requestID, clientModel, "", nil, nil, "no_candidate",
			fmt.Sprintf("no available provider for model '%s'", clientModel),
			int(time.Since(startTime).Milliseconds()), bodyBytes)
		writeErrorJSON(w, http.StatusServiceUnavailable, requestID, fmt.Sprintf("no available provider for model '%s'", clientModel), "server_error", "no_candidate")
		return
	}
	if len(candidates) == 0 {
		h.recordFailedRequest(requestID, clientModel, "", nil, nil, "no_candidate",
			fmt.Sprintf("no available provider for model '%s'", clientModel),
			int(time.Since(startTime).Milliseconds()), bodyBytes)
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
		var providerID, credentialID *int
		if len(candidates) > 0 {
			providerID = intPtr(candidates[0].ProviderID)
			credentialID = intPtr(candidates[0].CredentialID)
		}
		if execErr, ok := execErr.(*routing.ExecuteError); ok && execErr.Exhausted {
			h.recordFailedRequest(requestID, clientModel, explicitOutbound, providerID, credentialID,
				"model_not_found",
				fmt.Sprintf("No available provider for model '%s'. All %d candidates failed.", clientModel, execErr.Tried),
				int(time.Since(startTime).Milliseconds()), bodyBytes)
			writeErrorJSON(w, http.StatusServiceUnavailable, requestID,
				fmt.Sprintf("No available provider for model '%s'. All %d candidates failed.", clientModel, execErr.Tried),
				"server_error", "model_not_found")
			return
		}
		h.recordFailedRequest(requestID, clientModel, explicitOutbound, providerID, credentialID,
			"provider_error", execErr.Error(),
			int(time.Since(startTime).Milliseconds()), bodyBytes)
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

	h.telemetryClient.EmitRequestLog(reqLog)
}

func (h *ChatHandler) recordFailedRequest(requestID, clientModel, outboundModel string, providerID, credentialID *int, errCode, errMessage string, latencyMs int, requestBody []byte) {
	if h.telemetryClient == nil || !h.telemetryClient.Enabled() {
		return
	}
	var requestBodyText *string
	if len(requestBody) > 0 {
		v := string(requestBody)
		requestBodyText = &v
	}
	latency := latencyMs
	reqLog := &telemetry.RequestLogEntry{
		RequestID:     requestID,
		TenantID:      "default",
		ClientModel:   strPtr(clientModel),
		OutboundModel: strPtr(outboundModel),
		ProviderID:    providerID,
		CredentialID:  credentialID,
		LatencyMs:     &latency,
		Success:       false,
		ErrorKind:     strPtr(errCode),
		RequestBody:   requestBodyText,
	}
	h.telemetryClient.EmitRequestLog(reqLog)
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
