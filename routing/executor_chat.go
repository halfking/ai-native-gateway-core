package routing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/kaixuan/llm-gateway-go/audit"
	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
	"github.com/kaixuan/llm-gateway-go/disguise"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/internal/upstreamurl"
	"github.com/kaixuan/llm-gateway-go/pool"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/transform"
	upstreampkg "github.com/kaixuan/llm-gateway-go/upstream"
)

// ChatExecutor is the ProtocolHandler for OpenAI Chat Completions
// protocol. It owns: chat-completions URL, Bearer auth, OpenAI-shaped
// stream chunks, OpenAI usage field, XML tool-call fallback for
// providers like minimax M2.7 / xiaomi-mimo.
type ChatExecutor struct {
	Common *CommonExecutor
	// Hooks (set via SetXxx) for downstream consumers.
	Normalize          func([]byte, bool) []byte
	XMLCoerceNonStream func([]byte, bool) []byte
	StreamChat         func(http.ResponseWriter, *http.Response, string, string, audit.StreamCapture) StreamOutcome
	// StripMinimaxFields strips minimax-private top-level fields
	// (nvext, audio_content, name, etc.) from the chat response body
	// before it is returned to the client. Wired from main.go.
	StripMinimaxFields func([]byte) []byte
	// QualityProcessNonStream runs the per-provider tool_call quality
	// check (017_quality_fix_mode.sql). Wired from main.go; routing
	// cannot import relay (relay imports routing), so the processor
	// is injected as a hook. Returns (possibly rewritten body,
	// quality signals). When nil, the executor treats the provider
	// as quality_fix_mode='off' (passthrough, no detect, no rewrite).
	QualityProcessNonStream func(body []byte, mode string) (outBody []byte, flags []string, fixActions []byte, score *float64)
	// QualityProcessStreamLine is the streaming equivalent; one SSE
	// "data: ..." line at a time. Accumulates flags and seen tool_call
	// ids across the stream. nil ⇒ off-mode.
	QualityProcessStreamLine func(line, mode string, accFlags []string, seenIDs map[string]int) (outLine string, outFlags []string, outSeen map[string]int)
}

var _ ProtocolHandler = (*ChatExecutor)(nil)

func (c *ChatExecutor) BuildRequest(cand provider.Candidate, body []byte, isStream bool) (*http.Request, error) {
	upstreamURL := upstreamurl.ChatCompletionsURL(cand.BaseURL)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, upstreamURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cand.APIKey)
	if isStream {
		req.Header.Set("Accept", "text/event-stream")
	}
	return req, nil
}

func (c *ChatExecutor) WriteNonStreamResponse(w http.ResponseWriter, resp *http.Response, clientModel, qualityFixMode string, qualitySignals *QualitySignals) error {
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if c.XMLCoerceNonStream != nil {
		body = c.XMLCoerceNonStream(body, false)
	}
	if c.Normalize != nil {
		body = c.Normalize(body, false)
	}
	if clientModel != "" {
		body = replaceModelInResponseBody(body, clientModel)
	}
	if c.StripMinimaxFields != nil {
		body = c.StripMinimaxFields(body)
	}
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, err = w.Write(body)
	return err
}

func (c *ChatExecutor) StreamResponse(w http.ResponseWriter, resp *http.Response) StreamOutcome {
	if c.StreamChat != nil {
		return c.StreamChat(w, resp, "", "", audit.StreamCapture{})
	}
	return legacyStreamChat(w, resp)
}

func (c *ChatExecutor) ExtractUsage(resp *http.Response, body []byte) (inputTokens, outputTokens *int) {
	return extractOpenAIUsageFromBody(body)
}

func (c *ChatExecutor) CheckSoftMismatch(reqModel, respModel string) (bool, string) {
	if reqModel != respModel && reqModel != "" && respModel != "" {
		return true, "openai_protocol_no_silent_fallback"
	}
	return false, ""
}

