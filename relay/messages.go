package relay

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/kaixuan/llm-gateway-go/audit"
	"github.com/kaixuan/llm-gateway-go/auth"
	"github.com/kaixuan/llm-gateway-go/identity"
	"github.com/kaixuan/llm-gateway-go/internal/textsplit"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/resolve"
	"github.com/kaixuan/llm-gateway-go/routing"
	"github.com/kaixuan/llm-gateway-go/sessions"
	"github.com/kaixuan/llm-gateway-go/transform"
)

type messagesRequestBody struct {
	Model         string          `json:"model"`
	Messages      json.RawMessage `json:"messages"`
	MaxTokens     int             `json:"max_tokens"`
	System        string          `json:"system,omitempty"`
	Stream        bool            `json:"stream"`
	Temperature   *float64        `json:"temperature,omitempty"`
	TopP          *float64        `json:"top_p,omitempty"`
	TopK          *int            `json:"top_k,omitempty"`
	StopSequences json.RawMessage `json:"stop_sequences,omitempty"`
	Tools         json.RawMessage `json:"tools,omitempty"`
	ToolChoice    json.RawMessage `json:"tool_choice,omitempty"`
	Metadata      *anthropicMeta  `json:"metadata,omitempty"`
}

type anthropicMeta struct {
	UserID string `json:"user_id,omitempty"`
}

type MessagesHandler struct {
	chatHandler *ChatHandler
}

func NewMessagesHandler(ch *ChatHandler) *MessagesHandler {
	return &MessagesHandler{chatHandler: ch}
}

