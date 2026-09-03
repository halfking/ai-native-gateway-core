package executors

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
	"github.com/kaixuan/llm-gateway-go/domain"                    //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"       //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/transformation"    //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/internal/ir"
	"github.com/kaixuan/llm-gateway-go/internal/paramguard"
	"github.com/kaixuan/llm-gateway-go/internal/paramreg"
	"github.com/kaixuan/llm-gateway-go/internal/textsplit"
	"github.com/kaixuan/llm-gateway-go/internal/upstreamurl"
	"github.com/kaixuan/llm-gateway-go/pool"
	"github.com/kaixuan/llm-gateway-go/provider"
	upstreampkg "github.com/kaixuan/llm-gateway-go/upstream"
)

// maxPassthroughErrorBody caps how many bytes of an upstream 4xx body are
// forwarded verbatim to the client on the non-retryable raw-passthrough path
// (see executeAnthropic). Vendor error envelopes are small (a few KB), so 64
// KiB is generous while preventing a pathological/malicious upstream from
// forcing the gateway to buffer a huge body into memory before echoing it.
const maxPassthroughErrorBody = 64 << 10 // 64 KiB

// readAndDrainErrorBody captures at most maxPassthroughErrorBody bytes for
// classification or passthrough, then consumes the rest of the response.
// Reading through LimitReader (rather than relying on one Read call) handles
// short reads and preserves HTTP connection reuse. The caller owns closing the
// response body.
func readAndDrainErrorBody(body io.Reader) ([]byte, error) {
	if body == nil {
		return nil, nil
	}

	captured, readErr := io.ReadAll(io.LimitReader(body, maxPassthroughErrorBody))
	// Always attempt to consume the remainder, even when the bounded read
	// reports an error. Some response bodies can return data with an error and
	// still expose a readable tail; leaving it unread prevents connection reuse.
	_, drainErr := io.Copy(io.Discard, body)
	if readErr != nil {
		return captured, readErr
	}
	return captured, drainErr
}

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
	// P1-2 fix (2026-08-28): Added ctx parameter for context propagation to gate.
	PassthroughStream func(ctx context.Context, w http.ResponseWriter, resp *http.Response) StreamOutcome
	// OpenAITranslator converts Anthropic SSE upstream into OpenAI SSE
	// chunks for the Q3 path (openai client -> anthropic upstream).
	// When nil, the Q3 stream path falls back to PassthroughStream
	// (preserving the pre-fix behavior; the OpenAI client will fail to
	// parse the result, but a misconfig won't take the service down).
	// P1-2 fix (2026-08-28): Added ctx parameter for context propagation to gate.
	OpenAITranslator func(ctx context.Context, w http.ResponseWriter, resp *http.Response, clientModel, outboundModel, requestID string, capture *audit.StreamCapture) StreamOutcome
	// ResponsesTranslator (Phase E, 2026-07-01) converts Anthropic SSE
	// upstream into OpenAI Responses API SSE events. Wired only when
	// ClientProtocol == "openai-responses".
	// P1-2 fix (2026-08-28): Added ctx parameter for context propagation to gate.
	ResponsesTranslator func(ctx context.Context, w http.ResponseWriter, resp *http.Response, clientModel, outboundModel, requestID string, capture *audit.StreamCapture) StreamOutcome
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
	// IR is the unified protocol-conversion interface (Phase D, 2026-06-22).
	// When set, the Q3 non-stream response path uses
	// IR.ParseAnthropicResponse + IR.SerializeOpenAIResponse instead of
	// the legacy ChatResponseConverter callback. Nil falls back to the
	// callback path.
	IR IRConverter
	// ProviderID is the provider_id of the upstream candidate, used for
	// per-provider circuit breaker isolation in IR conversions (added
	// 2026-08-09). Set during AnthropicExecutor construction from cand.ProviderID.
	ProviderID int
}

var _ ProtocolHandler = (*AnthropicExecutor)(nil)

const anthropicVersion = "2023-06-01"

// applyClientAnthropicHeaders forwards the client's anthropic-version and
// anthropic-beta headers onto the upstream request (whitelist only — no
// arbitrary client header leakage).
//
//   - anthropic-version: prefers the client value, falling back to whatever
//     BuildRequest already set (the compiled anthropicVersion constant) when
//     the client omitted it.
//   - anthropic-beta: clients may send multiple comma-separated flags or
//     repeat the header; non-empty values are joined into a single comma-
//     separated string. Empty headers are skipped. If no non-empty beta
//     values exist, no header is added.
func applyClientAnthropicHeaders(dst, src http.Header) {
	if dst == nil || src == nil {
		return
	}
	if cv := strings.TrimSpace(src.Get("anthropic-version")); cv != "" {
		dst.Set("anthropic-version", cv)
	}
	betaVals := src.Values("anthropic-beta")
	var nonEmpty []string
	for _, v := range betaVals {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			nonEmpty = append(nonEmpty, trimmed)
		}
	}
	if len(nonEmpty) > 0 {
		dst.Set("anthropic-beta", strings.Join(nonEmpty, ","))
	}
}

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