// extractOpenAIUsageFromBody pulls prompt_tokens / completion_tokens
// from an OpenAI chat completions response body.
func extractOpenAIUsageFromBody(body []byte) (*int, *int) {
	var v struct {
		Usage struct {
			PromptTokens     *int `json:"prompt_tokens"`
			CompletionTokens *int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, nil
	}
	return v.Usage.PromptTokens, v.Usage.CompletionTokens
}

// legacyStreamChat is a minimal OpenAI stream forwarder. Real stream
// handling lives in the relay package (StreamChatWithCaptureAndToolFallback)
// and is wired in by cmd/gateway/main.go through Executor's StreamChat field.
// This fallback exists so ChatExecutor can be used standalone (Phase 1 tests).
func legacyStreamChat(w http.ResponseWriter, resp *http.Response) StreamOutcome {
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	_, _ = io.Copy(w, resp.Body)
	return StreamOutcome{}
}

// executeOpenAI is the Q1/Q2 (OpenAI / openai-completions) path of the
// existing Executor. The body was previously named tryCandidate in
// executor.go and is moved here verbatim — this is a no-behavior-change
// refactor in support of the anthropic-passthrough split (Phase 1).
//
// The signature matches the plan: (params, cand) -> (*ExecuteResult, error).
// The retry loop / circuit / credential-state handling is at the call
// site (Execute()) and is not duplicated here.
func (e *Executor) executeOpenAI(
	params *ExecParams,
	cand provider.Candidate,
	maxRetries int,
	tTotal time.Time,
	fpLease *credentialfpslot.Lease,
) (*ExecuteResult, error) {
	sourceBody := append([]byte(nil), params.BodyBytes...)
	bodyBytes, err := e.finalizeOpenAIUpstreamBody(params, cand, sourceBody)
	if err != nil {
		return nil, err
	}
	// Round 47 compression v7 T-NEW-4: capture the pre-request trim
	// delta (transform.CompressMessagesIfNeeded inside finalize) so
	// emitTelemetry writes compression_meta into request_logs even when
	// no 4xx happened. Without this the pre-request trim runs silently
	// and operators can't see how many bytes were saved.
	preTrimMeta := buildPreRequestTrimMeta(len(sourceBody), len(bodyBytes), cand.ContextWindow)
	outboundModel := params.OutboundModel
	if outboundModel == "" {
		outboundModel = cand.RawModel
	}

	contextLenRecovery := contextLengthRecoveryState{}

	// BUG-2 fix (2026-06-19): compute timeout once outside the retry loop.
	// Previously the timeout was computed inside the anonymous closure, which
	// caused it to be recomputed on every attempt — minor but cleaner here.
	timeout := e.UpstreamTimeout
	if params.IsStream {
		timeout = e.StreamTimeout
	}

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			delay := time.Duration(500*(1<<(attempt-1))) * time.Millisecond
			// BUG-5 design note (2026-06-19): for session requests the upstream
			// HTTP call uses context.Background() (C1) so client disconnect does
			// NOT cancel the vendor call. However, the backoff select below still
			// uses params.R.Context() — a client disconnect *during* the sleep
			// aborts the retry loop. This asymmetry is intentional: avoid burning
			// vendor quota on retries when the client clearly gave up, while still
			// completing an in-flight vendor call so the response can be cached.
			select {
			case <-params.R.Context().Done():
				return nil, params.R.Context().Err()
			case <-time.After(delay):
			}
		}

		// BUG-2 fix (2026-06-19): create the upstream context at loop scope
		// (not inside the closure with defer cancel()). For the session path,
		// upstreamContext returns a context.WithTimeout(context.Background(),
		// streamTimeout) whose timer goroutine previously lived until the outer
		// function returned if the closure happened to return a retryableError.
		// By calling upCancel() explicitly at the end of every attempt we
		// release the timer immediately, reducing unnecessary timer goroutine
		// accumulation under rapid-retry scenarios.
		upCtx, upCancel := e.upstreamContext(params, timeout)

		result, tryErr := func() (*ExecuteResult, error) {
			var reqPool *pool.Pool
			var upstreamURL string
			if cand.Protocol == "anthropic-messages" {
				upstreamURL = upstreamurl.MessagesURL(cand.BaseURL)
			} else {
				upstreamURL = upstreamurl.ChatCompletionsURL(cand.BaseURL)
			}

			// BUG-2 fix: use upCtx (created at loop scope) directly so the
			// request carries the correct context from the start. Previously
			// the request was created with params.R.Context() and then replaced
			// via req.WithContext(upCtx) — that's equivalent but wasteful.
			//
			// 2026-06-19 quality fix mode (017_quality_fix_mode.sql): stamp
			// the chosen provider's quality_fix_mode onto the upstream
			// context so the relay-side stream reader can apply the
			// per-line quality check without needing a direct Candidate
			// reference. The SetQualityFixModeOnContext helper lives in
			// the relay package; we bridge via the QualitySetMode hook
			// (wired in cmd/gateway/main.go) so routing can stay free
			// of the relay import.
			if e.QualitySetMode != nil {
				upCtx = e.QualitySetMode(upCtx, cand.QualityFixMode)
			}
			req, err := http.NewRequestWithContext(
				upCtx,
				http.MethodPost,
				upstreamURL,
				bytes.NewReader(bodyBytes),
			)
			if err != nil {
				return nil, err
			}

			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+cand.APIKey)
			if params.IsStream {
				req.Header.Set("Accept", "text/event-stream")
			}
			req.Header.Set("X-Request-Id", params.R.Header.Get("X-Request-Id"))
			// Track C C2 audit fix 3.1: propagate session headers
			// to the upstream request so the StreamChat closure in
			// main.go can detect session-bearing requests and
			// attach a pendingCapturer. Without this, the capturer
			// is never created and stream caching is dead code.
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

			if params.Transform != nil {
				for _, h := range params.Transform.StripHeaders {
					req.Header.Del(h)
				}
				for k, v := range params.Transform.InjectHeaders {
					req.Header.Set(k, v)
				}
			}

			if e.HeaderProfiles != nil {
				prof := e.HeaderProfiles.load(params.R.Context(), cand.CatalogCode, cand.Protocol)
				if prof != nil {
					for k, v := range prof.Headers {
						req.Header.Set(k, v)
					}
				}
			}

			// Apply disguise headers (User-Agent / Accept-Language rotation).
			// These override any UA already set above.
			if e.DisguisePool != nil {
				for k, v := range e.DisguisePool.Headers() {
					req.Header.Set(k, v)
				}
				e.DisguisePool.MaybeRotate()
			}

			var httpClient *http.Client
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
			} else if err := reqPool.Acquire(upCtx); err != nil {
				return nil, err
			} else {
				defer reqPool.Release()
			}

			// Track C (2026-06-18): upCtx is set at loop scope above.
			// For session requests it is context.WithTimeout(background, streamTimeout)
			// so client disconnect does not cancel the vendor call; the response
			// is cached for reconnect via pending/ and sessions/handler.go C3.
			// For stateless requests upCtx inherits params.R.Context() so client
			// disconnect still cancels the vendor call immediately.

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
				if !errorsx.IsRetryable(errKind) || attempt >= maxRetries {
					return nil, uErr
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
				// Step 4 (2026-06-18): removed the "if upstreamLatency > 10s
				// reclassify as transient" branch. A slow 404 is still a 404;
				// re-routing it as a retryable transient just makes the same
				// credential re-dialed, wasting RTT and hiding the real cause
				// from the caller. The classifier now requires a matching 4xx
				// status (P5) and a tightened regex, so this branch is the
				// canonical model_not_found path.
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
					} else {
						slog.Info("upstream rejected request as client bug",
							"credential_id", cand.CredentialID,
							"provider_id", cand.ProviderID,
							"status", resp.StatusCode,
							"kind", errKind,
							"body_preview", string(body[:min(n, 200)]),
						)
					}
				} else if errKind == errorsx.KindRateLimit {
					e.Limiter.Shrink(cand.ProviderID, cand.CredentialID)
				} else if errKind == errorsx.KindConcurrent {
					e.Circuit.RecordFailure(cand.ProviderID, cand.CredentialID, errorsx.KindConcurrent)
					e.writeCredentialStateOnError(params.R.Context(), cand.CredentialID, errorsx.KindConcurrent,
						fmt.Errorf("upstream %d concurrent overload: %s", resp.StatusCode, string(body[:min(n, 200)])))
					slog.Warn("credential concurrent-overload, failing over to next candidate",
						"credential_id", cand.CredentialID,
						"provider_id", cand.ProviderID,
						"status", resp.StatusCode,
						"body_preview", string(body[:min(n, 120)]),
					)
				}
				if !errorsx.IsRetryable(errKind) || attempt >= maxRetries {
					// Context-length retry path: if the upstream rejected
					// the request because the conversation exceeded the
					// model's context window, attempt one client-side trim
					// + retry. The pre-request trim path
					// (transform.CompressMessagesIfNeeded) catches obvious
					// overshoots; this catches the case where the heuristic
					// underestimated (e.g. tool_call payloads are heavier
					// than raw chars suggest) and the upstream is the
					// authority on its own context window.
					//
					// The retry attempts at most once. If the second
					// attempt also fails, we bubble the 4xx up unchanged
					// — at that point the client is sending genuinely
					// too much history for this model and needs to make
					// room on its own.
					//
					// Q4 (anthropic-messages passthrough) is skipped: the
					// body bytes are owned by the Q4 streaming writer and
					// mid-stream rewriting would break the byte contract.
					if (errorsx.IsContextLength(errKind) ||
						shouldHeuristicCompact(resp.StatusCode, errKind, len(sourceBody), cand.ContextWindow)) &&
						cand.Protocol != "anthropic-messages" {
						switch e.handleContextLengthRecovery(params.R.Context(), params, cand, &sourceBody, &contextLenRecovery, resp.StatusCode) {
						case ctxLenRetry:
							bodyBytes, err = e.finalizeOpenAIUpstreamBody(params, cand, sourceBody)
							if err != nil {
								return nil, err
							}
							return nil, &retryableError{err: fmt.Errorf("upstream %d", resp.StatusCode)}
						case ctxLenGiveUp:
							// Return a typed error so the outer Execute
							// loop knows this is a context-length
							// exhaustion (model-size limit, not a
							// credential fault) and can route the
							// failover accordingly without recording a
							// circuit failure or disabling the model
							// offer.
							return nil, &contextLengthExhaustedError{
								credentialID: cand.CredentialID,
								rawModel:     cand.RawModel,
								status:       resp.StatusCode,
								body:         string(body[:min(n, 200)]),
							}
						}
					}
					// Do not write 4xx to ResponseWriter here — Execute() may
					// fail over to the next credential. Writing first would
					// prepend e.g. "404 page not found" before a later 200 body.
					return nil, fmt.Errorf("upstream %d: %s", resp.StatusCode, string(body[:min(n, 200)]))
				}
				return nil, &retryableError{err: fmt.Errorf("upstream %d", resp.StatusCode)}
			}

			e.Circuit.RecordSuccess(cand.ProviderID, cand.CredentialID)
			latencyMs := int(time.Since(tTotal).Milliseconds())

			if params.IsStream {
				var streamOutcome StreamOutcome
				if params.StreamWrapper != nil {
					streamOutcome = params.StreamWrapper(params.W, resp, e.Normalize, params.Capture)
				} else if e.StreamChat != nil {
					streamOutcome = e.StreamChat(params.W, resp, params.ClientModel, outboundModel, e.Normalize, params.Capture, params.ToolsRequested)
				}
				// 2026-06-19 quality fix mode (017_quality_fix_mode.sql):
				// relay/stream.go writes detected flags into the capture
				// during the stream read loop. Pluck them out here so
				// emitTelemetry can persist them on the request_log row.
				var streamQualityFlags []string
				var streamQualityScore *float64
				if params.Capture != nil {
					streamQualityFlags = params.Capture.QualityFlags
					streamQualityScore = params.Capture.QualityScore
				}
				if streamOutcome.Interrupted && streamOutcome.Reason != "client_cancel" {
					isResumable := streamOutcome.Resumable && streamOutcome.ChunkCount < e.StreamRetryThreshold

					streamKind := errorsx.KindStreamTimeout
					if errorsx.IsConcurrentOverload(streamOutcome.Reason) {
						streamKind = errorsx.KindConcurrent
					}

					isBenignEOF := streamOutcome.Reason == "eof_without_done" && streamOutcome.ChunkCount > 0

					slog.Warn("executor: stream interrupted",
						"credential_id", cand.CredentialID,
						"provider_id", cand.ProviderID,
						"reason", streamOutcome.Reason,
						"chunk_count", streamOutcome.ChunkCount,
						"resumable", isResumable,
						"classified_as", streamKind,
						"benign_eof", isBenignEOF,
					)

				if isBenignEOF {
					e.Circuit.RecordSuccess(cand.ProviderID, cand.CredentialID)
					return &ExecuteResult{
						Response:    resp,
						Candidate:   cand,
						LatencyMs:   latencyMs,
						RequestBody: append([]byte(nil), bodyBytes...),
						// Round 47 T-NEW-4: stream success path also records
						// pre-request trim metadata so emitTelemetry can write
						// compression_meta for streaming requests.
						CompressionReason:   strPtrCompat(contextLenRecovery.lastReason),
						CompressionStrategy: strPtrCompat(contextLenRecovery.lastStrategy),
						CompressionMeta:     mergeCompressionMeta(contextLenRecovery.lastMeta, preTrimMeta),
					}, nil
					} else if isResumable {
						e.Circuit.RecordFailure(cand.ProviderID, cand.CredentialID, streamKind)
						if streamKind == errorsx.KindConcurrent {
							e.writeCredentialStateOnError(params.R.Context(), cand.CredentialID, streamKind,
								fmt.Errorf("stream %s (concurrent-overload inferred)", streamOutcome.Reason))
						} else if e.shouldWriteCredentialStateOnConfirmedFailure(cand.ProviderID, cand.CredentialID, streamKind) {
							e.writeCredentialStateOnError(params.R.Context(), cand.CredentialID, streamKind, fmt.Errorf("stream %s", streamOutcome.Reason))
						}
					} else if streamKind == errorsx.KindConcurrent {
						e.Circuit.RecordFailure(cand.ProviderID, cand.CredentialID, streamKind)
						e.writeCredentialStateOnError(params.R.Context(), cand.CredentialID, streamKind,
							fmt.Errorf("stream %s (concurrent-overload inferred, non-resumable)", streamOutcome.Reason))
						slog.Warn("non-resumable stream interrupted by concurrent-overload, credential now in 5-min cooling",
							"credential_id", cand.CredentialID,
							"provider_id", cand.ProviderID,
							"reason", streamOutcome.Reason,
							"chunk_count", streamOutcome.ChunkCount,
						)
					} else {
						e.Circuit.RecordFailure(cand.ProviderID, cand.CredentialID, streamKind)
						if e.shouldWriteCredentialStateOnConfirmedFailure(cand.ProviderID, cand.CredentialID, streamKind) {
							e.writeCredentialStateOnError(params.R.Context(), cand.CredentialID, streamKind,
								fmt.Errorf("stream %s (non-resumable)", streamOutcome.Reason))
						}
						slog.Warn("non-resumable stream interrupted",
							"credential_id", cand.CredentialID,
							"provider_id", cand.ProviderID,
							"reason", streamOutcome.Reason,
							"kind", streamKind,
							"chunk_count", streamOutcome.ChunkCount,
						)
					}

					return &ExecuteResult{
						Response:    resp,
						Candidate:   cand,
						LatencyMs:   latencyMs,
						RequestBody: append([]byte(nil), bodyBytes...),
						// 2026-06-19 quality fix mode: capture any flags the
						// stream reader observed before the interrupt fired.
						QualityFlags: streamQualityFlags,
						QualityScore: streamQualityScore,
					}, &streamInterruptedError{reason: streamOutcome.Reason, credentialID: cand.CredentialID, resumable: isResumable, kind: streamKind}
				}
				return &ExecuteResult{
					Response:    resp,
					Candidate:   cand,
					LatencyMs:   latencyMs,
					RequestBody: append([]byte(nil), bodyBytes...),
					// Round 47 T-NEW-4: stream success path (ClientIsStreaming=true,
					// StreamChat handled the write) also records pre-request trim
					// metadata so emitTelemetry can write compression_meta.
					CompressionReason:   strPtrCompat(contextLenRecovery.lastReason),
					CompressionStrategy: strPtrCompat(contextLenRecovery.lastStrategy),
					CompressionMeta:     mergeCompressionMeta(contextLenRecovery.lastMeta, preTrimMeta),
					// 2026-06-19 quality fix mode: relay/stream.go populated
					// capture.QualityFlags as each chunk was processed.
					// relay/handler.go emitTelemetry writes these into
					// request_logs.quality_flags / quality_score.
					QualityFlags: streamQualityFlags,
					QualityScore: streamQualityScore,
				}, nil
			}
			defer resp.Body.Close()
			respBody, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxBodySize)+1))
			if err != nil {
				return nil, err
			}
			if len(respBody) > maxBodySize {
				slog.Warn("upstream response truncated", "size", len(respBody))
				respBody = respBody[:maxBodySize]
			}
			// 2026-06-19 quality fix mode (017_quality_fix_mode.sql).
			// Run before any other body transform so the scanner sees
			// the raw upstream tool_calls shape. detect_only/fix modes
			// may rewrite empty names to __unknown_tool_<i>__; the
			// subsequent XMLCoerce / Normalize / StripMinimax passes
			// then operate on the (possibly) cleaner body.
			//
			// We only invoke the hook when QualityFixMode is non-empty
			// so off-mode providers skip the call entirely. The hook
			// itself also short-circuits on 'off' (defence in depth) but
			// the routing-side guard keeps the no-op free of the JSON
			// unmarshal pass in applyFixes.
			qualityBody := respBody
			var qualityFlags []string
			var qualityActions []byte
			var qualityScore *float64
			if e.QualityProcessNonStream != nil && cand.QualityFixMode != "" {
				qualityBody, qualityFlags, qualityActions, qualityScore =
					e.QualityProcessNonStream(respBody, cand.QualityFixMode)
				if qualityBody != nil {
					respBody = qualityBody
				}
			}
			if params.ClientModel != "" {
				respBody = replaceModelInResponseBody(respBody, params.ClientModel)
			}
			if e.XMLCoerceNonStream != nil {
				respBody = e.XMLCoerceNonStream(respBody, params.ToolsRequested)
			}
			if e.Normalize != nil {
				respBody = e.Normalize(respBody, false)
			}
			if e.StripMinimaxFields != nil {
				respBody = e.StripMinimaxFields(respBody)
			}
			if !params.SuppressSuccessWrite {
				for k, vs := range resp.Header {
					for _, v := range vs {
						params.W.Header().Add(k, v)
					}
				}
				params.W.WriteHeader(resp.StatusCode)
				params.W.Write(respBody)
			}
			return &ExecuteResult{
				Response:     resp,
				Candidate:    cand,
				LatencyMs:    latencyMs,
				RequestBody:  append([]byte(nil), bodyBytes...),
				ResponseBody: append([]byte(nil), respBody...),
				// Round 47 compression v7 T-NEW-2: surface the compression
				// event captured by handleContextLengthRecovery so
				// relay/handler.go emitTelemetry can write it to
				// request_logs.compression_*.
				CompressionReason:   strPtrCompat(contextLenRecovery.lastReason),
				CompressionStrategy: strPtrCompat(contextLenRecovery.lastStrategy),
				CompressionMeta:     mergeCompressionMeta(contextLenRecovery.lastMeta, preTrimMeta),
				// Round 47 compression v7 T-NEW-4: pre-request trim
				// metadata merged in via mergeCompressionMeta above.
				// 2026-06-19 quality fix mode: relay/handler.go emitTelemetry
				// copies these into request_logs.quality_* columns.
				QualityFlags:      qualityFlags,
				QualityFixActions: qualityActions,
				QualityScore:      qualityScore,
			}, nil
		}()

		if tryErr == nil {
			// BUG-2 fix: cancel the upstream context immediately on success.
			// The timer goroutine is released without waiting for the outer
			// function to return.
			upCancel()
			// Memora persistence (fire-and-forget). We enqueue the
			// request conversation + non-stream response body so L1
			// session memory accumulates facts for later retrieval on
			// context-overflow. The sink is nil-checked inside the helper.
			e.enqueueMemoraWrite(params, sourceBody, result.ResponseBody)
			return result, nil
		}
		// BUG-2 fix: always cancel the upstream context at the end of each
		// attempt to release the timer goroutine created by
		// context.WithTimeout immediately, regardless of whether the attempt
		// succeeded or failed.
		upCancel()
		if _, ok := tryErr.(*retryableError); !ok {
			return nil, tryErr
		}
	}
	return nil, fmt.Errorf("exhausted %d retries for credential %d", maxRetries, cand.CredentialID)
}

