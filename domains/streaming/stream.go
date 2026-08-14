package streaming

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/errorsx"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/internal/ir"
	"github.com/kaixuan/llm-gateway-go/metrics"
)

const (
	streamBufSize       = 64 * 1024
	sseKeepaliveComment = ": keep-alive\n\n"

	// 2026-07-15: empty-stream content-gate parameters.
	// The gate buffers up to emptyGateMaxChunks chunks (64 KiB total) before
	// any byte is committed to the client. If a chunk with real content
	// arrives, the buffer is flushed and we switch to write-through (no
	// latency penalty for normal streams). If [DONE] arrives while still
	// buffering with zero content seen, we return Resumable=true so the
	// executor transparently fails over to the next candidate — killing the
	// NIM (Provider 18) ~13% empty-response rate.
	emptyGateMaxChunks = 8
	emptyGateMaxBytes  = 64 * 1024
)

// qualityFixModeCtxKey is the context value key used to thread the
// per-provider quality_fix_mode (017_quality_fix_mode.sql) from the
// routing executor into the relay-side stream reader. The executor
// sets it via SetQualityFixModeOnContext before the upstream call;
// the stream reader pulls it out via qualityFixModeFromContext on
// every line.
type qualityFixModeCtxKey struct{}

// SetQualityFixModeOnContext stamps the provider's quality_fix_mode
// onto the given context. Empty string ⇒ "no mode set" (off by
// default). The routing executor calls this before issuing the
// upstream request so the relay stream reader can look it up without
// needing direct access to the provider.Candidate struct.
func SetQualityFixModeOnContext(ctx context.Context, mode string) context.Context {
	return context.WithValue(ctx, qualityFixModeCtxKey{}, mode)
}

// qualityFixModeFromContext returns the mode stashed by
// SetQualityFixModeOnContext, or empty string if none was set. The
// stream reader calls this once per chunk; the cost is one map lookup
// which is fine on the hot path.
func qualityFixModeFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(qualityFixModeCtxKey{}).(string); ok {
		return v
	}
	return ""
}

// chunkHasContent returns true when an OpenAI SSE chunk carries real
// user-facing content. Used by the empty-stream content-gate to decide
// whether to flush the buffer (real content seen) or fail over (zero
// content seen before [DONE]).
//
// "Real content" = delta.content != "" OR delta.reasoning_content != ""
// OR delta.tool_calls non-empty. Usage-only and role-only chunks
// (Type="usage" / first-chunk assistant role announcement) do NOT count.
//
// Returns false (no content) on parse errors — a malformed chunk is treated
// like an empty one so the gate keeps buffering and either hits the chunk/
// byte cap or [DONE] arrives with zero content → Resumable failover.
func chunkHasContent(payload string) bool {
	if payload == "" || payload == "[DONE]" {
		return false
	}
	chunk, err := ir.ParseOpenAIStreamChunk("data: " + payload + "\n\n")
	if err != nil || chunk == nil {
		return false
	}
	if chunk.Type == ir.ChunkTypeDone || chunk.Type == ir.ChunkTypeError {
		return false
	}
	if chunk.Delta == nil {
		return false
	}
	if chunk.Delta.Content != "" || chunk.Delta.ReasoningContent != "" {
		return true
	}
	if len(chunk.Delta.ToolCalls) > 0 {
		return true
	}
	if chunk.Delta.AudioDelta != nil &&
		(chunk.Delta.AudioDelta.Data != "" || chunk.Delta.AudioDelta.Transcript != "") {
		return true
	}
	return false
}

// runEmptyStreamGate buffers upstream chunks BEFORE writing them to the
// client, so an empty stream (notably the NIM (Provider 18) ~13% failure
// mode: stream opens, sends 1-3 chunks with empty choices, then [DONE])
// can be detected and the executor transparently fails over to the next
// candidate. Architecture mirrors the existing json_error_in_stream /
// first_byte_timeout branches that already return Resumable=true with
// ChunkCount=0 < StreamRetryThreshold(50).
//
// The HTTP 200 + SSE headers are already committed by the caller before
// this gate runs (stream.go:142). On Resumable return the client will see
// 200 + SSE headers + (nothing from this candidate) + the next candidate's
// real chunks — SSE clients tolerate this because no data chunks were
// sent before failover.
//
// Cost: zero added latency for normal streams — the first chunk with real
// content triggers an immediate buffer flush and switch to write-through.
// For pathological all-empty streams, we cap buffering at emptyGateMaxChunks
// / emptyGateMaxBytes then flush+continue (don't block slow models
// indefinitely).
//
// The starting line is the already-transformed first line (quality fix /
// XML coerce / model rewrite / normalize already applied). The gate does
// not re-transform it — it only buffers subsequent lines and applies the
// same transforms to them.
//
// Returns:
//   - gatePassed=true, flushedLines=non-empty: gate flushed the buffer (either
//     because content appeared or because the buffer cap was hit). Caller
//     writes flushedLines via safeWriteSSE and continues with the main loop.
//     lastSend/chunkCount are updated inside this function.
//   - gatePassed=false, outcome!=nil: empty stream detected → caller returns
//     the outcome (Interrupted=true, Resumable=true, ChunkCount=0).
//   - gatePassed=true, flushedLines=nil: timed out / error during buffering,
//     fall through to normal main-loop read.
func runEmptyStreamGate(
	ctx context.Context,
	reader *bufio.Reader,
	bodyCloser io.ReadCloser,
	w http.ResponseWriter,
	flusher http.Flusher,
	norm *Normalizer,
	capture *audit.StreamCapture,
	pc *pendingCapturer,
	clientModel string,
	discoveredUpstream *string,
	startingLine string,
	firstByteTimeout time.Duration,
	lastSend *time.Time,
	chunkCount *int,
	onRawLine func(string),
) (flushedLines []string, outcome *StreamOutcome) {
	buffered := make([]string, 0, emptyGateMaxChunks)
	bufferedBytes := 0
	if startingLine != "" {
		buffered = append(buffered, startingLine)
		bufferedBytes += len(startingLine)
	}

	for {
		// Read the next upstream line, with the same first-byte / inter-chunk
		// timeout semantics as the main loop.
		line, err := readLineWithTimeoutAndCloser(ctx, reader, bodyCloser, firstByteTimeout)
		if onRawLine != nil {
			onRawLine(line)
		}
		if err != nil {
			// Timeout / network error during buffering → flush whatever we
			// have so the caller can write them, then let the main loop
			// surface the error as a stream interruption.
			return buffered, nil
		}

		// Observe chunks in capture for audit, but do NOT count them as
		// "sent to client" — nothing reached the wire during buffering.
		payload := extractPayload(line)
		if payload != "" && capture != nil {
			if chunk, perr := ir.ParseOpenAIStreamChunk(line); perr == nil {
				capture.ObserveChunk(chunk)
				// 2026-07-28: capture the upstream-returned model
				// (the first SSE chunk carries it under
				// `chat.completion.chunk.model`). Used by the
				// integrity detector at end-of-stream to detect
				// silent model substitution. The Anthropic side
				// already records model via message_start in
				// anthropic_passthrough_stream.go:195; this closes
				// the OpenAI SSE gap.
				if chunk.Model != "" {
					capture.SetRespModelIfEmpty(chunk.Model)
				}
				if capture.IntegrityBreached() {
					return nil, ptrStreamOutcome(integrityBreachOutcome(capture, *chunkCount))
				}
			}
		}

		// Apply the same line transforms the main loop would apply, so
		// flushed chunks are byte-identical to what write-through would
		// have produced (quality fix / XML coerce / model rewrite / norm).
		line = applyGateLineTransforms(ctx, line, clientModel, discoveredUpstream, norm, capture)

		buffered = append(buffered, line)
		bufferedBytes += len(line)

		// [DONE] while buffering: classify and decide.
		if payload == "[DONE]" {
			break
		}

		// Real content seen? Flush immediately. This is the common case
		// for normal streams — first content chunk arrives, gate exits,
		// caller writes flushed lines and continues write-through.
		if chunkHasContent(payload) {
			return buffered, nil
		}

		// Buffer cap: too many chunks / bytes without content. Likely a
		// degenerate stream (usage-only blocks, role announcements, etc.).
		// Flush and fall through to write-through — don't block slow models.
		if len(buffered) >= emptyGateMaxChunks || bufferedBytes >= emptyGateMaxBytes {
			return buffered, nil
		}
	}

	// Loop exited because [DONE] was seen while still buffering.
	// Look back: did any buffered chunk have real content?
	sawContent := false
	for _, l := range buffered {
		if chunkHasContent(extractPayload(l)) {
			sawContent = true
			break
		}
	}
	if !sawContent {
		// Empty stream — signal Resumable failover. The executor will
		// continue to the next candidate. We do NOT write [DONE] to the
		// client, so the next candidate's stream begins cleanly.
		if capture != nil {
			capture.MarkInterruptedWithReason("empty_stream_no_content")
		}
		*chunkCount = 0
		_ = lastSend
		return nil, &StreamOutcome{
			Interrupted: true,
			Reason:      "empty_stream_no_content",
			Resumable:   true,
			ChunkCount:  0,
		}
	}

	// Had content + [DONE] — flush all buffered chunks to the client and
	// continue write-through (the caller will resume the main loop).
	return buffered, nil
}

