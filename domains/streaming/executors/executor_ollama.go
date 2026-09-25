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
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/internal/ir"
	"github.com/kaixuan/llm-gateway-go/internal/upstreamurl"
	"github.com/kaixuan/llm-gateway-go/provider"
	upstreampkg "github.com/kaixuan/llm-gateway-go/upstream"
)

// OllamaExecutor implements ProtocolHandler for Ollama's native /api/chat
// (NDJSON chat-completion) protocol. It pairs with the IR contracts in
// internal/ir:
//
//   - SerializeOllama (serialize_ollama.go) — build the upstream request
//     body preserving every Ollama-private top-level / options.* field
//     (format, keep_alive, raw, options.num_ctx, ...).
//   - ParseOllamaResponse (parse_ollama.go) — parse a single non-stream
//     JSON response into IR.InternalResponse.
//   - ParseOllamaStreamChunk (parse_ollama_stream.go) — parse one NDJSON
//     line into one or more IR.StreamChunk; contract: content is
//     CUMULATIVE and lives on StreamChunk.CumulativeContent, reasoning is
//     incremental and lives on StreamChunk.Delta.ReasoningContent.
//
// # Scope (audit-r0924 P4.3)
//
// This task wires the OUTBOUND side only:
//
//   - Outbound: gateway → Ollama upstream at POST /api/chat
//     (NDJSON for stream, JSON for non-stream). All Ollama-private
//     fields (options.*, format, keep_alive, raw, tools) are
//     preserved verbatim via SerializeOllama.
//   - Client-facing wire: OpenAI Chat Completions (chat.completion
//     JSON / chat.completion.chunk SSE). OpenAI Responses is rejected
//     until its distinct response envelope and stream events are supported.
//   - Inbound Ollama native /api/chat handler is not included here.
//     The dispatcher supplies an OpenAI Chat request body; the executor
//     parses it into IR before serializing the Ollama upstream body.
//
// Wire differences vs. OpenAI Chat Completions (see
// docs/供应商协议优化-实施规划.md §2.1):
//
//   - Path is /api/chat (no /v1 prefix), constructed via upstreamurl.EpOllamaChat.
//   - Streaming body is application/x-ndjson, not text/event-stream.
//     Termination is signalled by a chunk whose top-level "done" is true
//     (NOT a `[DONE]` SSE sentinel).
//   - Sampling parameters are nested under `options.*`; max_tokens is
//     `options.num_predict`.
//   - Reasoning content is `message.thinking` (Ollama 0.5+), surfaced as
//     StreamDelta.ReasoningContent.
//   - Upstream errors are a top-level "error" string on a 200 response.
//
// Lifecycle semantics (audit-r0924 §11.6, §3.6):
//   - Non-stream errors: returned via *upstreampkg.Error with a typed
//     Kind so the dispatcher can route them through the §11.6 fail wire.
//     An Ollama `error` field on a 200 is converted to a
//     KindUpstreamDown *upstreampkg.Error (which surfaces ir.StreamError
//     verbatim via errors.As).
//   - Stream interruptions: StreamOutcome.Interrupted=true with a Kind
//     when the NDJSON loop terminates without seeing a `done:true`
//     frame, so the dispatcher / state machine can avoid a "successful
//     empty stream" false-positive.
//   - BeginUpstreamAttempt: this executor intentionally does NOT call
//     beginUpstreamAttempt (which lives on the enclosing *Executor).
//     The common executor routes the call into the right protocol
//     branch; this struct is the protocol-specific worker and stays
//     unaware of attempt bookkeeping. Wiring is the dispatcher's job
//     (see executor_dispatch.go, which the audit explicitly scopes out
//     for this task — P4.3 is the executor + tests only).
type OllamaExecutor struct {
	// ClientProtocol is the wire format the inbound client used. Empty
	// or "openai-completions" → synthesize OpenAI chat wire (the
	// production target today). "openai-responses" is reserved for P5.
	ClientProtocol string
	// ProviderID is forwarded to per-provider IR converters so circuit
	// breaker isolation stays correct (added 2026-08-09 for Anthropic;
	// mirrored here for symmetry).
	ProviderID int
	// IR is the unified protocol-conversion interface used for the
	// response IR→client-wire serialization step. When nil the executor
	// falls back to a hand-rolled OpenAI chat wire shape (sufficient
	// for the executor's standalone tests; production wires IR via
	// main.go the same way AnthropicExecutor does).
	IR IRConverter
}

// Compile-time guarantee that OllamaExecutor satisfies ProtocolHandler.
var _ ProtocolHandler = (*OllamaExecutor)(nil)

// maxOllamaNDJSONLineSize caps a single NDJSON line the executor is
// willing to buffer. Ollama's largest known output is a long thinking
// trace; 4 MiB matches executor_chat.go's SSE line buffer and is
// generous without enabling memory blowups on a hostile upstream.
const maxOllamaNDJSONLineSize = 4 * 1024 * 1024

// ollamaClientWire returns the ClientProtocol default ("openai-completions")
// when unset. The downstream OpenAI client today is the only supported
// inbound wire; anthropic/responses variants are future work.
func (o *OllamaExecutor) clientWire() string {
	if o == nil || o.ClientProtocol == "" {
		return "openai-completions"
	}
	return o.ClientProtocol
}

// BuildRequest assembles the upstream HTTP POST against the Ollama
// /api/chat endpoint.
//
//	body MUST already be the IR-serialized Ollama native body produced
//	by ir.SerializeOllama (the dispatch bridge does this conversion
//	once per attempt and caches the result). The executor does NOT
//	re-serialize — re-running SerializeOllama here would re-interpret
//	Extensions and could double-emit private keys if the IR has been
//	mutated between the bridge pass and the wire send.
//
// Auth header follows Ollama's convention: most self-hosted instances
// ignore Authorization, but operators can opt into OLLAMA_API_KEY at
// which point Ollama expects a Bearer token (matching OpenAI Chat
// shape). We therefore emit Bearer when a key is configured and omit
// the header otherwise — letting the server enforce its own auth policy.
func (o *OllamaExecutor) BuildRequest(cand provider.Candidate, body []byte, isStream bool) (*http.Request, error) {
	if cand.BaseURL == "" {
		return nil, fmt.Errorf("ollama executor: candidate base URL is empty")
	}
	upstreamURL := upstreamurl.OllamaChatURL(cand.BaseURL)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, upstreamURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama executor: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if cand.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cand.APIKey)
	}
	if isStream {
		// Ollama's NDJSON framing is gated on Accept: some HTTP
		// middleboxes reject text/event-stream and route the request
		// to a non-stream handler. application/x-ndjson is the
		// documented Ollama streaming content type.
		req.Header.Set("Accept", "application/x-ndjson")
	}
	return req, nil
}