// finalizeOpenAIUpstreamBody applies prepareRequestBody plus OpenAI-path-only
// transforms (Q2 anthropic→openai conversion, disguise, prompt-cache injection).
func (e *Executor) finalizeOpenAIUpstreamBody(params *ExecParams, cand provider.Candidate, sourceBody []byte) ([]byte, error) {
	p := *params
	p.BodyBytes = sourceBody
	bodyBytes := prepareRequestBody(&p, cand)
	if e.NormalizeOpenAITools != nil {
		bodyBytes = e.NormalizeOpenAITools(bodyBytes)
	}
	if params.ClientProtocol == "anthropic-messages" {
		if e.AnthropicToOpenAI != nil {
			converted, err := e.AnthropicToOpenAI(bodyBytes)
			if err != nil {
				return nil, fmt.Errorf("convert anthropic body to openai: %w", err)
			}
			bodyBytes = converted
		}
	}
	if disguise.IsEnabled() && disguise.ShouldApply(bodyBytes) {
		profileName := ""
		if params.Transform != nil && params.Transform.DisguiseProfileID != "" {
			profileName = params.Transform.DisguiseProfileID
		} else if params.ClientID.Fingerprint.ClientProfile != "" {
			profileName = params.ClientID.Fingerprint.ClientProfile
		}
		if profileName != "" {
			bodyBytes, _ = disguise.Apply(bodyBytes, nil, nil, profileName, 0)
			slog.Debug("disguise layer applied", "profile", profileName)
		}
	}
	if params.SessionKey != "" && cand.SupportsPromptCache {
		bodyBytes, _ = injectCacheParams(bodyBytes, cand.CacheMode, params.SessionKey)
	}
	return bodyBytes, nil
}