func (h *MessagesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	// ── requestAttempt safety-net: every Anthropic /v1/messages
	//    request must produce exactly one request_logs row.  See
	//    ChatHandler.ServeHTTP for the rationale.  attemptLogged
	//    and the attempt-state fields are shared with the inner
	//    blocks via Go's closure capture (pointer-typed for slices
	//    / strings so writes propagate back).
	var (
		attemptLoggedFlag   bool
		attemptKeyInfo     *auth.KeyInfo
		attemptClientModel string
		attemptErrCode     string
		attemptErrMsg      string
		attemptProviderID  *int
		attemptCredentialID *int
		attemptRequestBody []byte
	)
	attemptLogged := &attemptLoggedFlag
	requestID := r.Header.Get("X-Request-Id")
	if requestID == "" {
		requestID = uuid.NewString()
		w.Header().Set("X-Request-Id", requestID)
	}
	startTime := time.Now()
	_, _ = &attemptErrCode, &attemptErrMsg
	defer func() {
		// Panic recovery: catch any panic from the inner pipeline and
		// emit a safety-net request_logs row before bubbling up.
		if rec := recover(); rec != nil {
			slog.Error("messages handler panic", "panic", rec, "request_id", requestID)
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
				writeAnthropicError(w, http.StatusInternalServerError, "api_error", "internal server error")
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
		// 2026-06-20 audit fix: capture the body + model so the
		// request_logs row records what the client actually sent
		// (not just "method not allowed"). The deferred safety
		// net at the top of ServeHTTP will read these closure
		// variables and emit a fully-populated row when the
		// inner function returns.
		captureAttemptBody(r, &attemptRequestBody, &attemptClientModel)
		if attemptClientModel == "" {
			attemptClientModel = "<unknown>"
		}
		writeAnthropicError(w, http.StatusMethodNotAllowed, "invalid_request", "Method not allowed")
		return
	}

	_ = ensureRequestBodyBuffered(r, &attemptRequestBody, &attemptClientModel)

	var keyInfo *auth.KeyInfo
	if h.chatHandler.keyVerifier != nil && h.chatHandler.keyVerifier.Enabled() {
		rawKey := extractBearerToken(r)
		if rawKey == "" {
			attemptErrCode = "missing_key"
			attemptErrMsg = "missing api key"
			captureAttemptBody(r, &attemptRequestBody, &attemptClientModel)
			writeAnthropicError(w, http.StatusUnauthorized, "authentication_error", "Missing API key")
			return
		}
		ki, verifyErr := h.chatHandler.keyVerifier.Verify(r.Context(), rawKey)
		if verifyErr != nil {
			if _, ok := verifyErr.(*auth.InvalidKeyError); ok {
				attemptErrCode = "invalid_key"
				attemptErrMsg = "invalid or expired api key"
				captureAttemptBody(r, &attemptRequestBody, &attemptClientModel)
				writeAnthropicError(w, http.StatusUnauthorized, "authentication_error", "Invalid or expired API key")
				return
			}
			attemptErrCode = "auth_unavailable"
			attemptErrMsg = "authentication service temporarily unavailable"
			captureAttemptBody(r, &attemptRequestBody, &attemptClientModel)
			slog.Warn("messages: key verification RPC failed", "error", verifyErr)
			writeAnthropicError(w, http.StatusServiceUnavailable, "api_error", "Authentication service temporarily unavailable")
			return
		} else {
			keyInfo = ki
			attemptKeyInfo = ki
		}
	}

	if rlOutcome := checkGatewayRateLimit(keyInfo, h.chatHandler.rateLimiter); !rlOutcome.Skipped {
		writeRateLimitHeaders(w, rlOutcome)
		if rlOutcome.Blocked {
			attemptErrCode = "rate_limit_exceeded"
			attemptErrMsg = "rate limit exceeded"
			// Peek the body so the safety-net can recover client_model
			// and request preview from the rejected request. Body is read
			// lazily so non-rate-limited requests are not penalised.
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
			writeAnthropicError(w, 529, "rate_limit_error", "Rate limit exceeded. Please wait and retry.")
			return
		}
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, int64(maxBodySize)+1))
	if err != nil {
		if len(bodyBytes) > 0 {
			attemptRequestBody = bodyBytes
			if attemptClientModel == "" {
				attemptClientModel = extractModelFromBody(bodyBytes)
			}
		}
		attemptErrCode = "body_read_error"
		attemptErrMsg = fmt.Sprintf("failed to read request body: %v", err)
		slog.Warn("anthropic request body read failed",
			"request_id", requestID,
			"error", err,
			"content_length", r.ContentLength,
			"partial_bytes", len(bodyBytes),
			"client_model", attemptClientModel,
		)
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request", "Failed to read request body")
		return
	}
	if len(bodyBytes) > 0 {
		attemptRequestBody = bodyBytes
	}
	if len(bodyBytes) > maxBodySize {
		attemptErrCode = "body_too_large"
		attemptErrMsg = "request body too large"
		// 2026-06-20 audit fix: extract model from the (truncated) body
		if attemptClientModel == "" {
			attemptClientModel = extractModelFromBody(bodyBytes[:maxBodySize])
		}
		if attemptClientModel == "" {
			attemptClientModel = "<unknown>"
		}
		writeAnthropicError(w, http.StatusRequestEntityTooLarge, "invalid_request", "Request body too large")
		return
	}

	var reqBody messagesRequestBody
	if err := json.Unmarshal(bodyBytes, &reqBody); err != nil {
		attemptErrCode = "json_parse_error"
		attemptErrMsg = "invalid JSON in request body"
		// 2026-06-20 audit fix: extract model from the invalid JSON (best effort)
		if attemptClientModel == "" {
			attemptClientModel = extractModelFromBody(bodyBytes)
		}
		if attemptClientModel == "" {
			attemptClientModel = "<unknown>"
		}
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request", "Invalid JSON in request body")
		return
	}
	if reqBody.Model == "" {
		attemptErrCode = "missing_model"
		attemptErrMsg = "model is required"
		attemptClientModel = "<unknown>"
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request", "model is required")
		return
	}
	if reqBody.MaxTokens <= 0 {
		attemptErrCode = "missing_max_tokens"
		attemptErrMsg = "max_tokens must be > 0"
		attemptClientModel = reqBody.Model
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request", "max_tokens must be > 0")
		return
	}

	// Q2 vs Q4 dispatch happens after candidate selection below:
	// if the first candidate's upstream speaks Anthropic, the
	// original Anthropic body bytes are forwarded unchanged (Q4
	// passthrough). Otherwise the body is converted to OpenAI chat
	// format (Q2 path). We precompute the converted body here so
	// the conversion-error path stays in one place; the Q4 path
	// discards it later via selectUpstreamBodyBytes.
	chatBody := convertToChatBody(&reqBody)
	chatBodyBytes, err := json.Marshal(chatBody)
	if err != nil {
		attemptErrCode = "conversion_error"
		attemptErrMsg = "internal conversion error"
		attemptClientModel = reqBody.Model
		writeAnthropicError(w, http.StatusInternalServerError, "api_error", "Internal conversion error")
		return
	}
	attemptClientModel = reqBody.Model

	clientModel := reqBody.Model
	isStream := reqBody.Stream
	sessionID := r.Header.Get("X-Gw-Session-Id")
	if sessionID == "" {
		sessionID = r.Header.Get("X-Session-Id")
	}
	var endUser string
	if reqBody.Metadata != nil && reqBody.Metadata.UserID != "" {
		endUser = reqBody.Metadata.UserID
	} else {
		endUser = extractEndUser(r)
	}
	clientID := identity.BuildIdentityFromRequest(r, tenant(keyInfo), appID(keyInfo), apiKeyIDPtr(keyInfo), clientProfileFromKey(keyInfo))

	auditBuilder := newAuditEvent(requestID).
		ClientModel(clientModel).
		IdentityHash(clientID.ShortID()).
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
		h.chatHandler.auditor.Emit(r.Context(), auditBuilder.Build())
	}()

