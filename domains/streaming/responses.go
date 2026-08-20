package streaming

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/authentication" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"    //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"      //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/identity"            //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/session"             //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/streaming/state"     //nolint:depguard // SP-02 state machine wiring
	"github.com/kaixuan/llm-gateway-go/domains/transformation"      //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/i18n"
	"github.com/kaixuan/llm-gateway-go/modelname"
	"github.com/kaixuan/llm-gateway-go/resolve"
)

type responsesRequestBody struct {
	Model           string                     `json:"model"`
	Input           json.RawMessage            `json:"input"`
	Instructions    string                     `json:"instructions,omitempty"`
	MaxOutputTokens *int                       `json:"max_output_tokens,omitempty"`
	Stream          bool                       `json:"stream"`
	Temperature     *float64                   `json:"temperature,omitempty"`
	TopP            *float64                   `json:"top_p,omitempty"`
	Extra           map[string]json.RawMessage `json:"-"`
}

// responsesHasTools reports whether the request body declared a `tools`
// array (either in the top-level Extra map, or as part of an input
// item). Used to set ExecParams.ToolsRequested so the executor and
// streaming bridges know the client actually asked for tool calls.
func responsesHasTools(req *responsesRequestBody) bool {
	if req == nil {
		return false
	}
	if raw, ok := req.Extra["tools"]; ok && len(raw) > 0 && string(raw) != "null" {
		return true
	}
	if len(req.Input) == 0 {
		return false
	}
	var probe any
	if err := json.Unmarshal(req.Input, &probe); err != nil {
		return false
	}
	switch typed := probe.(type) {
	case []any:
		for _, item := range typed {
			if m, ok := item.(map[string]any); ok {
				if _, has := m["tools"]; has {
					return true
				}
			}
		}
	case map[string]any:
		if _, has := typed["tools"]; has {
			return true
		}
	}
	return false
}

func (r *responsesRequestBody) UnmarshalJSON(data []byte) error {
	type alias responsesRequestBody
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	delete(raw, "model")
	delete(raw, "input")
	delete(raw, "instructions")
	delete(raw, "max_output_tokens")
	delete(raw, "stream")
	delete(raw, "temperature")
	delete(raw, "top_p")
	*r = responsesRequestBody(decoded)
	r.Extra = raw
	return nil
}

type ResponsesHandler struct {
	chatHandler *ChatHandler
}

func NewResponsesHandler(ch *ChatHandler) *ResponsesHandler {
	return &ResponsesHandler{chatHandler: ch}
}