// applyGateLineTransforms runs quality-fix + XML-coerce + model-rewrite +
// normalize on a line that the content gate is buffering. Mirrors the
// transformations the main loop applies at stream.go:467-504 so flushed
// chunks are byte-identical to write-through output.
func applyGateLineTransforms(
	ctx context.Context,
	line string,
	clientModel string,
	discoveredUpstream *string,
	norm *Normalizer,
	capture *audit.StreamCapture,
) string {
	qualityMode := qualityFixModeFromContext(ctx)
	if qualityMode != "" && qualityMode != QualityModeOff && capture != nil {
		newLine, newFlags, newSeen := ProcessStreamLine(line, qualityMode, capture.QualityFlags, capture.QualitySeenToolCallIDs)
		if newLine != "" {
			line = newLine
		}
		if len(newFlags) > 0 {
			capture.QualityFlags = newFlags
		}
		if newSeen != nil {
			capture.QualitySeenToolCallIDs = newSeen
		}
	}
	line = coerceXMLToolCallsInStreamLine(line, false)
	if clientModel != "" && *discoveredUpstream == "" {
		*discoveredUpstream = extractModelFromChunk(line)
	}
	if clientModel != "" {
		line = replaceModelInChunk(line, clientModel, *discoveredUpstream)
	}
	if norm != nil {
		line = string(norm.NormalizeChunk([]byte(line), true))
	}
	return line
}

type StreamOutcome struct {
	Interrupted bool
	Reason      string
	Resumable   bool // Whether the stream can be resumed with a different credential
	ChunkCount  int  // Number of chunks sent before interruption

	// Kind (2026-07-28 §5.6) is the structured errorsx.ErrorKind
	// the executor assigns to the interruption. When non-empty,
	// streamErrorKindForDetailCode prefers it over the legacy
	// detail-code switch.
	Kind errorsx.ErrorKind
}

// integrityBreachOutcome converts a latched capture integrity breach into
// the common stream interruption shape. The observer itself only reports
// the breach; transformers call this helper at their public capture update
// points so the executor's existing recoverable-stream failover path can
// decide whether another candidate may retry.
func integrityBreachOutcome(capture *audit.StreamCapture, chunkCount int) StreamOutcome {
	reason := "integrity_breach"
	if capture != nil {
		if r := capture.IntegrityBreachReason(); r != "" {
			reason = r
		}
		capture.MarkInterruptedWithReason(reason)
	}
	return StreamOutcome{
		Interrupted: true,
		Reason:      reason,
		Resumable:   true,
		ChunkCount:  chunkCount,
		Kind:        errorsx.KindEmptyResponse,
	}
}

func ptrStreamOutcome(o StreamOutcome) *StreamOutcome { return &o }

func StreamChat(w http.ResponseWriter, resp *http.Response, clientModel, outboundModel string, norm *Normalizer) StreamOutcome {
	return StreamChatWithCapture(w, resp, clientModel, outboundModel, norm, nil)
}

func StreamChatWithCapture(w http.ResponseWriter, resp *http.Response, clientModel, outboundModel string, norm *Normalizer, capture *audit.StreamCapture) StreamOutcome {
	return StreamChatWithCaptureAndToolFallback(w, resp, clientModel, outboundModel, norm, capture, false, nil)
}

func StreamChatWithCaptureAndToolFallback(w http.ResponseWriter, resp *http.Response, clientModel, outboundModel string, norm *Normalizer, capture *audit.StreamCapture, toolsRequested bool, stripFn func([]byte) []byte) (outcome StreamOutcome) {
	return StreamChatWithPendingCapture(w, resp, clientModel, outboundModel, norm, capture, toolsRequested, stripFn, nil)
}