func (a *AnthropicExecutor) WriteNonStreamResponse(w http.ResponseWriter, resp *http.Response, clientModel, qualityFixMode string, qualitySignals *QualitySignals) ([]byte, error) {
	//nolint:errcheck // best-effort close
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	// Q3 mode (openai client -> anthropic upstream): translate the
	// Anthropic Messages response body into an OpenAI chat.completion
	// body so the OpenAI parser doesn't choke on the shape mismatch.
	// Without this branch the OpenAI client receives a Messages-format
	// JSON body (with `content[].text` and `stop_reason`) and reports
	// "供应商错误" to the end-user.
	//
	// Phase E (2026-07-01): Responses API client target. The wire
	// shape differs (output[] with message / function_call / reasoning
	// items, not chat.completion.choices[]), so dispatch to
	// IR.SerializeResponsesResponse when ClientProtocol == "openai-responses".
	if a.ClientProtocol != "anthropic-messages" {
		if a.IR != nil {
			var irScoped IRConverter
			if scoped, ok := a.IR.(ProviderScoped); ok {
				irScoped = scoped.WithProviderScope(a.ProviderID)
			} else {
				irScoped = a.IR
			}
			irResp, irErr := irScoped.ParseAnthropicResponse(body)
			if irErr != nil {
				return nil, &upstreampkg.Error{
					Kind:       errorsx.KindConversion,
					Message:    "parse Anthropic response for client protocol",
					Err:        irErr,
					StatusCode: resp.StatusCode,
				}
			}
			var (
				converted []byte
				serErr    error
			)
			if a.ClientProtocol == "openai-responses" {
				converted, serErr = irScoped.SerializeResponsesResponse(irResp, clientModel)
			} else {
				converted, serErr = irScoped.SerializeOpenAIResponse(irResp, clientModel)
			}
			if serErr != nil {
				return nil, &upstreampkg.Error{
					Kind:       errorsx.KindConversion,
					Message:    "convert Anthropic response to client protocol",
					Err:        serErr,
					StatusCode: resp.StatusCode,
				}
			}
			body = converted
		} else if a.ChatResponseConverter != nil {
			if a.ClientProtocol == "openai-responses" {
				return nil, &upstreampkg.Error{
					Kind:       errorsx.KindConversion,
					Message:    "Responses API response conversion requires IR converter",
					StatusCode: resp.StatusCode,
				}
			}
			converted, convErr := a.ChatResponseConverter(body, clientModel)
			if convErr != nil {
				return nil, &upstreampkg.Error{
					Kind:       errorsx.KindConversion,
					Message:    "convert Anthropic response to OpenAI response",
					Err:        convErr,
					StatusCode: resp.StatusCode,
				}
			}
			body = converted
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
		copyNonStreamResponseHeaders(w.Header(), resp.Header, len(body))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, err = w.Write(body)
		return body, err
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
	copyNonStreamResponseHeaders(w.Header(), resp.Header, len(body))
	w.WriteHeader(resp.StatusCode)
	_, err = w.Write(body)
	return body, err
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

// P1-2 fix (2026-08-28): Added ctx parameter for context propagation to gate.
func (a *AnthropicExecutor) StreamResponse(ctx context.Context, w http.ResponseWriter, resp *http.Response) StreamOutcome {
	// Q3 mode (openai client -> anthropic upstream): translate the
	// upstream Anthropic SSE stream into OpenAI SSE chunks so the
	// OpenAI parser receives data: {...} chunks instead of the raw
	// event: ... lines. Falls back to PassthroughStream if the
	// translator hook isn't wired (defensive: a misconfig shouldn't
	// take the service down).
	//
	// Phase E (2026-07-01): Responses API routing. When ClientProtocol
	// is "openai-responses", dispatch to ResponsesTranslator instead so
	// the client receives `response.output_text.delta` events instead of
	// `chat.completion.chunk`. Translator selection priority:
	//   1. ResponsesTranslator (Responses client target)
	//   2. OpenAITranslator (Chat Completions client target)
	//   3. PassthroughStream (defensive fallback)
	if a.ClientProtocol == "openai-responses" {
		if a.ResponsesTranslator != nil {
			return a.ResponsesTranslator(ctx, w, resp, "", "", "", nil)
		}
	} else if a.ClientProtocol != "anthropic-messages" {
		if a.OpenAITranslator != nil {
			return a.OpenAITranslator(ctx, w, resp, "", "", "", nil)
		}
	}
	if a.PassthroughStream != nil {
		return a.PassthroughStream(ctx, w, resp)
	}
	return defaultAnthropicPassthrough(ctx, w, resp)
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
// P1-2 fix (2026-08-28): Added ctx parameter for context propagation to gate.
func defaultAnthropicPassthrough(ctx context.Context, w http.ResponseWriter, resp *http.Response) StreamOutcome {
	// Sole consumer of this body (only reached when the PassthroughStream hook
	// is unwired), so closing it here is safe and required for connection reuse.
	//nolint:errcheck // best-effort close
	defer resp.Body.Close()
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
	// Phase B (2026-06-22): When e.IR is set, use Parse→IR→Serialize for Q3
	// (openai → anthropic) conversion instead of the legacy ChatToAnthropic callback.
	//
	// FIX (2026-06-23): Only convert when BOTH client and upstream use different protocols.
	// Before: converted whenever client != anthropic, even if upstream was also openai.
	// After: only convert when client=openai AND upstream=anthropic.
	needsConversion := params.ClientProtocol != "anthropic-messages" &&
		params.ClientProtocol != "" &&
		cand.Protocol == "anthropic-messages"

	if needsConversion && e.IR != nil {
		// Check format_conversion.enabled (provider-level override)
		if e.ProviderSettings != nil {
			if enabled, ok := e.ProviderSettings.GetBool(params.R.Context(), cand.ProviderID, "format_conversion.enabled"); ok && !enabled {
				return nil, fmt.Errorf("format conversion disabled for provider %d (openai→anthropic)", cand.ProviderID)
			}
		}
		// Select a provider-scoped converter before injecting request metadata.
		var irScoped IRConverter
		if scoped, ok := e.IR.(ProviderScoped); ok {
			irScoped = scoped.WithProviderScope(cand.ProviderID)
		} else {
			irScoped = e.IR
		}
		if converter, ok := irScoped.(interface {
			SetContext(*domain.TransportContext)
		}); ok {
			// V6-W1.6 T2: carry the dispatch request class (定时请求) into the
			// converter so the parsed IR is class-stamped.
			converter.SetContext(&domain.TransportContext{
				UpstreamCatalogCode: cand.CatalogCode,
				ProviderID:          cand.ProviderID,
				RequestClass:        string(ir.ClassOf(params.DispatchDueAt)),
				DueAt:               params.DispatchDueAt,
			})
		}
		// Parse OpenAI body → IR → Serialize Anthropic
		irReq, err := irScoped.ParseOpenAI(sourceBody)
		if err != nil {
			// 2026-08-08 P0 Fix: when the IR stream-side circuit breaker is
			// OPEN (process-local counter, see
			// domains/transformation/circuit_breaker.go), factory.Pick()
			// already routes stream requests to the legacy path, but the
			// non-stream conversion path here runs the IR converter
			// unconditionally and returns a hard error — producing 503 for
			// every Claude-sonnet-5 request whose sole credential trips
			// the IR circuit. Fall back to the legacy ChatToAnthropic
			// callback (set up by cmd/gateway/main.go:984) so the request
			// still reaches upstream while the IR layer recovers.
			if errors.Is(err, transformation.ErrConverterCircuitOpen) && e.ChatToAnthropic != nil {
				slog.Warn("ir_converter_circuit_open_fallback_to_legacy_anthropic",
					"request_id", params.RequestID,
					"provider_id", cand.ProviderID,
					"credential_id", cand.CredentialID,
					"raw_model", cand.RawModel,
					"client_model", params.ClientModel,
					"stage", "parse_openai",
				)
				return e.legacyAnthropicBody(params, cand, sourceBody)
			}
			// 2026-08-12 P0 fix: preserve Kind=KindToolCallIdMismatch so the
			// outer RecordFailure() in executor_common.go:104 can see it
			// (and skip circuit-breaker counting via IsClientBug check at
			// domains/credential/breaker.go:320). Without this typed wrap,
			// classifyKind() in executor_common.go:124 falls back to
			// ClassifyError() regex matching on the wrapped string, which
			// does NOT match toolCallIdMismatchRe (that pattern only
			// matches 2013 error codes) and regresses to KindTransient —
			// the direct cause of "tool_call validation failed" cascading
			// into circuit-open flicker every time a Cursor/Claude Code
			// client compresses history and drops a tool_use block.
			// Mirrors the 2026-07-03 P0 fix at executor_chat.go:879-895.
			return nil, &upstreampkg.Error{
				Kind:    errorsx.KindToolCallIdMismatch,
				Message: fmt.Sprintf("ir parse openai: %s", err.Error()),
				Err:     err,
			}
		}
		// Override model to outbound model (matching existing behavior)
		irReq.Model = resolveOutboundModel(params, cand)
		// Pass target provider catalog code to IR so the serializer can handle
		// provider-specific protocol variants (e.g. MiniMax uses tool_call_id
		// instead of the Anthropic-standard tool_use_id for tool_result blocks).
		// Fixes MiniMax-M3 tool_call_id not found (2013) bug.
		irReq.TargetProvider = cand.CatalogCode
		bodyBytes, err := irScoped.SerializeAnthropic(irReq)
		if err != nil {
			// 2026-08-08 P0 Fix: same IR-circuit-open fallback as ParseOpenAI.
			// SerializeAnthropic routes through the same process-local
			// breaker; on OPEN we drop back to the legacy ChatToAnthropic
			// callback so the upstream still receives a well-formed body.
			if errors.Is(err, transformation.ErrConverterCircuitOpen) && e.ChatToAnthropic != nil {
				slog.Warn("ir_converter_circuit_open_fallback_to_legacy_anthropic",
					"request_id", params.RequestID,
					"provider_id", cand.ProviderID,
					"credential_id", cand.CredentialID,
					"raw_model", cand.RawModel,
					"client_model", params.ClientModel,
					"stage", "serialize_anthropic",
				)
				return e.legacyAnthropicBody(params, cand, sourceBody)
			}
			// 2026-08-12 P0 fix: preserve Kind=KindToolCallIdMismatch so
			// circuit-breaker RecordFailure() recognises this as a client
			// bug (IsClientBug=true) and skips credential state degradation.
			// Without this, "tool_call validation failed" surfaces as
			// KindTransient via the wrapped fmt.Errorf string and triggers
			// circuit-open flicker on the credential (see 2026-08-12
			// prod incident: claude-sonnet-5 / cred 17 / 130dao). Mirrors
			// the 2026-07-03 P0 fix at executor_chat.go:879-895.
			return nil, &upstreampkg.Error{
				Kind:    errorsx.KindToolCallIdMismatch,
				Message: fmt.Sprintf("ir serialize anthropic: %s", err.Error()),
				Err:     err,
			}
		}
		// Apply remaining Anthropic-path transforms (sanitize, fix, validate)
		if e.SanitizeAnthropicTools != nil {
			bodyBytes = e.SanitizeAnthropicTools(bodyBytes)
		}
		fixedBytes, fixErr := transformation.FixAnthropicMessages(bodyBytes)
		if fixErr != nil {
			return nil, fmt.Errorf("fix anthropic messages: %w", fixErr)
		}
		bodyBytes = fixedBytes
		if valErr := transformation.ValidateAnthropicMessages(bodyBytes); valErr != nil {
			slog.Warn("invalid anthropic message sequence after fix",
				"error", valErr,
				"tenant_id", params.TenantID,
			)
		}
		return e.finalizeAnthropicRequestBody(params, cand, bodyBytes), nil
	}

	// Legacy path (no IR converter set): use existing callbacks
	return e.legacyAnthropicBody(params, cand, sourceBody)

}

// legacyAnthropicBody performs the legacy ChatToAnthropic conversion path.
// Extracted so the IR-converter circuit-open fallback above can reuse the
// same logic instead of duplicating it.
func (e *Executor) legacyAnthropicBody(params *ExecParams, cand provider.Candidate, sourceBody []byte) ([]byte, error) {
	bodyBytes := append([]byte(nil), sourceBody...)

	// Q3 conversion: OpenAI /v1/chat/completions → Anthropic /v1/messages.
	// FIX (2026-06-23): Only convert when upstream protocol is anthropic-messages.
	// This prevents converting OpenAI→Anthropic when talking to OpenAI-compatible upstreams like MiniMax.
	needsConversion := params.ClientProtocol != "anthropic-messages" &&
		params.ClientProtocol != "" &&
		cand.Protocol == "anthropic-messages"
	if needsConversion {
		// Phase 3.2: Check format_conversion.enabled (provider-level override)
		if e.ProviderSettings != nil {
			if enabled, ok := e.ProviderSettings.GetBool(params.R.Context(), cand.ProviderID, "format_conversion.enabled"); ok && !enabled {
				return nil, fmt.Errorf("format conversion disabled for provider %d (openai→anthropic)", cand.ProviderID)
			}
		}
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

	// FIX (2026-06-23): Only apply Anthropic-specific transforms when upstream is anthropic-messages.
	// MiniMax and other openai-completions upstreams should receive OpenAI format unchanged.
	if cand.Protocol == "anthropic-messages" {
		// MiniMax anthropic-messages rejects tools carrying OpenAI/custom type
		// wrappers (error 2013: invalid tool type). Always emit name/input_schema.
		if e.SanitizeAnthropicTools != nil {
			bodyBytes = e.SanitizeAnthropicTools(bodyBytes)
		}

		// 2026-06-21: Fix empty response issue (Request ID: 92ef59e52efae25c396d0504efbfa2e6)
		// Claude API doesn't support "tool" role and requires user/assistant alternation.
		// Convert "tool" role to "user" + tool_result block and merge consecutive messages.
		fixedBytes, fixErr := transformation.FixAnthropicMessages(bodyBytes)
		if fixErr != nil {
			return nil, fmt.Errorf("fix anthropic messages: %w", fixErr)
		}
		bodyBytes = fixedBytes

		// Validate message sequence (warning mode only, don't block requests)
		if valErr := transformation.ValidateAnthropicMessages(bodyBytes); valErr != nil {
			slog.Warn("invalid anthropic message sequence after fix",
				"error", valErr,
				"tenant_id", params.TenantID,
			)
		}
	}

	return e.finalizeAnthropicRequestBody(params, cand, bodyBytes), nil
}

func (e *Executor) finalizeAnthropicRequestBody(params *ExecParams, cand provider.Candidate, bodyBytes []byte) []byte {
	// Candidate-aware enforcement must run for both the IR and legacy paths.
	// The gateway's 2M admission ceiling is not the provider context window;
	// trim the serialized Anthropic body at the shared 80% provider threshold.
	if cand.ContextWindow != nil {
		reserve := transformation.OutputTokenReserve(bodyBytes, "anthropic-messages")
		bodyBytes = transformation.CompressAnthropicMessagesIfNeededWithReserve(bodyBytes, *cand.ContextWindow, reserve)
	}
	requestCtx := context.Background()
	if params != nil && params.R != nil {
		requestCtx = params.R.Context()
	}
	if out, applied := e.runCompressionStrategies(requestCtx, bodyBytes, cand.ContextWindow, compression.ModeAutoThreshold, forceCompression(params)); applied {
		bodyBytes = out
	}
	return paramguard.Apply(bodyBytes, paramreg.DialectAnthropic)
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
		// Phase D (2026-06-22): IR response converter for Q3 non-stream.
		// When set, WriteNonStreamResponse uses IR instead of the legacy
		// ChatResponseConverter callback.
		IR:         e.IR,
		ProviderID: cand.ProviderID,
	}
	if e.AnthropicPassthroughStream != nil {
		clientModel := params.ClientModel
		requestID := diagnosticRequestID(params)
		// P1-2 fix (2026-08-28): capture ctx for context propagation to gate.
		capturedCtx := params.R.Context()
		// Track C C5 (2026-06-21): the capturer is built by the main.go
		// wrapper that owns the AnthropicPassthroughStream closure (it
		// has access to pendingStore + the upstream resp to check the
		// session header). This layer just forwards nil because the
		// upstream http.Response passed in already carries the session
		// id in its headers, so the main.go wrapper can re-derive pc
		// from resp.Request.Header on each call. To avoid double-build
		// we currently pass nil here; the real wiring is in main.go.
		// P1-2 fix (2026-08-28): lambda accepts ctx parameter.
		ae.PassthroughStream = func(ctx context.Context, w http.ResponseWriter, resp *http.Response) StreamOutcome {
			return e.AnthropicPassthroughStream(capturedCtx, w, resp, clientModel, outboundModel, requestID, params.Capture, nil)
		}
	}
	if e.AnthropicToOpenAIStream != nil && params.ClientProtocol != "anthropic-messages" {
		clientModel := params.ClientModel
		requestID := diagnosticRequestID(params)
		// P1-2 fix (2026-08-28): capture ctx for context propagation to gate.
		capturedCtx := params.R.Context()
		ae.OpenAITranslator = func(ctx context.Context, w http.ResponseWriter, resp *http.Response, _, _, _ string, _ *audit.StreamCapture) StreamOutcome {
			return e.AnthropicToOpenAIStream(capturedCtx, w, resp, clientModel, outboundModel, requestID, params.Capture, nil)
		}
	}
	// Phase E (2026-07-01): wire ResponsesTranslator when the client
	// uses the Responses API. This is the streaming counterpart of the
	// IR-based non-stream response path that runs through
	// SerializeResponsesResponse (see executor_anthropic.go:WriteNonStreamResponse).
	if e.AnthropicToResponsesStream != nil && params.ClientProtocol == "openai-responses" {
		clientModel := params.ClientModel
		requestID := diagnosticRequestID(params)
		// P1-2 fix (2026-08-28): capture ctx for context propagation to gate.
		capturedCtx := params.R.Context()
		ae.ResponsesTranslator = func(ctx context.Context, w http.ResponseWriter, resp *http.Response, _, _, _ string, _ *audit.StreamCapture) StreamOutcome {
			return e.AnthropicToResponsesStream(capturedCtx, w, resp, clientModel, outboundModel, requestID, params.Capture, nil)
		}
	}
	if e.AnthropicToChatResponse != nil && params.ClientProtocol != "anthropic-messages" {
		ae.ChatResponseConverter = e.AnthropicToChatResponse
	}
	if e.AnthropicPassthroughStream != nil {
		clientModel := params.ClientModel
		requestID := diagnosticRequestID(params)
		// P1-2 fix (2026-08-28): capture ctx for context propagation to gate.
		capturedCtx := params.R.Context()
		// Second assignment is defensive (the if-block at line 411
		// already assigned this); the capturer plumbing is identical.
		// P1-2 fix (2026-08-28): lambda accepts ctx parameter.
		ae.PassthroughStream = func(ctx context.Context, w http.ResponseWriter, resp *http.Response) StreamOutcome {
			return e.AnthropicPassthroughStream(capturedCtx, w, resp, clientModel, outboundModel, requestID, params.Capture, nil)
		}
	}

	contextLenRecovery := contextLengthRecoveryState{}

	// 2026-09-01 fix (queue/concurrency audit P1): context-length recovery
	// success flag. When set, the next iteration should NOT consume the retry
	// budget (attempt is decremented right after the loop increment), so the
	// compressed body is actually sent even when maxRetries==0. Mirrors the
	// executor_chat.go ctxLenRecoveryRetry mechanism.
	ctxLenRecoveryRetry := false
	var lastErr error // 2026-07-03 (Bug #N extension): preserve last error for "exhausted retries"
	for attempt := 0; attempt <= maxRetries; attempt++ {
		// 2026-09-01 fix: if the previous iteration succeeded in context-length
		// recovery, cancel the increment so the compressed retry is free.
		if ctxLenRecoveryRetry {
			ctxLenRecoveryRetry = false
			attempt--
		}
		if attempt > 0 {
			delay := time.Duration(500*(1<<(attempt-1))) * time.Millisecond
			select {
			case <-params.R.Context().Done():
				return nil, params.R.Context().Err()
			case <-time.After(delay):
			}
		}

		result, tryErr := e.executeAnthropicOnce(params, cand, ae, sourceBody, bodyBytes, outboundModel, tTotal, fpLease)
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
			switch e.handleContextLengthRecovery(params.R.Context(), params, cand, &sourceBody, &contextLenRecovery, cle.status, cle.body) {
			case ctxLenRetry:
				bodyBytes, err = e.prepareAnthropicRequestBody(params, cand, sourceBody)
				if err != nil {
					return nil, err
				}
				// 2026-09-01 fix: recovery succeeded — the compressed body
				// retry must not consume the retry budget, otherwise with
				// maxRetries==0 the compressed payload is never sent and the
				// loop falls through to the generic "exhausted 0 retries".
				ctxLenRecoveryRetry = true
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
	return nil, fmt.Errorf("exhausted %d retries for credential %d", maxRetries, cand.CredentialID)
}

// anthropicReadBodyError classifies a non-stream upstream body read failure
// and wraps it following the file's 4xx/5xx convention: only
// errorsx.IsRetryable kinds get the retryableError wrapper (which drives the
// same-credential retry ladder in executeAnthropic). A client cancellation
// reads as KindCanceled and must surface unwrapped so the loop returns it
// immediately instead of retrying a dead request with backoff.
func anthropicReadBodyError(err error, resp *http.Response) error {
	kind := errorsx.ClassifyError(err, nil)
	wrapped := &upstreampkg.Error{
		Kind:       kind,
		Message:    fmt.Sprintf("read anthropic upstream response: %v", err),
		Err:        err,
		StatusCode: resp.StatusCode,
		RetryAfter: upstreampkg.RetryAfterFromHeaders(resp.Header),
	}
	if !errorsx.IsRetryable(kind) {
		return wrapped
	}
	return &retryableError{err: wrapped}
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
	sourceBody []byte,
	bodyBytes []byte,
	outboundModel string,
	tTotal time.Time,
	fpLease *credentialfpslot.Lease,
) (*ExecuteResult, error) {
	req, err := ae.BuildRequest(cand, bodyBytes, params.IsStream)
	if err != nil {
		return nil, err
	}
	// 2026-08-04: forward the client's anthropic-version and anthropic-beta
	// headers instead of discarding them. Previously BuildRequest hardcoded
	// anthropic-version to "2023-06-01" and dropped anthropic-beta entirely
	// (IRExtensionExtractor captured both into ExtensionsBag.Headers, but
	// IRExtensionRestorer explicitly skips Headers and BuildRequest never read
	// them — a dead chain). Agent clients rely on beta features such as
	// extended thinking, prompt caching and interleaved-thinking; losing the
	// beta header caused the upstream to behave differently than a direct
	// connection — early stop_reason, missing tool_use blocks — which the
	// client experienced as a truncated/interrupted task.
	applyClientAnthropicHeaders(req.Header, params.R.Header)
	req.Header.Set("X-Request-Id", diagnosticRequestID(params))
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

	// Apply disguise headers (User-Agent / Accept-Language).
	// Slot-bound sessions get a stable UA per slot (no churn between
	// requests). Stateless requests (no fpLease) fall back to a random
	// pick so the pool still gets exercised.
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

	timeout := e.UpstreamTimeout
	if params.IsStream {
		timeout = e.StreamTimeout
	}
	ctx, cancel := e.upstreamContext(params, timeout)
	defer cancel()

	var httpClient *http.Client
	var reqPool *pool.Pool
	var poolUnavailable error

	if e.Pools != nil {
		poolKey := pool.PoolKey{
			IdentityHash: params.ClientID.IdentityHash,
			ProviderID:   cand.ProviderID,
			CredentialID: cand.CredentialID,
		}
		if p := e.Pools.GetOrCreate(poolKey, ""); p != nil {
			reqPool = p
			httpClient = p.Client()
		}

	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	} else if err := reqPool.Acquire(ctx); err != nil {
		poolUnavailable = err
		httpClient = nil
	} else {
		defer reqPool.Release()
	}
	if poolUnavailable != nil {
		return nil, poolUnavailable
	}

	// Use the same lifecycle for pool acquisition and the vendor request.
	// Ordinary streams follow client cancellation; explicit stream sessions and
	// survival attempts remain alive for pending-response capture.
	req = req.WithContext(ctx)

	if e.Upstream != nil {
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(bodyBytes)), nil
		}
	}
	if err := e.beginUpstreamAttempt(params, cand, diagnosticProtocol(cand.Protocol, "anthropic-messages"), bodyBytes); err != nil {
		return nil, err
	}

	reqStart := time.Now()
	var resp *http.Response
	var uErr *upstreampkg.Error
	if e.Upstream != nil {
		if reqPool != nil {
			resp, uErr = e.Upstream.DoWithHTTPClient(req, httpClient)
		} else {
			resp, uErr = e.Upstream.Do(req)
		}
	} else {
		var doErr error
		resp, doErr = httpClient.Do(req)

		if doErr != nil {
			uErr = &upstreampkg.Error{Kind: errorsx.ClassifyError(doErr, nil), Message: doErr.Error(), Err: doErr}
		}
	}
	upstreamLatency := time.Since(reqStart)
	if reqPool != nil {
		if uErr != nil || resp == nil || resp.StatusCode >= 500 {
			reqPool.RecordFailure()
		} else {
			reqPool.RecordSuccess()
		}
	}

	attemptAttrs := []any{
		"request_id", params.RequestID,
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
	if uErr != nil {
		attemptAttrs = append(attemptAttrs, "err_kind", uErr.Kind, "err_message", uErr.Message)
	}
	if resp != nil {
		attemptAttrs = append(attemptAttrs, "upstream_status", resp.StatusCode)
	}
	slog.Info("upstream_http_attempt", attemptAttrs...)

	if uErr != nil && (resp == nil || resp.StatusCode >= 500) {
		errKind := uErr.Kind
		// 2026-08-08: see executor_chat.go — an overload-shaped 5xx body is a
		// load signal, not a dead upstream.
		if len(uErr.Body) > 0 {
			if bodyKind := errorsx.ClassifyErrorWithBody(uErr.StatusCode, uErr.Body); bodyKind == errorsx.KindUpstreamOverloaded {
				errKind = bodyKind
				uErr.Kind = bodyKind
			}
		}
		if errKind == errorsx.KindRateLimit {
			e.Limiter.Shrink(cand.ProviderID, cand.CredentialID)
		}
		// Overload uses the ordinary retryable path — see the long note in
		// executor_chat.go. Returning unwrapped here to force an immediate
		// credential switch broke single-candidate models in production
		// (empty 200 where the same-credential retry had been succeeding).
		if errKind == errorsx.KindUpstreamOverloaded {
			slog.Warn("upstream overloaded",
				"credential_id", cand.CredentialID,
				"provider_id", cand.ProviderID,
				"upstream_status", uErr.StatusCode,
				"retry_after_ms", uErr.RetryAfter.Milliseconds(),
			)
		}
		return nil, &retryableError{err: uErr}
	}

	if resp != nil && resp.StatusCode >= 400 {
		if resp.Body != nil {
			//nolint:errcheck // best-effort close
			defer resp.Body.Close()
		}
		capturedBody, bodyReadErr := readAndDrainErrorBody(resp.Body)
		body := capturedBody
		if len(body) > 4096 {
			body = body[:4096]
		}
		if bodyReadErr != nil {
			slog.Warn("anthropic upstream error body read failed", "error", bodyReadErr)
		}
		e.logUpstreamResponse(params, diagnosticProtocol(cand.Protocol, "anthropic-messages"), body)
		errKind := errorsx.ClassifyErrorWithBody(resp.StatusCode, body)

		if bodyKind := errorsx.ClassifyResponseBody(resp.StatusCode, body); bodyKind == errorsx.KindModelNotFound || bodyKind == errorsx.KindModelDeprecated {
			// Step 4 (2026-06-18): removed the 10-second slow-upstream
			// reclassification. See executor_chat.go for the rationale.
			slog.Info("model_not_found skip offer",
				"credential_id", cand.CredentialID,
				"model", cand.RawModel,
				"status", resp.StatusCode,
				"kind", bodyKind,
				"upstream_latency_ms", upstreamLatency.Milliseconds(),
				"body_preview", string(body[:min(len(body), 120)]),
			)
			return nil, &modelNotFoundError{
				credentialID: cand.CredentialID,
				rawModel:     cand.RawModel,
				body:         string(body),
				status:       resp.StatusCode,
				kind:         bodyKind,
			}
		}

		if resp.StatusCode >= 400 && resp.StatusCode < 500 &&
			resp.StatusCode != 429 && resp.StatusCode != 401 &&
			resp.StatusCode != 403 && resp.StatusCode != 402 &&
			errKind != errorsx.KindConcurrent {
			if !errorsx.IsClientBug(errKind) {
				e.recordProtocolCircuitSuccess(params, cand.ProviderID, cand.CredentialID)
			}
		} else if errKind == errorsx.KindRateLimit {
			e.Limiter.Shrink(cand.ProviderID, cand.CredentialID)
		} else if errKind == errorsx.KindConcurrent {
			e.writeProtocolCredentialStateOnError(params, params.R.Context(), cand.CredentialID, cand.StandardizedName, errorsx.KindConcurrent,
				&upstreampkg.Error{
					Kind:       errorsx.KindConcurrent,
					Message:    fmt.Sprintf("upstream %d concurrent overload", resp.StatusCode),
					Body:       append([]byte(nil), body...),
					StatusCode: resp.StatusCode,
					RetryAfter: upstreampkg.RetryAfterFromHeaders(resp.Header),
				})
			e.forceUnpinOnFatalKind(params.R.Context(), fpLease.Holder, cand.CredentialID, errorsx.KindConcurrent)
		}
		if !errorsx.IsRetryable(errKind) {
			if errorsx.IsContextLength(errKind) || shouldHeuristicCompact(resp.StatusCode, errKind, len(bodyBytes), cand.ContextWindow) {
				return nil, &contextLengthHTTPError{
					status:  resp.StatusCode,
					body:    append([]byte(nil), body...),
					headers: resp.Header.Clone(),
				}
			}
			// 2026-07-03 (Bug #N, same as executor_chat.go): preserve
			// the precise errKind via a typed *upstreampkg.Error so the
			// outer Execute() loop and CandidateFailureLogger can read
			// Kind=errKind instead of re-classifying a fmt.Errorf string
			// via ClassifyError, which would degrade body-driven kinds
			// (KindToolCallIdMismatch, KindUnsupportedFeature) back to
			// KindTransient in request_logs.error_kind.
			upstreamErr := &upstreampkg.Error{
				Kind:       errKind,
				Message:    fmt.Sprintf("upstream %d", resp.StatusCode),
				Body:       append([]byte(nil), body...),
				StatusCode: resp.StatusCode,
				RetryAfter: upstreampkg.RetryAfterFromHeaders(resp.Header),
			}
			if params.PreStreamPrepared {
				return nil, upstreamErr
			}
			// 并发修复 2026-07-27：异步重试 goroutine 的 params.W 为 nil
			// （客户端已收到 202），错误体只能通过 PendingStore 回传，
			// 这里直接跳过客户端写。
			// 2026-08-28 audit fix: body is already fully captured and drained
			// by readAndDrainErrorBody, so we use capturedBody directly for
			// passthrough without re-reading resp.Body.
			fullBody := capturedBody
			if params.W != nil {
				// Surface an accurate Content-Length for the bytes we actually send
				// (the copied vendor Content-Length header would now be wrong if
				// the body exceeded the cap).
				params.W.Header().Set("Content-Length", strconv.Itoa(len(fullBody)))
				for k, vs := range resp.Header {
					if k == "Content-Length" || k == "Content-Encoding" {
						continue // we set Content-Length; skip the (possibly gzipped) encoding header
					}
					for _, v := range vs {
						params.W.Header().Add(k, v)
					}
				}
				params.W.WriteHeader(resp.StatusCode)
				if len(fullBody) > 0 {
					//nolint:errcheck // HTTP write error non-recoverable
					params.W.Write(fullBody)
				}
			}
			return nil, upstreamErr
		}
		// Even for retryable kinds (e.g. 413 classified as KindTransient),
		// check if this is a heuristic-compact candidate (413 or body-size
		// overflow). If so, return contextLengthHTTPError so the outer
		// loop triggers compaction recovery instead of wastefully retrying
		// the same oversized body.
		if shouldHeuristicCompact(resp.StatusCode, errKind, len(bodyBytes), cand.ContextWindow) {
			return nil, &contextLengthHTTPError{
				status:  resp.StatusCode,
				body:    append([]byte(nil), body...),
				headers: resp.Header.Clone(),
			}
		}
		// 2026-07-03 (Bug #N, same fix as above): preserve errKind on
		// the retryable wrapper so the outer loop sees the precise
		// body-driven kind (e.g. KindToolCallIdMismatch) instead of
		// a transient-shaped fmt.Errorf.
		return nil, &retryableError{err: &upstreampkg.Error{
			Kind:       errKind,
			Message:    fmt.Sprintf("upstream %d", resp.StatusCode),
			Body:       append([]byte(nil), body...),
			StatusCode: resp.StatusCode,
			RetryAfter: upstreampkg.RetryAfterFromHeaders(resp.Header),
		}}
	}

	latencyMs := int(time.Since(tTotal).Milliseconds())

	if params.IsStream {
		if params.OnStreamReady != nil {
			params.OnStreamReady()
			params.OnStreamReady = nil
		}
		// 并发修复 2026-07-27：见 responseSink 注释 —— 异步重试路径 W 为
		// nil，改写到丢弃 writer，upstream stream 仍被完整消费。
		// P1-2 fix (2026-08-28): Added ctx parameter for context propagation to gate.
		outcome := ae.StreamResponse(params.R.Context(), responseSink(params), resp)
		if outcome.Interrupted && (outcome.Reason == "client_cancel" || outcome.Kind == errorsx.KindCanceled) {
			return &ExecuteResult{
					Response:    resp,
					Candidate:   cand,
					LatencyMs:   latencyMs,
					RequestBody: append([]byte(nil), bodyBytes...),
					InboundBody: sourceBody,
				}, &streamInterruptedError{
					reason:       outcome.Reason,
					credentialID: cand.CredentialID,
					resumable:    false,
					kind:         errorsx.KindCanceled,
				}
		}
		if outcome.Interrupted {
			streamKind := outcome.Kind
			if streamKind == "" {
				streamKind = errorsx.KindStreamTimeout
			}
			if errorsx.IsConcurrentOverload(outcome.Reason) {
				streamKind = errorsx.KindConcurrent
			}
			isResumable := outcome.Resumable && outcome.ChunkCount < e.StreamRetryThreshold

			slog.Warn("executor: stream interrupted",
				"request_id", params.RequestID,
				"provider_id", cand.ProviderID,
				"credential_id", cand.CredentialID,
				"raw_model", cand.RawModel,
				"client_model", params.Model,
				"upstream_url", cand.BaseURL,
				"reason", outcome.Reason,
				"kind", streamKind,
				"chunk_count", outcome.ChunkCount,
				"resumable", isResumable,
				"classified_as", streamKind,
			)

			if !isResumable {
				e.recordProtocolCircuitFailure(params, cand.ProviderID, cand.CredentialID, streamKind)
			}
			return &ExecuteResult{
				Response:    resp,
				Candidate:   cand,
				LatencyMs:   latencyMs,
				RequestBody: append([]byte(nil), bodyBytes...),
				// Phase D (2026-06-22): inbound body for audit logging
				InboundBody: sourceBody,
			}, &streamInterruptedError{reason: outcome.Reason, credentialID: cand.CredentialID, resumable: isResumable, kind: streamKind}
		}
		e.recordProtocolCircuitSuccess(params, cand.ProviderID, cand.CredentialID)
		return &ExecuteResult{
			Response:    resp,
			Candidate:   cand,
			LatencyMs:   latencyMs,
			RequestBody: append([]byte(nil), bodyBytes...),
			// Phase D (2026-06-22): inbound body for audit logging
			InboundBody: sourceBody,
		}, nil
	}

	var qualitySignals QualitySignals
	if resp == nil || resp.Body == nil {
		return nil, &retryableError{err: &upstreampkg.Error{
			Kind:    errorsx.KindUpstreamDown,
			Message: "anthropic upstream returned an empty response",
		}}
	}
	// Defers close the ORIGINAL transport body: the receiver is evaluated at
	// the defer statement, before resp.Body is replaced by the reconstructed
	// in-memory reader below (WriteNonStreamResponse closes that replacement).
	// Mirrors executor_chat.go's defer before its own ReadAll.
	//nolint:errcheck // best-effort close
	defer resp.Body.Close()
	rawResponseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, anthropicReadBodyError(err, resp)
	}
	e.logUpstreamResponse(params, diagnosticProtocol(cand.Protocol, "anthropic-messages"), rawResponseBody)
	// 2026-08-27: non-stream empty-response failover, Anthropic parity with
	// executor_chat.go's 2026-07-15 check. The raw body is Anthropic-shaped on
	// every client protocol here (Q3 conversion happens inside
	// WriteNonStreamResponse, after this gate), so the check covers both the
	// native passthrough and the OpenAI/Responses conversions. The error is a
	// bare *upstreampkg.Error — NOT wrapped in retryableError — because
	// KindEmptyResponse is deliberately absent from errorsx.IsRetryable: an
	// empty 2xx body must fail over to the next candidate immediately instead
	// of burning same-credential retries (see the KindEmptyResponse taxonomy
	// note in errorsx/classify.go).
	if isEmptyAnthropicMessagesResponse(rawResponseBody) {
		slog.Warn("executor: anthropic non-stream empty response, failing over to next candidate",
			"request_id", params.RequestID,
			"credential_id", cand.CredentialID,
			"provider_id", cand.ProviderID,
			"raw_model", cand.RawModel,
			"client_model", params.ClientModel,
			"status", resp.StatusCode,
		)
		return nil, &upstreampkg.Error{
			Kind:       errorsx.KindEmptyResponse,
			Message:    "upstream returned empty Anthropic Messages response",
			Body:       append([]byte(nil), rawResponseBody...),
			StatusCode: resp.StatusCode,
			RetryAfter: upstreampkg.RetryAfterFromHeaders(resp.Header),
		}
	}
	resp.Body = io.NopCloser(bytes.NewReader(rawResponseBody))
	// 并发修复 2026-07-27：异步重试路径 W 为 nil。WriteNonStreamResponse
	// 的返回值（转换后的 body）是 PendingStore 回传给客户端的内容，所以
	// 这里必须照常调用，只是把字节写进丢弃 writer（responseSink 在 W 非
	// nil 时就是 params.W）。
	responseBody, err := ae.WriteNonStreamResponse(responseSink(params), resp, params.ClientModel, cand.QualityFixMode, &qualitySignals)
	if err != nil {
		return nil, err
	}
	e.logClientResponse(params, diagnosticProtocol(params.ClientProtocol, "anthropic-messages"), responseBody)
	e.recordProtocolCircuitSuccess(params, cand.ProviderID, cand.CredentialID)
	return &ExecuteResult{
		Response:    resp,
		Candidate:   cand,
		LatencyMs:   latencyMs,
		RequestBody: append([]byte(nil), bodyBytes...),
		// Phase D (2026-06-22): inbound body for audit logging
		InboundBody:  sourceBody,
		ResponseBody: responseBody,
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
