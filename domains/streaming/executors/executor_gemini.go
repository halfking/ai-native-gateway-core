package executors

import (
	"bufio"
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
	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit" //nolint:depguard // matches executor.go
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/internal/ir"
	"github.com/kaixuan/llm-gateway-go/internal/paramreg"
	"github.com/kaixuan/llm-gateway-go/internal/upstreamurl"
	"github.com/kaixuan/llm-gateway-go/provider"
	providercatalog "github.com/kaixuan/llm-gateway-go/provider/catalog"
	"github.com/kaixuan/llm-gateway-go/ratelimit"
	"github.com/kaixuan/llm-gateway-go/security/sanitize"
	upstreampkg "github.com/kaixuan/llm-gateway-go/upstream"
)

// maxGeminiStreamLineSize bounds a single upstream Gemini stream frame.
//
// Gemini frames can carry base64 inline media (audio/image), so the
// default bufio.Scanner limit (64 KiB) is far too small — a legitimate
// audio frame would abort the stream. 4 MiB matches the cap the Ollama
// executor applies to its NDJSON frames.
const maxGeminiStreamLineSize = 4 * 1024 * 1024

// geminiRecoveryContextKey marks a request that is already a
// context-length recovery retry, so the recovery path can never recurse
// into itself (a provider that keeps rejecting the compressed body would
// otherwise loop forever).
type geminiRecoveryContextKey struct{}

// geminiOriginalBodyContextKey carries the ORIGINAL client body across a
// context-length recovery retry, so the telemetry still reports what the
// client actually sent (not the compressed rewrite).
type geminiOriginalBodyContextKey struct{}

func withGeminiRecoveryAttempt(ctx context.Context) context.Context {
	return context.WithValue(ctx, geminiRecoveryContextKey{}, true)
}

func geminiRecoveryAttempted(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(geminiRecoveryContextKey{}).(bool)
	return v
}

func withGeminiOriginalBody(ctx context.Context, body []byte) context.Context {
	return context.WithValue(ctx, geminiOriginalBodyContextKey{}, append([]byte(nil), body...))
}

func geminiOriginalBody(ctx context.Context, fallback []byte) []byte {
	if ctx != nil {
		if body, ok := ctx.Value(geminiOriginalBodyContextKey{}).([]byte); ok && len(body) > 0 {
			return append([]byte(nil), body...)
		}
	}
	return append([]byte(nil), fallback...)
}

// GeminiExecutor implements ProtocolHandler for Google's native
// generateContent wire format.
//
// # Why this file exists
//
// The provider catalog advertises `gemini-generate` as a first-class
// outbound protocol (provider/catalog/protocol.go: ProtocolGeminiGenerate,
// the DB CHECK constraint in sql/schema/01-schema.sql, the Web UI's
// protocol dropdown), and the migration layer normalizes a provider
// configured as `gemini` into `gemini-generate`
// (provider/catalog/protocol_normalize.go). But until this file existed,
// the dispatcher classified every such candidate as
// `routeUnsupportedGemini` and skipped it with
// KindUnsupportedFeature (executor_dispatch.go) ⇒ **a provider configured
// with the Gemini native protocol could never be selected, let alone
// used**. The IR layer was already complete (SerializeGemini /
// ParseGeminiResponse / ParseGeminiStreamChunk); only the wire half was
// missing. This file closes that half.
//
// The design is pinned by the retained vendor standard
// (docs/vendor-formats/gemini.md) and by the in-repo design note
// (docs/供应商协议优化.md §8.3 "Gemini executor 新增实现"), which names
// exactly these four ingredients:
//
//   - upstreamurl.GenerateContentURL(baseURL, model) — the URL SSOT
//   - ir.SerializeGemini — the request body SSOT
//   - x-goog-api-key header (or ?key= query) — the credential form
//   - ir/parse_gemini_stream.go — the stream parser
//
// # Wire contract (docs/vendor-formats/gemini.md §HTTP 形态)
//
//	POST {base}/v1beta/models/{MODEL}:generateContent
//	POST {base}/v1beta/models/{MODEL}:streamGenerateContent?alt=sse
//
// The credential is sent as `x-goog-api-key: <key>`. We deliberately do
// NOT put the key in the query string even though the vendor API accepts
// `?key=`: a query-string credential lands in every access log, every
// upstream-URL log line, and every telemetry row that records the
// request URL. The header form is equivalent for the vendor and is what
// the Google SDKs themselves use.
//
// # Client-facing wire
//
// Today only the OpenAI Chat Completions wire is emitted
// (chat.completion JSON / chat.completion.chunk SSE). That covers both
// plain OpenAI clients AND Gemini-native clients, because the Gemini
// ingress (domains/streaming/handler_gemini.go) converts the inbound
// Gemini body into OpenAI shape before dispatch and converts the
// OpenAI-shaped answer back into Gemini shape on the way out.
//
// Every other client wire (anthropic-messages, openai-responses) is
// rejected at the gate with KindUnsupportedFeature rather than answered
// with a wrong-shaped body — the same fail-closed rule the shipped
// Ollama-native executor applies (executor_ollama.go: finalizeOllama-
// UpstreamBody gate 2). A wrong-shaped 200 is worse than an explicit
// "not wired" error: the client SDK fails to parse it far away from the
// cause.
type GeminiExecutor struct {
	// ClientProtocol is the INBOUND client wire protocol, as resolved by
	// the dispatcher. Empty means OpenAI Chat Completions.
	ClientProtocol string

	// Model is the OUTBOUND model id for this attempt. Gemini's native
	// API is model-scoped (the id is a path segment), so the model must
	// travel with the request instead of living in the body.
	Model string

	// ProviderID scopes the IR converter (circuit breaker + extension
	// restoration are provider-scoped).
	ProviderID int

	// IR is the protocol converter. May be nil in isolated tests, in
	// which case the package-level ir helpers are used directly.
	IR IRConverter

	// clientModelOverride is the model the CLIENT asked for. It is
	// stamped onto the client-facing wire (the upstream's echoed
	// modelVersion must not leak to the client).
	clientModelOverride string

	// OutboundModel is the model actually sent upstream (may differ from
	// the client model under model mapping). Reported to the client-side
	// bridges for diagnostics.
	OutboundModel string

	// RequestID is the gateway request id, forwarded to the client-side
	// bridges so their diagnostics correlate with the request log.
	RequestID string

	// Capture is the audit stream capture, forwarded to the client-side
	// bridges so the audit sees the same events the client receives.
	Capture *audit.StreamCapture

	// ToolsRequested tells the Responses bridge whether the client asked
	// for tools (it changes the terminal event shape).
	ToolsRequested bool

	// InputTokensEstimate is the request-derived prompt token estimate the
	// Anthropic bridge writes into message_start.usage.input_tokens.
	// Anthropic clients need it at the stream head (SSE cannot amend
	// message_start retroactively) and the Gemini upstream reports usage
	// only at the tail — same reasoning as the OpenAI upstream path
	// (audit R3 #2, 2026-09-09).
	InputTokensEstimate int

	// OpenAIToAnthropicStream converts the OpenAI-shaped SSE this
	// executor produces into Anthropic-shaped SSE for an Anthropic client.
	// Wired from main.go (streaming.StreamOpenAIToAnthropicSSE).
	//
	// ★ Reusing the shipped bridge is the reason this executor does NOT
	// hand-roll Anthropic SSE framing: the Anthropic stream protocol is
	// strict (message_start → content_block_start → content_block_delta →
	// content_block_stop → message_delta → message_stop) and the repo
	// already has a battle-tested implementation of it.
	OpenAIToAnthropicStream OpenAIToAnthropicSSEFunc

	// OpenAIToResponsesStream converts the OpenAI-shaped SSE this executor
	// produces into Responses-shaped SSE for a Responses client. Wired
	// from main.go (streaming.StreamOpenAIToResponsesSSE).
	OpenAIToResponsesStream OpenAIToResponsesSSEFunc
}