// StreamChatWithPendingCapture (Track C C2, 2026-06-18) extends
// StreamChatWithCaptureAndToolFallback with an optional
// pendingCapturer. When supplied, every chunk the upstream
// sends is recorded into the capturer's buffer in addition
// to being forwarded to the client — regardless of whether
// the client is still connected. When the stream ends, the
// capturer's finalize is called with the terminal outcome
// so a caller-driven Save() can persist the buffer.
//
// Why this works even when the client is gone:
//   - C1 decoupled the upstream context from the client
//     context when the request carries a session id, so the
//     read loop keeps going past the client disconnect.
//   - safeWriteSSE already recovers from "write to closed
//     conn" panics, so existing w.WriteString calls are safe.
//
// The capturer is intentionally decoupled from the audit
// StreamCapture — it serves a different purpose (replay) with
// different size limits (1 MiB cap here, vs unbounded there).
func StreamChatWithPendingCapture(
	w http.ResponseWriter,
	resp *http.Response,
	clientModel, outboundModel string,
	norm *Normalizer,
	capture *audit.StreamCapture,
	toolsRequested bool,
	stripFn func([]byte) []byte,
	pc *pendingCapturer,
) (outcome StreamOutcome) {
	return StreamChatWithPendingCaptureAndDiagnostics(w, resp, clientModel, outboundModel, norm, capture, toolsRequested, stripFn, pc, nil)
}