candidates, policy, candErr := h.chatHandler.provider.GetCandidates(r.Context(), clientModel, clientID.Fingerprint.ClientProfile)
	if candErr != nil || len(candidates) == 0 {
		attemptErrCode = "no_candidate"
		attemptErrMsg = fmt.Sprintf("no available provider for model '%s'", clientModel)
		latency := int(time.Since(startTime).Milliseconds())
		h.chatHandler.recordFailedRequestWithKey(requestID, clientModel, "",
			nil, nil, attemptErrCode, attemptErrMsg, latency, bodyBytes, keyInfo, r)
		*attemptLogged = true
		writeAnthropicError(w, http.StatusServiceUnavailable, "overloaded_error", fmt.Sprintf("No available provider for model '%s'", clientModel))
		return
	}
	if len(candidates) > 0 {
		pid := candidates[0].ProviderID
		cid := candidates[0].CredentialID
		attemptProviderID = &pid
		attemptCredentialID = &cid
	}

	// Q2 vs Q4 dispatch: when the chosen upstream candidate speaks
	// Anthropic, forward the original body bytes unchanged. Otherwise
	// use the pre-converted OpenAI chat body. The pre-conversion
	// above is wasted work in the Q4 case but it lets us keep the
	// conversion-error path in one place; for the Q4 fast-path the
	// cost is a single in-memory json.Marshal on a small body.
	upstreamBody := selectUpstreamBodyBytes(candidates, bodyBytes, chatBodyBytes)

	var modelResolution *resolve.Resolution
	if h.chatHandler.resolver != nil {
		modelResolution = h.chatHandler.resolver.Resolve(r.Context(), clientModel, clientID.Fingerprint.ClientProfile)
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
	gwSessionID, gwTaskID := gwSessionTaskFromRequest(r, sessions.SessionFromContext(r.Context()))
	outboundForLog := explicitOutbound
	if len(candidates) > 0 {
		outboundForLog = outboundModelForLog(clientModel, explicitOutbound, candidates[0].RawModel)
	}
	h.chatHandler.recordInitialRequestLog(
		requestID, clientModel, outboundForLog, endUser, "messages", keyInfo,
		clientID.Fingerprint.ClientProfile, clientID.IdentityHash,
		attemptProviderID, attemptCredentialID, canonicalID,
		bodyBytes, txResult, egressProtocol, isStream,
		gwSessionID, gwTaskID,
		nil,
	)

	result, execErr := h.chatHandler.executor.Execute(&routing.ExecParams{
		W:              w,
		R:              r,
		BodyBytes:      upstreamBody,
		IsStream:       isStream,
		SuppressSuccessWrite: !isStream,
		ClientProtocol: "anthropic-messages",
		ClientModel:    clientModel,
		OutboundModel:  outboundForLog,
		ClientID:       clientID,
		Transform:      txResult,
		Resolution:     modelResolution,
		Candidates:    candidates,
		Policy:         policy,
		AuditBuilder:   auditBuilder,
		Capture:        streamCapture,
		ToolsRequested: false,
		StreamWrapper: anthropicStreamWrapper(requestID, clientModel, explicitOutbound, streamCapture),
		StickyKey:      buildRouteStickyKey(tenant(keyInfo), appID(keyInfo), apiKeyIDPtr(keyInfo), clientID.Fingerprint.ClientProfile, sessionID, endUser, clientID.Fingerprint.PrimarySeed(), clientModel),
		KeyID:            func() int { if keyInfo != nil { return keyInfo.ID }; return 0 }(),
		KeyConcurrentLimit: func() int { if keyInfo != nil { return keyInfo.EffectiveConcurrent() }; return 0 }(),
	})

	if execErr != nil {
		errCode := "provider_error"
		errMsg := execErr.Error()
		if ee, ok := execErr.(*routing.ExecuteError); ok && ee.Exhausted {
			errCode = "model_not_found"
			errMsg = "all providers unavailable"
		}
		attemptErrCode = errCode
		attemptErrMsg = errMsg
		latency := int(time.Since(startTime).Milliseconds())
		h.chatHandler.recordFailedRequestWithKey(requestID, clientModel, explicitOutbound,
			attemptProviderID, attemptCredentialID, errCode, errMsg, latency, upstreamBody, keyInfo, r)
		*attemptLogged = true
		if execErr, ok := execErr.(*routing.ExecuteError); ok && execErr.Exhausted {
			writeAnthropicError(w, http.StatusServiceUnavailable, "overloaded_error", "All providers unavailable")
			return
		}
		writeAnthropicError(w, http.StatusServiceUnavailable, "overloaded_error", "Upstream request failed")
		return
	}

	auditBuilder.Success(true).Latency(time.Duration(result.LatencyMs) * time.Millisecond)

	var responseBody []byte
	if !isStream {
		responseBody = h.writeNonStreamResponse(w, result.ResponseBody, clientModel, requestID)
	}

	h.chatHandler.emitTelemetry(auditBuilder.Build(), result, endUser, keyInfo, streamCapture, "messages", txResult, result.RequestBody, responseBody, nil)
	*attemptLogged = true
}