// canonicalInboundWire maps IR-detected inbound protocol names onto the
// catalog constants the executors' client-wire gates compare against.
//
// ir.DetectProtocol reports ProtocolOpenAIChat ("openai-chat") for the
// OpenAI Chat wire — and BOTH the OpenAI ingress and the Gemini-native
// ingress (whose handler synthesizes an OpenAI Chat body at step 6) produce
// exactly that value into ExecParams.ClientProtocol (domains/streaming/
// handler.go: clientProtocol := ir.DetectProtocol(bodyBytes)). The catalog
// layer already treats "openai-chat" as an alias of "openai-completions"
// (provider/catalog/protocol_normalize.go), and connection_registry_wiring
// accepts the same trio of spellings. Without this mapping every
// OpenAI-Chat-shaped ingress was rejected by the gemini/ollama client-wire
// gates with 501 "not yet wired" even though the wire IS wired
// (live-verified 2026-10-28: Gemini ingress → gemini-generate upstream
// returned `client protocol "openai-chat" not yet wired`). The legacy
// fallback "openai-completions" only appears when the IR converter is nil
// (pre-IR mode), which is why the executor tests — written against the
// legacy name — never caught the vocabulary drift.
func canonicalInboundWire(p string) string {
	if p == "openai-chat" {
		return "openai-completions"
	}
	return p
}

// clientWire returns the normalized client wire this executor answers in.
//
// "gemini-generate" never appears here: the Gemini ingress normalizes a
// native Gemini request into OpenAI shape before dispatch, so the
// dispatcher sees an OpenAI-shaped body and detects "openai-chat"
// (domains/streaming/handler.go: ir.DetectProtocol; normalized here onto
// the catalog constant). The Gemini-native answer is re-shaped by the
// Gemini handler's own writer.
func (g *GeminiExecutor) clientWire() string {
	if g == nil || canonicalInboundWire(g.ClientProtocol) == "" {
		return providercatalog.ProtocolOpenAICompletions
	}
	return canonicalInboundWire(g.ClientProtocol)
}

// scopedIR returns the provider-scoped IR converter, or nil when no
// converter is wired.
func (g *GeminiExecutor) scopedIR() IRConverter {
	if g == nil || g.IR == nil {
		return nil
	}
	if scoped, ok := g.IR.(ProviderScoped); ok {
		return scoped.WithProviderScope(g.ProviderID)
	}
	return g.IR
}

// BuildRequest assembles the upstream HTTP POST against the Gemini
// native generateContent endpoint.
//
// body MUST already be the Gemini-native wire body produced by
// ir.SerializeGemini (finalizeGeminiUpstreamBody does that conversion
// once per attempt). The executor does NOT re-serialize — re-running
// SerializeGemini here would re-interpret Extensions and could
// double-emit private fields if the IR was mutated between the
// conversion pass and the wire send.
//
// Auth header follows Google's convention: `x-goog-api-key`. The
// Authorization header is deliberately NOT also set — one credential,
// one form (see the file header for why the query form is rejected).
func (g *GeminiExecutor) BuildRequest(cand provider.Candidate, body []byte, isStream bool) (*http.Request, error) {
	if cand.BaseURL == "" {
		return nil, &upstreampkg.Error{
			Kind:    errorsx.KindClientBug,
			Message: "gemini executor: candidate base URL is empty",
		}
	}
	model := g.Model
	if model == "" {
		model = cand.RawModel
	}
	if model == "" {
		return nil, &upstreampkg.Error{
			Kind:    errorsx.KindClientBug,
			Message: "gemini executor: outbound model is empty",
		}
	}
	upstreamURL := upstreamurl.GenerateContentURL(cand.BaseURL, model, isStream)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, upstreamURL, bytes.NewReader(body))
	if err != nil {
		return nil, &upstreampkg.Error{
			Kind:    errorsx.KindClientBug,
			Message: "gemini executor: build request",
			Err:     err,
		}
	}
	// Stamp the provider so the shared upstream client can route
	// egress_profile='proxy' providers through their subscription node pool.
	req = upstreampkg.WithEgressMeta(req, cand.ProviderID)
	req.Header.Set("Content-Type", "application/json")
	if cand.APIKey != "" {
		req.Header.Set("x-goog-api-key", cand.APIKey)
	}
	if isStream {
		req.Header.Set("Accept", "text/event-stream")
	}
	return req, nil
}

// WriteNonStreamResponse writes a complete Gemini non-stream JSON
// response back to the client, translated to the client wire.
//
// Failure modes:
//
//   - Upstream returned non-2xx: the body is returned verbatim with the
//     upstream status preserved so the dispatcher (and the operator's
//     debug log) sees the actual vendor error envelope.
//   - Body parse failure: returned as a typed *upstreampkg.Error with
//     KindConversion, so the upstream payload is preserved for
//     diagnostics while the failure is non-retryable.
//
// qualityFixMode / qualitySignals: the Gemini tool-call wire shape
// differs from OpenAI's, so the OpenAI-shaped tool-call quality
// post-processor is deliberately NOT run here. Both parameters are left
// untouched, exactly like the Ollama executor.
func (g *GeminiExecutor) WriteNonStreamResponse(w http.ResponseWriter, resp *http.Response, clientModel, qualityFixMode string, qualitySignals *QualitySignals) ([]byte, error) {
	if resp == nil || resp.Body == nil {
		return nil, &upstreampkg.Error{
			Kind:       errorsx.KindUpstreamDown,
			Message:    "gemini upstream returned nil response",
			StatusCode: 0,
		}
	}
	//nolint:errcheck // best-effort close
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &upstreampkg.Error{
			Kind:       errorsx.KindNetwork,
			Message:    "gemini: read response body",
			Err:        err,
			StatusCode: resp.StatusCode,
			Body:       body,
		}
	}

	// Non-2xx: pass the body through verbatim with the upstream status
	// so the dispatcher's classifier sees the real vendor error.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		copyNonStreamResponseHeaders(w.Header(), resp.Header, len(body))
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(resp.StatusCode)
		_, werr := w.Write(body)
		return body, werr
	}

	parsed, parseErr := ir.ParseGeminiResponse(body)
	if parseErr != nil {
		return nil, &upstreampkg.Error{
			Kind:       errorsx.KindConversion,
			Message:    "gemini: parse response body",
			Err:        parseErr,
			StatusCode: resp.StatusCode,
			Body:       body,
		}
	}

	// Substitute the client-visible model with the model the client
	// actually asked for, so audit logs and SDKs see the model they sent
	// rather than the upstream's echoed identifier (mirrors the Ollama
	// executor's replaceModelInResponseBody behavior).
	if clientModel != "" {
		parsed.Model = clientModel
	}

	out, err := g.serializeResponse(parsed, clientModel)
	if err != nil {
		return nil, err
	}

	status := resp.StatusCode
	if status == 0 {
		status = http.StatusOK
	}
	copyNonStreamResponseHeaders(w.Header(), resp.Header, len(out))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, werr := w.Write(out)
	if werr != nil {
		return out, werr
	}
	return out, nil
}

