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

func (a *AnthropicExecutor) WriteNonStreamResponse(w http.ResponseWriter, resp *http.Response, clientModel string) error {
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
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

	contextTrimRetried := false

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
			return result, nil
		}
		if cle, ok := tryErr.(*contextLengthHTTPError); ok && !contextTrimRetried && cand.ContextWindow != nil {
			contextTrimRetried = true
			mechanicalFn := func(b []byte) []byte {
				if params.ClientProtocol == "anthropic-messages" {
					return transform.CompressAnthropicMessagesIfNeeded(b, *cand.ContextWindow)
				}
				return transform.CompressMessagesIfNeeded(b, *cand.ContextWindow)
			}
			newSource, progressed := e.applyMechanicalThenLLMCompaction(
				params.R.Context(), params, cand, sourceBody, mechanicalFn,
			)
			if progressed {
				sourceBody = newSource
				slog.Info("context_length 4xx → anthropic compaction retry",
					"credential_id", cand.CredentialID,
					"model", cand.RawModel,
					"context_window", *cand.ContextWindow,
					"status", cle.status,
					"source_bytes", len(sourceBody),
				)
				bodyBytes, err = e.prepareAnthropicRequestBody(params, cand, sourceBody)
				if err != nil {
					return nil, err
				}
				continue
			}
			// Mechanical trim + LLM compaction made no progress — bubble the original 4xx up.
			for k, vs := range cle.headers {
				for _, v := range vs {
					params.W.Header().Add(k, v)
				}
			}
			params.W.WriteHeader(cle.status)
			if len(cle.body) > 0 {
				params.W.Write(cle.body)
			}
			return nil, fmt.Errorf("upstream %d", cle.status)
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
	if fpLease != nil && fpLease.Egress != nil {
		credentialfpslot.ApplyEgressHeaders(req.Header, fpLease.Egress)
	} else {
		req.Header.Set("X-Virtual-Client-Id", params.ClientID.VirtualClientID)
		req.Header.Set("X-Virtual-IP", params.ClientID.VirtualIP)
		req.Header.Set("X-Virtual-MAC", params.ClientID.VirtualMAC)
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

		if bodyKind := errorsx.ClassifyResponseBody(body[:n]); bodyKind == errorsx.KindModelNotFound {
			slog.Info("model_not_found skip offer",
				"credential_id", cand.CredentialID,
				"model", cand.RawModel,
				"status", resp.StatusCode,
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
			if errorsx.IsContextLength(errKind) {
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

	if err := ae.WriteNonStreamResponse(params.W, resp, params.ClientModel); err != nil {
		return nil, err
	}
	return &ExecuteResult{
		Response:    resp,
		Candidate:   cand,
		LatencyMs:   latencyMs,
		RequestBody: append([]byte(nil), bodyBytes...),
	}, nil
}