func convertToChatBody(req *messagesRequestBody) map[string]any {
	chatBody := map[string]any{
		"model":      req.Model,
		"max_tokens": req.MaxTokens,
		"stream":     req.Stream,
	}

	var messages []any
	if req.System != "" {
		messages = append(messages, map[string]any{"role": "system", "content": req.System})
	}

	var rawMessages []json.RawMessage
	if len(req.Messages) > 0 {
		if err := json.Unmarshal(req.Messages, &rawMessages); err == nil {
			for _, raw := range rawMessages {
				var msg map[string]any
				if json.Unmarshal(raw, &msg) == nil {
					converted := convertAnthropicMessage(msg)
					messages = append(messages, converted)
				}
			}
		}
	}
	chatBody["messages"] = messages

	if req.Temperature != nil {
		chatBody["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		chatBody["top_p"] = *req.TopP
	}
	if len(req.StopSequences) > 0 && string(req.StopSequences) != "null" {
		var stops []string
		if json.Unmarshal(req.StopSequences, &stops) == nil && len(stops) > 0 {
			chatBody["stop"] = stops
		}
	}

	if len(req.Tools) > 0 && string(req.Tools) != "null" {
		var tools []any
		if json.Unmarshal(req.Tools, &tools) == nil {
			openaiTools := convertAnthropicTools(tools)
			if len(openaiTools) > 0 {
				chatBody["tools"] = openaiTools
			}
		}
	}
	if len(req.ToolChoice) > 0 && string(req.ToolChoice) != "null" {
		chatBody["tool_choice"] = convertAnthropicToolChoice(req.ToolChoice)
	}
	if req.Metadata != nil && req.Metadata.UserID != "" {
		chatBody["user"] = req.Metadata.UserID
	}

	return chatBody
}

// ConvertAnthropicBodyToOpenAI converts an Anthropic Messages-format
// request body to OpenAI chat completions format. Used as a callback
// by the executor when a client speaking the Anthropic protocol
// needs to be routed to an OpenAI-completions upstream candidate.
func ConvertAnthropicBodyToOpenAI(bodyBytes []byte) ([]byte, error) {
	var reqBody messagesRequestBody
	if err := json.Unmarshal(bodyBytes, &reqBody); err != nil {
		return nil, fmt.Errorf("parse anthropic body: %w", err)
	}
	chatBody := convertToChatBody(&reqBody)
	return json.Marshal(chatBody)
}

// selectUpstreamBodyBytes is kept for backward compatibility but now
// always returns originalBody. The Q2/Q4 dispatch has moved into the
// executor (per-candidate) to avoid the candidates[0] mismatch bug.
func selectUpstreamBodyBytes(candidates []provider.Candidate, originalBody, convertedBody []byte) []byte {
	return originalBody
}

func convertAnthropicMessage(msg map[string]any) map[string]any {
	role, _ := msg["role"].(string)
	content := msg["content"]

	switch role {
	case "user", "assistant":
	default:
		return msg
	}

	switch c := content.(type) {
	case string:
		return map[string]any{"role": role, "content": c}
	case []any:
		return convertBlockMessage(role, c)
	default:
		return map[string]any{"role": role, "content": fmt.Sprint(c)}
	}
}

func convertBlockMessage(role string, blocks []any) map[string]any {
	var textParts []string
	var toolCalls []map[string]any
	var passthrough []map[string]any

	for _, b := range blocks {
		block, ok := b.(map[string]any)
		if !ok {
			textParts = append(textParts, fmt.Sprint(b))
			continue
		}
		blockType, _ := block["type"].(string)
		switch blockType {
		case "text":
			if text, ok := block["text"].(string); ok && text != "" {
				textParts = append(textParts, text)
			}
		case "tool_use":
			args, _ := block["input"]
			argsJSON, _ := json.Marshal(args)
			toolCalls = append(toolCalls, map[string]any{
				"id":   block["id"],
				"type": "function",
				"function": map[string]any{
					"name":      block["name"],
					"arguments": string(argsJSON),
				},
			})
		case "tool_result":
			toolUseID, _ := block["tool_use_id"].(string)
			if toolUseID == "" {
				toolUseID, _ = block["id"].(string)
			}
			content := extractBlockText(block["content"])
			return map[string]any{
				"role":         "tool",
				"tool_call_id": toolUseID,
				"content":      content,
			}
		case "image":
			source, _ := block["source"].(map[string]any)
			if source != nil {
				if imgPart := convertImageBlock(source); imgPart != nil {
					return map[string]any{
						"role":    "user",
						"content": []any{imgPart},
					}
				}
			}
		default:
			passthrough = append(passthrough, block)
		}
	}

	if len(toolCalls) > 0 {
		result := map[string]any{
			"role":       role,
			"tool_calls": toolCalls,
		}
		if len(textParts) > 0 {
			result["content"] = strings.Join(textParts, "\n")
		}
		return result
	}

	if len(passthrough) > 0 {
		return map[string]any{"role": role, "content": passthrough}
	}
	return map[string]any{"role": role, "content": strings.Join(textParts, "\n")}
}

func extractBlockText(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var parts []string
		for _, item := range c {
			if m, ok := item.(map[string]any); ok {
				if t, ok := m["text"].(string); ok && t != "" {
					parts = append(parts, t)
				} else {
					b, _ := json.Marshal(m)
					parts = append(parts, string(b))
				}
			} else {
				parts = append(parts, fmt.Sprint(item))
			}
		}
		return strings.Join(parts, "\n")
	case nil:
		return ""
	default:
		return fmt.Sprint(c)
	}
}