// serializeResponse translates the parsed Gemini IR into the client wire.
//
// The three client wires are supported:
//
//   - openai-completions → ir.SerializeOpenAIResponse
//   - anthropic-messages → ir.SerializeAnthropicResponse
//   - openai-responses   → ir.SerializeResponsesResponse
//
// Every one of them is an IR-level serializer, so no extra wiring is
// needed: the IR converter interface already carries all three.
func (g *GeminiExecutor) serializeResponse(parsed *ir.InternalResponse, clientModel string) ([]byte, error) {
	var (
		out []byte
		err error
	)
	switch g.clientWire() {
	case providercatalog.ProtocolOpenAICompletions:
		if scoped := g.scopedIR(); scoped != nil {
			out, err = scoped.SerializeOpenAIResponse(parsed, clientModel)
		} else {
			out, err = ir.SerializeOpenAIResponse(parsed, clientModel)
		}
	case providercatalog.ProtocolAnthropicMessages:
		if scoped := g.scopedIR(); scoped != nil {
			out, err = scoped.SerializeAnthropicResponse(parsed, clientModel)
		} else {
			out, err = ir.SerializeAnthropicResponse(parsed, clientModel)
		}
	case providercatalog.ProtocolOpenAIResponses:
		if scoped := g.scopedIR(); scoped != nil {
			out, err = scoped.SerializeResponsesResponse(parsed, clientModel)
		} else {
			out, err = ir.SerializeResponsesResponse(parsed, clientModel)
		}
	default:
		return nil, &upstreampkg.Error{
			Kind:       errorsx.KindUnsupportedFeature,
			Message:    fmt.Sprintf("gemini executor: client protocol %q not yet wired", g.ClientProtocol),
			StatusCode: http.StatusNotImplemented,
		}
	}
	if err != nil {
		return nil, &upstreampkg.Error{
			Kind:    errorsx.KindConversion,
			Message: "gemini: serialize response to client wire",
			Err:     err,
		}
	}
	return out, nil
}

// geminiInboundWireName names the inbound client wire for diagnostics.
func geminiInboundWireName(clientProtocol string) string {
	switch clientProtocol {
	case providercatalog.ProtocolAnthropicMessages:
		return "Anthropic Messages"
	case providercatalog.ProtocolOpenAIResponses:
		return "OpenAI Responses"
	default:
		return "OpenAI Chat"
	}
}

// StreamResponse reads a Gemini native stream and writes an OpenAI SSE
// stream back to the client.
//
// # Framing tolerance
//
// The vendor API has two stream framings (docs/vendor-formats/gemini.md
// §HTTP 形态): with "?alt=sse" it emits SSE ("data: {...}"), without it
// it emits a chunked JSON array (one JSON object per line). We ask for
// SSE but accept BOTH, because a Gemini-compatible relay in front of
// the real API may drop the query parameter. A bare "{...}" line is
// therefore treated as a data line.
//
// # Terminal
//
// The Gemini stream has no "[DONE]" sentinel of its own — it simply
// stops after the chunk that carries "finishReason". The OpenAI wire
// REQUIRES a terminal "data: [DONE]", so we synthesize one when the
// upstream ends without having produced a terminal chunk. Without it the
// client SDK waits for EOF and reports a truncated answer.
//
// # Error frames
//
// A chunk that parses into an IR error is re-emitted as an OpenAI-shaped
// error frame so the client sees a typed error inline rather than a
// silent connection drop.
func (g *GeminiExecutor) StreamResponse(ctx context.Context, w http.ResponseWriter, resp *http.Response) StreamOutcome {
	switch g.clientWire() {
	case providercatalog.ProtocolAnthropicMessages:
		return g.streamViaAnthropicBridge(ctx, w, resp)
	case providercatalog.ProtocolOpenAIResponses:
		return g.streamViaResponsesBridge(ctx, w, resp)
	default:
		// OpenAI Chat Completions wire: the frames go straight to the client.
		flusher, _ := w.(http.Flusher)
		flush := func() {
			if flusher != nil {
				flusher.Flush()
			}
		}
		return g.writeOpenAISSE(ctx, w, w, flush, resp)
	}
}

// streamViaAnthropicBridge serves an Anthropic client by feeding the
// OpenAI-shaped frames this executor produces into the repo's shipped
// OpenAI→Anthropic SSE bridge.
//
// ★ Why a pipe instead of hand-rolled framing: the Anthropic stream
// protocol is strict (message_start → content_block_start →
// content_block_delta → content_block_stop → message_delta →
// message_stop), and the repo already has a battle-tested implementation
// of exactly that conversion (domains/streaming/anthropic_stream.go),
// wired into the Executor as OpenAIToAnthropicStream. Reusing it means
// the Gemini upstream inherits the SAME Anthropic wire behavior the
// OpenAI upstream path has — including the message_start input-token
// estimate (audit R3 #2) and the interruption/terminal semantics.
//
// The pipe is unbuffered, so the conversion stays streaming: the bridge
// reads a frame as soon as we write it, and nothing is accumulated in
// memory.
func (g *GeminiExecutor) streamViaAnthropicBridge(ctx context.Context, w http.ResponseWriter, resp *http.Response) StreamOutcome {
	if g.OpenAIToAnthropicStream == nil {
		return StreamOutcome{
			Interrupted: true,
			Reason:      "anthropic_bridge_not_wired",
			Kind:        errorsx.KindUnsupportedFeature,
		}
	}
	pr, pw := io.Pipe()
	proxyResp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       pr,
		Request:    resp.Request,
	}
	done := make(chan StreamOutcome, 1)
	go func() {
		done <- g.OpenAIToAnthropicStream(
			ctx, w, proxyResp,
			g.clientModelOverride, g.OutboundModel, g.RequestID,
			g.Capture, nil, g.InputTokensEstimate, g.ToolsRequested,
		)
	}()

	outcome := g.writeOpenAISSE(ctx, pw, w, nil, resp)
	if outcome.Interrupted {
		// Unblock the bridge reader with the failure reason instead of a
		// clean EOF (a clean EOF would look like a normal end of stream).
		_ = pw.CloseWithError(errors.New(outcome.Reason))
	} else {
		_ = pw.Close()
	}
	bridgeOutcome := <-done

	// The Gemini-side interruption is the more precise diagnosis (the
	// bridge only sees a broken pipe), so it wins.
	if outcome.Interrupted {
		outcome.ChunkCount = bridgeOutcome.ChunkCount
		return outcome
	}
	return bridgeOutcome
}

