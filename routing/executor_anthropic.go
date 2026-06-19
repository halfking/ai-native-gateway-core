package routing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/audit"
	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/internal/textsplit"
	"github.com/kaixuan/llm-gateway-go/internal/upstreamurl"
	"github.com/kaixuan/llm-gateway-go/pool"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/transform"
	upstreampkg "github.com/kaixuan/llm-gateway-go/upstream"
)

// AnthropicExecutor is the ProtocolHandler for Anthropic Messages API
// (and compatible endpoints like minimax /anthropic).
//
// CRITICAL INVARIANTS:
//   - MUST use x-api-key (NOT Authorization: Bearer) for auth
//   - MUST NOT inject stream_options (Anthropic doesn't have it)
//   - MUST NOT collapse tool history (Anthropic has native tool_use)
//   - MUST NOT call disguise (Anthropic field shapes differ)
//   - MUST NOT call XMLCoerce (Anthropic has native tool_use blocks)
//
// For passthrough mode (Q4), body bytes are forwarded unchanged.
// For conversion mode (Q3), the relay layer has already converted
// the body to Anthropic shape; this executor just sends it.
type AnthropicExecutor struct {
	Common *CommonExecutor
	// PassthroughStream is wired by the surrounding Executor at call
	// time and points at relay/anthropic_passthrough_stream.go's
	// StreamAnthropicPassthrough. Required for Q4 streaming — the
	// routing package cannot import relay (relay imports routing),
	// so the function is injected as a hook.
	PassthroughStream func(w http.ResponseWriter, resp *http.Response) StreamOutcome
	// OpenAITranslator converts Anthropic SSE upstream into OpenAI SSE
	// chunks for the Q3 path (openai client -> anthropic upstream).
	// When nil, the Q3 stream path falls back to PassthroughStream
	// (preserving the pre-fix behavior; the OpenAI client will fail to
	// parse the result, but a misconfig won't take the service down).
	OpenAITranslator func(w http.ResponseWriter, resp *http.Response, clientModel, outboundModel, requestID string, capture *audit.StreamCapture) StreamOutcome
	// ChatResponseConverter converts an Anthropic Messages JSON body
	// into an OpenAI chat.completion JSON body for the Q3 non-stream
	// path. When nil, the Q3 non-stream path falls back to passthrough
	// (Anthropic JSON body) which most OpenAI clients will reject.
	ChatResponseConverter func(body []byte, clientModel string) ([]byte, error)
	// ClientProtocol is the wire format the client used to send the
	// request. Empty defaults to "anthropic-messages" (Q4 passthrough).
	// "openai-completions" selects the Q3 conversion paths above.
	ClientProtocol string
	// 2026-06-19 quality fix mode (017_quality_fix_mode.sql):
	// QualityProcessNonStream is the per-provider tool_call quality
	// post-processor for the Q3 (openai client -> anthropic upstream)
	// non-stream response. The Anthropic → OpenAI converter
	// (ChatResponseConverter) produces an OpenAI-shaped body, so the
	// same OpenAI processor as the chat executor works here. Wired
	// from main.go (relay.WrapQualityProcessNonStream); nil ⇒ off mode.
	//
	// Q4 (anthropic passthrough) is left unprocessed for now: the
	// Anthropic Messages schema uses `tool_use.name` directly (no
	// nested `function.name`), and Anthropic SDK clients fail closed
	// on empty `tool_use.name` rather than degrading to a
	// user-friendly fallback. Adding a separate Anthropic-shape
	// processor is tracked in the deployment notes.
	QualityProcessNonStream QualityProcessNonStreamFunc
}

var _ ProtocolHandler = (*AnthropicExecutor)(nil)

const anthropicVersion = "2023-06-01"

func (a *AnthropicExecutor) BuildRequest(cand provider.Candidate, body []byte, isStream bool) (*http.Request, error) {
	upstreamURL := upstreamurl.MessagesURL(cand.BaseURL)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, upstreamURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", cand.APIKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	if isStream {
		req.Header.Set("Accept", "text/event-stream")
	}
	return req, nil
}