func convertImageBlock(source map[string]any) map[string]any {
	srcType, _ := source["type"].(string)
	switch srcType {
	case "base64":
		mediaType, _ := source["media_type"].(string)
		if mediaType == "" {
			mediaType = "image/png"
		}
		data, _ := source["data"].(string)
		url := fmt.Sprintf("data:%s;base64,%s", mediaType, data)
		return map[string]any{
			"type":      "image_url",
			"image_url": map[string]any{"url": url},
		}
	case "url":
		url, _ := source["url"].(string)
		return map[string]any{
			"type":      "image_url",
			"image_url": map[string]any{"url": url},
		}
	}
	return nil
}

func convertAnthropicTools(tools []any) []map[string]any {
	var result []map[string]any
	for _, t := range tools {
		tool, ok := t.(map[string]any)
		if !ok {
			continue
		}
		fn := map[string]any{"name": tool["name"]}
		if desc, ok := tool["description"].(string); ok && desc != "" {
			fn["description"] = desc
		}
		if schema, ok := tool["input_schema"]; ok {
			fn["parameters"] = schema
		}
		result = append(result, map[string]any{
			"type":     "function",
			"function": fn,
		})
	}
	return result
}

func convertAnthropicToolChoice(raw json.RawMessage) any {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	switch tc := v.(type) {
	case string:
		return tc
	case map[string]any:
		typ, _ := tc["type"].(string)
		switch typ {
		case "auto":
			return "auto"
		case "any":
			return "required"
		case "none":
			return "none"
		case "tool":
			name, _ := tc["name"].(string)
			return map[string]any{
				"type":     "function",
				"function": map[string]any{"name": name},
			}
		}
	}
	return v
}