// StreamChatWithPendingCaptureAndDiagnostics forwards an OpenAI stream with
// optional best-effort diagnostics. Diagnostic failures never affect the
// client-visible stream.
func StreamChatWithPendingCaptureAndDiagnostics(
	w http.ResponseWriter,
	resp *http.Response,
	clientModel, outboundModel string,
	norm *Normalizer,
	capture *audit.StreamCapture,
	toolsRequested bool,
	stripFn func([]byte) []byte,
	pc *pendingCapturer,
	diagnostics *DiagnosticContext,
) (outcome StreamOutcome) {
	//nolint:errcheck // best-effort close
	defer resp.Body.Close()

	requestID := ""
	if resp.Request != nil {
		requestID = resp.Request.Header.Get("X-Request-Id")
	}
	diagnosticCollector := &streamDiagnosticCollector{}
	defer func() {
		diagnosticCollector.report(diagnostics, requestID, "openai-completions", "openai-completions", outcome.Interrupted)
	}()

	// Top-level panic recovery so a panic during streaming (e.g. JSON parse
	// failure, write to a closed connection) does not skip the deferred
	// audit emit in the caller and lose the request_logs row entirely.
	defer func() {
		if r := recover(); r != nil {
			slog.Error("stream panic recovered", "panic", r, "stack", string(debug.Stack()), "client_model", clientModel)
			if capture != nil {
				capture.MarkInterruptedWithReason("stream_panic")
			}
			outcome.Interrupted = true
			outcome.Reason = "stream_panic"
			outcome.Kind = errorsx.KindUpstreamDown
			if pc != nil {
				pc.markInterrupted("stream_panic")
			}
		}
		// Best-effort capture finalise. If the stream completed
		// normally, the capturer holds the full body ready for
		// replay via GET /v1/sessions/{id}/pending-response.
		// If terminated abnormally, the capturer still holds
		// whatever was captured so the admin API can inspect.
		if pc != nil {
			pc.finalize(outcome)
		}
	}()

	runtimeCfg := currentStreamRuntimeConfig()


	// SR-W1: route client frames through the attempt commit gate.
	// Disabled (default) this is the identity function — legacy wire bytes.
	w, gate := wrapAttemptWriter(w, FrameProtocolOpenAIChat)
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return StreamOutcome{}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	var ctx context.Context
	if resp.Request != nil {
		ctx = resp.Request.Context()
	} else {
		ctx = context.Background()
	}

	// BUG-1 fix (2026-06-19): hold a reference to the raw body as an
	// io.ReadCloser so readLineWithTimeoutAndCloser can close it on timeout,
	// unblocking the ReadString goroutine immediately instead of leaking it
	// until StreamTimeout (up to 900 s on the session path).
	bodyCloser := resp.Body
	reader := bufio.NewReaderSize(bodyCloser, streamBufSize)
	discoveredUpstream := ""
	lastSend := time.Now()
	chunkCount := 0 // Track number of chunks sent

	if clientModel != "" && outboundModel != "" && clientModel != outboundModel {
		slog.Debug("upstream model diff",
			"client_model", clientModel,
			"outbound_model", outboundModel,
		)
	}

	// C1/C2: session-backed streams have an upstream timeout context
	// independent of the client connection. Once a pending capturer is
	// active, a failed client write only disables response writes; the
	// upstream body must still be consumed for replay.
	clientDisconnected := false
	writeClientLine := func(line string) bool {
		if clientDisconnected {
			return false
		}
		if !safeWriteSSE(w, line) || !safeFlush(flusher) {
			clientDisconnected = true
			slog.Info("stream client disconnected; continuing upstream capture")
			return false
		}
		return true
	}

	clientWriteFailure := func() bool {
		if !clientDisconnected || pc != nil {
			return false
		}
		outcome.Interrupted = true
		outcome.Reason = "client_write_failed"
		outcome.Kind = errorsx.KindUpstreamDown
		outcome.Resumable = chunkCount < 5
		outcome.ChunkCount = chunkCount
		if capture != nil {
			capture.MarkInterruptedWithReason("client_write_failed")
		}
		return true
	}

	// ── First-byte timeout ──────────────────────────────────────────

	firstLine, err := readLineWithTimeoutAndCloser(ctx, reader, bodyCloser, runtimeCfg.firstByteTimeout)
	if err != nil {
		if capture != nil {
			capture.MarkInterruptedWithReason("first_byte_timeout")
		}
		slog.Warn("stream first-byte timeout",
			"error", err,
			"first_byte_timeout_seconds", int(runtimeCfg.firstByteTimeout.Seconds()),
			"hint", "if frequent, increase LLM_GATEWAY_FIRST_BYTE_TIMEOUT or admin config (default 120s)",
		)
		if gate.MayWriteTerminal() {
			safeWriteSSE(w, "data: {\"error\":{\"message\":\"upstream first-byte timeout\",\"type\":\"timeout\",\"code\":\"first_byte_timeout\"}}\n\n")
			safeFlush(flusher)
		}
		outcome.Interrupted = true
		outcome.Reason = "first_byte_timeout"
		outcome.Kind = errorsx.KindStreamTimeout
		outcome.Resumable = true // First-byte timeout is resumable (no chunks sent)
		outcome.ChunkCount = 0
		return outcome
	}

	// upstreamDoneReceived tracks whether the upstream sent the literal
	// "data: [DONE]\n\n" terminator. If the stream ended by EOF without
	// [DONE] (e.g. upstream crashed mid-response), we do NOT want to
	// mark the capture as doneReceived=true — that would misreport an
	// interruption as a clean completion.
	//
	// 2026-07-15: hoisted before the empty-stream gate so the gate can
	// flip it true if its buffered output included [DONE].
	upstreamDoneReceived := false

	if firstLine != "" {
		logRawUpstreamFrame(diagnostics, auditFromDiagnostics(diagnostics, requestID, "openai-completions"), []byte(firstLine))
		firstRawPayload := extractPayload(firstLine)
		if firstRawPayload != "" && firstRawPayload != "[DONE]" {
			diagnosticCollector.observeRaw([]byte(firstRawPayload))
			if chunk, parseErr := ir.ParseOpenAIStreamChunk(firstLine); parseErr == nil {
				diagnosticCollector.observeChunk(chunk)
			} else {
				reportConversionAnomaly(diagnostics, requestID, "openai-completions", "openai-completions", "parse_stream_chunk", []byte(firstRawPayload), parseErr, nil)
			}
		}
		// 2026-06-20 audit fix: when the upstream returns a
		// non-SSE JSON error body (e.g. {"error":{"type":
		// "service_unavailable","message":"积分不足"}}) for a
		// stream request, do NOT pass the body through to the
		// client as if it were a valid first chunk. Treat it
		// as a resumable stream interruption so the executor
		// falls back to the next credential. This branch fires
		// before the firstLine is written to the client, so the
		// client never sees a 200 + the raw JSON error.
		//
		// Resumable=true + ChunkCount=0 < StreamRetryThreshold
		// (default 5) → executor_chat.go:521 will surface this
		// as a streamInterruptedError → executor.go:737 will
		// continue to the next candidate.
		if isErr, errKind, errMsg := isJSONErrorBody([]byte(firstLine)); isErr {
			slog.Warn("stream: upstream returned JSON error instead of SSE",
				"kind", errKind,
				"message", errMsg,
				"client_model", clientModel,
			)
			if capture != nil {
				capture.MarkInterruptedWithReason("json_error_in_stream")
			}
			// Surface a clean SSE error to the client instead of
			// the raw vendor error envelope, so SDK clients can
			// parse it as a normal chat.completion.chunk stream
			// error rather than choking on an unexpected shape.
			safeWriteSSE(w, fmt.Sprintf("data: {\"error\":{\"message\":%q,\"type\":%q,\"code\":%q}}\n\n", errMsg, "upstream_error", errKind))
			safeFlush(flusher)
			outcome.Interrupted = true
			outcome.Reason = "json_error_in_stream"
			outcome.Kind = errorsx.KindUpstreamDown
			outcome.Resumable = true
			outcome.ChunkCount = 0
			return outcome
		}
		// 2026-06-19 quality fix mode (017_quality_fix_mode.sql). Run
		// before XML coercion so the scanner sees the raw upstream
		// delta.tool_calls shape and can rewrite empty names to
		// __unknown_tool_stream_<i>__. detect_only mode leaves the
		// line byte-identical but still tags the issue in the
		// capture's QualityFlags slice.
		qualityMode := qualityFixModeFromContext(ctx)
		if qualityMode != "" && qualityMode != QualityModeOff && capture != nil {
			newLine, newFlags, newSeen := ProcessStreamLine(firstLine, qualityMode, capture.QualityFlags, capture.QualitySeenToolCallIDs)
			if newLine != "" {
				firstLine = newLine
			}
			if len(newFlags) > 0 {
				capture.QualityFlags = newFlags
			}
			if newSeen != nil {
				capture.QualitySeenToolCallIDs = newSeen
			}
		}
		firstLine = coerceXMLToolCallsInStreamLine(firstLine, toolsRequested)
		if clientModel != "" && discoveredUpstream == "" {
			discoveredUpstream = extractModelFromChunk(firstLine)
		}
		if clientModel != "" {
			firstLine = replaceModelInChunk(firstLine, clientModel, discoveredUpstream)
		}

		payload := extractPayload(firstLine)
		if payload != "" && capture != nil {
			// IR-based audit: parse to chunk and observe
			if chunk, err := ir.ParseOpenAIStreamChunk(firstLine); err == nil {
				capture.ObserveChunk(chunk)
				// 2026-07-28: capture upstream model for integrity
				// detector (silent substitution detection).
				if chunk.Model != "" {
					capture.SetRespModelIfEmpty(chunk.Model)
				}
				if capture.IntegrityBreached() {
					outcome = integrityBreachOutcome(capture, chunkCount)
					return outcome
				}
			}
		}

		if norm != nil {
			firstLine = string(norm.NormalizeChunk([]byte(firstLine), true))
		}

		// 2026-07-15: Empty-stream content-gate. Instead of writing the
		// first chunk immediately, buffer firstLine + subsequent chunks
		// until either real content appears (normal stream → flush
		// immediately, ~0 latency) or [DONE] arrives with zero content
		// (NIM empty-stream failure → Resumable failover to next
		// candidate BEFORE any byte reaches the client).
		//
		// When disabled via LLM_GATEWAY_ENABLE_EMPTY_STREAM_GATE=false
		// or config, fall through to the original write-immediately
		// path so behavior is unchanged from prior releases.
		if currentStreamRuntimeConfig().enableEmptyStreamGate {
			flushedLines, gateOutcome := runEmptyStreamGate(
				ctx, reader, bodyCloser, w, flusher, norm, capture, pc,
				clientModel, &discoveredUpstream, firstLine,
				runtimeCfg.firstByteTimeout, &lastSend, &chunkCount,
				func(line string) {
					logRawUpstreamFrame(diagnostics, auditFromDiagnostics(diagnostics, requestID, "openai-completions"), []byte(line))
					payload := extractPayload(line)
					if payload == "" || payload == "[DONE]" {
						return
					}
					diagnosticCollector.observeRaw([]byte(payload))
					if chunk, parseErr := ir.ParseOpenAIStreamChunk(line); parseErr == nil {
						diagnosticCollector.observeChunk(chunk)
					} else {
						reportConversionAnomaly(diagnostics, requestID, "openai-completions", "openai-completions", "parse_stream_chunk", []byte(payload), parseErr, nil)
					}
				},
			)
			if gateOutcome != nil {
				return *gateOutcome
			}
			// Write flushed lines and update counters; the last flushed
			// line may be [DONE] or a content chunk — the main loop
			// handles terminator detection and write-through either way.
			for _, l := range flushedLines {
				if pc != nil {
					pc.append(l)
				}
				p := extractPayload(l)
				if p == "[DONE]" {
					upstreamDoneReceived = true
				}
				diagnosticCollector.observeEmittedLine(l)
				if writeClientLine(l) {
					lastSend = time.Now()
					chunkCount++
					if capture != nil {
						capture.RecordChunkSent()
					}
				}
				if clientWriteFailure() {
					return outcome
				}

			}
		} else {
			// Gate disabled: original write-immediately path (unchanged
			// from pre-2026-07-15 behaviour).
			if pc != nil {
				pc.append(firstLine)
			}
			diagnosticCollector.observeEmittedLine(firstLine)
			if writeClientLine(firstLine) {
				lastSend = time.Now()
				chunkCount++ // Count first chunk
				if capture != nil {
					capture.RecordChunkSent()
				}
			}
			if clientWriteFailure() {
				return outcome
			}
		}
	}

	// ── Main streaming loop with keep-alive ─────────────────────────

	// upstreamDoneReceived tracks whether the upstream sent the literal
	// "data: [DONE]\n\n" terminator. If the stream ended by EOF without
	// [DONE] (e.g. upstream crashed mid-response), we do NOT want to
	// mark the capture as doneReceived=true — that would misreport an
	// interruption as a clean completion.
	//
	// 2026-07-15: declared earlier (before the empty-stream gate call
	// at first-line write) so the gate can flip it true if the gate
	// flushed [DONE] as part of its buffered output.
	for {
		select {
		case <-ctx.Done():
			if capture != nil {
				capture.MarkInterruptedWithReason("client_cancel")
			}
			outcome.Interrupted = true
			outcome.Reason = "client_cancel"
			outcome.Kind = errorsx.KindCanceled
			outcome.ChunkCount = chunkCount
			outcome.Resumable = false
			return outcome
		default:
		}

		readResult := readNextStreamLine(ctx, reader, bodyCloser, w, &lastSend, runtimeCfg)
		if readResult.err != nil {
			switch readResult.state {
			case streamReadEOF:
				// If upstream closed before sending [DONE], still send
				// the terminator to the client (clients expect a
				// well-formed stream) but mark the capture as
				// interrupted with reason "eof_without_done" so audit
				// queries can distinguish a clean completion from a
				// premature close.
				if !upstreamDoneReceived {
					slog.Warn("upstream EOF without [DONE]", "client_model", clientModel)
					if capture != nil {
						capture.MarkInterruptedWithReason("eof_without_done")
					}
					outcome.Interrupted = true
					outcome.Reason = "eof_without_done"
					outcome.Kind = errorsx.KindUpstreamDown
				}
				// When the client has gone away but the capturer is
				// still alive and the upstream DID send [DONE], do NOT
				// report eof_without_done as the failure reason — the
				// capture is complete and replayable.
				if pc != nil && upstreamDoneReceived {
					outcome.Interrupted = false
					outcome.Reason = ""
				}
				// 2026-08-06 hot-patch: capture whether this branch had to
				// synthesize the SSE terminator. The MiniMax API (~13% of
				// streams as of 2026-07-28) ends without sending
				// "data: [DONE]\n\n" — the gateway injects the trailing
				// frame so SSE clients can finalize the stream. Decision
				// "isBenignEOF" lives in executor_chat.go:975 + handler.go:5609;
				// this signal only provides operator visibility, not
				// classification.
				synthesizedDone := !upstreamDoneReceived
				if gate.MayWriteTerminal() {
					safeWriteSSE(w, "data: [DONE]\n\n")
					safeFlush(flusher)
					if synthesizedDone {
						slog.Warn("stream synthesized [DONE] terminator",
							"client_model", clientModel,
							"chunk_count", chunkCount,
							"had_capture", capture != nil,
						)
						metrics.Global().RecordStreamSynthesizedDone()
					}
				}
				if capture != nil && upstreamDoneReceived {
					capture.ObserveChunk(&ir.StreamChunk{
						Type:           ir.ChunkTypeDone,
						SourceProtocol: ir.ProtocolOpenAIChat,
					})
				}
				outcome.ChunkCount = chunkCount
			case streamReadCanceled:
				slog.Debug("stream cancelled by client")
				if capture != nil {
					capture.MarkInterruptedWithReason("client_cancel")
				}
				outcome.Interrupted = true
				outcome.Reason = "client_cancel"
				outcome.Kind = errorsx.KindCanceled
				outcome.ChunkCount = chunkCount
			case streamReadTimeout:
				slog.Warn("stream read timeout",
					"chunks_received", chunkCount,
					"client_model", clientModel,
					"hint", "if timeout occurs frequently with chunks received, consider increasing llmgw_node_timeout_seconds (current default 120s, hotconfigurable via admin/settings)",
				)
				if gate.MayWriteTerminal() {
					safeWriteSSE(w, "data: {\"error\":{\"message\":\"upstream read timeout\",\"type\":\"timeout\",\"code\":\"stream_timeout\"}}\n\n")
					safeFlush(flusher)
				}
				if capture != nil {
					capture.MarkInterruptedWithReason("stream_timeout")
				}
				outcome.Interrupted = true
				outcome.Reason = "stream_timeout"
				outcome.Kind = errorsx.KindStreamTimeout
				outcome.Resumable = true // Timeout is resumable
				outcome.ChunkCount = chunkCount
			default:
				slog.Warn("stream read error", "error", readResult.err)
				if capture != nil {
					capture.MarkInterruptedWithReason("read_error")
				}
				outcome.Interrupted = true
				outcome.Reason = "read_error"
				outcome.Kind = errorsx.KindUpstreamDown
				outcome.Resumable = true // Read error is resumable
				outcome.ChunkCount = chunkCount
			}
			return outcome
		}

		line := readResult.line
		logRawUpstreamFrame(diagnostics, auditFromDiagnostics(diagnostics, requestID, "openai-completions"), []byte(line))
		rawPayload := extractPayload(line)
		if rawPayload != "" && rawPayload != "[DONE]" {
			diagnosticCollector.observeRaw([]byte(rawPayload))
			if chunk, parseErr := ir.ParseOpenAIStreamChunk(line); parseErr == nil {
				diagnosticCollector.observeChunk(chunk)
			} else {
				reportConversionAnomaly(diagnostics, requestID, "openai-completions", "openai-completions", "parse_stream_chunk", []byte(rawPayload), parseErr, nil)
			}
		}

		// 2026-06-19 quality fix mode (017_quality_fix_mode.sql). See
		// the first-line equivalent above for the rationale. We run
		// the quality check before XML coercion and normalisation so
		// the scanner sees the raw upstream delta.tool_calls shape.
		qualityMode := qualityFixModeFromContext(ctx)
		if qualityMode != "" && qualityMode != QualityModeOff && capture != nil {
			newLine, newFlags, newSeen := ProcessStreamLine(line, qualityMode, capture.QualityFlags, capture.QualitySeenToolCallIDs)
			if newLine != "" {
				line = newLine
			}
			if len(newFlags) > 0 {
				capture.QualityFlags = newFlags
			}
			if newSeen != nil {
				capture.QualitySeenToolCallIDs = newSeen
			}
		}

		line = coerceXMLToolCallsInStreamLine(line, toolsRequested)

		if clientModel != "" && discoveredUpstream == "" {
			discoveredUpstream = extractModelFromChunk(line)
		}
		if clientModel != "" {
			line = replaceModelInChunk(line, clientModel, discoveredUpstream)
		}

		if stripFn != nil {
			line = stripChunkFields(line, stripFn)
		}

		payload := extractPayload(line)
		if payload != "" {
			if payload == "[DONE]" {
				upstreamDoneReceived = true
			}
			if capture != nil {
				if chunk, err := ir.ParseOpenAIStreamChunk(line); err == nil {
					capture.ObserveChunk(chunk)
					// 2026-07-28: integrity detector needs the
					// upstream-returned model for silent
					// substitution detection.
					if chunk.Model != "" {
						capture.SetRespModelIfEmpty(chunk.Model)
					}
					if capture.IntegrityBreached() {
						outcome = integrityBreachOutcome(capture, chunkCount)
						return outcome
					}
				}
			}
		}

		if norm != nil {
			line = string(norm.NormalizeChunk([]byte(line), true))
		}

		// Track C C2: capture the chunk into the pending buffer
		// BEFORE attempting the client write. The capturer is
		// bounded by maxBytes (1 MiB default) so a runaway
		// upstream cannot OOM the gateway.
		if pc != nil {
			pc.append(line)
		}

		// 2026-06-22 fix (refined 2026-07-27): Filter empty-choices blocks
		// from OpenAI streams ONLY when they carry no other useful payload.
		//
		// Some upstreams (e.g. glm-5.2 at https://api.supxh.xin) send
		// {"choices":[]} blocks which crash OpenAI clients that assume
		// choices[0] exists. Those should be dropped.
		//
		// BUT OpenAI's own spec, when stream_options.include_usage is set,
		// emits a terminal frame {"choices":[],"usage":{...}} — the canonical
		// usage-reporting frame. The original filter dropped these too,
		// silently losing token accounting for any client using include_usage.
		// The refined check parses the JSON and only drops the frame when
		// choices is empty AND there is no usage / other meaningful payload.
		if shouldDropEmptyChoicesFrame(line) {
			checkPayload := extractPayload(line)
			slog.Warn("relay: dropping empty choices block (no payload)",
				"payload_preview", truncateForLog(checkPayload, 100))
			continue // Skip this chunk
		}

		diagnosticCollector.observeEmittedLine(line)
		if writeClientLine(line) {
			lastSend = time.Now()
			chunkCount++ // Track chunks sent
			if capture != nil {
				capture.RecordChunkSent()
			}
		}

	}
}