// prepareRequestBody builds the upstream request body from params and cand.
//
// It performs the protocol-aware transformations that happen BEFORE the
// request is sent: model-name substitution, OpenAI stream_options injection
// (skipped for anthropic-messages since Anthropic has no such field),
// transform whitelist, tool-history collapse, capability sanitizer, message
// merge.
//
// Extracted as a free function so unit tests can verify each protocol
// branch without spinning up the full HTTP retry loop.

// resolveOutboundModel picks the upstream model field.
// Mirrors Python prepare_candidate → render_outbound_model() default path:
// transform-rendered OutboundModel wins; else cand.RawModel which is
// COALESCE(outbound_model_name, raw_model_name) from model_offers.
func resolveOutboundModel(params *ExecParams, cand provider.Candidate) string {
	if params.OutboundModel != "" {
		return params.OutboundModel
	}
	return cand.RawModel
}

func prepareRequestBody(params *ExecParams, cand provider.Candidate) []byte {
	outboundModel := params.OutboundModel
	if outboundModel == "" {
		outboundModel = cand.RawModel
	}

	bodyBytes := params.BodyBytes
	if outboundModel != params.ClientModel {
		bodyBytes = replaceModelInRequestBody(bodyBytes, outboundModel)
	}
	// injectStreamOptions adds OpenAI-specific `"stream_options":{"include_usage":true}`
	// to streaming requests so upstream returns a final usage chunk we can
	// attribute for billing. Anthropic streams usage via message_start +
	// message_delta events and has no stream_options field; injecting it would
	// either be silently ignored or, worse, rejected by strict providers.
	// Guard on protocol.
	if params.IsStream && cand.Protocol != "anthropic-messages" {
		bodyBytes = injectStreamOptions(bodyBytes)
	}
	if params.Transform != nil {
		bodyBytes = transform.ApplyRequestWhitelist(
			bodyBytes,
			params.Transform.PassthroughFields,
			params.Transform.StripRequestFields,
			cand.Protocol,
		)
	}
	if !transform.IsToolUseCapable(cand.CatalogCode, cand.Protocol) && transform.NeedsToolCollapse(bodyBytes) {
		bodyBytes = transform.CollapseToolHistory(bodyBytes)
	}
	bodyBytes = transform.ApplyCapabilitySanitizer(bodyBytes, cand.CatalogCode)
	bodyBytes = transform.MergeConsecutiveMessages(bodyBytes)
	// Client-side context window enforcement for Q1/Q2/Q3 openai protocol.
	// Q4 (anthropic-messages) is handled in prepareAnthropicRequestBody
	// (executor_anthropic.go). See transform/ctx_compress.go for rationale:
	// upstreams like minimax trim server-side on direct calls, but proxy
	// clients must trim at the gateway.
	if cand.Protocol != "anthropic-messages" && cand.ContextWindow != nil {
		bodyBytes = transform.CompressMessagesIfNeeded(bodyBytes, *cand.ContextWindow)
	}
	return bodyBytes
}