func (h *ResponsesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w, r, journeyWriter := beginRequestJourney(w, r, h.chatHandler)
	defer finishRequestJourney(r, journeyWriter)
	r = markExplicitStreamSession(r)
	defer func() { _ = r.Body.Close() }()

	var (
		attemptLoggedFlag   bool
		attemptKeyInfo      *authentication.KeyInfo
		attemptClientModel  string
		attemptErrCode      string
		attemptErrMsg       string
		attemptProviderID   *int
		attemptCredentialID *int
		attemptRequestBody  []byte
	)
	attemptLogged := &attemptLoggedFlag
	// 2026-06-26: ALWAYS generate a server-side UUID. The
	// RequestIDMiddleware already overwrites X-Request-Id with a fresh
	// value when the request enters the chain, so r.Header.Get reads
	// the server value here. The defensive uuid.NewString fallback
	// covers direct unit-test invocations that bypass the middleware.
	requestIdentity := initializeRequestIdentity(r)
	requestID := requestIdentity.RequestID
	clientRequestID := requestIdentity.ClientRequestID
	w.Header().Set("X-Request-Id", requestID)
	w.Header().Set("X-Gw-Session-Id", requestIdentity.SessionID)
	logCtx := h.chatHandler.NewRequestLogContext(r, requestID, time.Now())
	logCtx.ClientRequestID = clientRequestID
	startTime := logCtx.StartTime

	// SP-02: state machine — share the same entry point as chat completions.
	// requests that fail before any state transition still hit a terminal
	// state via the deferred cancel below.
	rt, _ := h.chatHandler.initRequestStateMachine(r.Context(), requestID, "")
	defer cancelRequestStateMachine(rt, nil)

	// Generate a provisional session ID for early-failure branches.
	// Declared before the deferred safety-net so the closure can capture it.
	provisionalSessionID := requestIdentity.SessionID
	if h.chatHandler.requestLogger != nil {
		if err := h.chatHandler.requestLogger.CreateInitial(r.Context(), &telemetry.InitialRequest{
			RequestID:   requestID,
			TenantID:    "default",
			SessionID:   provisionalSessionID,
			Provisional: true,
		}); err != nil {
			slog.Warn("request_logger: responses early CreateInitial failed", "request_id", requestID, "error", err)
		}
	}
	defer func() {
		// Ensure the safety-net logger always sees a non-empty
		// gw_session_id, even for pre-body-parse failures. Idempotent.
		applyProvisionalGatewaySessionHeader(r, provisionalSessionID)
		if rec := recover(); rec != nil {
			slog.Error("responses handler panic", "panic", rec, "request_id", requestID)
			attemptErrCode = "internal_panic"
			attemptErrMsg = "internal server error"
			if len(attemptRequestBody) == 0 {
				captureAttemptBody(r, &attemptRequestBody, &attemptClientModel)
			}
			if attemptClientModel == "" {
				attemptClientModel = "<unknown>"
			}
			latency := int(time.Since(startTime).Milliseconds())
			h.chatHandler.recordFailedRequestWithKey(requestID, attemptClientModel, "",
				attemptProviderID, attemptCredentialID,
				attemptErrCode, attemptErrMsg, latency, attemptRequestBody, attemptKeyInfo, r)
			if !*attemptLogged {
				writeResponsesError(w, http.StatusInternalServerError, "internal server error", "api_error", "internal_panic")
			}
			return
		}
		if attemptErrCode != "" && !*attemptLogged {
			latency := int(time.Since(startTime).Milliseconds())
			h.chatHandler.recordFailedRequestWithKey(requestID, attemptClientModel, "",
				attemptProviderID, attemptCredentialID,
				attemptErrCode, attemptErrMsg, latency, attemptRequestBody, attemptKeyInfo, r)
		}
	}()

	if r.Method != http.MethodPost {
		attemptErrCode = "method_not_allowed"
		attemptErrMsg = "method not allowed"
		writeResponsesError(w, http.StatusMethodNotAllowed, "Method not allowed", "invalid_request", "method_not_allowed")
		return
	}

	// A capture failure leaves attemptRequestBody holding a truncated prompt
	// that is still forwarded upstream, so it must not be swallowed.
	if err := ensureRequestBodyBuffered(r, &attemptRequestBody, &attemptClientModel); err != nil {
		h.chatHandler.recordDataLoss(r.Context(), AnomalyRequestBodyTruncated, string(SeverityHigh), requestID,
			"responses: request body capture failed: "+err.Error(),
			map[string]any{"captured_bytes": len(attemptRequestBody), "path": r.URL.Path})
	}

	var keyInfo *authentication.KeyInfo
	if h.chatHandler.keyVerifier != nil && h.chatHandler.keyVerifier.Enabled() {
		rawKey := extractBearerToken(r)
		if rawKey == "" {
			attemptErrCode = "missing_key"
			attemptErrMsg = "missing api key"
			captureAttemptBody(r, &attemptRequestBody, &attemptClientModel)
			writeResponsesError(w, http.StatusUnauthorized, "Missing API key", "authentication_error", "missing_key")
			return
		}
		ki, verifyErr := h.chatHandler.keyVerifier.Verify(r.Context(), rawKey)
		if verifyErr != nil {
			if _, ok := verifyErr.(*authentication.InvalidKeyError); ok {
				attemptErrCode = "invalid_key"
				attemptErrMsg = "invalid or expired api key"
				captureAttemptBody(r, &attemptRequestBody, &attemptClientModel)
				writeResponsesError(w, http.StatusUnauthorized, "Invalid or expired API key", "authentication_error", "invalid_key")
				return
			}
			attemptErrCode = "auth_unavailable"
			attemptErrMsg = "authentication service temporarily unavailable"
			captureAttemptBody(r, &attemptRequestBody, &attemptClientModel)
			slog.Warn("responses: key verification RPC failed", "error", verifyErr)
			writeResponsesError(w, http.StatusServiceUnavailable, "Authentication service temporarily unavailable", "api_error", "auth_unavailable")
			return
		}
		keyInfo = ki
		attemptKeyInfo = ki
		bindRequestJourney(r, ki.TenantID, attemptClientModel)
		// SP-02: state machine — auth succeeded.
		rt.Emit(state.EventAuthed)
	}

	if rlOutcome := checkGatewayRateLimit(keyInfo, h.chatHandler.rateLimiter); !rlOutcome.Skipped {
		writeRateLimitHeaders(w, rlOutcome)
		if rlOutcome.Blocked {
			attemptErrCode = "rate_limit_exceeded"
			attemptErrMsg = "rate limit exceeded"
			peeked, _ := io.ReadAll(io.LimitReader(r.Body, int64(maxBodySize)+1))
			if len(peeked) > maxBodySize {
				peeked = peeked[:maxBodySize]
			}
			if len(peeked) > 0 {
				attemptRequestBody = peeked
				if attemptClientModel == "" {
					attemptClientModel = extractModelFromBody(peeked)
				}
			}
			writeResponsesError(w, http.StatusTooManyRequests, "Rate limit exceeded", "rate_limit_exceeded", "rate_limit_exceeded")
			return
		}
	}

	bodyBytes, err := readRequestBody(r.Context(), r.Body, maxBodySize)
	if err != nil {
		if len(bodyBytes) > 0 {
			attemptRequestBody = bodyBytes
			if attemptClientModel == "" {
				attemptClientModel = extractModelFromBody(bodyBytes)
			}
		}
		attemptErrCode = "body_read_error"
		attemptErrMsg = fmt.Sprintf("failed to read request body: %v", err)
		slog.Warn("responses request body read failed",
			"request_id", requestID,
			"error", err,
			"content_length", r.ContentLength,
			"partial_bytes", len(bodyBytes),
			"client_model", attemptClientModel,
		)
		writeResponsesError(w, http.StatusBadRequest, "Failed to read request body", "invalid_request", "body_read_error")
		return
	}
	if len(bodyBytes) > 0 {
		attemptRequestBody = bodyBytes
	}
	if len(bodyBytes) > maxBodySize {
		attemptErrCode = "body_too_large"
		attemptErrMsg = "request body too large"
		writeResponsesError(w, http.StatusRequestEntityTooLarge, "Request body too large", "invalid_request", "body_too_large")
		return
	}

	var reqBody responsesRequestBody
	if err := json.Unmarshal(bodyBytes, &reqBody); err != nil {
		attemptErrCode = "json_parse_error"
		attemptErrMsg = "invalid JSON in request body"
		writeResponsesError(w, http.StatusBadRequest, "Invalid JSON in request body", "invalid_request", "json_parse_error")
		return
	}
	if reqBody.Model == "" {
		attemptErrCode = "missing_model"
		attemptErrMsg = "model is required"
		attemptClientModel = "<unknown>"
		writeResponsesError(w, http.StatusBadRequest, "model is required", "invalid_request", "missing_model")
		return
	}

	chatBody := convertResponsesToChatBody(&reqBody)
	chatBodyBytes, err := json.Marshal(chatBody)
	if err != nil {
		attemptErrCode = "conversion_error"
		attemptErrMsg = "internal conversion error"
		attemptClientModel = reqBody.Model
		writeResponsesError(w, http.StatusInternalServerError, "Internal conversion error", "server_error", "conversion_error")
		return
	}
	attemptClientModel = reqBody.Model
	requestedModel := reqBody.Model

	// model=auto: classify + rewrite before CanonicalizeClientModel.
	if reqBody.Model == autoRequestMagic {
		var apiKeyID int
		if keyInfo != nil {
			apiKeyID = keyInfo.ID
		}
		newBody, wire, shouldFail := h.maybeResolveAutoForResponses(&reqBody, bodyBytes, r, apiKeyID)
		if shouldFail {
			attemptErrCode = "auto_route_decider_failed"
			attemptErrMsg = "auto-route temporarily unavailable; pass an explicit model name and retry"
			writeResponsesError(w, http.StatusBadGateway,
				"auto-route temporarily unavailable; pass an explicit model name and retry",
				"server_error", "auto_route_decider_failed")
			return
		}
		bodyBytes = newBody
		attemptClientModel = reqBody.Model
		if wire != nil {
			writeAutoDecisionHeader(w, wire)
		}
	}

	// 2026-07-14: lowercase at the wire boundary.
	clientModel := modelname.CanonicalizeClientModel(reqBody.Model)
	resolveRequestJourney(r, tenant(keyInfo), requestedModel, clientModel)

	if keyInfo != nil {
		profile := clientProfileFromKey(keyInfo)
		denied, canonical, _ := enforceTenantModelPolicy(
			r.Context(), clientModel, keyInfo, h.chatHandler.modelPolicy, h.chatHandler.resolver, profile,
		)
		if denied {
			attemptErrCode = "model_forbidden"
			attemptErrMsg = fmt.Sprintf("Model '%s' is not available for your account", canonical)
			attemptClientModel = canonical
			applyProvisionalGatewaySessionHeader(r, provisionalSessionID)
			h.chatHandler.recordFailedRequestWithKey(requestID, canonical, "",
				nil, nil, attemptErrCode, attemptErrMsg, 0, bodyBytes, keyInfo, r)
			*attemptLogged = true
			writeResponsesError(w, http.StatusForbidden,
				fmt.Sprintf("Model '%s' is not available for your account", canonical),
				"permission_error", "model_forbidden")
			return
		}
	}

	isStream := reqBody.Stream

	// ── Session resolution (2026-06-29) ────────────────────────────
	// Priority: body > header > Redis Get > CreateV2 > provisional.
	sessionID := extractSessionIDFromRequest(r, bodyBytes)
	var sessionInfo *session.Session
	if sessionID == "" {
		assignment, assignErr := h.chatHandler.assignGatewaySession(r.Context(), bodyBytes, r, keyInfo, sessionID, sessionInfo, clientProfileFromKey(keyInfo))
		if assignErr != nil {
			attemptErrCode = "session_assignment_failed"
			attemptErrMsg = "failed to assign gateway session id"
			applyProvisionalGatewaySessionHeader(r, provisionalSessionID)
			h.chatHandler.recordFailedRequestWithKey(requestID, clientModel, "",
				nil, nil, attemptErrCode, attemptErrMsg, int(time.Since(startTime).Milliseconds()), bodyBytes, keyInfo, r)
			*attemptLogged = true
			writeResponsesError(w, http.StatusInternalServerError, "failed to assign session id", "internal_error", "session_assignment_failed")
			return
		}
		if assignment != nil && assignment.SessionID != "" {
			sessionID = assignment.SessionID
			sessionInfo = assignment.SessionInfo
			if assignment.Resumed {
				w.Header().Set("X-Gw-Session-Id-Resume", sessionID)
				w.Header().Set("X-Gw-Session-Reused", "true")
			}
			if assignment.AutoCreated {
				w.Header().Set("X-Gw-Session-Id-Resume", sessionID)
				w.Header().Set("X-Gw-Session-Auto", "true")
			}
		}
	} else {
		// Client provided session ID — resolve SessionInfo for context
		// propagation, but honor the client's ID even if Redis doesn't
		// know it yet (first request in a new session).
		if keyInfo != nil && h.chatHandler.sessionGetter != nil {
			if si, getErr := h.chatHandler.sessionGetter.Get(r.Context(), sessionID); getErr == nil && si != nil {
				sessionInfo = si
			}
		}
	}
	if sessionID == "" {
		sessionID = provisionalSessionID
	}
	r = applyResolvedGatewaySession(r, sessionID, sessionInfo)
	logCtx.SetSession(sessionInfo)
	if h.chatHandler.requestLogger != nil {
		tenantID := "default"
		if keyInfo != nil {
			tenantID = keyInfo.TenantID
		}
		if err := h.chatHandler.requestLogger.CreateInitial(r.Context(), &telemetry.InitialRequest{
			RequestID:   requestID,
			TenantID:    tenantID,
			SessionID:   sessionID,
			ClientModel: clientModel,
			Provisional: false,
		}); err != nil {
			slog.Warn("request_logger: responses session merge failed", "request_id", requestID, "error", err)
		}
	}
	// 2026-08-06 audit fix: extractEndUser only checks X-End-User-Id and
	// r.Body (which is already drained at this point). The original
	// request body bytes are in bodyBytes — pass them in so we recover
	// the OpenAI Responses "user" top-level field on the success path.
	endUser := resolveEndUser("", r, bodyBytes)
	clientID := identity.BuildIdentityFromRequest(r, tenant(keyInfo), appID(keyInfo), apiKeyIDPtr(keyInfo), clientProfileFromKey(keyInfo))

	auditBuilder := newAuditEvent(requestID).
		ClientModel(clientModel).
		IdentityHash(clientID.ShortID()).
		ClientProfile(clientID.Fingerprint.ClientProfile).
		Stream(isStream).
		RequestChecksum(bodyBytes)

	var streamCapture *audit.StreamCapture
	if isStream {
		streamCapture = h.chatHandler.newStreamCapture()
	}
	defer func() {
		if streamCapture != nil {
			auditBuilder.StreamMetrics(streamCapture)
		}
		h.chatHandler.auditor.Emit(r.Context(), auditBuilder.Build())
	}()

	// 2026-07-03: Bug #7 fix - pass tenantID from keyInfo
	tenantID := ""
	if keyInfo != nil {
		tenantID = keyInfo.TenantID
	}

	// ── SR-12 durable snapshot cut point (doc 18 §11.2) ────────────────
	var durableStream *DurableStreamBinding
	if h.chatHandler.durableStore != nil && DurableRequested(r, isStream) {
		in := DurableSnapshotInput{
			Protocol:       "openai-responses",
			Endpoint:       "/v1/responses",
			TenantID:       tenantID,
			ApplicationID:  appIDValue(keyInfo),
			APIKeyID:       apiKeyIDValue(keyInfo),
			SessionID:      sessionID,
			SessionSource:  deriveSessionSource(bodyBytes, r),
			ClientModel:    clientModel,
			Body:           bodyBytes,
			IdentityHash:   clientID.IdentityHash,
			ClientProfile:  clientID.Fingerprint.ClientProfile,
			RequestID:      requestID,
			ToolsRequested: responsesHasTools(&reqBody),
		}
		decision, binding := h.chatHandler.maybeStartDurable(w, r, in, isStream, true)
		if decision == durableHandled {
			return
		}
		durableStream = binding
	}

	candidates, policy, _, candErr := resolveCandidatesForRequest(r.Context(), h.chatHandler.provider, clientModel, clientID.Fingerprint.ClientProfile, tenantID, bodyBytes)
	// SP-02: state machine — routing produced a final candidate set.
	rt.Emit(state.EventRouted)
	if candErr != nil {
		// SP-02: routing error path.
		rt.Emit(state.EventFailed)
		// Database or infrastructure error - do NOT disguise as no_candidate
		slog.Error("failed to get candidates from provider", "error", candErr, "model", clientModel, "request_id", requestID)
		rc := classifyRoutingError(candErr)
		latency := int(time.Since(startTime).Milliseconds())
		h.chatHandler.recordFailedRequestWithKey(requestID, clientModel, "",
			nil, nil, rc.code, rc.message, latency, bodyBytes, keyInfo, r)
		*attemptLogged = true
		releaseDurableBeforeSurvival(durableStream, "candidate_resolution_failed")
		writeResponsesError(w, rc.httpStatus, rc.message, "server_error", rc.code)
		return
	}
	if len(candidates) == 0 {
		// Distinguish "model not recognized anywhere" (400 invalid_model) from
		// "model recognized but no routable provider right now" (503 no_candidate).
		// The ModelKnown probe is one cheap indexed query and saves an upstream
		// round-trip + a confusing 503 for typos in the client request.
		if !h.chatHandler.provider.ModelKnown(r.Context(), clientModel) {
			attemptErrCode = "invalid_model"
			attemptErrMsg = fmt.Sprintf("Model '%s' is not supported by this gateway", clientModel)
			latency := int(time.Since(startTime).Milliseconds())
			h.chatHandler.recordFailedRequestWithKey(requestID, clientModel, "",
				nil, nil, attemptErrCode, attemptErrMsg, latency, bodyBytes, keyInfo, r)
			*attemptLogged = true
			releaseDurableBeforeSurvival(durableStream, "invalid_model")
			writeResponsesError(w, http.StatusBadRequest, attemptErrMsg, "invalid_request_error", "invalid_model")
			return
		}
		// This is the real no_candidate case - no database error, just no matching providers
		attemptErrCode = "no_candidate"
		attemptErrMsg = fmt.Sprintf("No available provider for model '%s'", clientModel)
		latency := int(time.Since(startTime).Milliseconds())
		h.chatHandler.recordFailedRequestWithKey(requestID, clientModel, "",
			nil, nil, attemptErrCode, attemptErrMsg, latency, bodyBytes, keyInfo, r)
		*attemptLogged = true
		releaseDurableBeforeSurvival(durableStream, "no_candidate")
		writeResponsesError(w, http.StatusServiceUnavailable, attemptErrMsg, "server_error", "no_candidate")
		return
	}
	if len(candidates) > 0 {
		pid := candidates[0].ProviderID
		cid := candidates[0].CredentialID
		attemptProviderID = &pid
		attemptCredentialID = &cid
	}

	var modelResolution *resolve.Resolution
	if h.chatHandler.resolver != nil {
		modelResolution = h.chatHandler.resolver.Resolve(r.Context(), clientModel, clientID.Fingerprint.ClientProfile)
	}

	var txResult *transformation.TransformResult
	tCtx := &transformation.TransformContext{
		RequestMode:   "responses",
		ClientProfile: clientID.Fingerprint.ClientProfile,
		ClientModel:   clientModel,
	}
	if modelResolution != nil && modelResolution.CanonicalName != nil {
		tCtx.CanonicalName = *modelResolution.CanonicalName
	}
	if h.chatHandler.matrix != nil {
		txResult = h.chatHandler.matrix.Resolve(tCtx)
	}
	explicitOutbound := ""
	if len(candidates) > 0 {
		explicitOutbound = renderOutboundFromTransform(txResult, candidates[0], tCtx.CanonicalName)
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
	auditCtx := executors.AuditContextFromRequest(r, sessionInfo, bodyBytes, keyInfo)
	auditCtx.RequestID = requestID
	auditCtx.GWSessionID = gwSessionID
	auditCtx.GWTaskID = gwTaskID
	outboundForLog := explicitOutbound
	if len(candidates) > 0 {
		outboundForLog = outboundModelForLog(clientModel, explicitOutbound, candidates[0].RawModel)
	}
	h.chatHandler.recordInitialRequestLog(
		r.Context(),
		requestID, clientModel, outboundForLog, endUser, "responses", keyInfo,
		clientID.Fingerprint.ClientProfile, clientID.IdentityHash,
		attemptProviderID, attemptCredentialID, canonicalID,
		canonicalNameFromResolution(modelResolution), // 2026-07-27: 标准模型名 (migration 458)
		bodyBytes, txResult, egressProtocol, isStream,
		gwSessionID, gwTaskID,
		logCtx,
	)

	var preStream *preStreamKeepalive
	preStreamPrepared := false
	if isStream {
		cfg := currentStreamRuntimeConfig()
		if cfg.enablePreStreamKeepalive {
			if psk, ok := startPreStreamKeepalive(r.Context(), w, cfg.keepaliveInterval, requestID); ok {
				preStream = psk
				preStreamPrepared = true
				w = psk.Writer()
				defer psk.stop()
			}
		}
	}

	journeyInstanceID, journeySeq, journeyTerminal := requestJourneyExecState(r)
	dispatchAllowProviderChange, dispatchAllowModelChange, dispatchModelAlternatives := responsesDispatchOptions(
		logCtx, hasMultipleProviders(candidates),
	)
	buildExecParams := func(streamWriter http.ResponseWriter) *executors.ExecParams {
		return &executors.ExecParams{
			W:                          streamWriter,
			R:                          r,
			BodyBytes:                  chatBodyBytes,
			IsStream:                   isStream,
			StreamSurvivesClientCancel: explicitStreamSession(r.Context()),
			PreStreamPrepared:          preStreamPrepared,
			OnStreamReady:              func() {},
			OnStreamHeartbeat: func() error {
				if preStream != nil {
					return preStream.session.Heartbeat()
				}
				return nil
			},
			SuppressSuccessWrite: !isStream,
			// Phase E (2026-07-01): was incorrectly "openai-completions",
			// which caused executor_anthropic.go to use the Q3 OpenAI
			// translator (Anthropic→chat.completion.chunk) for /v1/responses
			// clients, instead of the IR-based Responses translator.
			ClientProtocol: "openai-responses",
			ClientModel:    clientModel,
			// See domains/streaming/handler.go for the rationale: the
			// executor resolves the upstream model per candidate, so we
			// pass clientModel here to avoid leaking the FIRST candidate's
			// outbound id into retry/failover attempts.
			OutboundModel:               clientModel,
			ClientID:                    clientID,
			Transform:                   txResult,
			Resolution:                  modelResolution,
			Candidates:                  candidates,
			Policy:                      policy,
			DispatchAllowProviderChange: dispatchAllowProviderChange,
			DispatchAllowModelChange:    dispatchAllowModelChange,
			DispatchModelAlternatives:   dispatchModelAlternatives,
			AuditBuilder:                auditBuilder,
			Capture:                     streamCapture,

			ToolsRequested: responsesHasTools(&reqBody),
			// StreamWrapper intentionally unset. The executor routes via
			// AnthropicToResponsesStream / OpenAIToResponsesStream based on
			// ClientProtocol + cand.Protocol — see executor_anthropic.go:StreamResponse
			// and executor_chat.go's Responses bridge block.
			StickyKey: buildRouteStickyKey(tenant(keyInfo), appID(keyInfo), apiKeyIDPtr(keyInfo), clientID.Fingerprint.ClientProfile),
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
			SessionID:                gwSessionID,
			Model:                    clientModel,
			TenantID:                 tenant(keyInfo),
			AppID:                    appID(keyInfo),
			ApiKeyID:                 apiKeyIDPtr(keyInfo),
			RequestID:                requestID,
			ClientRequestID:          auditCtx.ClientRequestID,
			GWTaskID:                 auditCtx.GWTaskID,
			ParentRequestID:          auditCtx.ParentRequestID,
			TraceID:                  auditCtx.TraceID,
			SpanID:                   auditCtx.SpanID,
			Audit:                    auditCtx,
			JourneyGatewayInstanceID: journeyInstanceID,
			JourneySeq:               journeySeq,
			JourneyTerminal:          journeyTerminal,
		}
	}

	usedSurvival := isStream && (durableStream != nil || h.chatHandler.survivalTenantAllowed != nil && h.chatHandler.survivalTenantAllowed(tenantID))
	var result *executors.ExecuteResult
	var execErr error
	if usedSurvival {
		base := w
		if h.chatHandler.responseInterceptor != nil {
			base = newInterceptingStreamWriter(w, h.chatHandler.responseInterceptor, r.Context(), response.StreamMeta{
				SessionID:   gwSessionID,
				RequestID:   requestID,
				TenantID:    tenantID,
				ClientModel: clientModel,
			})
			defer base.(*interceptingStreamWriter).finish()
		}
		// SP-02: state machine — survival branch dispatches upstream.
		rt.Emit(state.EventDispatching)
		result, execErr = h.chatHandler.runSurvivalCoordinator(r, base, buildExecParams, tenantID, durableStream)
	} else {
		if durableStream != nil {
			durableStream.Stop()
			slog.Error("durable stream escaped survival branch; failing closed",
				"request_id", requestID, "task_id", durableStream.task.ID)
			writeResponsesError(w, http.StatusServiceUnavailable, "durable request cannot run in-connection on this gateway", "api_error", "durable_survival_unavailable")
			return
		}
		// SP-02: state machine — executor has accepted the request.
		rt.Emit(state.EventDispatching)
		result, execErr = h.chatHandler.executor.Execute(buildExecParams(w))
	}

	if execErr != nil {
		// SP-02: state machine — executor failed (skip when the failure was a
		// client cancel, since r.Context() cancellation has already driven
		// the runtime to StateCancelled).
		if !errors.Is(r.Context().Err(), context.Canceled) {
			rt.Emit(state.EventFailed)
		}
		errCode := "provider_error"
		errMsg := execErr.Error()
		if ee, ok := execErr.(*executors.ExecuteError); ok && ee.Exhausted {
			errCode = "model_not_found"
			errMsg = "all providers unavailable"
		}
		attemptErrCode = errCode
		attemptErrMsg = errMsg
		latency := int(time.Since(startTime).Milliseconds())
		h.chatHandler.recordFailedRequestWithKey(requestID, clientModel, explicitOutbound,
			attemptProviderID, attemptCredentialID, errCode, errMsg, latency, chatBodyBytes, keyInfo, r)
		*attemptLogged = true
		if usedSurvival {
			return
		}
		if execErr, ok := execErr.(*executors.ExecuteError); ok && execErr.Exhausted {
			// Content moderation: render 400 with upstream reason + hint.
			if execErr.LastKind == errorsx.KindContentFilter {
				reason := extractUpstreamReason(execErr)
				msg := i18n.T(r.Context(), i18n.MsgContentFilter,
					map[string]any{"Reason": reason})
				h.chatHandler.recordFailedRequestWithKey(requestID, clientModel, explicitOutbound,
					attemptProviderID, attemptCredentialID, "content_filter", msg, latency, chatBodyBytes, keyInfo, r)
				if preStreamPrepared {
					writeResponsesStreamError(w, msg, "content_filter")
				} else {
					writeResponsesError(w, http.StatusBadRequest, msg, "content_filter", "content_filter")
				}
				return
			}
			if preStreamPrepared {
				writeResponsesStreamError(w, "All providers unavailable", "provider_unavailable")
			} else {
				writeResponsesError(w, http.StatusServiceUnavailable, "All providers unavailable", "server_error", "provider_unavailable")
			}
			return
		}
		if preStreamPrepared {
			writeResponsesStreamError(w, "Upstream request failed", "upstream_error")
		} else {
			writeResponsesError(w, http.StatusServiceUnavailable, "Upstream request failed", "server_error", "upstream_error")
		}
		return
	}
	if result != nil && result.CachedReplay {
		return
	}

	auditBuilder.Success(true).Latency(time.Duration(result.LatencyMs) * time.Millisecond)

	var responseBody []byte
	if !isStream {
		responseBody = h.writeNonStreamResponse(w, result.ResponseBody, clientModel, requestID)
	}

	h.chatHandler.emitTelemetry(auditBuilder.Build(), result, endUser, keyInfo, streamCapture, "responses", txResult, result.InboundBody, responseBody, logCtx)
	*attemptLogged = true
}

func responsesDispatchOptions(logCtx *RequestLogContext, allowProviderChange bool) (bool, bool, []string) {
	allowModelChange := logCtx != nil && logCtx.IsAutoRequest && dispatchAllowModelChangeEnabled()
	if !allowModelChange {
		return allowProviderChange, false, nil
	}
	return allowProviderChange, true, append([]string(nil), logCtx.AutoFallbackModels...)
}

func convertResponsesToChatBody(req *responsesRequestBody) map[string]any {
	chatBody := map[string]any{
		"model": req.Model,

		"stream": req.Stream,
	}

	var messages []any
	if req.Instructions != "" {
		messages = append(messages, map[string]any{"role": "system", "content": req.Instructions})
	}

	var rawInput = req.Input
	if len(rawInput) > 0 {
		switch rawInput[0] {
		case '"':
			var s string
			if json.Unmarshal(rawInput, &s) == nil {
				messages = append(messages, map[string]any{"role": "user", "content": s})
			}
		case '[':
			var items []map[string]any
			if json.Unmarshal(rawInput, &items) == nil {
				for _, item := range items {
					if message, ok := convertResponsesInputItem(item); ok {
						messages = append(messages, message)
						continue
					}
					// 2026-07-23 fix (audit a9ff405d...): unrecognised item
					// shape must NEVER produce an empty-content message.
					// Empty user/assistant content would be silently sent to
					// the upstream model and trigger the "no instruction"
					// symptom that prompted this audit.
					role, _ := item["role"].(string)
					if role == "" {
						role = "user"
					}
					content := normalizeItemContent(item["content"])
					if content == nil {
						slog.Warn("responses input item dropped: no usable content after conversion",
							"item_keys", mapKeys(item), "role", role)
						continue
					}
					messages = append(messages, map[string]any{"role": role, "content": content})
				}
			}
		}
	}
	chatBody["messages"] = messages

	if req.MaxOutputTokens != nil {
		chatBody["max_tokens"] = *req.MaxOutputTokens
	}
	if req.Temperature != nil {
		chatBody["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		chatBody["top_p"] = *req.TopP
	}

	for key, raw := range req.Extra {
		if len(raw) == 0 || string(raw) == "null" {
			continue
		}
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			continue
		}
		if key == "tools" {
			value = normalizeResponsesTools(value)
		}
		chatBody[key] = value
	}

	return chatBody
}

// convertResponsesInputItem preserves the tool-call chain when translating
// Responses API input items to Chat Completions messages. Dropping these
// fields leaves function_call_output without a matching function_call.
func convertResponsesInputItem(item map[string]any) (map[string]any, bool) {
	typ, _ := item["type"].(string)
	switch typ {
	case "function_call":
		id, _ := item["call_id"].(string)
		if id == "" {
			id, _ = item["id"].(string)
		}
		name, _ := item["name"].(string)
		arguments, _ := item["arguments"].(string)
		if id == "" || name == "" {
			return nil, false
		}
		return map[string]any{
			"role":    "assistant",
			"content": nil,
			"tool_calls": []any{map[string]any{
				"id":   id,
				"type": "function",
				"function": map[string]any{
					"name":      name,
					"arguments": arguments,
				},
			}},
		}, true
	case "function_call_output":
		id, _ := item["call_id"].(string)
		if id == "" {
			return nil, false
		}
		output := item["output"]
		if output == nil {
			output = ""
		}
		return map[string]any{
			"role":         "tool",
			"tool_call_id": id,
			"content":      output,
		}, true
	case "message", "input_text", "text":
		// 2026-07-23 fix (audit a9ff405d...): OpenAI Responses API input items
		// put user/assistant text under "text", not "content". Without this
		// branch the fallback silently dropped every Chinese (UTF-8) prompt
		// into {"role":"user","content":""} — upstream Claude saw an empty
		// user message and answered as if no instruction had been issued.
		text := extractItemText(item)
		if text == "" {
			return nil, false
		}
		role, _ := item["role"].(string)
		if role == "" {
			role = "user"
		}
		if role != "user" && role != "assistant" && role != "system" && role != "tool" {
			role = "user"
		}
		return map[string]any{"role": role, "content": text}, true
	case "input_image", "image_url":
		// Forward image input as Chat Completions image_url content part.
		// Anthropic adapter maps this through its own vision bridge.
		imageURL, _ := item["image_url"].(map[string]any)
		if imageURL == nil {
			if s, ok := item["image_url"].(string); ok {
				imageURL = map[string]any{"url": s}
			}
		}
		if imageURL == nil {
			return nil, false
		}
		return map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "image_url", "image_url": imageURL},
			},
		}, true
	case "input_audio":
		// 2026-07-23: explicit case for gpt-4o-audio-preview audio inputs.
		// Pass the audio block through to the upstream provider verbatim.
		if _, ok := item["input_audio"].(map[string]any); !ok {
			return nil, false
		}
		return map[string]any{
			"role":    "user",
			"content": []any{item},
		}, true
	case "input_file", "file":
		// File input is forwarded as a placeholder text reference; the
		// Anthropic adapter / attachment pipeline resolves the actual bytes
		// from the persisted storage using file_id / filename.
		fileID, _ := item["file_id"].(string)
		filename, _ := item["filename"].(string)
		if fileID == "" {
			if fileMap, ok := item["file"].(map[string]any); ok {
				fileID, _ = fileMap["file_id"].(string)
				if filename == "" {
					filename, _ = fileMap["filename"].(string)
				}
			}
		}
		if fileID == "" && filename == "" {
			return nil, false
		}
		ref := fileID
		if ref == "" {
			ref = filename
		}
		return map[string]any{
			"role":    "user",
			"content": fmt.Sprintf("[attached file: %s]", ref),
		}, true
	default:
		return nil, false
	}
}

