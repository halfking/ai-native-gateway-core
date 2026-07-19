package executors

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
	"github.com/kaixuan/llm-gateway-go/disguise"
	"github.com/kaixuan/llm-gateway-go/domain"                 //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"    //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/transformation" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/internal/ir"
	"github.com/kaixuan/llm-gateway-go/internal/upstreamurl"
	"github.com/kaixuan/llm-gateway-go/pool"
	"github.com/kaixuan/llm-gateway-go/provider"
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
	// StripZhipuFields strips zhipu/GLM-private fields (zhipu_request_id,
	// cache_read_tokens, web_search_results, etc.). Wired from main.go.
	StripZhipuFields func([]byte) []byte
	// StripDeepSeekFields strips deepseek-private fields (reasoning_tokens,
	// prompt_cache_hit_tokens, deepseek_request_id, etc.). Wired from main.go.
	StripDeepSeekFields func([]byte) []byte
	// StripDoubaoFields strips doubao/volcengine-private fields (doubao_request_id,
	// seeddance_request_id, content_safety_score, etc.). Wired from main.go.
	StripDoubaoFields func([]byte) []byte
	// RedactBodyFn (2026-07-09) write-time 客户端可见脱敏。
	// 在 w.Write 前调用，让客户端真正收到脱敏后字节（与 post-response
	// OutputComplianceInterceptor 互补：前者改客户端，后者改 telemetry）。
	// 签名：func(body []byte, sessionID, tenantID string) []byte
	// Wired from main.go via streaming.BuildRedactBodyFn.
	RedactBodyFn func([]byte, string, string) []byte
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