func (h *MessagesHandler) writeNonStreamResponse(w http.ResponseWriter, body []byte, clientModel, requestID string) []byte {
	if len(body) == 0 {
		writeAnthropicError(w, http.StatusInternalServerError, "api_error", "Failed to read upstream response")
		return nil
	}

	anthropicBody := convertChatResponseToAnthropic(body, clientModel, requestID)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Request-Id", requestID)
	w.WriteHeader(http.StatusOK)
	w.Write(anthropicBody)
	return anthropicBody
}

func convertChatResponseToAnthropic(body []byte, clientModel, requestID string) []byte {
	var chatResp map[string]json.RawMessage
	if err := json.Unmarshal(body, &chatResp); err != nil {
		return body
	}

	var choices []map[string]any
	if raw, ok := chatResp["choices"]; ok {
		json.Unmarshal(raw, &choices)
	}

	finishReason := "stop"
	textContent := ""
	reasoningContent := ""
	var toolCalls []map[string]any

	if len(choices) > 0 {
		choice := choices[0]
		if fr, ok := choice["finish_reason"].(string); ok {
			finishReason = mapAnthropicStopReason(fr)
		}
		if msg, ok := choice["message"].(map[string]any); ok {
			if rc, ok := msg["reasoning_content"].(string); ok {
				reasoningContent = rc
			}
			if c, ok := msg["content"].(string); ok {
				textContent = c
			}
			if tc, ok := msg["tool_calls"].([]any); ok {
				for _, call := range tc {
					if cm, ok := call.(map[string]any); ok {
						fn, _ := cm["function"].(map[string]any)
						argsStr, _ := fn["arguments"].(string)
						var args any
						if json.Unmarshal([]byte(argsStr), &args) != nil {
							args = map[string]any{}
						}
						toolCalls = append(toolCalls, map[string]any{
							"type":  "tool_use",
							"id":    cm["id"],
							"name":  fn["name"],
							"input": args,
						})
					}
				}
			}
		}
	}

	contentBlocks := []map[string]any{}
	// Phase 2: OpenAI `reasoning_content` field → independent thinking block.
	// Phase 4 (non-stream variant): if the upstream packed reasoning
	// into the `content` field as `<think>...</think>` (typical of
	// minimax OpenAI), split it here so SDK clients see the trace
	// separately from the visible answer.
	if reasoningContent != "" {
		contentBlocks = append(contentBlocks, map[string]any{
			"type":     "thinking",
			"thinking": reasoningContent,
		})
	} else if think, rest, ok := textsplit.SplitLeadingThink(textContent); ok {
		contentBlocks = append(contentBlocks, map[string]any{
			"type":     "thinking",
			"thinking": think,
		})
		textContent = rest
	}
	if textContent != "" {
		contentBlocks = append(contentBlocks, map[string]any{
			"type": "text",
			"text": textContent,
		})
	}
	for _, tc := range toolCalls {
		contentBlocks = append(contentBlocks, tc)
	}
	if len(contentBlocks) == 0 {
		contentBlocks = append(contentBlocks, map[string]any{"type": "text", "text": ""})
	}

	inputTokens := 0
	outputTokens := 0
	if raw, ok := chatResp["usage"]; ok {
		var usage map[string]any
		if json.Unmarshal(raw, &usage) == nil {
			if v, ok := usage["prompt_tokens"].(float64); ok {
				inputTokens = int(v)
			}
			if v, ok := usage["completion_tokens"].(float64); ok {
				outputTokens = int(v)
			}
		}
	}

	msgID := "msg_" + requestID
	if len(requestID) > 24 {
		msgID = "msg_" + requestID[:24]
	}

	resp := map[string]any{
		"id":            msgID,
		"type":          "message",
		"role":          "assistant",
		"content":       contentBlocks,
		"model":         clientModel,
		"stop_reason":   finishReason,
		"stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens":  inputTokens,
			"output_tokens": outputTokens,
		},
	}

	result, err := json.Marshal(resp)
	if err != nil {
		return body
	}
	return result
}