// extractItemText reads text out of an input item in either Responses-API
// flat form ("text": "...") or wrapped content-array form
// ("content": [{"type":"input_text","text":"..."}, ...]).
// Returns "" when no usable text is found; caller must treat "" as "drop".
func extractItemText(item map[string]any) string {
	if s, ok := item["text"].(string); ok && s != "" {
		return s
	}
	raw, ok := item["content"]
	if !ok {
		return ""
	}
	switch v := raw.(type) {
	case string:
		return v
	case []any:
		// Concatenate text parts in order; preserves multi-block user messages.
		var b strings.Builder
		for _, part := range v {
			pm, ok := part.(map[string]any)
			if !ok {
				continue
			}
			ptype, _ := pm["type"].(string)
			if ptype != "" && ptype != "text" && ptype != "input_text" && ptype != "output_text" {
				continue
			}
			if s, ok := pm["text"].(string); ok && s != "" {
				b.WriteString(s)
			}
		}
		return b.String()
	}
	return ""
}

// normalizeItemContent returns nil when the input value would render as an
// empty message (nil, "", or an array containing no usable parts). Returning
// nil tells the caller to drop the item rather than emit {"content":""}.
//
// For non-text shapes (audio blocks, image_url, custom maps) the value is
// returned untouched so that endpoint-specific extension data (e.g.
// gpt-4o-audio-preview "input_audio" blocks) continues to flow through to
// the upstream provider unmodified.
func normalizeItemContent(v any) any {
	if v == nil {
		return nil
	}
	switch x := v.(type) {
	case string:
		if strings.TrimSpace(x) == "" {
			return nil
		}
		return x
	case []any:
		// Empty array → drop (audit invariant: no empty user messages).
		if len(x) == 0 {
			return nil
		}
		// When every part carries text we collapse to a single string,
		// matching the audio test "input_audio_type_passthrough" expectations
		// only when text content is the whole story.
		hasNonTextPart := false
		var b strings.Builder
		for _, part := range x {
			pm, ok := part.(map[string]any)
			if !ok {
				hasNonTextPart = true
				continue
			}
			ptype, _ := pm["type"].(string)
			if ptype != "" && ptype != "text" && ptype != "input_text" && ptype != "output_text" {
				hasNonTextPart = true
				continue
			}
			if s, ok := pm["text"].(string); ok && s != "" {
				b.WriteString(s)
			}
		}
		// Mixed content (text + audio/image/etc.) or all non-text: keep
		// the original array so upstream can render multimodal. This
		// preserves behaviour that audio/video tests depend on.
		if hasNonTextPart {
			return x
		}
		// Text-only array: collapse to a single string.
		if b.Len() == 0 {
			return nil
		}
		return b.String()
	}
	// Any other shape (bool/number/map): pass through. Endpoint-specific
	// blocks such as {"type":"input_audio","input_audio":{...}} arrive
	// here and must reach the upstream model — collapsing to "" would
	// silently drop audio inputs and break multimodal features.
	return v
}