// streamViaResponsesBridge serves a Responses client by feeding the
// OpenAI-shaped frames into the repo's shipped OpenAI→Responses SSE
// bridge (domains/streaming/responses_bridge.go), wired into the
// Executor as OpenAIToResponsesStream.
//
// Same reasoning as streamViaAnthropicBridge: reuse the shipped
// conversion (including its response.completed orchestration) instead of
// re-implementing the Responses terminal events.
func (g *GeminiExecutor) streamViaResponsesBridge(ctx context.Context, w http.ResponseWriter, resp *http.Response) StreamOutcome {
	if g.OpenAIToResponsesStream == nil {
		return StreamOutcome{
			Interrupted: true,
			Reason:      "responses_bridge_not_wired",
			Kind:        errorsx.KindUnsupportedFeature,
		}
	}
	pr, pw := io.Pipe()
	proxyResp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       pr,
		Request:    resp.Request,
	}
	done := make(chan StreamOutcome, 1)
	go func() {
		done <- g.OpenAIToResponsesStream(
			ctx, w, proxyResp,
			g.clientModelOverride, g.OutboundModel, g.RequestID,
			g.Capture, nil, g.ToolsRequested,
		)
	}()

	outcome := g.writeOpenAISSE(ctx, pw, w, nil, resp)
	if outcome.Interrupted {
		_ = pw.CloseWithError(errors.New(outcome.Reason))
	} else {
		_ = pw.Close()
	}
	bridgeOutcome := <-done
	if outcome.Interrupted {
		outcome.ChunkCount = bridgeOutcome.ChunkCount
		return outcome
	}
	return bridgeOutcome
}

// writeOpenAISSE reads the Gemini native stream and writes it as
// OpenAI-shaped SSE frames into out.
//
// It is the single implementation behind all three client wires: the
// OpenAI chat wire writes straight to the client, while the Anthropic
// and Responses wires write into a pipe that their bridge drains.
//
// `client` is the client's ResponseWriter — used ONLY to look up the
// first-semantic-byte callback (the frames themselves go to out).
func (g *GeminiExecutor) writeOpenAISSE(ctx context.Context, out io.Writer, client http.ResponseWriter, flush func(), resp *http.Response) StreamOutcome {
	if resp == nil || resp.Body == nil {
		return StreamOutcome{
			Interrupted: true,
			Reason:      "empty_response",
			Resumable:   false,
			Kind:        errorsx.KindEmptyResponse,
		}
	}
	//nolint:errcheck // best-effort close (we are the sole consumer)
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	// A Gemini frame carries base64 inline media, so the default 64 KiB
	// scanner buffer is far too small.
	scanner.Buffer(make([]byte, 0, 64*1024), maxGeminiStreamLineSize)

	firstByteFired := false
	fireFirstByte := func() {
		if firstByteFired {
			return
		}
		firstByteFired = true
		if client != nil {
			if setter, ok := client.(interface{ FirstSemanticByteCallback() func() }); ok {
				if cb := setter.FirstSemanticByteCallback(); cb != nil {
					cb()
				}
			}
		}
	}

	outcome := StreamOutcome{}
	chunkCount := 0
	sawTerminal := false
	parseFailures := 0

	// ★ One stable chat id for the whole stream. The OpenAI wire carries
	// the completion id on EVERY frame; the IR chunk has no id (Gemini
	// does not send one), so without this the frames would fall back to
	// the IR's constant default — and a per-frame fallback would give
	// the client a DIFFERENT id on every chunk.
	chatID := fmt.Sprintf("chatcmpl-gemini-%d", time.Now().UnixNano())

	// ★ The OpenAI wire marks the assistant role on the FIRST delta.
	// The Gemini chunk carries no role either (Gemini's role lives on
	// content.role, and the IR delta does not carry it across), so the
	// marker is stamped once here — the Ollama executor does the same.
	roleStamped := false

	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			outcome.Interrupted = true
			outcome.Reason = "client_disconnected"
			outcome.Kind = errorsx.KindCanceled
			outcome.ChunkCount = chunkCount
			return outcome
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		// SSE comment (heartbeat) and SSE event-name lines carry no
		// payload for the Gemini protocol.
		if strings.HasPrefix(line, ":") || strings.HasPrefix(line, "event:") {
			continue
		}
		payload, ok := geminiStreamDataLine(line)
		if !ok {
			continue
		}

		chunk, perr := ir.ParseGeminiStreamChunk(payload)
		if perr != nil {
			// Per-line tolerance: one malformed frame is skipped and the
			// stream keeps going (same rule as the Ollama executor).
			parseFailures++
			continue
		}
		if chunk == nil {
			continue
		}

		switch chunk.Type {
		case ir.ChunkTypeError:
			if chunk.Error != nil {
				sse := geminiOpenAISSEError(chunk.Error)
				if sse != "" {
					if _, werr := io.WriteString(out, sse); werr != nil {
						outcome.Interrupted = true
						outcome.Reason = "client_write_failed"
						outcome.Kind = errorsx.KindNetwork
						outcome.ChunkCount = chunkCount
						return outcome
					}
					if flush != nil {
						flush()
					}
				}
			}
			outcome.Interrupted = true
			outcome.Reason = "upstream_error"
			outcome.Kind = errorsx.KindUpstreamDown
			outcome.ChunkCount = chunkCount
			return outcome
		case ir.ChunkTypeDone:
			sawTerminal = true
		}

		// The client-visible model is the model the client asked for; the
		// upstream's echoed modelVersion must not leak into the client
		// wire (same substitution as the non-stream path).
		if chunk.Model != "" && g.clientModelOverride != "" {
			chunk.Model = g.clientModelOverride
		}

		// Stamp the assistant role on the first delta that actually
		// carries content (the OpenAI wire marks the role exactly once).
		if !roleStamped && chunk.Type == ir.ChunkTypeDelta && chunk.Delta != nil {
			if chunk.Delta.Content != "" || chunk.Delta.ReasoningContent != "" || len(chunk.Delta.ToolCalls) > 0 {
				chunk.Delta.Role = "assistant"
				roleStamped = true
			}
		}

		sse := chunk.SerializeOpenAI(chatID, chunk.Model, 0)
		if sse == "" {
			continue
		}
		if chunk.Type == ir.ChunkTypeDelta && chunk.Delta != nil &&
			(chunk.Delta.Content != "" || chunk.Delta.ReasoningContent != "" || len(chunk.Delta.ToolCalls) > 0) {
			fireFirstByte()
		}
		if _, werr := io.WriteString(out, sse); werr != nil {
			outcome.Interrupted = true
			outcome.Reason = "client_write_failed"
			outcome.Kind = errorsx.KindNetwork
			outcome.ChunkCount = chunkCount
			return outcome
		}
		if flush != nil {
			flush()
		}
		chunkCount++
	}

	if err := scanner.Err(); err != nil {
		outcome.Interrupted = true
		outcome.Reason = "upstream_read_error"
		outcome.Kind = errorsx.KindNetwork
		outcome.ChunkCount = chunkCount
		return outcome
	}

	// The Gemini stream has no "[DONE]" sentinel; the OpenAI wire needs
	// one. Synthesize it when the upstream ended without a terminal
	// chunk of its own.
	if !sawTerminal {
		if _, werr := io.WriteString(out, "data: [DONE]\n\n"); werr == nil && flush != nil {
			flush()
		}
	}
	outcome.ChunkCount = chunkCount
	return outcome
}