func extractPayload(line string) string {
	if !strings.HasPrefix(line, "data: ") {
		return ""
	}
	payload := strings.TrimPrefix(line, "data: ")
	return strings.TrimSpace(payload)
}

// shouldDropEmptyChoicesFrame reports whether an SSE data line is a
// {"choices":[]} frame that carries no other useful payload and should be
// dropped before forwarding to the client.
//
// Drop criteria (all must hold):
//   - line is a "data: {...}" SSE frame (not [DONE], not a comment/event line)
//   - payload parses as a JSON object
//   - top-level "choices" exists and is an empty array []
//
// Keep criteria (return false → forward the frame) — the frame carries
// legitimate non-choices data the client may need:
//   - "usage" present  (OpenAI stream_options.include_usage terminal frame:
//     {"choices":[],"usage":{...}} — dropping this loses token accounting)
//   - "prompt_annotations" / "prompt_filter_results" present (Azure content
//     moderation frames sometimes arrive with empty choices)
//   - any other top-level key besides id/object/created/model/system_fingerprint
//     (i.e. something we don't recognize but the client might want)
//
// On any parse failure the function returns false (best-effort: forward
// unchanged rather than risk dropping a legitimate frame).
func shouldDropEmptyChoicesFrame(line string) bool {
	payload := extractPayload(line)
	if payload == "" || payload == "[DONE]" {
		return false
	}
	if !isOpenAIFormatData([]byte(payload)) {
		return false
	}
	// Fast path: no "choices":[] substring → definitely not a drop candidate.
	if !strings.Contains(payload, `"choices":[]`) {
		return false
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payload), &obj); err != nil {
		// Malformed JSON — forward unchanged (do not risk dropping data).
		return false
	}

	// Confirm choices is actually an empty array (the substring could be
	// nested elsewhere, e.g. inside usage metadata of a quirky provider).
	choicesRaw, ok := obj["choices"]
	if !ok {
		return false
	}
	var choicesArr []json.RawMessage
	if err := json.Unmarshal(choicesRaw, &choicesArr); err != nil || len(choicesArr) > 0 {
		return false // choices missing, not an array, or non-empty → keep
	}

	// choices is []. Now decide whether the rest of the frame is "useful".
	// These keys are pure usage/accounting/context that clients consume even
	// with empty choices — keep the frame if any is present.
	for _, keepKey := range []string{"usage", "prompt_annotations", "prompt_filter_results"} {
		if v, present := obj[keepKey]; present && string(v) != "null" {
			return false
		}
	}
	// Any other non-boilerplate top-level key is treated as potential signal.
	for k := range obj {
		switch k {
		case "id", "object", "created", "model", "system_fingerprint", "choices":
			continue // boilerplate; ignore
		default:
			return false // unrecognized key → forward to be safe
		}
	}
	return true
}