// WriteNonStreamResponse writes a complete Ollama non-stream JSON
// response back to the client, translated to OpenAI chat.completion
// wire shape.
//
// Failure modes:
//
//   - Upstream returned non-2xx (e.g. 400 model not found): the body is
//     returned verbatim with the upstream status preserved so the
//     dispatcher can see the actual upstream error. We DO NOT try to
//     classify the upstream HTTP code here — that's executor_dispatch's
//     job once it sees the *upstreampkg.Error that BuildRequest will
//     later surface via the response writer.
//   - Ollama surfaces some failures as a 200 + `{"error":"..."}` body.
//     ParseOllamaResponse returns this as an *ir.StreamError, which we
//     repackage as a typed *upstreampkg.Error{Kind: KindUpstreamError}
//     so the dispatcher's §11.6 fail wire keys on the typed error
//     (errors.As(err, **StreamError) succeeds all the way through).
//   - Body parse failure (malformed JSON, etc.): returned as a typed
//     *upstreampkg.Error{Kind: KindConversion} with the parse error
//     wrapped, so the upstream payload is preserved for diagnostics
//     while the failure is non-retryable.
//
// qualityFixMode / qualitySignals: Ollama doesn't share the OpenAI
// tool-call quality path (its tool_calls wire shape differs and the
// Ollama-server-side tool call handling is upstream's responsibility).
// We deliberately leave both untouched — the executor interface marks
// the signals as optional, and QualitySignals is nil in this branch.
func (o *OllamaExecutor) WriteNonStreamResponse(w http.ResponseWriter, resp *http.Response, clientModel, qualityFixMode string, qualitySignals *QualitySignals) ([]byte, error) {
	if resp == nil || resp.Body == nil {
		return nil, &upstreampkg.Error{
			Kind:       errorsx.KindUpstreamDown,
			Message:    "ollama upstream returned nil response",
			StatusCode: 0,
		}
	}
	//nolint:errcheck // best-effort close
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &upstreampkg.Error{
			Kind:       errorsx.KindNetwork,
			Message:    "ollama: read response body",
			Err:        err,
			StatusCode: resp.StatusCode,
			Body:       body,
		}
	}

	// Non-2xx: pass the body through verbatim with the upstream status
	// so the dispatcher's classifier sees the real vendor error. We
	// intentionally do NOT translate this to a synthetic OpenAI-shaped
	// error envelope — the upstream envelope (Ollama's `{"error":"..."}`
	// or proxy-injected HTML) is what the operator's debug log needs
	// to see. The dispatcher's §11.6 fail wire will turn it into the
	// appropriate client-facing error envelope.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		copyNonStreamResponseHeaders(w.Header(), resp.Header, len(body))
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(resp.StatusCode)
		_, werr := w.Write(body)
		// Even on write error, return the captured body so the
		// dispatcher's request-log preview sees the upstream payload.
		return body, werr
	}

	// Parse the Ollama native JSON body into IR. The parser is
	// responsible for the "200 + error" branch — it surfaces an
	// *ir.StreamError which we re-key as *upstreampkg.Error.
	parsed, parseErr := ir.ParseOllamaResponse(body)
	if parseErr != nil {
		// r0924 fix-a task 3: typed StreamError from the parser
		// indicates an upstream `error` payload on a 200. Repackage
		// as *upstreampkg.Error{Kind: KindUpstreamError} so the
		// dispatcher's §11.6 fail wire can route it through
		// errors.As(err, **StreamError) and emit a real vendor
		// envelope rather than the legacy "empty response" message.
		var streamErr *ir.StreamError
		if errors.As(parseErr, &streamErr) && streamErr != nil {
			return nil, &upstreampkg.Error{
				Kind:       errorsx.KindUpstreamDown,
				Message:    streamErr.Message,
				Err:        streamErr,
				StatusCode: resp.StatusCode,
				Body:       body,
			}
		}
		return nil, &upstreampkg.Error{
			Kind:       errorsx.KindConversion,
			Message:    "ollama: parse response body",
			Err:        parseErr,
			StatusCode: resp.StatusCode,
			Body:       body,
		}
	}

	// Substitute the client-visible model with the actual client
	// request model so audit logs and OpenAI clients see the model
	// they sent, not the upstream's echoed identifier. This mirrors
	// ChatExecutor's replaceModelInResponseBody behavior.
	if clientModel != "" {
		parsed.Model = clientModel
	}

	// Translate IR → client wire. Today the only supported client is
	// OpenAI chat.completion; future openai-responses support can be
	// added behind a switch on ClientProtocol exactly the way
	// AnthropicExecutor branches today.
	var out []byte
	switch o.clientWire() {
	case "openai-completions", "":
		if o.IR != nil {
			var irScoped IRConverter = o.IR
			if scoped, ok := irScoped.(ProviderScoped); ok {
				irScoped = scoped.WithProviderScope(o.ProviderID)
			}
			out, err = irScoped.SerializeOpenAIResponse(parsed, clientModel)
		} else {
			out, err = serializeOllamaIRToOpenAIChat(parsed, clientModel)
		}
	default:
		// Reserved for P5 (openai-responses / future Ollama-native
		// client). Return a clear typed error rather than fabricating
		// a wrong-shape body — the dispatcher's classifier will then
		// surface it as KindUnsupportedFeature for now.
		return nil, &upstreampkg.Error{
			Kind:       errorsx.KindUnsupportedFeature,
			Message:    fmt.Sprintf("ollama executor: client protocol %q not yet wired", o.ClientProtocol),
			StatusCode: resp.StatusCode,
		}
	}
	if err != nil {
		return nil, &upstreampkg.Error{
			Kind:       errorsx.KindConversion,
			Message:    "ollama: serialize response to client wire",
			Err:        err,
			StatusCode: resp.StatusCode,
			Body:       body,
		}
	}

	// Honor the upstream status if it was a 2xx-with-warning (rare for
	// Ollama but possible via reverse-proxy injection) by passing the
	// status through; default 200 otherwise.
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