// geminiStreamDataLine normalizes one upstream stream line into the
// "data: ..." form the IR stream parser accepts.
//
// The vendor API emits "data: {...}" when "?alt=sse" is present and a
// bare "{...}" per line when it is not (chunked JSON array). Both are
// accepted; a bare object is prefixed here.
func geminiStreamDataLine(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return "", false
	}
	if strings.HasPrefix(trimmed, "data:") {
		return trimmed, true
	}
	if strings.HasPrefix(trimmed, "{") {
		return "data: " + trimmed, true
	}
	return "", false
}

// geminiOpenAISSEError renders an IR stream error as an OpenAI-shaped
// error frame.
func geminiOpenAISSEError(streamErr *ir.StreamError) string {
	if streamErr == nil {
		return ""
	}
	body, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"type":    streamErr.Type,
			"message": streamErr.Message,
			"code":    streamErr.Code,
		},
	})
	if err != nil {
		return ""
	}
	return "data: " + string(body) + "\n\n"
}

// ExtractUsage pulls token counts out of the upstream response body.
//
// Gemini reports usage under "usageMetadata" with three counters
// (promptTokenCount / candidatesTokenCount / totalTokenCount). The
// non-stream path parses the body through the IR, but the executor's
// ExtractUsage contract takes the raw body, so the counters are read
// directly here.
func (g *GeminiExecutor) ExtractUsage(resp *http.Response, body []byte) (inputTokens, outputTokens *int) {
	if len(body) == 0 {
		return nil, nil
	}
	var payload struct {
		UsageMetadata *struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
			TotalTokenCount      int `json:"totalTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.UsageMetadata == nil {
		return nil, nil
	}
	um := payload.UsageMetadata
	in := um.PromptTokenCount
	out := um.CandidatesTokenCount
	if um.TotalTokenCount > 0 && in == 0 && out == 0 {
		// Only the total is present: report it as input tokens so the
		// accounting is not silently lost.
		in = um.TotalTokenCount
	}
	if in == 0 && out == 0 {
		return nil, nil
	}
	return &in, &out
}

// CheckSoftMismatch returns true if the upstream model name doesn't
// match the requested one.
//
// Gemini reports the served model in "modelVersion", not in "model" —
// so the comparison is done against modelVersion only. An empty
// upstream model is NOT a mismatch: many relays omit it.
func (g *GeminiExecutor) CheckSoftMismatch(reqModel, respModel string) (bool, string) {
	if reqModel == "" || respModel == "" {
		return false, ""
	}
	if strings.EqualFold(reqModel, respModel) {
		return false, ""
	}
	// Gemini reports "gemini-2.5-flash" where the client may have asked
	// for a dated alias ("gemini-2.5-flash-002"). Compare on the family
	// prefix before declaring a mismatch.
	if geminiModelFamily(reqModel) == geminiModelFamily(respModel) {
		return false, ""
	}
	return true, fmt.Sprintf("upstream model %q != requested %q", respModel, reqModel)
}

// geminiModelFamily strips the dated/versioned suffix from a Gemini
// model id ("gemini-2.5-flash-preview-05-15" → "gemini-2.5-flash").
func geminiModelFamily(model string) string {
	family := strings.ToLower(model)
	for _, sep := range []string{"-preview", "-exp", "-latest"} {
		if idx := strings.Index(family, sep); idx > 0 {
			family = family[:idx]
		}
	}
	// Trailing "-NNN" / "-NN-NN" style dated stamps.
	for {
		idx := strings.LastIndex(family, "-")
		if idx <= 0 {
			break
		}
		tail := family[idx+1:]
		if tail == "" || !isAllDigits(tail) {
			break
		}
		family = family[:idx]
	}
	return family
}

// isAllDigits reports whether s is a non-empty run of ASCII digits.
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// ────────────────────────────────────────────────────────────────────────
// Executor integration
// ────────────────────────────────────────────────────────────────────────

// executeGemini is the dispatch entry point for candidates whose
// providers.protocol resolves to ProtocolGeminiGenerate.
//
// It mirrors executeOllama's shape (build request → Do → status
// branching → write non-stream / stream response) and reuses the same
// lifecycle helpers: beginUpstreamAttempt (per-attempt budget + live
// action event), logUpstreamResponse (upstream body digest),
// recordProtocolCircuitSuccess/Failure (per-protocol circuit breaker).
func (e *Executor) executeGemini(
	params *ExecParams,
	cand provider.Candidate,
	maxRetries int,
	tTotal time.Time,
	fpLease *credentialfpslot.Lease,
) (*ExecuteResult, error) {
	sourceBody := append([]byte(nil), params.BodyBytes...)
	// The ORIGINAL client body, preserved across a context-length
	// recovery retry (the retry sends a compressed rewrite, but the
	// telemetry must still report what the client sent).
	originalBody := geminiOriginalBody(params.R.Context(), sourceBody)

	bodyBytes, err := e.finalizeGeminiUpstreamBody(params, cand, sourceBody)
	if err != nil {
		return nil, err
	}

	outboundModel := resolveOutboundModel(params, cand)
	ge := &GeminiExecutor{
		ClientProtocol:      params.ClientProtocol,
		Model:               outboundModel,
		ProviderID:          cand.ProviderID,
		IR:                  e.IR,
		clientModelOverride: params.ClientModel,
		OutboundModel:       outboundModel,
		RequestID:           params.RequestID,
		Capture:             params.Capture,
		ToolsRequested:      params.ToolsRequested,
		// ★ The Anthropic bridge needs a request-derived prompt token
		// estimate at the stream head (Anthropic clients manage their
		// context from message_start.usage.input_tokens, and SSE cannot
		// amend message_start retroactively). Same estimate the OpenAI
		// upstream path uses (audit R3 #2).
		InputTokensEstimate:     EstimateAnthropicInputTokens(sourceBody),
		OpenAIToAnthropicStream: e.OpenAIToAnthropicStream,
		OpenAIToResponsesStream: e.OpenAIToResponsesStream,
	}

	req, err := ge.BuildRequest(cand, bodyBytes, params.IsStream)
	if err != nil {
		return nil, err
	}
	// Forward the session headers so the upstream probe path can
	// recognize session-bearing requests if it ever needs to.
	if sid := params.R.Header.Get("X-Gw-Session-Id"); sid != "" {
		req.Header.Set("X-Gw-Session-Id", sid)
	}
	if sid := params.R.Header.Get("X-Session-Id"); sid != "" {
		req.Header.Set("X-Session-Id", sid)
	}
	req.Header.Set("X-Request-Id", diagnosticRequestID(params))
	if fpLease != nil && fpLease.Egress != nil {
		credentialfpslot.ApplyEgressHeaders(req.Header, fpLease.Egress)
	} else {
		req.Header.Set("X-Virtual-Client-Id", params.ClientID.VirtualClientID)
		req.Header.Set("X-Virtual-IP", params.ClientID.VirtualIP)
		req.Header.Set("X-Virtual-MAC", params.ClientID.VirtualMAC)
	}
	// Apply disguise headers (User-Agent / Accept-Language).
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
	// ★ Header profile (docs/供应商协议优化.md §8.3: the credential form
	// is tied to the endpoint's header_profile_code).
	//
	// The profile is a per-(catalog_code, protocol) table of extra outbound
	// headers. Applying it here means a Gemini-compatible relay whose
	// profile declares (say) "Authorization: Bearer" gets it — without
	// this, the Gemini executor would send ONLY x-goog-api-key and the
	// relay would reject the request.
	//
	// ★ The profile is applied AFTER the credential header, so a profile
	// that declares its own credential header wins — that is the point of
	// the mechanism (it exists to express "this endpoint wants its
	// credential in another shape").
	if e.HeaderProfiles != nil {
		prof := e.HeaderProfiles.load(params.R.Context(), cand.CatalogCode, cand.Protocol)
		if prof != nil {
			for k, v := range prof.Headers {
				req.Header.Set(k, v)
			}
		}
	}
	// Stamp after configured egress headers so they cannot erase the
	// validated marker-ownership signal on a recursive gateway call.
	if nonce := sanitize.SanitizeHopForOutbound(params.R.Context()); nonce != "" {
		req.Header.Set(sanitize.SanitizeHopHeader, nonce)
	}

	timeout := e.UpstreamTimeout
	if params.IsStream {
		timeout = e.StreamTimeout
	}
	ctx, cancel := e.upstreamContext(params, timeout)
	defer cancel()
	req = req.WithContext(ctx)

	if err := e.beginUpstreamAttempt(params, cand, diagnosticProtocol(cand.Protocol, "gemini-native"), bodyBytes); err != nil {
		return nil, err
	}

	reqStart := time.Now()
	var resp *http.Response
	var uErr *upstreampkg.Error
	if e.Upstream != nil {
		resp, uErr = e.Upstream.Do(req)
	} else {
		var doErr error
		resp, doErr = http.DefaultClient.Do(req)
		if doErr != nil {
			uErr = &upstreampkg.Error{
				Kind:    errorsx.ClassifyError(doErr, nil),
				Message: doErr.Error(),
				Err:     doErr,
			}
		}
	}
	latencyMs := int(time.Since(tTotal).Milliseconds())
	upstreamLatency := time.Since(reqStart)

	slog.Info("upstream_http_attempt",
		"request_id", params.RequestID,
		"provider_id", cand.ProviderID,
		"credential_id", cand.CredentialID,
		"raw_model", cand.RawModel,
		"client_model", params.ClientModel,
		"upstream_url", req.URL.String(),
		"upstream_method", req.Method,
		"body_bytes", len(bodyBytes),
		"is_stream", params.IsStream,
		"latency_ms", upstreamLatency.Milliseconds(),
	)
	if uErr != nil {
		slog.Info("gemini upstream attempt error",
			"request_id", params.RequestID,
			"credential_id", cand.CredentialID,
			"kind", uErr.Kind,
			"err_message_bytes", len(uErr.Message),
		)
		if params.RoutingTracker != nil {
			statusCode := 0
			if resp != nil {
				statusCode = resp.StatusCode
			}
			retryable := errorsx.ProjectRecovery(uErr.Kind).GenericRetryable
			params.RoutingTracker.Add(RoutingAttempt{
				ProviderID:   int64(cand.ProviderID),
				CredentialID: int64(cand.CredentialID),
				ProviderName: cand.CatalogCode,
				RawModel:     cand.RawModel,
				UpstreamURL:  req.URL.String(),
				Result:       ClassifyResult(uErr, statusCode),
				LatencyMs:    upstreamLatency.Milliseconds(),
				HTTPStatus:   statusCode,
				ErrorMessage: uErr.Message,
				Stage:        "upstream",
				ErrorKind:    string(uErr.Kind),
				Retryable:    &retryable,
			})
		}
		if uErr.Kind == errorsx.KindRateLimit && e.Limiter != nil {
			e.Limiter.Shrink(cand.ProviderID, cand.CredentialID)
		}
		if errorsx.IsRetryable(uErr.Kind) || uErr.Kind == errorsx.KindUpstreamDown || uErr.Kind == errorsx.KindNetwork {
			return nil, &retryableError{err: uErr}
		}
		return nil, uErr
	}

	// 4xx handling: capture the body for diagnostics and surface a typed
	// error. The body is capped so a hostile upstream can't blow up
	// memory.
	if resp.StatusCode >= 400 {
		if resp.Body != nil {
			//nolint:errcheck // best-effort close
			defer resp.Body.Close()
		}
		captured, _ := readAndDrainErrorBody(resp.Body)
		if len(captured) > maxPassthroughErrorBody {
			captured = captured[:maxPassthroughErrorBody]
		}
		e.logUpstreamResponse(params, diagnosticProtocol(cand.Protocol, "gemini-native"), captured)
		errKind := errorsx.ClassifyErrorWithBody(resp.StatusCode, captured)
		// ★ Context-length recovery: the upstream rejected the request as
		// too long. Reuse the shared recovery coordinator (the same one
		// the OpenAI and Ollama paths use) to compress the conversation and
		// retry ONCE with the smaller body.
		//
		// The retry is deliberately free of the outer credential retry
		// budget: it is an internal rewrite of THIS request, not failover.
		// The recovery-attempt marker keeps it from recursing.
		if errorsx.IsContextLength(errKind) && !geminiRecoveryAttempted(params.R.Context()) {
			recoveredBody := append([]byte(nil), sourceBody...)
			recovery := contextLengthRecoveryState{}
			if e.handleContextLengthRecovery(params.R.Context(), params, cand, &recoveredBody, &recovery, resp.StatusCode, captured) == ctxLenRetry && len(recoveredBody) < len(sourceBody) {
				retryParams := *params
				retryParams.BodyBytes = recoveredBody
				retryCtx := withGeminiRecoveryAttempt(params.R.Context())
				retryParams.R = params.R.WithContext(withGeminiOriginalBody(retryCtx, originalBody))
				meterExtraUpstreamCall(params)
				result, retryErr := e.executeGemini(&retryParams, cand, 0, tTotal, fpLease)
				if retryErr == nil && result != nil {
					reason := recovery.lastReason
					if reason == "" {
						reason = "context_length"
					}
					strategy := recovery.lastStrategy
					if strategy == "" {
						strategy = "mechanical_trim"
					}
					result.CompressionReason = &reason
					result.CompressionStrategy = &strategy
				}
				return result, retryErr
			}
		}
		if errKind == errorsx.KindModelNotFound || errKind == errorsx.KindModelDeprecated {
			slog.Info("model_not_found skip offer",
				"credential_id", cand.CredentialID,
				"model", cand.RawModel,
				"status", resp.StatusCode,
				"kind", errKind,
				"upstream_latency_ms", upstreamLatency.Milliseconds(),
				"body_digest", safeUpstreamBodyDigest(captured[:min(len(captured), 120)]),
				"body_bytes", min(len(captured), 120),
			)
			return nil, &modelNotFoundError{
				credentialID: cand.CredentialID,
				rawModel:     cand.RawModel,
				body:         string(captured),
				status:       resp.StatusCode,
				kind:         errKind,
			}
		}
		upstreamErr := &upstreampkg.Error{
			Kind:       errKind,
			Message:    fmt.Sprintf("upstream %d", resp.StatusCode),
			Body:       append([]byte(nil), captured...),
			StatusCode: resp.StatusCode,
			RetryAfter: upstreampkg.RetryAfterFromHeaders(resp.Header),
		}
		// Non-retryable client errors → forward upstream body verbatim
		// to the client so SDKs can show the actual error.
		if !errorsx.IsRetryable(errKind) && !errorsx.IsClientBug(errKind) {
			if params.W != nil {
				params.W.Header().Set("Content-Length", strconv.Itoa(len(captured)))
				for k, vs := range resp.Header {
					if k == "Content-Length" || k == "Content-Encoding" || ratelimit.IsGatewayRateLimitScopeHeader(k) {
						continue
					}
					for _, v := range vs {
						params.W.Header().Add(k, v)
					}
				}
				params.W.WriteHeader(resp.StatusCode)
				if len(captured) > 0 {
					//nolint:errcheck // HTTP write error non-recoverable
					params.W.Write(captured)
				}
			}
		}
		return nil, upstreamErr
	}

	// 2xx success path. Branch on stream vs non-stream.
	if params.IsStream {
		if params.OnStreamReady != nil {
			params.OnStreamReady()
			params.OnStreamReady = nil
		}
		outcome := ge.StreamResponse(streamReaderContext(params, resp), responseSink(params), resp)
		if outcome.Interrupted && isClientStreamInterruption(outcome.Kind, outcome.Reason) {
			slog.Info("executor: client disconnected during gemini stream",
				"credential_id", cand.CredentialID,
				"provider_id", cand.ProviderID,
				"chunk_count", outcome.ChunkCount,
			)
			return &ExecuteResult{
				Response:       resp,
				Candidate:      cand,
				LatencyMs:      latencyMs,
				RequestBody:    append([]byte(nil), bodyBytes...),
				InboundBody:    originalBody,
				RoutingTracker: params.RoutingTracker,
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
			slog.Warn("executor: gemini stream interrupted",
				"request_id", params.RequestID,
				"provider_id", cand.ProviderID,
				"credential_id", cand.CredentialID,
				"raw_model", cand.RawModel,
				"client_model", params.ClientModel,
				"upstream_url", cand.BaseURL,
				"reason", outcome.Reason,
				"kind", streamKind,
				"chunk_count", outcome.ChunkCount,
				"resumable", isResumable,
			)
			if !isResumable {
				e.recordProtocolCircuitFailure(params, cand.ProviderID, cand.CredentialID, streamKind, cand.BillingMode)
			}
			return &ExecuteResult{
				Response:       resp,
				Candidate:      cand,
				LatencyMs:      latencyMs,
				RequestBody:    append([]byte(nil), bodyBytes...),
				InboundBody:    originalBody,
				RoutingTracker: params.RoutingTracker,
			}, &streamInterruptedError{
				reason:           outcome.Reason,
				credentialID:     cand.CredentialID,
				resumable:        isResumable,
				kind:             streamKind,
				terminalRendered: outcome.TerminalRendered,
			}
		}
		e.recordProtocolCircuitSuccess(params, cand.ProviderID, cand.CredentialID)
		return &ExecuteResult{
			Response:       resp,
			Candidate:      cand,
			LatencyMs:      latencyMs,
			RequestBody:    append([]byte(nil), bodyBytes...),
			InboundBody:    originalBody,
			RoutingTracker: params.RoutingTracker,
		}, nil
	}

	// Non-stream success path. WriteNonStreamResponse closes resp.Body.
	if resp == nil || resp.Body == nil {
		return nil, &retryableError{err: &upstreampkg.Error{
			Kind:    errorsx.KindUpstreamDown,
			Message: "gemini upstream returned an empty response",
		}}
	}
	body, werr := ge.WriteNonStreamResponse(responseSink(params), resp, params.ClientModel, cand.QualityFixMode, nil)
	if werr != nil {
		var ue *upstreampkg.Error
		if errors.As(werr, &ue) && ue != nil {
			switch ue.Kind {
			case errorsx.KindConversion, errorsx.KindUnsupportedFeature, errorsx.KindUpstreamDown:
				return nil, ue
			}
		}
		return &ExecuteResult{
			Response:       resp,
			Candidate:      cand,
			LatencyMs:      latencyMs,
			RequestBody:    append([]byte(nil), bodyBytes...),
			InboundBody:    originalBody,
			ResponseBody:   body,
			RoutingTracker: params.RoutingTracker,
		}, nil
	}
	e.recordProtocolCircuitSuccess(params, cand.ProviderID, cand.CredentialID)
	return &ExecuteResult{
		Response:       resp,
		Candidate:      cand,
		LatencyMs:      latencyMs,
		RequestBody:    append([]byte(nil), bodyBytes...),
		InboundBody:    originalBody,
		ResponseBody:   body,
		RoutingTracker: params.RoutingTracker,
	}, nil
}

// finalizeGeminiUpstreamBody validates and converts the inbound client
// wire body into the Gemini native wire body.
//
// # Contract
//
// The body arriving here is the raw client wire. Only the OpenAI Chat
// Completions shape is wired today (ClientProtocol="" or
// "openai-completions"), parsed via ir.ParseOpenAI.
//
// ★ "gemini-generate" never appears as an inbound ClientProtocol: the
// Gemini ingress converts a native Gemini request into OpenAI shape
// before dispatch (domains/streaming/handler_gemini.go step 6), so the
// dispatcher detects "openai-completions" for Gemini clients too.
//
// Validation gates (each is an explicit check, no fall-throughs):
//
//  1. non-empty body
//  2. ClientProtocol ∈ {"", "openai-completions"} — the only wired
//     inbound target. Other wires fail closed here rather than getting a
//     wrong-shaped answer later.
//  3. body parses strictly as OpenAI Chat wire JSON (ir.ParseOpenAI).
//     The parser preserves unknown fields in Extensions so vendor extras
//     don't silently disappear.
//  4. model required — Gemini's API is model-scoped, so a missing model
//     would produce a URL without a model segment.
//  5. messages required — an empty conversation is a guaranteed-failure
//     upstream call.
//  6. the dispatcher's stream decision wins over any body-level
//     "stream" flag, so the wire framing always matches what the
//     response path expects.
//
// On any failure the executor returns a typed *upstreampkg.Error so the
// dispatcher classifies the precise kind. We never fabricate a default
// body and never forward an unvalidated body to the upstream.
func (e *Executor) finalizeGeminiUpstreamBody(params *ExecParams, cand provider.Candidate, body []byte) ([]byte, error) {
	if len(body) == 0 {
		return nil, &upstreampkg.Error{
			Kind:    errorsx.KindClientBug,
			Message: "gemini executor: empty upstream body",
		}
	}
	switch canonicalInboundWire(params.ClientProtocol) {
	case "", providercatalog.ProtocolOpenAICompletions:
		// supported — "openai-chat" (what ir.DetectProtocol actually
		// reports for the OpenAI Chat wire, including the Gemini ingress's
		// synthetic body) is normalized onto the same arm by
		// canonicalInboundWire.
	case providercatalog.ProtocolAnthropicMessages:
		// supported — the Anthropic request wire is parsed into IR and
		// re-serialized into the Gemini native wire below.
	case providercatalog.ProtocolOpenAIResponses:
		// supported — same, from the Responses request wire.
	default:
		return nil, &upstreampkg.Error{
			Kind:       errorsx.KindUnsupportedFeature,
			Message:    fmt.Sprintf("gemini executor: client protocol %q not yet wired", params.ClientProtocol),
			StatusCode: http.StatusNotImplemented,
		}
	}

	// Parse the inbound client wire into IR. Which parser applies is
	// decided by the INBOUND protocol, not by the upstream: the upstream
	// is always Gemini-native.
	var irReq *ir.InternalRequest
	var err error
	switch params.ClientProtocol {
	case providercatalog.ProtocolAnthropicMessages:
		irReq, err = ir.ParseAnthropic(body)
	case providercatalog.ProtocolOpenAIResponses:
		irReq, err = ir.ParseResponses(body)
	default:
		irReq, err = ir.ParseOpenAI(body)
	}
	if err != nil {
		return nil, &upstreampkg.Error{
			Kind:       errorsx.KindClientBug,
			Message:    fmt.Sprintf("gemini executor: inbound body is not valid %s wire JSON", geminiInboundWireName(params.ClientProtocol)),
			Err:        err,
			StatusCode: 0,
		}
	}
	// Trailing-data guard: ir.ParseOpenAI uses json.Unmarshal which
	// accepts trailing garbage. Reject it so a hostile client can't
	// smuggle a second body after the legitimate request.
	if hasTrailingJSON(body) {
		return nil, &upstreampkg.Error{
			Kind:    errorsx.KindClientBug,
			Message: "gemini executor: inbound body has trailing JSON content",
		}
	}

	irReq.SourceProtocol = ir.ProtocolOpenAIChat
	if params.ClientProtocol == providercatalog.ProtocolAnthropicMessages {
		irReq.SourceProtocol = ir.ProtocolAnthropicMessages
	} else if params.ClientProtocol == providercatalog.ProtocolOpenAIResponses {
		irReq.SourceProtocol = ir.ProtocolOpenAIResponses
	}

	if irReq.Model == "" {
		return nil, &upstreampkg.Error{
			Kind:       errorsx.KindClientBug,
			Message:    "gemini executor: client wire is missing required model field",
			StatusCode: 0,
		}
	}
	if len(irReq.Messages) == 0 {
		return nil, &upstreampkg.Error{
			Kind:       errorsx.KindClientBug,
			Message:    "gemini executor: client wire has empty messages array",
			StatusCode: 0,
		}
	}

	// Honor the dispatcher's stream decision. The client must not be
	// able to override the framing via a body-level "stream" flag —
	// otherwise the upstream could emit SSE while we built a non-stream
	// request (or vice versa) and the response path would mis-parse it.
	irReq.Stream = params.IsStream

	// The candidate's outbound model is authoritative for this attempt,
	// including failover to an offer with a different raw model name.
	if outboundModel := resolveOutboundModel(params, cand); outboundModel != "" {
		irReq.Model = outboundModel
	}

	// Mark the target dialect so the IR serializer renders the Gemini
	// thinking budget shape instead of the OpenAI one (same rule the
	// OpenAI-family path applies at its serialization point).
	if cand.CatalogCode != "" {
		irReq.TargetProvider = cand.CatalogCode
	} else {
		irReq.TargetProvider = paramreg.CatalogCodeUnknownRelay
	}
	// Restore the inbound reasoning intent (R45): a Gemini-native client
	// carries its thinking budget in the inbound body, which by now has
	// been normalized into OpenAI shape; the intent is recovered from the
	// request context so the Gemini serializer can render it.
	//
	// The nil guard matters: params.R is nil in isolated executor tests,
	// and a nil-pointer dereference inside a serialization helper is a
	// far worse failure mode than a missing thinking budget.
	if params.R != nil {
		irReq = ir.RestoreSourceReasoning(irReq, params.R.Context())
	}
	irReq = stampIRRequestID(irReq, params.RequestID)

	serialized, err := ir.SerializeGemini(irReq)
	if err != nil {
		return nil, &upstreampkg.Error{
			Kind:       errorsx.KindConversion,
			Message:    "gemini: serialize IR to native wire",
			Err:        err,
			StatusCode: 0,
		}
	}
	return serialized, nil
}