// stripChunkFields applies stripFn to the JSON payload of a "data: {...}" line.
// Non-data lines (event:, comment:, blank) are returned unchanged.
func stripChunkFields(line string, stripFn func([]byte) []byte) string {
	if !strings.HasPrefix(line, "data: ") || stripFn == nil {
		return line
	}
	payload := strings.TrimPrefix(line, "data: ")
	payload = strings.TrimSpace(payload)
	if payload == "" || payload == "[DONE]" {
		return line
	}
	stripped := stripFn([]byte(payload))
	if len(stripped) == 0 {
		return line
	}
	return "data: " + string(stripped) + "\n"
}

func readLineWithTimeout(ctx context.Context, reader *bufio.Reader, timeout time.Duration) (string, error) {
	return newTimedLineReader(reader, nil).ReadLine(ctx, timeout)
}

// readLineWithTimeoutAndCloser is like readLineWithTimeout but also takes the
// underlying io.ReadCloser. On timeout it closes the closer to unblock the
// ReadString goroutine, then drains the channel — eliminating the goroutine
// leak that existed in the plain readLineWithTimeout path (BUG-1 fix).
func readLineWithTimeoutAndCloser(ctx context.Context, reader *bufio.Reader, closer io.ReadCloser, timeout time.Duration) (string, error) {
	return newTimedLineReader(reader, closer).ReadLine(ctx, timeout)
}