// StreamResponse reads an Ollama native NDJSON stream and writes an
// OpenAI SSE stream back to the client.
//
// Lifecycle (audit-r0924 fix-a tasks 1+4):
//
//   - One NDJSON line may carry BOTH content AND done (terminal one-shot
//     answers like "yes" / "no"). ParseOllamaStreamChunk already splits
//     that into a content chunk first then a Done chunk; we honor the
//     ordering and never collapse them.
//   - "Fabricated success on missing done" guard: if the upstream EOFs
//     without ever emitting `done:true`, StreamOutcome.Interrupted=true
//     with KindEmptyResponse. The dispatcher's stream gate treats
//     KindEmptyResponse as a non-billable upstream silence rather than
//     a successful empty response (audit-r0924 §11.6 eof_without_done).
//   - First semantic byte callback fires on the first content OR
//     reasoning OR tool-call chunk we actually write downstream — NOT
//     on heartbeats or pre-done lines that produced no delta. This
//     matches the executor_dispatch.go contract on
//     params.FirstSemanticByteCallback (see executor.go:1314).
//   - Terminal [DONE] SSE sentinel is written exactly once when the
//     NDJSON stream signals done (either via the done chunk or via
//     upstream EOF-without-done, in which case it's preceded by an
//     `error` SSE event so the client doesn't mistake the silence
//     for a normal stream end).
//   - Per-line parse errors are tolerated: a malformed NDJSON line is
//     skipped and the loop continues. Repeated parse failures log a
//     warning but do not abort the stream unless we have already
//     failed to see a single valid chunk before EOF.
func (o *OllamaExecutor) StreamResponse(ctx context.Context, w http.ResponseWriter, resp *http.Response) StreamOutcome {
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
	// Each NDJSON line is at most maxOllamaNDJSONLineSize bytes; this
	// also handles Ollama's long thinking traces on local servers.
	scanner.Buffer(make([]byte, 0, 64*1024), maxOllamaNDJSONLineSize)

	// Cumulative-content tracking (audit-r0924 fix-a task 1).
	// Ollama's wire-level `message.content` is CUMULATIVE (the full
	// assistant text so far), NOT a per-character delta like
	// OpenAI/Anthropic SSE. ParseOllamaStreamChunk therefore reports
	// it via StreamChunk.CumulativeContent and leaves Delta nil for
	// content frames. We diff each new cumulative value against the
	// previous one and emit ONLY the new tail as
	// delta.content — anything else would duplicate the full text on
	// every frame, which is the exact bug §11.6 calls out.
	var prevCumulative string

	// Reasoning content is incremental: ParseOllamaStreamChunk emits
	// delta frames with the new thinking text. We forward verbatim.
	// First-byte callback latches when we write the FIRST semantic
	// byte (content delta, reasoning delta, or tool-call). We DO NOT
	// fire on Done chunks or empty deltas.
	firstByteFired := false
	fireFirstByte := func() {
		if firstByteFired {
			return
		}
		firstByteFired = true
		if w != nil {
			if setter, ok := w.(interface{ FirstSemanticByteCallback() func() }); ok {
				if cb := setter.FirstSemanticByteCallback(); cb != nil {
					cb()
				}
			}
		}
	}

	flusher, _ := w.(http.Flusher)
	flush := func() {
		if flusher != nil {
			flusher.Flush()
		}
	}

	outcome := StreamOutcome{}
	chunkCount := 0
	sawDone := false
	parseFailures := 0

	// Build a small per-stream writer that emits OpenAI SSE chunks.
	sseWriter := newOllamaSSEWriter(w, flush)

	for scanner.Scan() {
		// Honor client cancellation: if the request context has been
		// canceled (client disconnect, server-side abort), break the
		// loop and surface Interrupted=true. We deliberately do not
		// wrap this in a goroutine because bufio.Scanner blocks on
		// Read and we need the ctx check at the top of each iteration
		// to catch a cancel that arrived mid-line.
		if err := ctx.Err(); err != nil {
			outcome.Interrupted = true
			outcome.Reason = "client_disconnected"
			outcome.Kind = errorsx.KindCanceled
			outcome.ChunkCount = chunkCount
			return outcome
		}

		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}

		chunks, perr := ir.ParseOllamaStreamChunk(line)
		if perr != nil {
			// Per the §11.6 noise-tolerance rule: a single malformed
			// NDJSON line is skipped, the stream keeps going.
			parseFailures++
			continue
		}
		if len(chunks) == 0 {
			continue
		}

		for _, chunk := range chunks {
			if chunk == nil {
				continue
			}
			switch chunk.Type {
			case ir.ChunkTypeError:
				// Upstream surfaced an in-band error frame. The
				// §11.6 fail wire wants the client to see a typed
				// error event followed by [DONE]; we render it now,
				// mark the stream interrupted (so the dispatcher
				// does not double-render), and stop.
				_ = sseWriter.WriteError(chunk.Error)
				_ = sseWriter.WriteDone()
				outcome.Interrupted = true
				if chunk.Error != nil && chunk.Error.Message != "" {
					outcome.Reason = chunk.Error.Message
				} else {
					outcome.Reason = "upstream_error"
				}
				outcome.ChunkCount = chunkCount + 1
				outcome.Kind = errorsx.KindUpstreamDown
				outcome.TerminalRendered = true
				return outcome

			case ir.ChunkTypeDone:
				// Terminal frame. Emit [DONE] (the OpenAI SSE
				// sentinel) so the client knows the stream is over,
				// then exit cleanly. We do NOT fire the first-byte
				// callback on Done frames — by definition the first
				// semantic byte already fired earlier, or no semantic
				// byte ever arrived (empty upstream).
				if err := sseWriter.WriteDone(); err != nil {
					outcome.Interrupted = true
					outcome.Reason = "client_write_failed"
					outcome.Kind = errorsx.KindNetwork
					outcome.ChunkCount = chunkCount
					return outcome
				}
				sawDone = true
				outcome.ChunkCount = chunkCount + 1
				// Fall through to loop exit below.

			case ir.ChunkTypeDelta:
				// Content delta (cumulative → emit only the new tail)
				// or reasoning delta (incremental → forward verbatim).
				wrote := false
				if chunk.CumulativeContent != "" {
					// Diff cumulative against the previous value. The
					// first time we see content, prev is "" and the
					// entire string is the delta. The §11.6 invariant:
					// never re-emit bytes already sent.
					newTail := chunk.CumulativeContent
					if strings.HasPrefix(newTail, prevCumulative) {
						newTail = newTail[len(prevCumulative):]
					} else {
						// Defensive: if the upstream rolled backward
						// (model reset, retry), treat the full value
						// as new. This should never happen with a
						// well-behaved Ollama, but guarding here is
						// cheaper than duplicating text on the wire.
						newTail = chunk.CumulativeContent
					}
					prevCumulative = chunk.CumulativeContent
					if newTail != "" {
						if err := sseWriter.WriteContentDelta(newTail, chunk.Model, chunk.ID, chunk.Created); err != nil {
							outcome.Interrupted = true
							outcome.Reason = "client_write_failed"
							outcome.Kind = errorsx.KindNetwork
							outcome.ChunkCount = chunkCount
							return outcome
						}
						wrote = true
						chunkCount++
					}
				}
				if chunk.Delta != nil {
					if chunk.Delta.ReasoningContent != "" {
						if err := sseWriter.WriteReasoningDelta(chunk.Delta.ReasoningContent, chunk.Model, chunk.ID, chunk.Created); err != nil {
							outcome.Interrupted = true
							outcome.Reason = "client_write_failed"
							outcome.Kind = errorsx.KindNetwork
							outcome.ChunkCount = chunkCount
							return outcome
						}
						wrote = true
						chunkCount++
					}
					if len(chunk.Delta.ToolCalls) > 0 {
						if err := sseWriter.WriteToolCalls(chunk.Delta.ToolCalls, chunk.Model, chunk.ID, chunk.Created); err != nil {
							outcome.Interrupted = true
							outcome.Reason = "client_write_failed"
							outcome.Kind = errorsx.KindNetwork
							outcome.ChunkCount = chunkCount
							return outcome
						}
						wrote = true
						chunkCount++
					}
				}
				if wrote {
					fireFirstByte()
				}

			case ir.ChunkTypeUsage:
				// Pure usage frame (Ollama's done line carries usage
				// too, so this branch is rare in practice — but if a
				// future Ollama build emits a usage-only chunk we
				// still want to surface it). We don't increment
				// chunkCount for usage-only frames (no client-visible
				// semantic bytes), so the first-byte callback isn't
				// fired here.
				_ = chunk.Usage // noted; outcome.Usage not part of StreamOutcome
			}
		}
		if sawDone {
			// Break out of the outer scanner loop so we don't waste a
			// Read on the next iteration; the upstream may keep the
			// connection open until the keep-alive timeout.
			break
		}
	}

	// Scanner done. Distinguish "clean EOF after done" from "EOF before
	// done ever arrived". The §11.6 eof_without_done frame is the
	// canonical way to surface the latter to OpenAI clients.
	if err := scanner.Err(); err != nil {
		// Network-level read error after at least one chunk: treat as
		// interrupted mid-stream so the dispatcher can decide whether
		// the error is recoverable.
		outcome.Interrupted = true
		outcome.Reason = "ollama_stream_read_error: " + err.Error()
		outcome.Kind = errorsx.KindNetwork
		outcome.ChunkCount = chunkCount
		outcome.Resumable = chunkCount == 0
		return outcome
	}

	if !sawDone {
		// Upstream EOF without ever emitting a `done:true` frame. This
		// is the §11.6 eof_without_done case — we MUST NOT report
		// success (that would bill an empty response and hide the
		// truncation), so we emit an `error` SSE event plus [DONE]
		// and mark the outcome as a non-billable interruption.
		_ = sseWriter.WriteEOFWithoutDone()
		_ = sseWriter.WriteDone()
		outcome.Interrupted = true
		outcome.Reason = "eof_without_done"
		outcome.Kind = errorsx.KindEmptyResponse
		outcome.ChunkCount = chunkCount
		outcome.Resumable = chunkCount == 0
		outcome.TerminalRendered = true
		return outcome
	}

	if parseFailures > 0 && chunkCount == 0 {
		// We saw lines but none were valid NDJSON. Surface as
		// conversion error so the dispatcher can demote the candidate.
		outcome.Interrupted = true
		outcome.Reason = fmt.Sprintf("ollama_ndjson_unparseable: %d bad lines", parseFailures)
		outcome.Kind = errorsx.KindConversion
		outcome.ChunkCount = 0
		return outcome
	}

	return outcome
}