func mapAnthropicStopReason(finishReason string) string {
	switch strings.ToLower(finishReason) {
	case "stop", "end_turn":
		return "end_turn"
	case "tool_calls", "function_call":
		return "tool_use"
	case "length", "max_tokens":
		return "max_tokens"
	case "content_filter":
		return "end_turn"
	default:
		return "end_turn"
	}
}

func writeAnthropicError(w http.ResponseWriter, statusCode int, errType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(map[string]any{
		"type":  "error",
		"error": map[string]any{"type": errType, "message": message},
	})
}

func anthropicStreamWrapper(requestID, clientModel, outboundModel string, capture *audit.StreamCapture) routing.StreamWrapperFunc {
	return func(w http.ResponseWriter, resp *http.Response, norm routing.NormalizerFunc, cap *audit.StreamCapture) routing.StreamOutcome {
		c := cap
		if c == nil {
			c = capture
		}
		return StreamAnthropicSSE(w, resp, clientModel, outboundModel, requestID, c)
	}
}

func extractEndUser(r *http.Request) string {
	if v := r.Header.Get("X-End-User-Id"); v != "" {
		return v
	}
	return "anonymous"
}

func tenant(ki *auth.KeyInfo) string {	if ki != nil {
		return ki.TenantID
	}
	return "default"
}

func appID(ki *auth.KeyInfo) *int {
	if ki != nil {
		return &ki.ApplicationID
	}
	return nil
}

func apiKeyIDPtr(ki *auth.KeyInfo) *int {
	if ki != nil {
		return &ki.ID
	}
	return nil
}

func buildRouteStickyKey(tenantID string, appID, apiKeyID *int, clientProfile, sessionID, endUser, fpSeed, clientModel string) string {
	if sessionID != "" {
		// Include client model so mid-session model switches get a fresh sticky
		// bucket instead of reusing the credential pinned by the first model.
		model := strings.TrimSpace(strings.ToLower(clientModel))
		if model == "" {
			model = "default"
		}
		scopedSession := sessionID + ":" + model
		return routing.BuildSessionStickyKey(tenantID, appID, apiKeyID, clientProfile, scopedSession)
	}
	return routing.BuildStickyKey(tenantID, appID, apiKeyID, endUser, fpSeed)
}