// executeAnthropic is the Q3/Q4 (anthropic-messages upstream) path.
// The real implementation is in executor_anthropic.go; it lives there
// (not in this file) so that OpenAI-shape assumptions cannot leak into
// the Anthropic path.

// strPtrCompat returns a pointer to the given string. Used by the
// compression v7 fields which need a pointer helper that doesn't conflict
// with the relay package's strPtr (we can't import relay from routing
// without introducing a cycle).
func strPtrCompat(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// hasSessionID reports whether the request carries a gateway session
// id (X-Gw-Session-Id). When true, the executor decouples the
// upstream context from the client context so a client disconnect
// does not cancel the vendor request — the response is cached for
// the client to pick up on reconnect (see pending/ Store + the GET
// endpoint in sessions/handler.go).
//
// Track C (2026-06-18). Mirrors the X-Session-Id → X-Gw-Session-Id
// fallback used in relay/handler.go, but here we only care about
// "is the client claiming session tracking" — the actual lookup
// happens earlier in the handler.
func hasSessionID(params *ExecParams) bool {
	if params == nil || params.R == nil {
		return false
	}
	if v := params.R.Header.Get("X-Gw-Session-Id"); v != "" {
		return true
	}
	if v := params.R.Header.Get("X-Session-Id"); v != "" {
		return true
	}
	return false
}

// upstreamContext (Track C, 2026-06-18) returns the context used for
// the upstream HTTP call. When the request carries a session id,
// the context is decoupled from the client request context so a
// client disconnect does not cancel the vendor request. Otherwise,
// the original behaviour is preserved (client disconnect cancels
// upstream immediately) to avoid wasting vendor budget on requests
// the client will not retrieve.
//
// The decoupling is intentionally minimal in C1: we do NOT yet
// implement the response buffering or the cache write — those are
// C2 (stream.go) and C4 (executor.go async retry). C1 only proves
// the "upstream does not cancel on client disconnect" building
// block. The timeout is still respected, so a stuck vendor is
// bounded regardless of client state.
func (e *Executor) upstreamContext(params *ExecParams, timeout time.Duration) (context.Context, context.CancelFunc) {
	if hasSessionID(params) {
		return context.WithTimeout(context.Background(), timeout)
	}
	return context.WithTimeout(params.R.Context(), timeout)
}