func (c *ChatExecutor) WriteNonStreamResponse(w http.ResponseWriter, resp *http.Response, clientModel, qualityFixMode string, qualitySignals *QualitySignals) ([]byte, error) {
	//nolint:errcheck // best-effort close
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
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
	// Write-time 客户端可见脱敏（2026-07-09，增强 1）。
	// 在 w.Write 前调用，让客户端真正收到脱敏后字节。
	// sessionID/tenantID 需从上下文传入（当前简化为空，TODO: 从 routing 上下文注入）。
	if c.RedactBodyFn != nil {
		body = c.RedactBodyFn(body, "", "")
	}
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, err = w.Write(body)
	return body, err
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

	// 2026-07-16: Pre-request validation
	if e.PreRequestValidator != nil {
		validationResult, validationErr := e.PreRequestValidator.Validate(params.R.Context(), bodyBytes)
		if validationErr != nil {
			slog.Warn("pre_request_validation failed",
				"error", validationErr,
				"credential_id", cand.CredentialID,
				"model", params.Model,
			)
			// Non-strict mode: log but continue
		}
		if validationResult != nil && len(validationResult.Warnings) > 0 {
			for _, warning := range validationResult.Warnings {
				slog.Debug("pre_request_validation warning",
					"warning", warning,
					"credential_id", cand.CredentialID,
					"model", params.Model,
				)
			}
		}
	}

	// Round 47 compression v7 T-NEW-4: capture the pre-request trim
	// delta (transformation.CompressMessagesIfNeeded inside finalize) so
	// emitTelemetry writes compression_meta into request_logs even when
	// no 4xx happened. Without this the pre-request trim runs silently
	// and operators can't see how many bytes were saved.
	preTrimMeta := buildPreRequestTrimMeta(len(sourceBody), len(bodyBytes), cand.ContextWindow)
	outboundModel := params.OutboundModel
	if outboundModel == "" {
		outboundModel = cand.RawModel
	}

	contextLenRecovery := contextLengthRecoveryState{}

	// Internal retry for model_not_found (2026-06-20):
	// Track whether we've already retried once for model_not_found on this credential.
	// This flag persists across attempts within the retry loop.
	mnfRetried := false

	// BUG-2 fix (2026-06-19): compute timeout once outside the retry loop.
	// Previously the timeout was computed inside the anonymous closure, which
	// caused it to be recomputed on every attempt — minor but cleaner here.
	// 2026-07-16: Use adaptive timeout if available
	timeout := e.UpstreamTimeout
	if params.IsStream {
		timeout = e.StreamTimeout

		// Apply adaptive timeout calculation
		if e.TimeoutAdapter != nil {
			var recentTTFB *time.Duration
			if e.TTFBTracker != nil {
				stats := e.TTFBTracker.Get(cand.CredentialID)
				if stats != nil {
					recentTTFB = &stats.RecentTTFB
				}
			}

			adaptiveTimeout := e.TimeoutAdapter.Calculate(AdaptiveTimeoutInput{
				RequestSize: len(bodyBytes),
				IsSession:   params.SessionID != "",
				IsRetry:     false, // Will be updated in retry loop
				AttemptNum:  0,
				ProviderURL: cand.BaseURL,
				RecentTTFB:  recentTTFB,
			})

			slog.Debug("adaptive_timeout calculated",
				"credential_id", cand.CredentialID,
				"model", params.Model,
				"request_size", len(bodyBytes),
				"is_session", params.SessionID != "",
				"provider_url", cand.BaseURL,
				"recent_ttfb_ms", func() int64 {
					if recentTTFB != nil {
						return recentTTFB.Milliseconds()
					}
					return 0
				}(),
				"calculated_timeout_ms", adaptiveTimeout.Milliseconds(),
				"default_timeout_ms", timeout.Milliseconds(),
			)

			timeout = adaptiveTimeout
		}
	}

	// Ensure at least 1 retry is available for internal model_not_found retry.
	// When maxRetries is 0 (from policy.RetryPerCredential), we still need
	// one retry attempt to verify if model_not_found is transient.
	effectiveMaxRetries := maxRetries
	if effectiveMaxRetries < 1 {
		effectiveMaxRetries = 1
	}

	// P3-9: mnfBonus grants exactly one extra loop iteration when an mnf
	// retry is outstanding. It flips to 1 the moment mnfRetried is set
	// (inside the mnf branch), and the flag stays true so the bonus never
	// accumulates. This lets the single mnf probe happen even when policy
	// maxRetries is 0, without forcing unrelated errors to retry.
	mnfBonus := 0
	var lastErr error // 2026-07-03 (Bug #N extension): preserve last error for "exhausted retries"
	for attempt := 0; attempt <= effectiveMaxRetries+mnfBonus; attempt++ {
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

			// Apply disguise headers (User-Agent / Accept-Language).
			// Slot-bound sessions get a stable UA per slot (no churn
			// between requests). Stateless requests (no fpLease) fall
			// back to a random pick so the pool still gets exercised.
			if e.DisguisePool != nil {
				slotIdx := -1
				if fpLease != nil {
					slotIdx = fpLease.SlotIndex
				}
				for k, v := range e.DisguisePool.HeadersForSlot(slotIdx) {
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
			// Streaming requests use a detached context so a browser cancellation
			// cannot abort the upstream deadline before timeout health is recorded.

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
					kind := errorsx.ClassifyError(doErr, nil)
					// P0 fix (2026-07-16): distinguish timeout from client cancel
					// in non-session mode. When the client disconnects WHILE the
					// gateway's own context deadline has been reached, Go's context
					// tree returns Canceled (inherited from the parent) instead of
					// DeadlineExceeded — but the real cause is an upstream timeout.
					// Check the actual deadline to disambiguate.
					if kind == errorsx.KindCanceled {
						if dl, ok := upCtx.Deadline(); ok && !time.Now().Before(dl) {
							kind = errorsx.KindTimeout
						}
					}
					uErr = &upstreampkg.Error{Kind: kind, Message: doErr.Error(), Err: doErr}
				}
			}
			upstreamLatency := time.Since(reqStart)

			// 2026-07-18: structured log around every upstream HTTP attempt
			// so journald can correlate req_id → upstream url → status /
			// err_kind. The minmax-m3 incident
			// (3905e839e0abab5a53efc09222e2d45b) had ZERO logs from this
			// code path: we knew the request was sent, but not to which
			// provider/credential/raw_model and what came back. Without
			// this, distinguishing client-side tool_id_mismatch vs
			// upstream rate-limit vs upstream timeout is impossible.
			attemptLog := func(status int, errKind string, bodyPreview string) {
				attrs := []any{
					"request_id", params.RequestID,
					"attempt", attempt,
					"provider_id", cand.ProviderID,
					"credential_id", cand.CredentialID,
					"raw_model", cand.RawModel,
					"client_model", params.Model,
					"upstream_url", req.URL.String(),
					"upstream_method", req.Method,
					"body_bytes", len(bodyBytes),
					"is_stream", params.IsStream,
					"latency_ms", upstreamLatency.Milliseconds(),
				}
				if status > 0 {
					attrs = append(attrs, "upstream_status", status)
				}
				if errKind != "" {
					attrs = append(attrs, "err_kind", errKind)
				}
				if bodyPreview != "" {
					attrs = append(attrs, "body_preview", bodyPreview)
				}
				if uErr != nil {
					attrs = append(attrs, "err_message", uErr.Message)
				}
				slog.Info("upstream_http_attempt", attrs...)
			}
			_ = attemptLog // ensure variable used even if early-return below
			if uErr != nil {
				attemptLog(0, string(uErr.Kind), "")
			}
			if resp != nil {
				attemptLog(resp.StatusCode, "", "")
				// 2026-07-18: when upstream returns 4xx, the body often
				// carries the real classifier signal
				// ("tool_call_id_mismatch" / "invalid_request_format" / …).
				// Capture a 256-byte preview of the 4xx body so operators
				// don't need tcpdump / request_logs excavation to figure
				// out WHY the upstream rejected us. Goes after the
				// attemptLog call (the resp.Body is still untouched at
				// this point).
				if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.Body != nil {
					peek := make([]byte, 256)
					n, _ := resp.Body.Read(peek)
					if n > 0 {
						attemptLog(0, "", strings.TrimSpace(string(peek[:n])))
						// Restore the preview so the classifier below sees the
						// complete upstream body, not only its unread suffix.
						resp.Body = io.NopCloser(io.MultiReader(bytes.NewReader(peek[:n]), resp.Body))
					}
				}
			}

			if uErr != nil && (resp == nil || resp.StatusCode >= 500) {
				errKind := uErr.Kind
				if errKind == errorsx.KindRateLimit {
					e.Limiter.Shrink(cand.ProviderID, cand.CredentialID)
				}
				if !errorsx.IsRetryable(errKind) || attempt >= effectiveMaxRetries {
					return nil, uErr
				}
				return nil, &retryableError{err: uErr}
			}

			if resp != nil && resp.StatusCode >= 400 {
				//nolint:errcheck // best-effort close
				defer resp.Body.Close()
				body := make([]byte, 4096)
				n, _ := resp.Body.Read(body)
				_, _ = io.Copy(io.Discard, resp.Body)
				errKind := errorsx.ClassifyErrorWithBody(resp.StatusCode, body[:n])

				if bodyKind := errorsx.ClassifyResponseBody(resp.StatusCode, body[:n]); bodyKind == errorsx.KindModelNotFound {
					// Internal retry for model_not_found (2026-06-20):
					// When upstream returns model_not_found, it may be transient instability
					// rather than a permanent model removal. We retry once on the same
					// credential after a brief delay to verify if the issue persists.
					// This reduces false-positive errors forwarded to clients.
					//
					// P2-8 fix (2026-06-22 audit): the pre-retry delay was a bare
					// time.Sleep, which blocks the goroutine even after the client
					// has disconnected. Replaced with a context-aware select so a
					// client disconnect during the 150ms wait aborts immediately.
					//
					// P3-9 fix: this retry is gated by mnfRetried alone and returns
					// retryableError; the loop exit (below) grants one extra
					// iteration for an outstanding mnf retry even when policy
					// maxRetries is 0, so we no longer force a global
					// effectiveMaxRetries floor.
					if !mnfRetried {
						slog.Info("model_not_found retry",
							"credential_id", cand.CredentialID,
							"model", cand.RawModel,
							"status", resp.StatusCode,
							"upstream_latency_ms", upstreamLatency.Milliseconds(),
							"body_preview", string(body[:min(n, 120)]),
						)
						// Brief delay before retry (150ms) to give upstream time to
						// recover. Abort early if the client disconnects.
						select {
						case <-params.R.Context().Done():
							return nil, params.R.Context().Err()
						case <-time.After(150 * time.Millisecond):
						}
						// Mark as retried and return a retryable error to trigger
						// retry on same credential. mnfBonus grants the extra
						// iteration this retry needs (P3-9).
						mnfRetried = true
						mnfBonus = 1
						return nil, &retryableError{err: &modelNotFoundError{
							credentialID: cand.CredentialID,
							rawModel:     cand.RawModel,
							body:         string(body[:n]),
							status:       resp.StatusCode,
						}}
					}

					// Step 4 (2026-06-18): removed the "if upstreamLatency > 10s
					// reclassify as transient" branch. A slow 404 is still a 404;
					// re-routing it as a retryable transient just makes the same
					// credential re-dialed, wasting RTT and hiding the real cause
					// from the caller. The classifier now requires a matching 4xx
					// status (P5) and a tightened regex, so this branch is the
					// canonical model_not_found path.
					slog.Info("model_not_found skip offer (after retry)",
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
						status:       resp.StatusCode,
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
					e.writeCredentialStateOnError(params.R.Context(), cand.CredentialID, cand.StandardizedName, errorsx.KindConcurrent,
						fmt.Errorf("upstream %d concurrent overload: %s", resp.StatusCode, string(body[:min(n, 200)])))
					e.forceUnpinOnFatalKind(params.R.Context(), fpLease.Holder, cand.CredentialID, errorsx.KindConcurrent)
					slog.Warn("credential concurrent-overload, failing over to next candidate",
						"credential_id", cand.CredentialID,
						"provider_id", cand.ProviderID,
						"status", resp.StatusCode,
						"body_preview", string(body[:min(n, 120)]),
					)
				}
				if !errorsx.IsRetryable(errKind) || attempt >= effectiveMaxRetries {
					// Context-length retry path: if the upstream rejected
					// the request because the conversation exceeded the
					// model's context window, attempt one client-side trim
					// + retry. The pre-request trim path
					// (transformation.CompressMessagesIfNeeded) catches obvious
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
							// 2026-07-03 (Bug #N extension): preserve errKind in the
							// retryableError wrapper so if retries are exhausted, the
							// outer tryCandidate returns lastErr with the precise Kind.
							return nil, &retryableError{err: &upstreampkg.Error{
								Kind:       errKind,
								Message:    fmt.Sprintf("upstream %d (context-length recovery retry)", resp.StatusCode),
								Body:       append([]byte(nil), body[:n]...),
								StatusCode: resp.StatusCode,
							}}
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
					//
					// 2026-07-03 (Bug #N): preserve errKind via a typed
					// *upstreampkg.Error so the outer Execute() loop and
					// CandidateFailureLogger can read Kind=errKind instead
					// of re-classifying the wrapped fmt.Errorf string via
					// ClassifyError, which falls through to KindTransient
					// for body-driven kinds (KindToolCallIdMismatch,
					// KindContextLength, KindUnsupportedFeature). Without
					// this, the same minimax-m3 tool_call_id_mismatch
					// 4xx that surfaces as "client bug" on the inner
					// branch (line 456) regresses to "transient" in
					// request_logs.error_kind / circuit / sticky / URSM.
					return nil, &upstreampkg.Error{
						Kind:       errKind,
						Message:    fmt.Sprintf("upstream %d: %s", resp.StatusCode, string(body[:min(n, 200)])),
						Body:       append([]byte(nil), body[:n]...),
						StatusCode: resp.StatusCode,
					}
				}
				// Same fix for the retryable path: keep the precise kind
				// on the *retryableError wrapper so the outer loop sees
				// the right value if it ever unwraps to a *upstreampkg.Error.
				return nil, &retryableError{err: &upstreampkg.Error{
					Kind:       errKind,
					Message:    fmt.Sprintf("upstream %d", resp.StatusCode),
					Body:       append([]byte(nil), body[:n]...),
					StatusCode: resp.StatusCode,
				}}
			}

			e.Circuit.RecordSuccess(cand.ProviderID, cand.CredentialID)
			latencyMs := int(time.Since(tTotal).Milliseconds())

			// 2026-07-16: Record TTFB and execution outcome
			ttfbMs := upstreamLatency.Milliseconds()
			if e.TTFBTracker != nil {
				e.TTFBTracker.Record(cand.CredentialID, upstreamLatency)
			}
			if e.PostExecutionHook != nil {
				_ = e.PostExecutionHook.RecordOutcome(params.R.Context(), ExecutionOutcome{
					CredentialID:   cand.CredentialID,
					ProviderID:     cand.ProviderID,
					RawModel:       cand.RawModel,
					CanonicalModel: params.Model,
					RequestID:      params.RequestID,
					TenantID:       params.TenantID,
					Success:        true,
					LatencyMs:      int64(latencyMs),
					TTFBMs:         ttfbMs,
					IsStream:       params.IsStream,
					ChunkCount:     0, // Will be updated for stream
					IsRetry:        attempt > 0,
					AttemptNum:     attempt,
					StartedAt:      tTotal,
					CompletedAt:    time.Now(),
				})
			}

			if params.IsStream {
				if params.OnStreamReady != nil {
					params.OnStreamReady()
					params.OnStreamReady = nil
				}
				if params.OnStreamStarted != nil {
					params.OnStreamStarted(int(ttfbMs))
					params.OnStreamStarted = nil
				}

				var streamOutcome StreamOutcome
				switch {
				case e.OpenAIToAnthropicStream != nil &&
					params.ClientProtocol == "anthropic-messages" &&
					cand.Protocol != "anthropic-messages":
					streamOutcome = e.OpenAIToAnthropicStream(
						params.W, resp,
						params.ClientModel, outboundModel,
						params.R.Header.Get("X-Request-Id"),
						params.Capture, nil,
					)
				case e.OpenAIToResponsesStream != nil &&
					params.ClientProtocol == "openai-responses" &&
					cand.Protocol != "anthropic-messages":
					streamOutcome = e.OpenAIToResponsesStream(
						params.W, resp,
						params.ClientModel, outboundModel,
						params.R.Header.Get("X-Request-Id"),
						params.Capture, nil,
					)
				case params.StreamWrapper != nil:
					streamOutcome = params.StreamWrapper(params.W, resp, e.Normalize, params.Capture)
				case e.StreamChat != nil:
					streamOutcome = e.StreamChat(params.W, resp, params.ClientModel, outboundModel, cand.CatalogCode, e.Normalize, params.Capture, params.ToolsRequested)
				}
				if params.OnStreamCompleted != nil {
					params.OnStreamCompleted(streamOutcome)
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
					if streamOutcome.Reason == "client_write_failed" {
						// The client closed the response. Preserve the interruption
						// for request telemetry, but do not treat it as upstream
						// failure or trigger a probe.
						slog.Info("executor: client disconnected during stream",
							"credential_id", cand.CredentialID,
							"provider_id", cand.ProviderID,
							"chunk_count", streamOutcome.ChunkCount,
						)
						return &ExecuteResult{
								Response:    resp,
								Candidate:   cand,
								LatencyMs:   latencyMs,
								RequestBody: append([]byte(nil), bodyBytes...),
								InboundBody: sourceBody,
							}, &streamInterruptedError{
								reason:       streamOutcome.Reason,
								credentialID: cand.CredentialID,
								resumable:    false,
								kind:         errorsx.KindCanceled,
							}
					}
					isResumable := streamOutcome.Resumable && streamOutcome.ChunkCount < e.StreamRetryThreshold

					streamKind := errorsx.KindStreamTimeout
					if errorsx.IsConcurrentOverload(streamOutcome.Reason) {
						streamKind = errorsx.KindConcurrent
					}

					// 2026-07-15: empty-stream content-gate returns
					// Resumable=true with ChunkCount=0 when the upstream
					// opened a stream but produced zero content (notably
					// NIM's 13% empty-stream rate). Classify it as
					// KindEmptyResponse so:
					//   - freeCredentialsTolerateTransient does NOT skip
					//     RecordFailure (we want circuit feedback to demote
					//     the chronically-empty credential via recent_success_rate)
					//   - shouldWriteCredentialState returns false (soft kind,
					//     keeps the credential 'ready' for retry)
					//   - isCredentialFatal returns false (transient)
					//   - Resumable + ChunkCount=0 < StreamRetryThreshold
					//     routes it through the candidate-loop continue,
					//     failing over to the next credential transparently.
					if streamOutcome.Reason == "empty_stream_no_content" {
						streamKind = errorsx.KindEmptyResponse
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
							// Phase D (2026-06-22): inbound body for audit logging
							InboundBody: sourceBody,
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
							e.writeCredentialStateOnError(params.R.Context(), cand.CredentialID, cand.StandardizedName, streamKind,
								fmt.Errorf("stream %s (concurrent-overload inferred)", streamOutcome.Reason))
							e.forceUnpinOnFatalKind(params.R.Context(), fpLease.Holder, cand.CredentialID, streamKind)
						} else if e.shouldWriteCredentialStateOnConfirmedFailure(cand.ProviderID, cand.CredentialID, streamKind) {
							e.writeCredentialStateOnError(params.R.Context(), cand.CredentialID, cand.StandardizedName, streamKind, fmt.Errorf("stream %s", streamOutcome.Reason))
							e.forceUnpinOnFatalKind(params.R.Context(), fpLease.Holder, cand.CredentialID, streamKind)
						}
					} else if streamKind == errorsx.KindConcurrent {
						e.Circuit.RecordFailure(cand.ProviderID, cand.CredentialID, streamKind)
						e.writeCredentialStateOnError(params.R.Context(), cand.CredentialID, cand.StandardizedName, streamKind,
							fmt.Errorf("stream %s (concurrent-overload inferred, non-resumable)", streamOutcome.Reason))
						e.forceUnpinOnFatalKind(params.R.Context(), fpLease.Holder, cand.CredentialID, streamKind)
						slog.Warn("non-resumable stream interrupted by concurrent-overload, credential now in 5-min cooling",
							"credential_id", cand.CredentialID,
							"provider_id", cand.ProviderID,
							"reason", streamOutcome.Reason,
							"chunk_count", streamOutcome.ChunkCount,
						)
					} else {
						e.Circuit.RecordFailure(cand.ProviderID, cand.CredentialID, streamKind)
						if e.shouldWriteCredentialStateOnConfirmedFailure(cand.ProviderID, cand.CredentialID, streamKind) {
							e.writeCredentialStateOnError(params.R.Context(), cand.CredentialID, cand.StandardizedName, streamKind,
								fmt.Errorf("stream %s (non-resumable)", streamOutcome.Reason))
							e.forceUnpinOnFatalKind(params.R.Context(), fpLease.Holder, cand.CredentialID, streamKind)
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
						// Phase D (2026-06-22): inbound body for audit logging
						InboundBody: sourceBody,
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
					// Phase D (2026-06-22): inbound body for audit logging
					InboundBody: sourceBody,
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
			//nolint:errcheck // best-effort close
			defer resp.Body.Close()
			respBody, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxBodySize)+1))
			if err != nil {
				return nil, err
			}
			if len(respBody) > maxBodySize {
				slog.Warn("upstream response truncated", "size", len(respBody))
				respBody = respBody[:maxBodySize]
			}
			// 2026-07-15: non-stream empty-response failover. The upstream
			// returned HTTP 200 with a well-formed but content-less body
			// (notably NIM: `{"choices":[{"message":{}}],"usage":{...}}`).
			// Previously this fell through to the handler's terminal 502
			// (messages.go:863 / responses.go:691). Now we return a
			// retryable error with KindEmptyResponse BEFORE WriteHeader so
			// the outer candidate loop's transient-error continue branch
			// (executor.go:1814) fails over to the next credential with
			// no client-side error. The handler-side 502 stays as the
			// final fallback when ALL candidates are empty.
			if !params.IsStream && isNonStreamEmptyResponse(respBody) {
				slog.Warn("executor: non-stream empty response, failing over to next candidate",
					"credential_id", cand.CredentialID,
					"provider_id", cand.ProviderID,
					"raw_model", cand.RawModel,
				)
				return nil, &upstreampkg.Error{
					Kind:       errorsx.KindEmptyResponse,
					Message:    "upstream returned empty response (zero content)",
					Body:       append([]byte(nil), respBody...),
					StatusCode: resp.StatusCode,
				}
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
			var qualityBody []byte
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
			// Q2 non-stream response (anthropic client ← openai upstream):
			// the upstream body is still OpenAI-shaped at this point; if
			// the client is Anthropic, convert to Anthropic Messages JSON
			// before writing to the wire. Without this branch the
			// Anthropic SDK receives a `choices[].message.content` shape
			// and rejects it as malformed.
			//
			// Prefer the IR path when the feature flag is on; otherwise
			// fall back to the legacy hook. 2026-06-29 fix — see
			// docs/2026-06-29-protocol-conversion-matrix.md.
			if params.ClientProtocol == "anthropic-messages" && cand.Protocol != "anthropic-messages" {
				if e.IR != nil {
					if irResp, irErr := e.IR.ParseOpenAIResponse(respBody); irErr == nil {
						if converted, serErr := e.IR.SerializeAnthropicResponse(irResp, params.ClientModel); serErr == nil {
							respBody = converted
						} else {
							slog.Warn("q2 ir serialize anthropic response failed; forwarding raw body",
								"error", serErr, "request_id", params.R.Header.Get("X-Request-Id"))
						}
					} else {
						slog.Warn("q2 ir parse openai response failed; forwarding raw body",
							"error", irErr, "request_id", params.R.Header.Get("X-Request-Id"))
					}
				} else if e.ChatResponseToAnthropic != nil {
					if converted, convErr := e.ChatResponseToAnthropic(respBody, params.ClientModel, params.R.Header.Get("X-Request-Id")); convErr == nil {
						respBody = converted
					} else {
						slog.Warn("q2 chat_to_anthropic response convert failed; forwarding raw body",
							"error", convErr, "request_id", params.R.Header.Get("X-Request-Id"))
					}
				}
			}
			if !params.SuppressSuccessWrite {
				for k, vs := range resp.Header {
					for _, v := range vs {
						params.W.Header().Add(k, v)
					}
				}
				params.W.WriteHeader(resp.StatusCode)
				//nolint:errcheck // HTTP write error non-recoverable
				params.W.Write(respBody)
			}
			return &ExecuteResult{
				Response:    resp,
				Candidate:   cand,
				LatencyMs:   latencyMs,
				RequestBody: append([]byte(nil), bodyBytes...),
				// Phase D (2026-06-22): inbound body for audit logging
				InboundBody:  sourceBody,
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
		lastErr = tryErr // 2026-07-03 (Bug #N extension): save for "exhausted retries"
		if _, ok := tryErr.(*retryableError); !ok {
			return nil, tryErr
		}
	}
	// 2026-07-03 (Bug #N extension): return lastErr instead of fmt.Errorf
	// so the precise Kind (e.g. KindContextLength, KindTimeout) is preserved.
	// Before this fix, "exhausted retries" always re-classified to KindTransient.
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("exhausted %d retries for credential %d", effectiveMaxRetries, cand.CredentialID)
}

// finalizeOpenAIUpstreamBody applies prepareRequestBody plus OpenAI-path-only
// transforms (Q2 anthropic→openai conversion, disguise, prompt-cache injection).
func (e *Executor) finalizeOpenAIUpstreamBody(params *ExecParams, cand provider.Candidate, sourceBody []byte) ([]byte, error) {
	// Phase B (2026-06-22): When e.IR is set, use Parse→IR→Serialize instead of
	// the legacy AnthropicToOpenAI callback. This reduces conversion complexity
	// from O(N²) to O(N) and unifies all protocol handling through the IR layer.
	if params.ClientProtocol == "anthropic-messages" && e.IR != nil {
		// Check format_conversion.enabled (provider-level override)
		if e.ProviderSettings != nil {
			if enabled, ok := e.ProviderSettings.GetBool(params.R.Context(), cand.ProviderID, "format_conversion.enabled"); ok && !enabled {
				return nil, fmt.Errorf("format conversion disabled for provider %d (anthropic→openai)", cand.ProviderID)
			}
		}
		// Inject catalog code context for same-provider extension restoration
		if converter, ok := e.IR.(interface {
			SetContext(*domain.TransportContext)
		}); ok {
			ctx := &domain.TransportContext{
				UpstreamCatalogCode: cand.CatalogCode,
				// ClientCatalogCode remains empty until routing layer tracks it
			}
			converter.SetContext(ctx)
		}
		// Parse Anthropic body → IR → Serialize OpenAI
		irReq, err := e.IR.ParseAnthropic(sourceBody)
		if err != nil {
			return nil, fmt.Errorf("ir parse anthropic: %w", err)
		}
		// Override model to outbound model (matching existing behavior)
		irReq.Model = resolveOutboundModel(params, cand)
		// 2026-07-12: Validate and fix request format before serialization.
		// Applies automatic fixes: orphaned tool messages, empty content,
		// tool_call structure, role alternation, system message placement.
		// Prevents upstream rejections from MiniMax 2013, OpenAI item_reference,
		// and other format-related errors.
		//
		// 2026-07-18: thread params.RequestID so each removal logs are
		// request-scoped. Without this, the legacy + IR paths' orphan-
		// removal logs cannot be correlated to gateway request id in
		// journald, defeating the purpose of the fix.
		irReq = ir.ValidateAndFixRequest(irReq, params.RequestID)
		// 2026-07-18 (audit): summarize the IR path's effect so we can
		// prove in production logs that the fix actually fired for the
		// offending request.
		if lr, lf := len(irReq.Messages), len(irReq.Messages); lf != lr {
			slog.Debug("finalizeOpenAIUpstreamBody: IR validate+fix applied",
				"request_id", params.RequestID,
				"path", "anthropic_to_openai_ir",
				"model", params.Model,
				"messages", lf,
			)
		} else {
			slog.Debug("finalizeOpenAIUpstreamBody: IR validate+fix passed",
				"request_id", params.RequestID,
				"path", "anthropic_to_openai_ir",
				"model", params.Model,
				"messages", lf,
			)
		}
		bodyBytes, err := e.IR.SerializeOpenAI(irReq)
		if err != nil {
			return nil, fmt.Errorf("ir serialize openai: %w", err)
		}
		// Apply remaining OpenAI-path transforms (disguise, prompt cache)
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

	// Legacy path (no IR converter set): use existing callbacks
	p := *params
	p.BodyBytes = sourceBody
	bodyBytes := prepareRequestBody(&p, cand)

	// 2026-07-12: Apply format validation even in legacy path.
	// Parse → Validate → Serialize to clean up malformed requests.
	//
	// 2026-07-18: thread params.RequestID + log path/body-size/msg-count
	// so we can prove in prod logs whether validation ran and what it
	// changed. The minmax-m3 tool_call_id_mismatch incident (req
	// 3905e839e0abab5a53efc09222e2d45b) had no logs from this path,
	// making it impossible to confirm whether the sanitizer was hit.
	if e.IR != nil {
		preBodyBytes := len(bodyBytes)
		preMsgs := -1 // populated only on successful parse
		irReq, parseErr := e.IR.ParseOpenAI(bodyBytes)
		if parseErr == nil {
			preMsgs = len(irReq.Messages)
			irReq = ir.ValidateAndFixRequest(irReq, params.RequestID)
			// Override model to outbound model
			irReq.Model = resolveOutboundModel(params, cand)
			bodyBytes, _ = e.IR.SerializeOpenAI(irReq)
			slog.Info("finalizeOpenAIUpstreamBody: legacy IR path validated",
				"request_id", params.RequestID,
				"path", "legacy_with_ir",
				"model", params.Model,
				"provider_id", cand.ProviderID,
				"credential_id", cand.CredentialID,
				"raw_model", cand.RawModel,
				"pre_body_bytes", preBodyBytes,
				"post_body_bytes", len(bodyBytes),
				"pre_messages", preMsgs,
				"post_messages", len(irReq.Messages),
			)
		} else {
			slog.Warn("legacy path: IR parse failed, skipping validation",
				"request_id", params.RequestID,
				"path", "legacy_with_ir_parse_failed",
				"model", params.Model,
				"provider_id", cand.ProviderID,
				"raw_model", cand.RawModel,
				"pre_body_bytes", preBodyBytes,
				"error", parseErr.Error(),
			)
		}
	} else {
		// 2026-07-18 (e.IR == nil): legacy path WITHOUT IR converter.
		// applyInlineValidation (inline_validation.go) was defined 2026-07-12
		// but had zero callers until 2026-07-18. THIS wiring is the fix
		// for the minimax-m3 tool_call_id_mismatch incident class
		// (req 3905e839e0abab5a53efc09222e2d45b): without it, orphan
		// tool messages reach upstream unmangled; MiniMax 4xx-cascades;
		// gateway loops 90s; client gets 503.
		preBodyBytes := len(bodyBytes)
		bodyBytes = applyInlineValidation(bodyBytes, params.RequestID)
		postBodyBytes := len(bodyBytes)
		slog.Info("finalizeOpenAIUpstreamBody: legacy path (no IR) + inline validation",
			"request_id", params.RequestID,
			"path", "legacy_no_ir_with_inline",
			"model", params.Model,
			"provider_id", cand.ProviderID,
			"credential_id", cand.CredentialID,
			"raw_model", cand.RawModel,
			"pre_body_bytes", preBodyBytes,
			"post_body_bytes", postBodyBytes,
			"delta_bytes", postBodyBytes-preBodyBytes,
		)
	}

	if e.NormalizeOpenAITools != nil {
		bodyBytes = e.NormalizeOpenAITools(bodyBytes)
	}
	if params.ClientProtocol == "anthropic-messages" {
		// Phase 3.2: Check format_conversion.enabled (provider-level override)
		if e.ProviderSettings != nil {
			if enabled, ok := e.ProviderSettings.GetBool(params.R.Context(), cand.ProviderID, "format_conversion.enabled"); ok && !enabled {
				return nil, fmt.Errorf("format conversion disabled for provider %d (anthropic→openai)", cand.ProviderID)
			}
		}
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

// resolveOutboundModel picks the upstream model field for the CURRENT candidate.
//
// Selection rules (2026-07-14 NIM fix):
//  1. If the resolved transform template explicitly names an upstream model
//     (params.Transform.OutboundModel non-empty) AND it differs from the
//     candidate's offer raw name, use the template. This preserves the
//     historical "admin-override transform wins" path relied on by
//     executor_glm_test.go::TestResolveOutboundModel_ExplicitTransformWins.
//  2. Otherwise, if params.OutboundModel is non-empty AND it differs from the
//     candidate's offer raw name, use params.OutboundModel. This preserves
//     the legacy call sites that pre-set OutboundModel from the FIRST
//     candidate's outboundForLog when that value still differs from the
//     current candidate's offer.
//  3. Otherwise use the CURRENT candidate's cand.RawModel (which is already
//     COALESCE(outbound_model_name, raw_model_name) from model_offers). This
//     guarantees that retries/failovers to another candidate send the new
//     candidate's own upstream model ID — fixing the previous behaviour where
//     the FIRST candidate's value was applied unconditionally and caused
//     cross-provider 404s (e.g. NVIDIA NIM receiving "glm-5.2" instead of the
//     required "z-ai/glm-5.2").
//
// handlers/messages/responses now set params.OutboundModel = clientModel
// (NOT the first candidate's outboundForLog), so step (2) is effectively
// dormant in the runtime path; we keep it for direct callers (tests, async
// retry) that still thread an explicit outbound.
//
// Mirrors Python prepare_candidate → render_outbound_model() default path.
func resolveOutboundModel(params *ExecParams, cand provider.Candidate) string {
	if params == nil {
		return cand.RawModel
	}
	if params.Transform != nil && params.Transform.OutboundModel != "" {
		if cand.OfferRawModel == "" || params.Transform.OutboundModel != cand.OfferRawModel {
			return params.Transform.OutboundModel
		}
	}
	if params.OutboundModel != "" && params.OutboundModel != params.ClientModel {
		if cand.OfferRawModel == "" || params.OutboundModel != cand.OfferRawModel {
			return params.OutboundModel
		}
	}
	return cand.RawModel
}

func prepareRequestBody(params *ExecParams, cand provider.Candidate) []byte {
	outboundModel := resolveOutboundModel(params, cand)

	bodyBytes := params.BodyBytes
	if outboundModel != "" && outboundModel != params.ClientModel {
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
		bodyBytes = transformation.ApplyRequestWhitelist(
			bodyBytes,
			params.Transform.PassthroughFields,
			params.Transform.StripRequestFields,
			cand.Protocol,
		)
	}
	if !transformation.IsToolUseCapable(cand.CatalogCode, cand.Protocol) && transformation.NeedsToolCollapse(bodyBytes) {
		bodyBytes = transformation.CollapseToolHistory(bodyBytes)
	}
	bodyBytes = transformation.ApplyCapabilitySanitizer(bodyBytes, cand.CatalogCode)
	bodyBytes = transformation.MergeConsecutiveMessages(bodyBytes)
	// Client-side context window enforcement for Q1/Q2/Q3 openai protocol.
	// Q4 (anthropic-messages) is handled in prepareAnthropicRequestBody
	// (executor_anthropic.go). See transform/ctx_compress.go for rationale:
	// upstreams like minimax trim server-side on direct calls, but proxy
	// clients must trim at the gateway.
	if cand.Protocol != "anthropic-messages" && cand.ContextWindow != nil {
		bodyBytes = transformation.CompressMessagesIfNeeded(bodyBytes, *cand.ContextWindow)
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
	if hasSessionID(params) || params.IsStream {
		return context.WithTimeout(context.WithoutCancel(params.R.Context()), timeout)
	}
	return context.WithTimeout(params.R.Context(), timeout)
}