type timedLineReader struct {
	reader *bufio.Reader
	// closer is the underlying io.ReadCloser (e.g. resp.Body). When non-nil,
	// ReadLine closes it on timeout so the blocked ReadString goroutine returns
	// immediately rather than leaking until the TCP connection is closed.
	closer io.ReadCloser
}

func newTimedLineReader(reader *bufio.Reader, closer io.ReadCloser) *timedLineReader {
	return &timedLineReader{reader: reader, closer: closer}
}

func (r *timedLineReader) ReadLine(ctx context.Context, timeout time.Duration) (string, error) {
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	readCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	go func() {
		defer func() {
			if r := recover(); r != nil {
				ch <- result{"", fmt.Errorf("read panic: %v", r)}
			}
		}()
		line, err := r.reader.ReadString('\n')
		ch <- result{line, err}
	}()

	select {
	case res := <-ch:
		return res.line, res.err
	case <-readCtx.Done():
		// BUG-1 fix (2026-06-19): close the underlying body to force the
		// blocked ReadString goroutine to return an error immediately.
		// Without this, the goroutine would leak until resp.Body.Close()
		// is called by the deferred cleanup in StreamChatWithPendingCapture,
		// which can be minutes later on the session path (context.Background).
		// After Close(), drain the channel so the goroutine completes before
		// we return — zero goroutine leak guarantee.
		if r.closer != nil {
			_ = r.closer.Close()
		}
		// Drain: the goroutine returns shortly after Close() because
		// ReadString on a closed body returns io.ErrClosedPipe or io.EOF.
		// The buffered channel (size 1) ensures this never blocks forever.
		<-ch
		if readCtx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("stream read timeout")
		}
		return "", readCtx.Err()
	}
}

func extractModelFromChunk(line string) string {
	if !strings.HasPrefix(line, "data: ") || strings.HasPrefix(line, "data: [DONE") {
		return ""
	}
	jsonStr := strings.TrimPrefix(line, "data: ")
	jsonStr = strings.TrimSpace(jsonStr)
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(jsonStr), &obj); err != nil {
		return ""
	}
	if modelRaw, ok := obj["model"]; ok {
		var modelStr string
		if err := json.Unmarshal(modelRaw, &modelStr); err == nil {
			return modelStr
		}
	}
	return ""
}

func safeFlush(flusher http.Flusher) (ok bool) {
	if flusher == nil {
		return false
	}
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("flush after close (client likely disconnected)", "recover", r)
			ok = false
		}
	}()
	flusher.Flush()
	return true
}

func safeWriteSSE(w io.Writer, line string) (ok bool) {
	if w == nil {
		return false
	}
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("write after close (client likely disconnected)", "recover", r)
			ok = false
		}
	}()
	n, err := io.WriteString(w, line)
	if err != nil {
		slog.Warn("failed to write SSE chunk to client", "error", err)
		return false
	}
	if n != len(line) {
		slog.Warn("incomplete write to client", "expected", len(line), "written", n)
		return false
	}
	return true
}

func replaceModelInChunk(line, clientModel, discoveredUpstream string) string {
	if !strings.HasPrefix(line, "data: ") || clientModel == "" {
		return line
	}
	if strings.HasPrefix(line, "data: [DONE") {
		return line
	}
	jsonStr := strings.TrimPrefix(line, "data: ")
	jsonStr = strings.TrimSpace(jsonStr)

	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(jsonStr), &obj); err != nil {
		return line
	}
	modelRaw, ok := obj["model"]
	if !ok {
		return line
	}
	var modelStr string
	if err := json.Unmarshal(modelRaw, &modelStr); err != nil {
		return line
	}
	if modelStr == clientModel {
		return line
	}
	if discoveredUpstream != "" && modelStr != discoveredUpstream {
		return line
	}
	obj["model"], _ = json.Marshal(clientModel)
	newJSON, err := json.Marshal(obj)
	if err != nil {
		return line
	}
	return "data: " + string(newJSON) + "\n"
}

func BuildSSEChunk(data string) string {
	var b strings.Builder
	scanner := bufio.NewScanner(strings.NewReader(data))
	for scanner.Scan() {
		fmt.Fprintf(&b, "data: %s\n", scanner.Text())
	}
	b.WriteString("\n")
	return b.String()
}

// pendingCapturer (Track C C2, 2026-06-18) records every chunk
// the upstream sends so a client that disconnected mid-stream
// can still recover the response via GET /v1/sessions/{id}/
// pending-response. The capturer is intentionally minimal:
// the streaming hot path stays in StreamChatWithPendingCapture;
// this type only collects bytes and finalises them at the end.
//
// Concurrency: written from one goroutine (the stream loop)
// and finalised from the deferred cleanup of that same
// goroutine. The mutex is belt-and-braces — in practice it's
// never contended.
// PendingFinalState is the state recorded by finalize() and
// read by Snapshot(). cmd/gateway/main.go reads these fields
// after the stream returns to write the captured body to the
// pending store. The fields are exported so the wiring caller
// can read them across the package boundary.
type PendingFinalState struct {
	Status      string
	ErrMessage  string
	CompletedAt int64
	Overflowed  bool
}

// pendingCapturer is the unexported canonical name. We also
// export it (below) so cmd/gateway/main.go can hold a
// reference without an awkward constructor signature.
type pendingCapturer struct {
	mu       sync.Mutex
	buffer   []byte
	bytes    int
	maxBytes int

	finalized  bool
	overflowed bool
	finalState PendingFinalState
}