// mapKeys returns the keys of a map for diagnostic logging.
func mapKeys(m map[string]any) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func normalizeResponsesTools(value any) any {
	tools, ok := value.([]any)
	if !ok {
		return value
	}

	normalized := make([]any, 0, len(tools))
	for _, item := range tools {
		tool, ok := item.(map[string]any)
		if !ok {
			normalized = append(normalized, item)
			continue
		}
		if _, exists := tool["function"]; exists {
			normalized = append(normalized, tool)
			continue
		}
		if toolType, _ := tool["type"].(string); toolType != "function" {
			normalized = append(normalized, tool)
			continue
		}

		function := map[string]any{}
		for _, key := range []string{"name", "description", "parameters", "strict"} {
			if val, exists := tool[key]; exists {
				function[key] = val
				delete(tool, key)
			}
		}
		if len(function) == 0 {
			normalized = append(normalized, tool)
			continue
		}
		tool["function"] = function
		normalized = append(normalized, tool)
	}

	return normalized
}

func (h *ResponsesHandler) writeNonStreamResponse(w http.ResponseWriter, body []byte, clientModel, requestID string) []byte {
	if len(body) == 0 {
		writeResponsesError(w, http.StatusInternalServerError, "Failed to read upstream response", "server_error", "upstream_read_error")
		return nil
	}

	format, empty := classifyNonStreamUpstreamResponse(body)
	if empty {
		writeResponsesError(w, http.StatusBadGateway, "模型未返回任何内容", "api_error", "empty_upstream_response")
		return nil
	}

	respBody := body
	if format != nonStreamResponseResponses {
		respBody = convertChatResponseToResponses(body, clientModel, requestID)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Request-Id", requestID)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(respBody)
	return respBody
}

func convertChatResponseToResponses(body []byte, clientModel, requestID string) []byte {
	var chatResp map[string]json.RawMessage
	if err := json.Unmarshal(body, &chatResp); err != nil {
		return body
	}

	var choices []map[string]any
	if raw, ok := chatResp["choices"]; ok {
		_ = json.Unmarshal(raw, &choices)
	}

	finishReason := "stop"
	textContent := ""
	reasoningContent := ""
	var toolCalls []map[string]any

	if len(choices) > 0 {
		choice := choices[0]
		if fr, ok := choice["finish_reason"].(string); ok {
			finishReason = fr
		}
		if msg, ok := choice["message"].(map[string]any); ok {
			if c, ok := msg["content"].(string); ok {
				textContent = c
			}
			if rc, ok := msg["reasoning_content"].(string); ok {
				reasoningContent = rc
			}
			if tc, ok := msg["tool_calls"].([]any); ok {
				for _, call := range tc {
					toolCall, ok := call.(map[string]any)
					if ok {
						toolCalls = append(toolCalls, toolCall)
					}
				}
			}
		}
	}

	status := "completed"
	if finishReason == "length" {
		status = "incomplete"
	}

	inputTokens, outputTokens, totalTokens := 0, 0, 0
	if raw, ok := chatResp["usage"]; ok {
		var usage map[string]any
		if json.Unmarshal(raw, &usage) == nil {
			if v, ok := usage["prompt_tokens"].(float64); ok {
				inputTokens = int(v)
			}
			if v, ok := usage["completion_tokens"].(float64); ok {
				outputTokens = int(v)
			}
			if v, ok := usage["total_tokens"].(float64); ok {
				totalTokens = int(v)
			}
		}
	}

	created := int(time.Now().Unix())
	if raw, ok := chatResp["created"]; ok {
		var v float64
		if json.Unmarshal(raw, &v) == nil {
			created = int(v)
		}
	}

	respID := "resp_"
	msgID := "msg_"
	if len(requestID) > 24 {
		respID += requestID[:24]
		msgID += requestID[8:24]
	} else {
		respID += requestID
		msgID += requestID
	}

	output := make([]map[string]any, 0, 2)
	if reasoningContent != "" {
		output = append(output, map[string]any{
			"type": "reasoning",
			"id":   msgID + "_reasoning",
			"summary": []map[string]any{{
				"type": "summary_text",
				"text": reasoningContent,
			}},
		})
	}
	if len(toolCalls) > 0 {
		for index, call := range toolCalls {
			function, _ := call["function"].(map[string]any)
			name, _ := function["name"].(string)
			arguments, _ := function["arguments"].(string)
			callID, _ := call["id"].(string)
			output = append(output, map[string]any{
				"type":      "function_call",
				"id":        fmt.Sprintf("%s_fc_%d", msgID, index),
				"call_id":   callID,
				"name":      name,
				"arguments": arguments,
				"status":    "completed",
			})
		}
	} else {
		output = append(output, map[string]any{
			"type":   "message",
			"id":     msgID,
			"status": status,
			"role":   "assistant",
			"content": []map[string]any{{
				"type":        "output_text",
				"text":        textContent,
				"annotations": []any{},
			}},
		})
	}

	resp := map[string]any{
		"id":         respID,
		"object":     "response",
		"created_at": created,
		"model":      clientModel,
		"status":     status,
		"output":     output,
		"usage": map[string]any{
			"input_tokens":  inputTokens,
			"output_tokens": outputTokens,
			"total_tokens":  totalTokens,
		},
		"x_request_id": requestID,
	}

	result, err := json.Marshal(resp)
	if err != nil {
		return body
	}
	return result
}

func writeResponsesStreamError(w http.ResponseWriter, message, code string) {
	payload, _ := json.Marshal(map[string]any{
		"type": "response.failed",
		"response": map[string]any{
			"error": map[string]any{"code": code, "message": message},
		},
	})
	_, _ = fmt.Fprintf(w, "event: response.failed\ndata: %s\n\n", payload)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func writeResponsesError(w http.ResponseWriter, statusCode int, message, errType, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    errType,
			"param":   "model",
			"code":    code,
		},
	})
}

func responsesStreamWrapper(requestID, clientModel, outboundModel string, capture *audit.StreamCapture) executors.StreamWrapperFunc { //nolint:unused
	return func(w http.ResponseWriter, resp *http.Response, norm executors.NormalizerFunc, cap *audit.StreamCapture) executors.StreamOutcome {
		c := cap
		if c == nil {
			c = capture
		}
		_ = norm
		return StreamResponsesSSE(w, resp, clientModel, outboundModel, requestID, c)
	}
}
