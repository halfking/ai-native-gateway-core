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

func (c *ChatExecutor) WriteNonStreamResponse(w http.ResponseWriter, resp *http.Response, clientModel string) error {
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
	bodyBytes := prepareRequestBody(params, cand)
	outboundModel := params.OutboundModel
	if outboundModel == "" {
		outboundModel = cand.RawModel
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

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			delay := time.Duration(500*(1<<(attempt-1))) * time.Millisecond
			select {
			case <-params.R.Context().Done():
				return nil, params.R.Context().Err()
			case <-time.After(delay):
			}
		}

		result, tryErr := func() (*ExecuteResult, error) {
			var reqPool *pool.Pool
			var upstreamURL string
			if cand.Protocol == "anthropic-messages" {
				upstreamURL = upstreamurl.MessagesURL(cand.BaseURL)
			} else {
				upstreamURL = upstreamurl.ChatCompletionsURL(cand.BaseURL)
			}

			req, err := http.NewRequestWithContext(
				params.R.Context(),
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
					if errorsx.IsContextLength(errKind) && attempt == 0 &&
						cand.Protocol != "anthropic-messages" && cand.ContextWindow != nil {
						slog.Info("context_length 4xx → client-side trim retry",
							"credential_id", cand.CredentialID,
							"model", cand.RawModel,
							"context_window", *cand.ContextWindow,
							"status", resp.StatusCode,
						)
						trimmed := transform.CompressMessagesIfNeeded(bodyBytes, *cand.ContextWindow)
						if len(trimmed) < len(bodyBytes) {
							bodyBytes = trimmed
							return nil, &retryableError{err: fmt.Errorf("upstream %d", resp.StatusCode)}
						}
						// Trim made no progress (already minimal) — fall
						// through to the 4xx bubble-up.
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
				var streamOutcome StreamOutcome
				if params.StreamWrapper != nil {
					streamOutcome = params.StreamWrapper(params.W, resp, e.Normalize, params.Capture)
				} else if e.StreamChat != nil {
					streamOutcome = e.StreamChat(params.W, resp, params.ClientModel, outboundModel, e.Normalize, params.Capture, params.ToolsRequested)
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
					}, &streamInterruptedError{reason: streamOutcome.Reason, credentialID: cand.CredentialID, resumable: isResumable, kind: streamKind}
				}
				return &ExecuteResult{
					Response:    resp,
					Candidate:   cand,
					LatencyMs:   latencyMs,
					RequestBody: append([]byte(nil), bodyBytes...),
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
			if params.ClientModel != "" {
				respBody = replaceModelInResponseBody(respBody, params.ClientModel)
			}
			if e.XMLCoerceNonStream != nil {
				respBody = e.XMLCoerceNonStream(respBody, params.ToolsRequested)
			}
			if e.Normalize != nil {
				respBody = e.Normalize(respBody, false)
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
			}, nil
		}()

		if tryErr == nil {
			return result, nil
		}
		if _, ok := tryErr.(*retryableError); !ok {
			return nil, tryErr
		}
	}
	return nil, fmt.Errorf("exhausted %d retries for credential %d", maxRetries, cand.CredentialID)
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
	// Client-side context window enforcement. Q4 (anthropic-messages
	// passthrough) is intentionally skipped — see transform/ctx_compress.go
	// for the rationale (byte-level passthrough contract). For Q1/Q2/Q3
	// (openai protocol) we drop the oldest non-system messages until the
	// estimated prompt tokens fit under context_window * 0.85. This is the
	// counterpart to minimax's server-side sliding-window trim: when a
	// client (Cursor/RooCode/opencode) is on the proxy path, the upstream
	// no longer trims for us, so we have to do it ourselves.
	if cand.Protocol != "anthropic-messages" && cand.ContextWindow != nil {
		bodyBytes = transform.CompressMessagesIfNeeded(bodyBytes, *cand.ContextWindow)
	}
	return bodyBytes
}

// executeAnthropic is the Q3/Q4 (anthropic-messages upstream) path.
// The real implementation is in executor_anthropic.go; it lives there
// (not in this file) so that OpenAI-shape assumptions cannot leak into
// the Anthropic path.