// PendingCapturer is the exported alias used by the wiring.
// Internally everything uses the unexported name so godoc
// links to the right type.
type PendingCapturer = pendingCapturer

// NewPendingCapturer is the exported constructor; the
// unexported name is the canonical one for godoc + tests.
func NewPendingCapturer(maxBytes int) *pendingCapturer {
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	return &pendingCapturer{maxBytes: maxBytes}
}

// append copies line into the internal buffer up to the cap.
// Once the cap is reached, subsequent chunks are dropped.
// Audit fix 3.3: if a chunk doesn't fully fit, we drop the
// entire chunk rather than truncating mid-JSON. A truncated
// SSE line produces a parse error on replay; dropping it
// leaves the preceding chunks intact (which is better).
func (p *pendingCapturer) append(line string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.bytes >= p.maxBytes {
		p.overflowed = true
		return
	}
	remaining := p.maxBytes - p.bytes
	if len(line) > remaining {
		// Drop the entire chunk — truncating mid-JSON would
		// produce an invalid SSE line on replay.
		p.overflowed = true
		return
	}
	p.buffer = append(p.buffer, line...)
	p.bytes = len(p.buffer)
}

// markInterrupted is called from the panic-recovery path to
// mark the buffer as failed (rather than completed).
func (p *pendingCapturer) markInterrupted(reason string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.finalized {
		return
	}
	p.finalState = PendingFinalState{
		Status:      "failed",
		ErrMessage:  "stream_" + reason,
		CompletedAt: time.Now().Unix(),
	}
	p.finalized = true
}

// finalize records the terminal state. A client_cancel with a non-empty
// buffer counts as "completed" because we have the body for replay.
// Other interrupted reasons count as "failed" so the replay surfaces
// the error to the client.
//
// BUG-4 fix (2026-06-19): if the client cancels before any chunk arrives
// (p.bytes == 0), mark the entry "failed" rather than "completed". An
// empty-body "completed" entry is misleading — the GET endpoint already
// guards against it (returning 404 for empty body) but the Status field
// itself is wrong, and any future code path inspecting Status == "completed"
// would misread it as a successful, replayable response.
//
// Track C C5 (2026-06-21): "client_disconnected" (used by the Anthropic
// passthrough path) is treated identically to "client_cancel" for
// replayability — both indicate the upstream kept streaming but the
// client went away mid-stream, so we have the body for replay.
func (p *pendingCapturer) finalize(outcome StreamOutcome) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.finalized {
		return
	}
	clientWentAway := outcome.Reason == "client_cancel" || outcome.Reason == "client_disconnected"
	if p.overflowed {
		p.finalState = PendingFinalState{
			Status:      "failed",
			ErrMessage:  "pending_capture_overflow",
			CompletedAt: time.Now().Unix(),
			Overflowed:  true,
		}
	} else if outcome.Interrupted {
		if clientWentAway && p.bytes > 0 {
			// Client disconnected but we captured at least one chunk —
			// the body is replayable.
			p.finalState = PendingFinalState{
				Status:      "completed",
				CompletedAt: time.Now().Unix(),
			}
		} else if clientWentAway {
			// Client cancelled before the first byte arrived. Nothing to
			// replay; mark failed so the GET endpoint returns a clear error.
			p.finalState = PendingFinalState{
				Status:      "failed",
				ErrMessage:  "client_cancel_before_first_chunk",
				CompletedAt: time.Now().Unix(),
			}
		} else {
			p.finalState = PendingFinalState{
				Status:      "failed",
				ErrMessage:  outcome.Reason,
				CompletedAt: time.Now().Unix(),
			}
		}
	} else {
		p.finalState = PendingFinalState{
			Status:      "completed",
			CompletedAt: time.Now().Unix(),
		}
	}
	p.finalized = true
}

// Snapshot returns a copy of the buffer and final state.
// Exposed for the wiring in cmd/gateway/main.go to read the
// captured body after the stream returns and write it to
// the pending store.
func (p *pendingCapturer) Snapshot() (body []byte, state PendingFinalState, ok bool) {
	if p == nil {
		return nil, PendingFinalState{}, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.finalized {
		return nil, PendingFinalState{}, false
	}
	out := make([]byte, len(p.buffer))
	copy(out, p.buffer)
	return out, p.finalState, true
}

// BytesCaptured returns the number of bytes in the buffer.
// Exposed for the wiring in cmd/gateway/main.go to compute
// the approximate createdAt timestamp.
func (p *pendingCapturer) BytesCaptured() int {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.bytes
}

// ClientHasSessionID (Track C C2, 2026-06-18) is a small helper
// used by the wiring in cmd/gateway/main.go to decide whether
// to attach a capturer to a stream. The decision mirrors the
// one in streaming.hasSessionID: if the upstream request
// carries X-Gw-Session-Id or X-Session-Id, the client is
// eligible for replay and we capture the stream body.
//
// Reads from w.Header() (the request the executor forwarded to
// the upstream) and from resp.Request (the actual http.Request
// the upstream call was built with). Both should agree in
// production; we check both for defence in depth.
func ClientHasSessionID(w http.ResponseWriter, resp *http.Response) bool {
	// w in production is the gateway response writer; we
	// cannot read the original request headers from it.
	// The canonical source is resp.Request.
	if resp == nil || resp.Request == nil {
		return false
	}
	if v := resp.Request.Header.Get("X-Gw-Session-Id"); v != "" {
		return true
	}
	if v := resp.Request.Header.Get("X-Session-Id"); v != "" {
		return true
	}
	return false
}

// SessionIDFromResp (Track C C2) reads the canonical session
// id from the upstream request headers. Returns "" if no
// session is in play — the caller decides whether that is a
// writeable key (it is not: no session means no GET endpoint).
func SessionIDFromResp(resp *http.Response) string {
	if resp == nil || resp.Request == nil {
		return ""
	}
	if v := resp.Request.Header.Get("X-Gw-Session-Id"); v != "" {
		return v
	}
	return resp.Request.Header.Get("X-Session-Id")
}

// RequestIDFromResp (Track C C2) reads the per-request id.
// Falls back to the time-suffixed synthetic id used in
// async-retry when no X-Request-Id is supplied. We use the
// same fallback here so the GET endpoint can always locate
// the entry by request_id.
func RequestIDFromResp(resp *http.Response) string {
	if resp == nil || resp.Request == nil {
		return ""
	}
	if v := resp.Request.Header.Get("X-Request-Id"); v != "" {
		return v
	}
	return ""
}