// ExtractUsage extracts token counts from a non-stream Ollama response
// body. Returns (inputTokens, outputTokens) as *int pointers so a nil
// means "unknown / not present" (mirrors Anthropic/OpenAI conventions).
func (o *OllamaExecutor) ExtractUsage(resp *http.Response, body []byte) (*int, *int) {
	if len(body) == 0 {
		return nil, nil
	}
	var v struct {
		PromptEvalCount *int `json:"prompt_eval_count"`
		EvalCount       *int `json:"eval_count"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, nil
	}
	return v.PromptEvalCount, v.EvalCount
}

// CheckSoftMismatch detects the silent-substitution class of bug for
// Ollama: the upstream echoes a different model than the client
// requested. Today Ollama's native API does not silently substitute
// (the server returns 404 if the model isn't installed), so the only
// realistic mismatch is operator-side model aliasing.
//
// We mirror ChatExecutor's semantics: non-empty req + resp where
// they differ case-insensitively is a soft mismatch. The dispatcher
// will record it on the request log so operators can audit silently
// misrouted traffic.
func (o *OllamaExecutor) CheckSoftMismatch(reqModel, respModel string) (bool, string) {
	if reqModel == "" || respModel == "" {
		return false, ""
	}
	if !strings.EqualFold(reqModel, respModel) {
		return true, "ollama_response_model_differs_from_request"
	}
	return false, ""
}

// ────────────────────────────────────────────────────────────────────────
// OpenAI-shaped fallback serializer
// ────────────────────────────────────────────────────────────────────────

// serializeOllamaIRToOpenAIChat is a minimal IR → OpenAI chat.completion
// JSON serializer used when OllamaExecutor.IR is nil (standalone tests,
// or deployments that opt out of the IR converter wiring). Production
// deployments SHOULD wire IR via main.go so per-provider overrides take
// effect — but this fallback keeps the executor usable in isolation
// without a circular dependency on the relay layer.
//
// The output shape matches OpenAI's chat.completion wire exactly:
//
//	{
//	  "id": "ollama-<...>",
//	  "object": "chat.completion",
//	  "created": <unix>,
//	  "model": <clientModel or resp.Model>,
//	  "choices": [{
//	    "index": 0,
//	    "message": {
//	      "role": "assistant",
//	      "content": <joined text content>,
//	      "reasoning_content": <reasoning>, // OpenAI-compatible extension
//	    },
//	    "finish_reason": <normalized>
//	  }],
//	  "usage": { "prompt_tokens":..., "completion_tokens":..., "total_tokens":... }
//	}
//
// We deliberately include `reasoning_content` (an OpenAI extension used
// by DeepSeek, Qwen QwQ, etc.) so a downstream OpenAI client that
// already understands reasoning_content can render the Ollama thinking
// trace without losing data.
func serializeOllamaIRToOpenAIChat(resp *ir.InternalResponse, clientModel string) ([]byte, error) {
	if resp == nil {
		return nil, fmt.Errorf("serialize ollama ir: nil response")
	}
	model := resp.Model
	if clientModel != "" {
		model = clientModel
	}
	// Concatenate text content blocks with a newline so multi-block
	// Ollama responses (rare, but possible if a future build splits
	// text and tool output across blocks) remain readable.
	var contentText strings.Builder
	for _, b := range resp.Content {
		if b.Type == "text" && b.Text != "" {
			if contentText.Len() > 0 {
				contentText.WriteString("\n")
			}
			contentText.WriteString(b.Text)
		}
	}
	message := map[string]any{
		"role": "assistant",
	}
	if contentText.Len() > 0 {
		message["content"] = contentText.String()
	} else {
		message["content"] = ""
	}
	if resp.ReasoningContent != "" {
		message["reasoning_content"] = resp.ReasoningContent
	}

	out := map[string]any{
		"id":      resp.ID,
		"object":  "chat.completion",
		"created": resp.Created,
		"model":   model,
		"choices": []map[string]any{
			{
				"index": 0,
				"message": message,
				"finish_reason": func() string {
					if resp.FinishReason != "" {
						return resp.FinishReason
					}
					return "stop"
				}(),
			},
		},
	}
	if resp.Usage.TotalTokens > 0 || resp.Usage.PromptTokens > 0 || resp.Usage.CompletionTokens > 0 {
		out["usage"] = map[string]int{
			"prompt_tokens":     resp.Usage.PromptTokens,
			"completion_tokens": resp.Usage.CompletionTokens,
			"total_tokens":      resp.Usage.TotalTokens,
		}
	}
	return json.Marshal(out)
}

// ────────────────────────────────────────────────────────────────────────
// SSE writer (OpenAI-shaped)
// ────────────────────────────────────────────────────────────────────────

// ollamaSSEWriter renders IR.StreamChunks into OpenAI SSE wire
// (`data: {...}\n\n` lines + a final `data: [DONE]\n\n`). One writer
// per stream; flushes after every frame so a slow client still sees
// progressive output (TTFB / first-byte metrics). All write errors
// are propagated up so the caller can mark the stream interrupted.
type ollamaSSEWriter struct {
	w        http.ResponseWriter
	flush    func()
	chunkIdx int
	id       string
	model    string
	created  int64
}

func newOllamaSSEWriter(w http.ResponseWriter, flush func()) *ollamaSSEWriter {
	return &ollamaSSEWriter{w: w, flush: flush, created: time.Now().Unix()}
}

// writeFrame serializes one OpenAI-shaped SSE chunk and writes it to
// the wire. Returns the underlying write error so the caller can abort
// the stream on client disconnect.
func (s *ollamaSSEWriter) writeFrame(payload []byte) error {
	if s.w == nil {
		return nil
	}
	if _, err := s.w.Write([]byte("data: ")); err != nil {
		return err
	}
	if _, err := s.w.Write(payload); err != nil {
		return err
	}
	if _, err := s.w.Write([]byte("\n\n")); err != nil {
		return err
	}
	if s.flush != nil {
		s.flush()
	}
	return nil
}

// writeDeltaFrame emits a chat.completion.chunk whose `choices[0].delta`
// carries the given fields. The first chunk in the stream also
// stamps `delta.role = "assistant"` so the client can establish the
// role exactly once.
func (s *ollamaSSEWriter) writeDeltaFrame(delta map[string]any, model, id string, created int64) error {
	if s.chunkIdx == 0 {
		delta["role"] = "assistant"
	}
	if id != "" {
		s.id = id
	}
	if model != "" {
		s.model = model
	}
	if created > 0 {
		s.created = created
	}
	frameID := s.id
	if frameID == "" {
		frameID = fmt.Sprintf("ollama-stream-%d", time.Now().UnixNano())
	}
	frameModel := s.model
	if frameModel == "" {
		frameModel = "ollama"
	}
	payload, err := json.Marshal(map[string]any{
		"id":      frameID,
		"object":  "chat.completion.chunk",
		"created": s.created,
		"model":   frameModel,
		"choices": []map[string]any{
			{"index": 0, "delta": delta},
		},
	})
	if err != nil {
		return err
	}
	s.chunkIdx++
	return s.writeFrame(payload)
}

// WriteContentDelta emits a chunk whose `delta.content` carries the
// new text tail.
func (s *ollamaSSEWriter) WriteContentDelta(text, model, id string, created int64) error {
	return s.writeDeltaFrame(map[string]any{"content": text}, model, id, created)
}

// WriteReasoningDelta emits a chunk whose `delta.reasoning_content`
// carries the incremental thinking text. We use the `reasoning_content`
// field name (not the more verbose `delta_type: reasoning`) because
// that's the de-facto wire convention used by DeepSeek, Qwen QwQ,
// and OpenAI o1/o3 SDKs that the gateway already supports.
func (s *ollamaSSEWriter) WriteReasoningDelta(text, model, id string, created int64) error {
	return s.writeDeltaFrame(map[string]any{"reasoning_content": text}, model, id, created)
}

// WriteToolCalls emits a delta frame for an incremental tool call.
func (s *ollamaSSEWriter) WriteToolCalls(calls []ir.StreamToolCallDelta, model, id string, created int64) error {
	if len(calls) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(calls))
	for _, c := range calls {
		entry := map[string]any{"index": c.Index}
		if c.ID != "" {
			entry["id"] = c.ID
		}
		if c.Type != "" {
			entry["type"] = c.Type
		}
		fn := map[string]any{}
		if c.Name != "" {
			fn["name"] = c.Name
		}
		if c.Arguments != "" {
			fn["arguments"] = c.Arguments
		}
		if len(fn) > 0 {
			entry["function"] = fn
		}
		out = append(out, entry)
	}
	return s.writeDeltaFrame(map[string]any{"tool_calls": out}, model, id, created)
}

// WriteError emits an `error` SSE frame so the OpenAI client sees
// the typed upstream error inline rather than as a connection drop.
// Follows the §11.6 fail-wire shape so callers routing this through
// the dispatcher's classifier get a stable envelope.
func (s *ollamaSSEWriter) WriteError(streamErr *ir.StreamError) error {
	if streamErr == nil {
		streamErr = &ir.StreamError{Type: "upstream_error", Message: "ollama upstream error"}
	}
	payload, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"type":    streamErr.Type,
			"message": streamErr.Message,
			"code":    streamErr.Code,
		},
	})
	if err != nil {
		return err
	}
	return s.writeFrame(payload)
}

// WriteDone emits the OpenAI `[DONE]` SSE sentinel that closes the
// stream. The literal string matches OpenAI's chat.completion chunk
// streaming protocol byte-for-byte.
func (s *ollamaSSEWriter) WriteDone() error {
	if s.w == nil {
		return nil
	}
	if _, err := s.w.Write([]byte("data: [DONE]\n\n")); err != nil {
		return err
	}
	if s.flush != nil {
		s.flush()
	}
	return nil
}

// WriteEOFWithoutDone emits the §11.6 eof_without_done terminal frame
// so the OpenAI client sees a real error event (not just an EOF)
// before the dispatcher's §11.6 fail wire closes the stream.
func (s *ollamaSSEWriter) WriteEOFWithoutDone() error {
	return s.WriteError(&ir.StreamError{
		Type:    "stream_truncated",
		Message: "ollama stream ended before done frame was received",
		Code:    "eof_without_done",
	})
}

// ────────────────────────────────────────────────────────────────────────
// Executor integration
// ────────────────────────────────────────────────────────────────────────

// executeOllama is the r0924 P4.3 dispatch entry point: when the
// candidate's Protocol resolves to ProtocolOllamaNative (Stage 1A
// passthrough OR the dispatcher's default Ollama-native case), the
// common Executor routes the attempt here.
//
// The implementation intentionally mirrors executeAnthropic's overall
// shape (BuildRequest → Do → status-code branching → WriteNonStreamResponse
// or StreamResponse) but is materially simpler:
//
//   - No anthropic-style request-body transform: the bridge has already
//     produced IR, the OllamaExecutor.BuildRequest re-serializes via
//     SerializeOllama so the wire body always has the canonical Ollama
//     options.* / format / keep_alive / raw passthrough.
//   - No streaming-shaped retry machinery: Ollama is local/edge-deployed
//     and the typical outage modes (429 / 401 / 5xx / network) use the
//     same ClassifyErrorWithBody path the other executors use, so the
//     outer Execute loop can retry / failover without this method
//     re-implementing the retry budget.
//   - No context-length recovery, no reqprobe mode-fallback: P4.3
//     focuses on the wire-level contract; recovery is layered on top
//     in a follow-up.
//
// beginUpstreamAttempt is the per-attempt lifecycle hook the executor
// owns: it consumes one attempt slot from the executor's attempt
// budget, stamps provider/credential/attempt metadata onto the
// params, and emits a live-action event for telemetry. It does NOT
// perform the HTTP call — the actual Do() happens below.
//
// P1-2 fix (2026-08-28): ctx is propagated into the stream call so
// the dispatcher's gate can cancel mid-stream without leaking
// goroutines.
func (e *Executor) executeOllama(
	params *ExecParams,
	cand provider.Candidate,
	maxRetries int,
	tTotal time.Time,
	fpLease *credentialfpslot.Lease,
) (*ExecuteResult, error) {
	sourceBody := append([]byte(nil), params.BodyBytes...)

	// Re-serialize the IR-shaped client body into the Ollama native
	// wire shape. This is the P4.3 core: every Ollama-private top-
	// level / options.* field is preserved verbatim. The bridge may
	// have already done this conversion earlier in the pipeline; we
	// run it again defensively so the executor is correct in
	// isolation (tests, future ad-hoc callers) and so any mutation of
	// params.BodyBytes between bridge-pass and wire-send is reflected
	// in the wire body.
	bodyBytes, err := e.finalizeOllamaUpstreamBody(params, cand, sourceBody)
	if err != nil {
		return nil, err
	}

	oe := &OllamaExecutor{
		ClientProtocol: params.ClientProtocol,
		ProviderID:     cand.ProviderID,
		IR:             e.IR,
	}

	req, err := oe.BuildRequest(cand, bodyBytes, params.IsStream)
	if err != nil {
		return nil, err
	}
	// Forward the session headers (Track C C2) so the upstream probe
	// path can recognize session-bearing requests if it ever needs to.
	if sid := params.R.Header.Get("X-Gw-Session-Id"); sid != "" {
		req.Header.Set("X-Gw-Session-Id", sid)
	}
	if sid := params.R.Header.Get("X-Session-Id"); sid != "" {
		req.Header.Set("X-Session-Id", sid)
	}
	req.Header.Set("X-Request-Id", diagnosticRequestID(params))
	if fpLease != nil && fpLease.Egress != nil {
		credentialfpslot.ApplyEgressHeaders(req.Header, fpLease.Egress)
	}

	timeout := e.UpstreamTimeout
	if params.IsStream {
		timeout = e.StreamTimeout
	}
	ctx, cancel := e.upstreamContext(params, timeout)
	defer cancel()
	req = req.WithContext(ctx)

	if err := e.beginUpstreamAttempt(params, cand, diagnosticProtocol(cand.Protocol, "ollama-native"), bodyBytes); err != nil {
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
		slog.Info("ollama upstream attempt error",
			"request_id", params.RequestID,
			"credential_id", cand.CredentialID,
			"kind", uErr.Kind,
			"err_message_bytes", len(uErr.Message),
		)
		// Track routing failures so the closed-loop-2 recovery path
		// sees Ollama-native candidates (mirrors the Anthropic path).
		if params.RoutingTracker != nil {
			statusCode := 0
			if resp != nil {
				statusCode = resp.StatusCode
			}
			retryable := errorsx.ProjectRecovery(uErr.Kind).GenericRetryable
			attempt := RoutingAttempt{
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
			}
			params.RoutingTracker.Add(attempt)
		}
		// 5xx + network errors → retryable; let the outer Execute
		// loop's failover machinery handle them.
		if uErr.Kind == errorsx.KindRateLimit {
			if e.Limiter != nil {
				e.Limiter.Shrink(cand.ProviderID, cand.CredentialID)
			}
		}
		// Network/timeout/overload → wrap in retryable so the outer
		// loop tries the next credential/candidate.
		if errorsx.IsRetryable(uErr.Kind) || uErr.Kind == errorsx.KindUpstreamDown || uErr.Kind == errorsx.KindNetwork {
			return nil, &retryableError{err: uErr}
		}
		// Non-retryable: propagate as a typed *upstreampkg.Error so
		// the dispatcher's classifier can record the precise kind.
		return nil, uErr
	}

	// 4xx handling: capture the body for diagnostics and surface a
	// typed error. Ollama's 4xx payloads are typically small (model
	// not found → {"error":"model 'xyz' not found"}); we still cap
	// at 64 KiB to match executor_anthropic's readAndDrainErrorBody
	// behavior so a hostile upstream can't blow up memory.
	if resp.StatusCode >= 400 {
		if resp.Body != nil {
			//nolint:errcheck // best-effort close
			defer resp.Body.Close()
		}
		captured, _ := readAndDrainErrorBody(resp.Body)
		if len(captured) > maxPassthroughErrorBody {
			captured = captured[:maxPassthroughErrorBody]
		}
		e.logUpstreamResponse(params, diagnosticProtocol(cand.Protocol, "ollama-native"), captured)
		errKind := errorsx.ClassifyErrorWithBody(resp.StatusCode, captured)
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
					if k == "Content-Length" || k == "Content-Encoding" {
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
		outcome := oe.StreamResponse(params.R.Context(), responseSink(params), resp)
		if outcome.Interrupted && isClientStreamInterruption(outcome.Kind, outcome.Reason) {
			slog.Info("executor: client disconnected during ollama stream",
				"credential_id", cand.CredentialID,
				"provider_id", cand.ProviderID,
				"chunk_count", outcome.ChunkCount,
			)
			return &ExecuteResult{
				Response:       resp,
				Candidate:      cand,
				LatencyMs:      latencyMs,
				RequestBody:    append([]byte(nil), bodyBytes...),
				InboundBody:    sourceBody,
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
			slog.Warn("executor: ollama stream interrupted",
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
				InboundBody:    sourceBody,
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
			InboundBody:    sourceBody,
			RoutingTracker: params.RoutingTracker,
		}, nil
	}

	// Non-stream success path. WriteNonStreamResponse closes resp.Body.
	if resp == nil || resp.Body == nil {
		return nil, &retryableError{err: &upstreampkg.Error{
			Kind:    errorsx.KindUpstreamDown,
			Message: "ollama upstream returned an empty response",
		}}
	}
	body, werr := oe.WriteNonStreamResponse(params.W, resp, params.ClientModel, cand.QualityFixMode, nil)
	if werr != nil {
		// Even on write error, return the body so the dispatcher's
		// request-log preview sees what we tried to send. Use
		// errors.As to extract any *upstreampkg.Error the executor
		// packaged (e.g. conversion errors / upstream errors / not-yet-
		// supported client wire shapes). For retryable network/timeout
		// errors we still return the result so the dispatcher can do
		// failover bookkeeping; for terminal errors we propagate the
		// typed error so the dispatcher classifies it correctly.
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
			InboundBody:    sourceBody,
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
		InboundBody:    sourceBody,
		ResponseBody:   body,
		RoutingTracker: params.RoutingTracker,
	}, nil
}

// finalizeOllamaUpstreamBody validates and converts the inbound
// client wire body into the Ollama native wire body.
//
// # Contract (audit-r0924 P4.3, fix-up for r0925 client-wire wiring)
//
// The body arriving at the Ollama executor is the raw client wire —
// the dispatcher routes inbound requests here BEFORE the relay
// converts them to IR. Today the only wired inbound target is
// OpenAI Chat Completions (ClientProtocol="" or "openai-completions"),
// parsed via ir.ParseOpenAI. The IR scoped-converter path
// (executor.IR.WithProviderScope(...).Parse*) is intentionally NOT
// used here: the Ollama executor does not own the circuit-breaker
// isolation the scoped converter provides — the dispatcher's
// candidate loop already covers that — and using it here would
// create a hidden dependency on a converter that may be nil in
// isolated tests. We pin SourceProtocol=ProtocolOpenAIChat so
// downstream debug logs can tell which inbound path produced this IR.
//
// This executor does NOT:
//
//   - Accept raw Ollama wire (`{"model":...,"messages":[{"content":"..."}]}`).
//     Bodies that look like Ollama wire are still raw OpenAI Chat
//     wire (Ollama's content type is a strict subset of OpenAI's),
//     so ir.ParseOpenAI happily accepts them — model aliasing and
//     raw fields pass through Extensions. The executor only fails
//     closed if the parse itself errors out.
//   - Accept OpenAI Responses bodies (ClientProtocol="openai-responses").
//     The Responses response path is NOT wired (StreamResponse emits
//     OpenAI SSE only, WriteNonStreamResponse emits OpenAI chat
//     JSON only). Per the task's fail-closed rule we reject at the
//     request side so a client expecting the Responses API doesn't
//     silently get an OpenAI-shaped response back. Pinning this at
//     the gate is also why we do NOT feed a Responses body into
//     ir.ParseOpenAI (which would parse successfully, leaving
//     StreamResponse to fabricate a wrong-shape response).
//   - Accept bodies from anthropic / gemini / future protocols.
//     These indicate a routing bug (the dispatcher's protocol
//     switch should never have routed them here) and surface as
//     KindUnsupportedFeature so the operator sees it instead of
//     getting a misleading 200.
//   - Fabricate a default model. Missing fields are rejected.
//
// Validation gates (each is an explicit check, no fall-throughs):
//
//  1. non-empty body
//  2. params.ClientProtocol ∈ {"", "openai-completions"} — today's
//     wired inbound target. "openai-responses" is fail-closed at
//     this gate even though ir.ParseResponses exists.
//  3. body parses strictly as OpenAI Chat wire JSON (ir.ParseOpenAI).
//     The parser preserves unknown fields in Extensions so vendor
//     extras (reasoning_effort, web_search_options, o1/o3 knobs,
//     etc.) survive end-to-end.
//  4. irReq.Model is non-empty (SerializeOllama also enforces this,
//     but we want a typed KindClientBug not the generic
//     ErrSerializeOllamaMissingModel sentinel).
//  5. irReq.Messages is non-empty (Ollama rejects empty chat).
//  6. params.IsStream stamps the IR Stream flag (the wire-level
//     stream field on the outbound body must match the
//     dispatcher/executor gate decision — otherwise we lie to the
//     upstream about whether we'll consume NDJSON).
//
// On any failure the executor returns a typed *upstreampkg.Error so
// the dispatcher's classifier records the precise kind and the
// audit log shows the precise reason. We never fabricate a default
// body and never forward an unvalidated body to the upstream.
func (e *Executor) finalizeOllamaUpstreamBody(params *ExecParams, cand provider.Candidate, body []byte) ([]byte, error) {
	if len(body) == 0 {
		return nil, &upstreampkg.Error{
			Kind:    errorsx.KindClientBug,
			Message: "ollama executor: empty upstream body",
		}
	}
	// Gate 2: ClientProtocol must be a wired inbound target. Note
	// that "openai-responses" is intentionally NOT accepted here:
	// the response write path (StreamResponse / WriteNonStreamResponse)
	// is OpenAI-Chat-shaped only, so a Responses request would get
	// a wrong-shape response back. Fail closed at the gate.
	switch params.ClientProtocol {
	case "", "openai-completions":
		// supported
	case "openai-responses":
		return nil, &upstreampkg.Error{
			Kind:       errorsx.KindUnsupportedFeature,
			Message:    "ollama executor: openai-responses response shape not wired (use openai-completions or wire SerializeResponses end-to-end)",
			StatusCode: http.StatusNotImplemented,
		}
	default:
		return nil, &upstreampkg.Error{
			Kind:       errorsx.KindUnsupportedFeature,
			Message:    fmt.Sprintf("ollama executor: client protocol %q not yet wired", params.ClientProtocol),
			StatusCode: http.StatusNotImplemented,
		}
	}

	// Gate 3: parse the raw client wire into IR. ir.ParseOpenAI
	// tolerates unknown top-level fields (Extensions preserves
	// them) — this is intentional so vendor extras don't silently
	// disappear. It DOES tolerate trailing JSON content via
	// json.Unmarshal; we add a trailing-data guard below so an
	// attacker can't smuggle a second body after the legitimate
	// request.
	irReq, err := ir.ParseOpenAI(body)
	if err != nil {
		return nil, &upstreampkg.Error{
			Kind:       errorsx.KindClientBug,
			Message:    "ollama executor: inbound body is not valid OpenAI Chat wire JSON",
			Err:        err,
			StatusCode: 0,
		}
	}
	// Trailing-data guard: ir.ParseOpenAI uses json.Unmarshal which
	// accepts trailing garbage. The strict semantics the audit
	// calls out require we reject so a hostile client can't append
	// a second body that future code paths might pick up. Cheap
	// to do — we already buffered the bytes.
	if hasTrailingJSON(body) {
		return nil, &upstreampkg.Error{
			Kind:    errorsx.KindClientBug,
			Message: "ollama executor: inbound body has trailing JSON content",
		}
	}

	// Pin SourceProtocol so downstream debug logs can tell which
	// inbound path produced this IR. We deliberately do NOT pin
	// to ProtocolOllamaChat — that would lie about the origin
	// (the IR was converted FROM the client protocol, not from a
	// raw /api/chat parse which the handler does not exist).
	irReq.SourceProtocol = ir.ProtocolOpenAIChat

	// Gate 4: model required (explicit check so we surface a typed
	// client-bug error instead of the generic
	// ErrSerializeOllamaMissingModel sentinel — different semantic,
	// "model field missing from client wire" vs "model was lost
	// during IR conversion").
	if irReq.Model == "" {
		return nil, &upstreampkg.Error{
			Kind:       errorsx.KindClientBug,
			Message:    "ollama executor: client wire is missing required model field",
			Err:        ir.ErrSerializeOllamaMissingModel,
			StatusCode: 0,
		}
	}

	// Gate 5: messages required. Ollama /api/chat returns a clear
	// "invalid chat message" on empty messages, but we'd rather
	// reject at the gateway with a typed client-bug error than
	// bill the client for a guaranteed-failure upstream call.
	if len(irReq.Messages) == 0 {
		return nil, &upstreampkg.Error{
			Kind:       errorsx.KindClientBug,
			Message:    "ollama executor: client wire has empty messages array",
			StatusCode: 0,
		}
	}

	// Gate 6: honor the dispatcher's stream decision. The executor
	// gate already trusts params.IsStream as authoritative (the
	// stream bridge flips it on/off); we MUST NOT let the client
	// override this via a body-level `stream: false` — otherwise
	// the upstream might emit NDJSON while we built a non-stream
	// request and we'd mis-parse the response.
	irReq.Stream = params.IsStream

	// The candidate's outbound model is authoritative for this attempt,
	// including failover to an offer with a different raw model name.
	// Match the other protocol executors' model-resolution contract.
	if outboundModel := resolveOutboundModel(params, cand); outboundModel != "" {
		irReq.Model = outboundModel
	}

	// Now safe to serialize. SerializeOllama is the SSOT for the
	// Ollama wire shape; every Ollama-private field
	// (format / keep_alive / raw / options.*) is preserved verbatim
	// via the IR.Extensions["ollama.<key>"] namespace per the IR
	// contract.
	serialized, err := ir.SerializeOllama(irReq)
	if err != nil {
		return nil, &upstreampkg.Error{
			Kind:       errorsx.KindConversion,
			Message:    "ollama: serialize IR to native wire",
			Err:        err,
			StatusCode: 0,
		}
	}
	return serialized, nil
}

// hasTrailingJSON reports whether body contains a complete JSON
// value followed by non-whitespace bytes. ir.ParseOpenAI uses
// json.Unmarshal which silently accepts trailing data, so the
// executor adds its own guard to keep the strict audit semantics
// (reject attackers appending a second body after the legitimate
// request). Cheap: we already buffered the bytes; we just decode the
// first value via json.Decoder and inspect whether the stream still
// has more.
func hasTrailingJSON(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	var v any
	if err := dec.Decode(&v); err != nil {
		// Bad JSON — let the parse step surface the error rather
		// than masking it with a "trailing data" message.
		return false
	}
	// dec.More() is true iff the stream contains another complete
	// JSON value after the one we just decoded. Whitespace alone
	// returns false.
	return dec.More()
}