func (a *AnthropicExecutor) WriteNonStreamResponse(w http.ResponseWriter, resp *http.Response, clientModel, qualityFixMode string, qualitySignals *QualitySignals) error {
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	// Copy upstream response headers, but drop hop-by-hop / length headers
	// (Content-Length will be re-derived from the body bytes written, and
	// Content-Type/Connection/etc are set explicitly below). Copying
	// Content-Length verbatim produced a duplicate Content-Length line —
	// nginx rejects it as 'upstream sent duplicate header line' and the
	// client sees a 502 even though the gateway returned 200 internally.
	for k, vs := range resp.Header {
		if strings.EqualFold(k, "Content-Length") ||
			strings.EqualFold(k, "Content-Encoding") ||
			strings.EqualFold(k, "Connection") ||
			strings.EqualFold(k, "Transfer-Encoding") {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}

	// Q3 mode (openai client -> anthropic upstream): translate the
	// Anthropic Messages response body into an OpenAI chat.completion
	// body so the OpenAI parser doesn't choke on the shape mismatch.
	// Without this branch the OpenAI client receives a Messages-format
	// JSON body (with `content[].text` and `stop_reason`) and reports
	// "供应商错误" to the end-user.
	if a.ClientProtocol != "anthropic-messages" {
		if a.ChatResponseConverter != nil {
			converted, convErr := a.ChatResponseConverter(body, clientModel)
			if convErr == nil {
				body = converted
			} else {
				slog.Warn("anthropic_to_chat convert failed; forwarding raw body",
					"error", convErr, "request_id", clientModel)
			}
		}
		// 2026-06-19 quality fix mode (017_quality_fix_mode.sql).
		// After Anthropic → OpenAI conversion the body is OpenAI-shaped,
		// so the same quality processor used by ChatExecutor works here.
		// detect_only mode is byte-identical passthrough; fix mode
		// rewrites empty tool_calls[].function.name to
		// __unknown_tool_<i>__ so the OpenAI client doesn't fall into
		// the "Model tried to call unavailable tool ''" trap. The
		// resulting signals (flags, fix-actions, score) are written
		// back through the optional out-parameter so the executor
		// can stash them on ExecuteResult for emitTelemetry to
		// persist on the request_log row.
		if a.QualityProcessNonStream != nil && qualityFixMode != "" {
			newBody, flags, actions, score := a.QualityProcessNonStream(body, qualityFixMode)
			if newBody != nil {
				body = newBody
			}
			if qualitySignals != nil && (len(flags) > 0 || len(actions) > 0 || score != nil) {
				qualitySignals.Flags = flags
				qualitySignals.FixActions = actions
				qualitySignals.Score = score
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, err = w.Write(body)
		return err
	}

	// Q4 mode (anthropic client -> anthropic upstream): passthrough
	// with the existing think-tag split so SDK clients receive a
	// well-formed Anthropic Messages response.
	if clientModel != "" {
		body = replaceModelInResponseBody(body, clientModel)
	}

	// minimax-anthropic upstream packs the reasoning trace inside the
	// text block as `<think>...</think>` rather than emitting a separate
	// thinking block. Anthropic's wire protocol expects an independent
	// `thinking` block; split it so SDK clients render the trace separately
	// instead of receiving a single text blob the user can't tell apart from
	// the answer.
	body = splitEmbeddedThinkTags(body)
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, err = w.Write(body)
	return err
}

// splitEmbeddedThinkTags inspects an Anthropic Messages response body and,
// for each text block whose text contains a leading `<think>...</think>`
// segment, replaces that block with a [thinking, text] pair. Non-text
// blocks and text blocks without `<think>` are passed through unchanged.
//
// The split is conservative: only the FIRST complete `<think>...</think>`
// at the start of a text block is promoted to a thinking block. This avoids
// any false-positive rewrites of legitimate XML/HTML content elsewhere in
// the response. Bodies that fail to parse as Anthropic Messages JSON are
// returned unchanged.
func splitEmbeddedThinkTags(body []byte) []byte {
	var resp struct {
		Content []json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return body
	}
	if len(resp.Content) == 0 {
		return body
	}
	changed := false
	out := make([]json.RawMessage, 0, len(resp.Content))
	for _, raw := range resp.Content {
		var b struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(raw, &b); err != nil || b.Type != "text" {
			out = append(out, raw)
			continue
		}
		think, rest, ok := textsplit.SplitLeadingThink(b.Text)
		if !ok {
			out = append(out, raw)
			continue
		}
		changed = true
		tb, _ := json.Marshal(map[string]string{"type": "thinking", "thinking": think})
		out = append(out, tb)
		nb, _ := json.Marshal(map[string]string{"type": "text", "text": rest})
		out = append(out, nb)
	}
	if !changed {
		return body
	}
	var generic map[string]json.RawMessage
	if err := json.Unmarshal(body, &generic); err != nil {
		return body
	}
	arr, err := json.Marshal(out)
	if err != nil {
		return body
	}
	generic["content"] = arr
	out2, err := json.Marshal(generic)
	if err != nil {
		return body
	}
	return out2
}

// splitLeadingThinkBlock was moved to internal/textsplit/textsplit.go so the
// relay and routing packages can share one implementation without forming an
// import cycle (relay already imports routing).

func (a *AnthropicExecutor) StreamResponse(w http.ResponseWriter, resp *http.Response) StreamOutcome {
	// Q3 mode (openai client -> anthropic upstream): translate the
	// upstream Anthropic SSE stream into OpenAI SSE chunks so the
	// OpenAI parser receives data: {...} chunks instead of the raw
	// event: ... lines. Falls back to PassthroughStream if the
	// translator hook isn't wired (defensive: a misconfig shouldn't
	// take the service down).
	if a.ClientProtocol != "anthropic-messages" {
		if a.OpenAITranslator != nil {
			return a.OpenAITranslator(w, resp, "", "", "", nil)
		}
	}
	if a.PassthroughStream != nil {
		return a.PassthroughStream(w, resp)
	}
	return defaultAnthropicPassthrough(w, resp)
}

func (a *AnthropicExecutor) ExtractUsage(resp *http.Response, body []byte) (inputTokens, outputTokens *int) {
	return extractAnthropicUsageFromBody(body)
}

func (a *AnthropicExecutor) CheckSoftMismatch(reqModel, respModel string) (bool, string) {
	if reqModel == "" || respModel == "" {
		return false, ""
	}
	if !strings.EqualFold(reqModel, respModel) {
		return true, "anthropic_response_model_differs_from_request"
	}
	return false, ""
}

func extractAnthropicUsageFromBody(body []byte) (*int, *int) {
	var v struct {
		Usage struct {
			InputTokens  *int `json:"input_tokens"`
			OutputTokens *int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, nil
	}
	return v.Usage.InputTokens, v.Usage.OutputTokens
}

// defaultAnthropicPassthrough is the Q4 fallback when no PassthroughStream
// hook is wired: forward SSE bytes unchanged via io.Copy. The production
// implementation (with side-channel audit capture) lives in
// relay/anthropic_passthrough_stream.go and is injected via the
// PassthroughStream hook on AnthropicExecutor.
func defaultAnthropicPassthrough(w http.ResponseWriter, resp *http.Response) StreamOutcome {
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	_, _ = io.Copy(w, resp.Body)
	return StreamOutcome{}
}

// prepareAnthropicRequestBody applies client-side context trimming on the
// client's native wire format, then optionally converts OpenAI chat bodies
// to Anthropic Messages (Q3) and substitutes the outbound model name.
//
// Minimax and other anthropic-messages upstreams perform server-side sliding-
// window trim on direct calls; when traffic goes through the gateway we must
// trim here so proxy clients (OpenCode/Cursor/RooCode) behave like direct API
// users.
func (e *Executor) prepareAnthropicRequestBody(params *ExecParams, cand provider.Candidate, sourceBody []byte) ([]byte, error) {
	bodyBytes := append([]byte(nil), sourceBody...)

	if cand.ContextWindow != nil {
		if params.ClientProtocol == "anthropic-messages" {
			bodyBytes = transform.CompressAnthropicMessagesIfNeeded(bodyBytes, *cand.ContextWindow)
		} else {
			bodyBytes = transform.CompressMessagesIfNeeded(bodyBytes, *cand.ContextWindow)
		}
	}

	// Q3 conversion: OpenAI /v1/chat/completions → Anthropic /v1/messages.
	if params.ClientProtocol != "anthropic-messages" && params.ClientProtocol != "" {
		if e.ChatToAnthropic != nil {
			converted, err := e.ChatToAnthropic(bodyBytes)
			if err != nil {
				return nil, fmt.Errorf("convert chat body to anthropic: %w", err)
			}
			bodyBytes = converted
		}
	}

	outboundModel := resolveOutboundModel(params, cand)
	if outboundModel != params.ClientModel {
		bodyBytes = replaceModelInRequestBody(bodyBytes, outboundModel)
	}
	// MiniMax anthropic-messages rejects tools carrying OpenAI/custom type
	// wrappers (error 2013: invalid tool type). Always emit name/input_schema.
	if e.SanitizeAnthropicTools != nil {
		bodyBytes = e.SanitizeAnthropicTools(bodyBytes)
	}
	return bodyBytes, nil
}

// executeAnthropic is the Q3/Q4 (anthropic-messages upstream) path of
// the Executor. It wires the AnthropicExecutor protocol handler into
// the common pool / circuit / credential-state machinery and drives
// the per-attempt loop with retry-on-transient semantics.
//
// The function is structurally similar to executeOpenAI (in
// executor_chat.go) but with the OpenAI-specific transforms stripped:
//   - no stream_options injection
//   - no tool-history collapse
//   - no XMLCoerce
//   - no disguise
//   - no capability sanitizer
//
// The body bytes are forwarded as-is. For Q3 (chat -> anthropic
// upstream) the relay layer has already converted the body to Anthropic
// shape by the time this runs; for Q4 (anthropic -> anthropic) the
// bytes are unchanged from what the client sent.
func (e *Executor) executeAnthropic(
	params *ExecParams,
	cand provider.Candidate,
	maxRetries int,
	tTotal time.Time,
	fpLease *credentialfpslot.Lease,
) (*ExecuteResult, error) {
	sourceBody := append([]byte(nil), params.BodyBytes...)
	bodyBytes, err := e.prepareAnthropicRequestBody(params, cand, sourceBody)
	if err != nil {
		return nil, err
	}
	// Round 47 T-NEW-4 (audit fix A4): capture pre-request trim delta for
	// Anthropic path, mirroring executor_chat.go. prepareAnthropicRequestBody
	// calls CompressAnthropicMessagesIfNeeded internally, so any trim is
	// already reflected in len(bodyBytes) vs len(sourceBody).
	preTrimMeta := buildPreRequestTrimMeta(len(sourceBody), len(bodyBytes), cand.ContextWindow)

	outboundModel := resolveOutboundModel(params, cand)

	ae := &AnthropicExecutor{
		Common: &CommonExecutor{
			Circuit:              e.Circuit,
			Limiter:              e.Limiter,
			Pools:                e.Pools,
			State:                e.State,
			HeaderProfiles:       e.HeaderProfiles,
			Upstream:             e.Upstream,
			FpSlots:              e.FpSlots,
			UpstreamTimeout:      e.UpstreamTimeout,
			StreamTimeout:        e.StreamTimeout,
			StreamRetryThreshold: e.StreamRetryThreshold,
		},
		ClientProtocol: params.ClientProtocol,
		// 2026-06-19 quality fix mode (017_quality_fix_mode.sql): the
		// Q3 non-stream path runs the OpenAI-shaped quality processor
		// after the Anthropic → OpenAI conversion. Wired from
		// main.go (relay.WrapQualityProcessNonStream); nil ⇒ off mode.
		QualityProcessNonStream: e.QualityProcessNonStream,
	}
	if e.AnthropicPassthroughStream != nil {
		clientModel := params.ClientModel
		requestID := ""
		if params.R != nil {
			requestID = params.R.Header.Get("X-Request-Id")
		}
		ae.PassthroughStream = func(w http.ResponseWriter, resp *http.Response) StreamOutcome {
			return e.AnthropicPassthroughStream(w, resp, clientModel, outboundModel, requestID, params.Capture)
		}
	}
	if e.AnthropicToOpenAIStream != nil && params.ClientProtocol != "anthropic-messages" {
		clientModel := params.ClientModel
		requestID := ""
		if params.R != nil {
			requestID = params.R.Header.Get("X-Request-Id")
		}
		ae.OpenAITranslator = func(w http.ResponseWriter, resp *http.Response, _, _, _ string, _ *audit.StreamCapture) StreamOutcome {
			return e.AnthropicToOpenAIStream(w, resp, clientModel, outboundModel, requestID, params.Capture)
		}
	}
	if e.AnthropicToChatResponse != nil && params.ClientProtocol != "anthropic-messages" {
		ae.ChatResponseConverter = e.AnthropicToChatResponse
	}
	if e.AnthropicPassthroughStream != nil {
		clientModel := params.ClientModel
		requestID := ""
		if params.R != nil {
			requestID = params.R.Header.Get("X-Request-Id")
		}
		ae.PassthroughStream = func(w http.ResponseWriter, resp *http.Response) StreamOutcome {
			return e.AnthropicPassthroughStream(w, resp, clientModel, outboundModel, requestID, params.Capture)
		}
	}

	contextLenRecovery := contextLengthRecoveryState{}

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			delay := time.Duration(500*(1<<(attempt-1))) * time.Millisecond
			select {
			case <-params.R.Context().Done():
				return nil, params.R.Context().Err()
			case <-time.After(delay):
			}
		}

		result, tryErr := e.executeAnthropicOnce(params, cand, ae, bodyBytes, outboundModel, tTotal, fpLease)
		if tryErr == nil {
			// Memora persistence (fire-and-forget). Enqueue the request
			// conversation so L1 session memory accumulates facts for
			// later retrieval on context-overflow.
			e.enqueueMemoraWrite(params, sourceBody, result.ResponseBody)
			// Round 47 compression v7 T-NEW-2 + T-NEW-4 (audit fix A4):
			// surface the compression event captured during any 4xx retry
			// that preceded this successful leg, merged with pre-request
			// trim metadata. relay/handler.go emitTelemetry writes these
			// into request_logs.compression_*.
			if result.CompressionReason == nil && contextLenRecovery.lastReason != "" && contextLenRecovery.lastReason != "noop" {
				reason := contextLenRecovery.lastReason
				strategy := contextLenRecovery.lastStrategy
				result.CompressionReason = &reason
				result.CompressionStrategy = &strategy
			}
			// Always merge preTrimMeta (may be nil if no trim happened).
			result.CompressionMeta = mergeCompressionMeta(contextLenRecovery.lastMeta, preTrimMeta)
			return result, nil
		}
		if cle, ok := tryErr.(*contextLengthHTTPError); ok {
			switch e.handleContextLengthRecovery(params.R.Context(), params, cand, &sourceBody, &contextLenRecovery, cle.status) {
			case ctxLenRetry:
				bodyBytes, err = e.prepareAnthropicRequestBody(params, cand, sourceBody)
				if err != nil {
					return nil, err
				}
				continue
			case ctxLenGiveUp:
				// Return a typed error so the outer Execute loop knows
				// this is a context-length exhaustion (model-size
				// limit, not a credential fault) and can route the
				// failover accordingly without recording a circuit
				// failure or disabling the model offer.
				return nil, &contextLengthExhaustedError{
					credentialID: cand.CredentialID,
					rawModel:     cand.RawModel,
					status:       cle.status,
					body:         string(cle.body[:min(len(cle.body), 200)]),
				}
			}
		}
		if _, ok := tryErr.(*retryableError); !ok {
			return nil, tryErr
		}
	}
	return nil, fmt.Errorf("exhausted %d retries for credential %d", maxRetries, cand.CredentialID)
}

// executeAnthropicOnce is a single-attempt Anthropic upstream call.
// Returns either:
//   - (*ExecuteResult, nil) on 2xx success
//   - (*ExecuteResult, nil) for benign stream EOF (stream path)
//   - (*ExecuteResult, &streamInterruptedError{...}) for stream interruption
//   - (nil, &retryableError{...}) to signal the outer loop to try again
//   - (nil, &modelNotFoundError{...}) to signal credential failover
//   - (nil, error) for any other fatal error
func (e *Executor) executeAnthropicOnce(
	params *ExecParams,
	cand provider.Candidate,
	ae *AnthropicExecutor,
	bodyBytes []byte,
	outboundModel string,
	tTotal time.Time,
	fpLease *credentialfpslot.Lease,
) (*ExecuteResult, error) {
	req, err := ae.BuildRequest(cand, bodyBytes, params.IsStream)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Request-Id", params.R.Header.Get("X-Request-Id"))
	// Track C C2 audit fix 3.1: propagate session headers (same
	// rationale as executor_chat.go).
	if sid := params.R.Header.Get("X-Gw-Session-Id"); sid != "" {
		req.Header.Set("X-Gw-Session-Id", sid)
	}
	if sid := params.R.Header.Get("X-Session-Id"); sid != "" {
		req.Header.Set("X-Session-Id", sid)
	}
	if fpLease != nil && fpLease.Egress != nil {
		credentialfpslot.ApplyEgressHeaders(req.Header, fpLease.Egress)
	} else {
		req.Header.Set("X-Virtual-Client-Id", params.ClientID.VirtualClientID)
		req.Header.Set("X-Virtual-IP", params.ClientID.VirtualIP)
		req.Header.Set("X-Virtual-MAC", params.ClientID.VirtualMAC)
	}

	// Apply disguise headers (User-Agent / Accept-Language rotation).
	if e.DisguisePool != nil {
		for k, v := range e.DisguisePool.Headers() {
			req.Header.Set(k, v)
		}
		e.DisguisePool.MaybeRotate()
	}

	var httpClient *http.Client
	var reqPool *pool.Pool
	if e.Pools != nil {
		poolKey := pool.PoolKey{
			IdentityHash: params.ClientID.IdentityHash,
			ProviderID:   cand.ProviderID,
			CredentialID: cand.CredentialID,
		}
		if p := e.Pools.GetOrCreate(poolKey, ""); p != nil && p.State() == pool.PoolActive {
			reqPool = p
			httpClient = p.Client()
		}
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	} else if err := reqPool.Acquire(params.R.Context()); err != nil {
		return nil, err
	} else {
		defer reqPool.Release()
	}

	timeout := e.UpstreamTimeout
	if params.IsStream {
		timeout = e.StreamTimeout
	}
	ctx, cancel := context.WithTimeout(params.R.Context(), timeout)
	defer cancel()
	req = req.WithContext(ctx)

	if e.Upstream != nil {
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(bodyBytes)), nil
		}
	}

	reqStart := time.Now()
	var resp *http.Response
	var uErr *upstreampkg.Error
	if e.Upstream != nil {
		resp, uErr = e.Upstream.Do(req)
	} else {
		var doErr error
		resp, doErr = httpClient.Do(req)
		if doErr != nil {
			uErr = &upstreampkg.Error{Kind: errorsx.ClassifyError(doErr, nil), Message: doErr.Error(), Err: doErr}
		}
	}
	upstreamLatency := time.Since(reqStart)

	if uErr != nil && (resp == nil || resp.StatusCode >= 500) {
		errKind := uErr.Kind
		if errKind == errorsx.KindRateLimit {
			e.Limiter.Shrink(cand.ProviderID, cand.CredentialID)
		}
		return nil, &retryableError{err: uErr}
	}

	if resp != nil && resp.StatusCode >= 400 {
		defer resp.Body.Close()
		body := make([]byte, 4096)
		n, _ := resp.Body.Read(body)
		_, _ = io.Copy(io.Discard, resp.Body)
		errKind := errorsx.ClassifyErrorWithBody(resp.StatusCode, body[:n])

		if bodyKind := errorsx.ClassifyResponseBody(resp.StatusCode, body[:n]); bodyKind == errorsx.KindModelNotFound {
			// Step 4 (2026-06-18): removed the 10-second slow-upstream
			// reclassification. See executor_chat.go for the rationale.
			slog.Info("model_not_found skip offer",
				"credential_id", cand.CredentialID,
				"model", cand.RawModel,
				"status", resp.StatusCode,
				"upstream_latency_ms", upstreamLatency.Milliseconds(),
				"body_preview", string(body[:min(n, 120)]),
			)
			return nil, &modelNotFoundError{
				credentialID: cand.CredentialID,
				rawModel:     cand.RawModel,
				body:         string(body[:n]),
			}
		}

		if resp.StatusCode >= 400 && resp.StatusCode < 500 &&
			resp.StatusCode != 429 && resp.StatusCode != 401 &&
			resp.StatusCode != 403 && resp.StatusCode != 402 &&
			errKind != errorsx.KindConcurrent {
			if !errorsx.IsClientBug(errKind) {
				e.Circuit.RecordSuccess(cand.ProviderID, cand.CredentialID)
			}
		} else if errKind == errorsx.KindRateLimit {
			e.Limiter.Shrink(cand.ProviderID, cand.CredentialID)
		} else if errKind == errorsx.KindConcurrent {
			e.Circuit.RecordFailure(cand.ProviderID, cand.CredentialID, errorsx.KindConcurrent)
			e.writeCredentialStateOnError(params.R.Context(), cand.CredentialID, errorsx.KindConcurrent,
				fmt.Errorf("upstream %d concurrent overload: %s", resp.StatusCode, string(body[:min(n, 200)])))
		}
		if !errorsx.IsRetryable(errKind) {
			if errorsx.IsContextLength(errKind) || shouldHeuristicCompact(resp.StatusCode, errKind, len(bodyBytes), cand.ContextWindow) {
				return nil, &contextLengthHTTPError{
					status:  resp.StatusCode,
					body:    append([]byte(nil), body[:n]...),
					headers: resp.Header.Clone(),
				}
			}
			for k, vs := range resp.Header {
				for _, v := range vs {
					params.W.Header().Add(k, v)
				}
			}
			params.W.WriteHeader(resp.StatusCode)
			if n > 0 {
				params.W.Write(body[:n])
			}
			return nil, fmt.Errorf("upstream %d", resp.StatusCode)
		}
		return nil, &retryableError{err: fmt.Errorf("upstream %d", resp.StatusCode)}
	}

	e.Circuit.RecordSuccess(cand.ProviderID, cand.CredentialID)
	latencyMs := int(time.Since(tTotal).Milliseconds())

	if params.IsStream {
		outcome := ae.StreamResponse(params.W, resp)
		if outcome.Interrupted && outcome.Reason != "client_cancel" {
			streamKind := errorsx.KindStreamTimeout
			if errorsx.IsConcurrentOverload(outcome.Reason) {
				streamKind = errorsx.KindConcurrent
			}
			isResumable := outcome.Resumable && outcome.ChunkCount < e.StreamRetryThreshold
			isBenignEOF := outcome.Reason == "eof_without_done" && outcome.ChunkCount > 0

			if !isBenignEOF && isResumable {
				e.Circuit.RecordFailure(cand.ProviderID, cand.CredentialID, streamKind)
			} else if !isBenignEOF {
				e.Circuit.RecordFailure(cand.ProviderID, cand.CredentialID, streamKind)
			}
			return &ExecuteResult{
				Response:    resp,
				Candidate:   cand,
				LatencyMs:   latencyMs,
				RequestBody: append([]byte(nil), bodyBytes...),
			}, &streamInterruptedError{reason: outcome.Reason, credentialID: cand.CredentialID, resumable: isResumable, kind: streamKind}
		}
		return &ExecuteResult{
			Response:    resp,
			Candidate:   cand,
			LatencyMs:   latencyMs,
			RequestBody: append([]byte(nil), bodyBytes...),
		}, nil
	}

	var qualitySignals QualitySignals
	if err := ae.WriteNonStreamResponse(params.W, resp, params.ClientModel, cand.QualityFixMode, &qualitySignals); err != nil {
		return nil, err
	}
	return &ExecuteResult{
		Response:    resp,
		Candidate:   cand,
		LatencyMs:   latencyMs,
		RequestBody: append([]byte(nil), bodyBytes...),
		// 2026-06-19 quality fix mode (017_quality_fix_mode.sql):
		// Q3 (openai client -> anthropic upstream) non-stream
		// signals come back from the AnthropicExecutor's hook so
		// they get persisted on the request_log row, just like the
		// ChatExecutor Q1/Q2 path.
		QualityFlags:      qualitySignals.Flags,
		QualityFixActions: qualitySignals.FixActions,
		QualityScore:      qualitySignals.Score,
	}, nil
}
