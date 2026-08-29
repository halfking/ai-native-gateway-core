package streaming

// anthropic_bridge.go ports the four relay/relay anthropic streaming/conversion
// helpers into the live streaming package so cmd/gateway can drop the
// _to-be-deprecated/relay import. Each wrapper here mirrors the
// deprecated implementation byte-for-byte and is exercised by the
// `cmd/gateway` flow tests once main.go is cut over.
//
// 2026-06-26 deep-integration: streaming package becomes the single
// source of truth for the OpenAI/Anthropic chat wire formats.

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	anthropictransform "github.com/kaixuan/llm-gateway-go/domains/transformation/anthropic"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/internal/ir"
	"github.com/kaixuan/llm-gateway-go/internal/textsplit"
	"github.com/kaixuan/llm-gateway-go/metrics"
)

const anthropicSSEBufSize = 64 * 1024

// clientStreamWriter wraps an http.ResponseWriter + http.Flusher pair and
// remembers whether the client has gone away. The protocol-bridge paths
// (Anthropic↔OpenAI, Anthropic↔Responses) use this wrapper so a failed
// client write latches a per-request "clientDisconnected" flag and
// subsequent writes short-circuit instead of repeatedly hitting a closed
// TCP connection. The capturer keeps appending either way so the upstream
// read loop can finish and persist a replayable body.
type clientStreamWriter struct {
	w                  http.ResponseWriter
	flusher            http.Flusher
	clientDisconnected bool
}

func newClientStreamWriter(w http.ResponseWriter, flusher http.Flusher) *clientStreamWriter {
	return &clientStreamWriter{w: w, flusher: flusher}
}

// write writes line to the client (best-effort) and updates the
// disconnected flag on failure.
func (c *clientStreamWriter) write(line string) bool {
	if c.clientDisconnected {
		return false
	}
	if !safeWriteSSE(c.w, line) || !safeFlush(c.flusher) {
		c.clientDisconnected = true
		return false
	}
	return true
}

// flush flushes the client connection and latches any error as a disconnect.
func (c *clientStreamWriter) flush() bool {
	if c.clientDisconnected || c.flusher == nil {
		return false
	}
	if !safeFlush(c.flusher) {
		c.clientDisconnected = true
		return false
	}
	return true
}

func applyClientDisconnectOutcome(outcome *StreamOutcome, clientWriter *clientStreamWriter, upstreamCompleted bool) {
	if outcome == nil || clientWriter == nil || !clientWriter.clientDisconnected {
		return
	}
	outcome.Interrupted = true
	outcome.Kind = errorsx.KindCanceled
	outcome.Resumable = false
	if upstreamCompleted {
		outcome.Reason = "client_disconnected"
	} else {
		outcome.Reason = "client_write_failed"
	}
}

// StreamAnthropicPassthrough is the live Q4 Anthropic SSE forwarder. It
// reads Anthropic-format SSE events from upstream and writes them to
// the client unchanged (byte-for-byte), while scanning for
// has_thinking / usage accounting in the side-channel audit capture.
//
// This is the "Q4" path: client speaks Anthropic, upstream speaks
// Anthropic (e.g. anthropic provider, or minimax's /anthropic
// compatible endpoint), no protocol conversion required.
//
// Track C C5 (2026-06-21): when pc is non-nil, every byte forwarded
// to the client is also appended to the capturer buffer so the
// gateway can replay the full SSE response from pending store after
// a client disconnect. The capturer is finalized before return so
// the caller can snapshot and persist it (see cmd/gateway/main.go's
// saveCapturedPending helper). nil pc is fine.
// P1-2 fix (2026-08-28): Added ctx parameter for context propagation to gate.
func StreamAnthropicPassthrough(
	ctx context.Context,
	w http.ResponseWriter,
	resp *http.Response,
	clientModel, outboundModel, requestID string,
	capture *audit.StreamCapture,
	pc *pendingCapturer,
) (outcome StreamOutcome) {
	return StreamAnthropicPassthroughWithDiagnostics(
		ctx, w, resp, clientModel, outboundModel, requestID, capture, pc, nil,
	)
}

// StreamAnthropicPassthroughWithDiagnostics forwards an Anthropic stream with
// optional best-effort diagnostics.
// P1-2 fix (2026-08-28): Added ctx parameter for context propagation to gate.
func StreamAnthropicPassthroughWithDiagnostics(
	ctx context.Context,
	w http.ResponseWriter,
	resp *http.Response,
	clientModel, outboundModel, requestID string,
	capture *audit.StreamCapture,
	pc *pendingCapturer,
	diagnostics *DiagnosticContext,
) (outcome StreamOutcome) {
	//nolint:errcheck // best-effort close
	defer resp.Body.Close()
	defer func() {
		if r := recover(); r != nil {
			slog.Error("anthropic passthrough panic", "panic", r, "stack", string(debug.Stack()))
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
		if pc != nil {
			pc.finalize(outcome)
		}
	}()

	// SR-W1: route client frames through the attempt commit gate.
	// Disabled (default) this is the identity function — legacy wire bytes.
	// The gate drives the error-path terminal rendering below: while the
	// attempt is still uncommitted (buffered mode), intercepted upstream
	// error frames never reach the wire and the interruption stays
	// transparently retryable.
	var attemptGate *AttemptCommitGate
	// P1-2 fix (2026-08-28): Pass ctx to wrapAttemptWriter for context propagation.
	w, attemptGate = wrapAttemptWriter(ctx, w, ProtocolAnthropic)
	defer func() {
		if finisher, ok := w.(interface{ Finish() error }); ok {
			if err := finisher.Finish(); err != nil && !outcome.Interrupted {
				// A pending capturer holds the completed upstream body
				// regardless of wire success. Finish() only fails when the
				// buffered gate's end-of-attempt flush hits the dead
				// client connection — that does not invalidate the
				// captured body, so keep the turn replayable.
				if pc != nil {
					return
				}
				// Client connection is dead by the time Finish() fails. A
				// transparent retry would attempt to write headers to the
				// same dead connection, wasting an upstream call. We
				// deliberately do not consult the gate here: regardless of
				// whether semantic output was committed, the new attempt
				// cannot reach the dead client.
				outcome = StreamOutcome{
					Interrupted: true,
					Reason:      "client_write_failed",
					Kind:        errorsx.KindCanceled,
					Resumable:   false,
					ChunkCount:  outcome.ChunkCount,
				}
				if capture != nil {
					capture.MarkInterruptedWithReason("client_write_failed")
				}
			}
		}
	}()
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return StreamOutcome{Interrupted: true, Reason: "no_flusher"}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	if requestID != "" {
		w.Header().Set("X-Request-Id", requestID)
	}
	w.WriteHeader(http.StatusOK)
	if !safeFlush(flusher) {
		if capture != nil {
			capture.MarkInterruptedWithReason("client_write_failed")
		}
		if pc != nil {
			pc.markInterrupted("client_write_failed")
		}
		// Client connection is dead before any frame — including headers —
		// reaches the wire. A transparent retry would re-attempt the same
		// header flush on the same dead connection, wasting an upstream call.
		return StreamOutcome{Interrupted: true, Reason: "client_write_failed", Kind: errorsx.KindCanceled, Resumable: false}
	}

	reader := bufio.NewReaderSize(resp.Body, anthropicSSEBufSize)
	// P1-2 fix (2026-08-28): ctx is now a function parameter, no need to redeclare.
	// Use the passed ctx directly; fallback to resp.Request.Context() is no longer needed
	// since the caller provides the authoritative context.
	runtimeCfg := currentStreamRuntimeConfig()
	clientWriter := newClientStreamWriter(w, flusher)
	chunkCount := 0
	// Tracks whether any content_block_start/delta/stop reached the wire —
	// distinct from chunkCount which counts every data line including
	// message_start/message_stop envelopes. The empty-response detector at
	// the bottom of this function must use semantic-content presence, not
	// envelope-only count, otherwise an upstream that returns just
	// message_start + message_stop (no real content) is misclassified as a
	// successful non-empty stream.
	semanticBlockCount := 0

	// Upstream error-event interception (2026-08-17): Anthropic `event:
	// error` frames are terminal stream failures, but relay-style upstreams
	// pack multi-line diagnostic blobs ("Turn execution failed / provider=…
	// reason=… retryable=…") into the error message. Forwarding those frames
	// byte-for-byte surfaced the relay's internals to the client and — when
	// the relay closed the connection cleanly right after — recorded the
	// turn as a success (EOF path). Intercept the frame instead: classify
	// it, keep the raw payload on the audit side channels only, and either
	// leave the interruption transparently retryable (nothing client-visible
	// yet) or render the gateway's own structured error event.
	interceptUpstreamErrorEvent := func(payload string) StreamOutcome {
		var ev struct {
			Error *struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal([]byte(payload), &ev)
		errType := ""
		if ev.Error != nil {
			errType = ev.Error.Type
		}
		kind := classifyAnthropicStreamError(errType, []byte(payload))
		msg := "upstream stream error"
		if errType != "" {
			msg = "upstream stream error: " + errType
		}
		oc := StreamOutcome{Interrupted: true, Reason: "upstream_error", Kind: kind, Resumable: true, ChunkCount: chunkCount}
		// finalize before MarkInterruptedWithReason: the mark pins the
		// capture's chunk counters, and finalize's RecordChunkSent must
		// still be able to advance them.
		finalizePassthroughInterruption(clientWriter, attemptGate, capture, &oc, chunkCount, msg)
		if capture != nil {
			observeAnthropicPayload(capture, payload, clientModel, outboundModel)
			capture.MarkInterruptedWithReason("upstream_error")
		}
		slog.Warn("anthropic passthrough: upstream terminal error event",
			"request_id", requestID,
			"client_model", clientModel,
			"outbound_model", outboundModel,
			"error_type", errType,
			"kind", string(kind),
			"client_visible_chunks", chunkCount,
			"resumable", oc.Resumable,
		)
		return oc
	}

	// heldErrorEventLine buffers a seen `event: error` line until its data
	// payload arrives: the forward/drop decision needs the payload, and an
	// event line without data never dispatches client-side (SSE spec), so
	// holding it is invisible to the client.
	heldErrorEventLine := ""

	for {
		line, err := readLineWithTimeoutAndCloser(ctx, reader, resp.Body, runtimeCfg.streamChunkTimeout)
		if err != nil {
			// EOF after a client disconnect is still a successful upstream
			// capture; pending replay must be allowed to finalize.
			if errors.Is(err, io.EOF) {
				if heldErrorEventLine != "" {
					// `event: error` then hard EOF: the frame never
					// completed, but the upstream's intent is unambiguous.
					outcome = interceptUpstreamErrorEvent("")
					return outcome
				}
				break
			}
			if clientWriter.clientDisconnected {
				// Client is already gone: also mark capture interrupted so
				// SummaryAsMap's stream_interrupted reflects reality (every
				// other interrupted branch in this function marks it).
				if capture != nil {
					capture.MarkInterruptedWithReason("client_write_failed")
				}
				outcome = StreamOutcome{Interrupted: true, Reason: "client_write_failed", Kind: errorsx.KindCanceled, ChunkCount: chunkCount}
				return outcome
			}
			if errors.Is(err, context.Canceled) || (ctx != nil && errors.Is(ctx.Err(), context.Canceled)) {
				outcome = StreamOutcome{Interrupted: true, Reason: "client_cancel", Kind: errorsx.KindCanceled, Resumable: false, ChunkCount: chunkCount}
			} else if errors.Is(err, context.DeadlineExceeded) || strings.Contains(strings.ToLower(err.Error()), "stream read timeout") {
				// Gate-aware resumability. Mirrors the eof_without_done,
				// stream_timeout, and upstream_error branches in this
				// function: a chunk timeout after the client already saw
				// semantic output must NOT be transparently retried — the
				// next supplier node would duplicate committed bytes.
				outcome = StreamOutcome{Interrupted: true, Reason: "stream_chunk_timeout", Kind: errorsx.KindStreamTimeout, Resumable: !attemptHasClientSemanticOutput(attemptGate, chunkCount), ChunkCount: chunkCount}
			} else {
				outcome = streamReadFailureOutcome(err, chunkCount)
			}
			// The stream already ended abnormally, so the relay's error
			// blob can no longer arrive — but if content frames of this
			// attempt are client-visible the client needs a structured
			// terminal error instead of a bare connection end, and the
			// capture must reflect that a retry would duplicate content.
			// finalize runs BEFORE the mark: MarkInterruptedWithReason
			// pins the chunk counters, and finalize's RecordChunkSent
			// must still be able to advance them.
			finalizePassthroughInterruption(clientWriter, attemptGate, capture, &outcome, chunkCount,
				fmt.Sprintf("upstream stream interrupted: %s", outcome.Reason))
			if outcome.Interrupted && capture != nil {
				capture.MarkInterruptedWithReason(outcome.Reason)
			}
			return outcome
		}
		if line == "" {
			// Blank line terminates an SSE event; a held `event: error`
			// line with no data payload never dispatches — drop the hold.
			heldErrorEventLine = ""
			continue
		}
		trimmed := strings.TrimSpace(line)
		isDataLine := strings.HasPrefix(trimmed, "data:")
		var dataPayload string
		if isDataLine {
			dataPayload = strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
		}
		if heldErrorEventLine != "" {
			held := heldErrorEventLine
			heldErrorEventLine = ""
			if dataPayload != "" {
				// The event line declared `error`: terminal regardless of
				// the payload's shape (relays sometimes ship malformed
				// payloads — classification handles those).
				if pc != nil {
					pc.append(line)
				}
				logRawUpstreamFrame(diagnostics, auditFromDiagnostics(diagnostics, requestID, "anthropic-messages"), []byte(line))
				outcome = interceptUpstreamErrorEvent(dataPayload)
				return outcome
			}
			// No data payload on the error event — release the held line.
			if !clientWriter.clientDisconnected && !clientWriter.write(held) {
				slog.Info("anthropic passthrough: client disconnected; continuing capture")
			}
		} else if isSSEEventLineNamed(trimmed, "error") {
			// Hold the error event line; side channels see it immediately.
			heldErrorEventLine = line
			if pc != nil {
				pc.append(line)
			}
			logRawUpstreamFrame(diagnostics, auditFromDiagnostics(diagnostics, requestID, "anthropic-messages"), []byte(line))
			continue
		}
		if dataPayload != "" && isAnthropicErrorPayload(dataPayload) {
			// Standalone error payload (relay omitted the event: line).
			if pc != nil {
				pc.append(line)
			}
			logRawUpstreamFrame(diagnostics, auditFromDiagnostics(diagnostics, requestID, "anthropic-messages"), []byte(line))
			outcome = interceptUpstreamErrorEvent(dataPayload)
			return outcome
		}
		// Count content_block_* envelopes for the empty-response detector
		// (semanticBlockCount). message_start/message_delta/message_stop are
		// protocol envelopes — they are NOT semantic content even though
		// they are data lines and contribute to chunkCount above.
		if dataPayload != "" && isContentBlockPayload(dataPayload) {
			semanticBlockCount++
		}
		if !clientWriter.clientDisconnected {
			if !clientWriter.write(line) {
				slog.Info("anthropic passthrough: client disconnected; continuing capture")
			} else if isDataLine {
				chunkCount++
			}
		}
		if pc != nil {
			pc.append(line)
		}
		if capture != nil && isDataLine {
			observeAnthropicPayload(capture, dataPayload, clientModel, outboundModel)
		}

		logRawUpstreamFrame(diagnostics, auditFromDiagnostics(diagnostics, requestID, "anthropic-messages"), []byte(line))
		if err == io.EOF {
			break
		}
	}
	if !clientWriter.clientDisconnected && !clientWriter.flush() {
		outcome = StreamOutcome{Interrupted: true, Reason: "client_write_failed", Kind: errorsx.KindCanceled, ChunkCount: chunkCount}
		return outcome
	}
	// NOTE(2026-08-27): mid-loop client disconnect is deliberately NOT marked
	// Interrupted on the completed-upstream exit — the pending capturer needs
	// a completed replay body (see pending_disconnect_test.go /
	// TestStreamAnthropicPassthroughContinuesAfterClientDisconnect).

	// audit-24h-20260828-r4 CRITICAL: anthropic stream empty-response parity
	// with the non-stream detector at executor_anthropic.go:1273. The earlier
	// r3 patch (c6ab79105) only wired this check into
	// domains/transformation/anthropic/anthropic_passthrough_stream.go, which
	// is NOT on the live Q4 hot path — the production entry point is
	// StreamAnthropicPassthroughWithDiagnostics (cmd/gateway/main.go:1269
	// wires StreamAnthropicPassthrough → here). Without this guard, an
	// upstream that returns message_start + message_stop with zero
	// content_block_* events is recorded as a successful empty stream and
	// never fails over.
	//
	// Empty = no content_block_* events reached the client. Usage tokens alone
	// do NOT count as content — matching the documented contract on
	// anthropic.IsAnthropicStreamEmpty (stream_support.go) and the non-stream
	// semantics in isEmptyAnthropicMessagesResponse (content array length).
	// Some Anthropic-compat relays (notably minimax via the Anthropic bridge)
	// emit `usage` in `message_start` with zero output content; counting those
	// as non-empty would suppress fail-over and silently 200 an empty
	// assistant turn.
	//
	// pc != nil means the caller has set up a pending replay buffer for
	// client-disconnect recovery (cmd/gateway/main.go wires one in for
	// captureable requests). The replay buffer MUST see a completed body
	// even on empty streams, so we skip the empty-response interrupt and
	// let pc.finalize() run with the captured-but-empty bytes — the executor
	// downstream (executor_anthropic.go) is the authority on whether to
	// re-attempt vs. surface the empty body to the client.
	if pc == nil && semanticBlockCount == 0 && (capture == nil || (capture.OutputTokens == nil && capture.InputTokens == nil)) {
		if capture != nil {
			capture.MarkInterruptedWithReason("anthropic_empty_response")
		}
		if pc != nil {
			pc.markInterrupted("anthropic_empty_response")
		}
		return StreamOutcome{
			Interrupted: true,
			Reason:      "anthropic_empty_response",
			Kind:        errorsx.KindEmptyResponse,
			Resumable:   true,
			ChunkCount:  chunkCount,
		}
	}
	outcome.ChunkCount = chunkCount
	return outcome
}

// finalizePassthroughInterruption renders the gateway's own Anthropic error
// event when frames of this attempt are already client-visible, and pins the
// capture's sent-chunk counter so the executor's mayRetryInterruptedStream
// cannot approve a retry that would duplicate client-visible content
// (RecordChunkSent was previously only wired on the OpenAI→OpenAI bridge).
//
// Nothing is rendered while the attempt commit gate still holds the frames
// uncommitted (buffered mode): those bytes die with the gate and the
// interruption stays transparently retryable (outcome.Resumable untouched).
func finalizePassthroughInterruption(
	cw *clientStreamWriter,
	gate *AttemptCommitGate,
	capture *audit.StreamCapture,
	oc *StreamOutcome,
	chunkCount int,
	message string,
) {
	if oc == nil || !oc.Interrupted || oc.Kind == errorsx.KindCanceled {
		return
	}
	if chunkCount == 0 || cw == nil || cw.clientDisconnected {
		return
	}
	if !attemptHasClientSemanticOutput(gate, chunkCount) {
		return
	}
	writePassthroughErrorEvent(cw, "upstream_error", message)
	// A client-visible terminal error makes the attempt non-transparently
	// retryable even when no capture is attached (capture==nil otherwise
	// bypasses the snapshot check in mayRetryInterruptedStream).
	oc.Resumable = false
	if capture != nil {
		capture.RecordChunkSent()
	}
}

// writePassthroughErrorEvent renders one Anthropic-shaped error event with
// the gateway's own envelope, replacing the raw upstream error frame so
// relay-internal diagnostic blobs never reach the client verbatim.
func writePassthroughErrorEvent(cw *clientStreamWriter, errType, message string) {
	if cw == nil {
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"type":  "error",
		"error": map[string]any{"type": errType, "message": message},
	})
	cw.write(fmt.Sprintf("event: error\ndata: %s\n\n", payload))
}

// isAnthropicErrorPayload reports whether an SSE data payload is an
// Anthropic terminal error event — the canonical
// {"type":"error","error":{...}} shape plus the bare {"error":{...}}
// variant some relays emit.
func isAnthropicErrorPayload(payload string) bool {
	if payload == "" || payload == "[DONE]" {
		return false
	}
	var probe struct {
		Type  string          `json:"type"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal([]byte(payload), &probe); err != nil {
		return false
	}
	return probe.Type == "error" || (len(probe.Error) > 0 && string(probe.Error) != "null")
}

// isContentBlockPayload reports whether an Anthropic SSE data payload carries
// a content_block_* envelope (start, delta, or stop). Used by the live Q4
// passthrough empty-response detector to distinguish a stream that emitted
// only protocol envelopes (message_start/message_stop with zero content)
// from a stream that actually delivered an assistant turn.
func isContentBlockPayload(payload string) bool {
	if payload == "" {
		return false
	}
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(payload), &probe); err != nil {
		return false
	}
	switch probe.Type {
	case "content_block_start", "content_block_delta", "content_block_stop":
		return true
	}
	return false
}

// isSSEEventLineNamed reports whether trimmedLine is an `event: <name>`
// declaration for the named SSE event.
func isSSEEventLineNamed(trimmedLine, name string) bool {
	if !strings.HasPrefix(trimmedLine, "event:") {
		return false
	}
	return strings.TrimSpace(strings.TrimPrefix(trimmedLine, "event:")) == name
}

// classifyAnthropicStreamError maps an upstream Anthropic error event to an
// errorsx kind for the executor's circuit / failover bookkeeping. Known
// error.type tokens map directly; anything else falls back to body-pattern
// classification (overload phrasing etc.), defaulting to KindUpstreamDown
// so in-stream terminal errors are always retryable-classified rather than
// the generic transient.
func classifyAnthropicStreamError(errType string, payload []byte) errorsx.ErrorKind {
	// Explicit Anthropic error-type → ErrorKind table (Anthropic Messages
	// API reference). Anything not listed here is a relay-specific label
	// and falls through to the body classifier, which can still rescue
	// transient / overloaded signals from the message text.
	switch errType {
	case "overloaded_error":
		return errorsx.KindUpstreamOverloaded
	case "rate_limit_error":
		return errorsx.KindRateLimit
	case "authentication_error", "permission_error":
		return errorsx.KindAuth
	case "timeout", "timeout_error":
		return errorsx.KindTimeout
	case "api_error":
		// Anthropic's catch-all for upstream-internal failures. Treat as
		// upstream-down so failover engages, instead of letting the body
		// classifier accidentally map it to e.g. content_filter or model
		// not_found based on relay-injected hint text.
		return errorsx.KindUpstreamDown
	case "invalid_request_error":
		// Client-shaped problem (bad params, schema mismatch); do not
		// failover — surface it as a non-retryable invalid-input.
		return errorsx.KindClientBug
	case "not_found_error":
		return errorsx.KindModelNotFound
	case "context_length_exceeded":
		return errorsx.KindContextLength
	case "content_policy_violation":
		return errorsx.KindContentFilter
	}
	kind := errorsx.ClassifyErrorWithBody(0, payload)
	if kind == errorsx.KindTransient {
		return errorsx.KindUpstreamDown
	}
	return kind
}

// observeAnthropicPayload inspects a single Anthropic SSE data payload
// and updates the side-channel audit capture accordingly.
func observeAnthropicPayload(c *audit.StreamCapture, payload, clientModel, outboundModel string) {
	if payload == "" || payload == "[DONE]" {
		return
	}
	var v struct {
		Type    string `json:"type"`
		Message *struct {
			Model string `json:"model"`
			Usage struct {
				InputTokens  *int `json:"input_tokens"`
				OutputTokens *int `json:"output_tokens"`
			} `json:"usage"`
		} `json:"message"`
		Usage *struct {
			OutputTokens *int `json:"output_tokens"`
		} `json:"usage"`
		Index        *int `json:"index"`
		ContentBlock *struct {
			Type string `json:"type"`
		} `json:"content_block"`
	}
	if err := json.Unmarshal([]byte(payload), &v); err != nil {
		return
	}
	switch v.Type {
	case "message_start":
		if v.Message != nil {
			if v.Message.Usage.InputTokens != nil {
				pt := *v.Message.Usage.InputTokens
				c.InputTokens = &pt
			}
			if v.Message.Model != "" {
				checkAnthropicModelMismatch(c, clientModel, outboundModel, v.Message.Model)
			}
		}
	case "message_delta":
		if v.Usage != nil && v.Usage.OutputTokens != nil {
			ot := *v.Usage.OutputTokens
			c.OutputTokens = &ot
		}
	case "message_stop":
		c.MarkDone()
	case "content_block_start":
		if v.ContentBlock != nil && v.ContentBlock.Type == "thinking" {
			// 2026-07-27 并发修复：走带锁 setter（见 audit.StreamCapture.MarkThinkingBlock），
			// 避免与 SummaryAsMap 的 sc.mu 读竞争。
			c.MarkThinkingBlock()
		}
	case "error":
		c.MarkStreamError()
	}
}

func checkAnthropicModelMismatch(c *audit.StreamCapture, clientModel, outboundModel, respModel string) {
	want := clientModel
	if want == "" {
		want = outboundModel
	}
	if want == "" || respModel == "" {
		return
	}
	if !strings.EqualFold(want, respModel) {
		c.ModelMismatch = true
	}
}

// StreamAnthropicSSEToOpenAI converts Anthropic-format SSE upstream
// responses into OpenAI-format SSE chunks for Q3 mode
// (openai-completions client -> anthropic-messages upstream).
//
// This must never forward raw Anthropic events such as message_start
// or content_block_delta to the client. OpenAI SDKs validate each SSE
// payload as either a chat.completion.chunk (`choices`) or an error
// object; leaking native Anthropic payloads causes client-side schema
// failures.
// P1-2 fix (2026-08-28): Added ctx parameter for context propagation to gate.
func StreamAnthropicSSEToOpenAI(
	ctx context.Context,
	w http.ResponseWriter,
	resp *http.Response,
	clientModel, outboundModel, requestID string,
	capture *audit.StreamCapture,
	pc *pendingCapturer,
) (outcome StreamOutcome) {
	return StreamAnthropicSSEToOpenAIWithDiagnostics(
		ctx, w, resp, clientModel, outboundModel, requestID, capture, pc, nil,
	)
}

// StreamAnthropicSSEToOpenAIWithDiagnostics converts an Anthropic stream with
// optional best-effort diagnostics.
// P1-2 fix (2026-08-28): Added ctx parameter for context propagation to gate.
func StreamAnthropicSSEToOpenAIWithDiagnostics(
	ctx context.Context,
	w http.ResponseWriter,
	resp *http.Response,
	clientModel, outboundModel, requestID string,
	capture *audit.StreamCapture,
	pc *pendingCapturer,
	diagnostics *DiagnosticContext,
) (outcome StreamOutcome) {
	//nolint:errcheck // best-effort close
	defer resp.Body.Close()
	defer func() {
		if r := recover(); r != nil {
			slog.Error("anthropic-to-openai stream panic recovered",
				"panic", r, "stack", string(debug.Stack()),
				"request_id", requestID)
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
		if pc != nil {
			pc.finalize(outcome)
		}
	}()

	diagnosticCollector := &streamDiagnosticCollector{}
	defer func() {
		diagnosticCollector.report(diagnostics, requestID, "anthropic-messages", "openai-completions", outcome.Interrupted)
	}()

	// SR-W1: route client frames through the attempt commit gate.
	// Disabled (default) this is the identity function — legacy wire bytes.
	// P1-2 fix (2026-08-28): Pass ctx to wrapAttemptWriter for context propagation.
	w, gate := wrapAttemptWriter(ctx, w, ProtocolOpenAIChat)
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return StreamOutcome{Interrupted: true, Reason: "no_flusher"}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	if requestID != "" {
		w.Header().Set("X-Request-Id", requestID)
	}
	w.WriteHeader(http.StatusOK)
	if !safeFlush(flusher) {
		if capture != nil {
			capture.MarkInterruptedWithReason("client_write_failed")
		}
		if pc != nil {
			pc.markInterrupted("client_write_failed")
		}
		// Client connection is dead before any frame — including headers —
		// reaches the wire. A transparent retry would re-attempt the same
		// header flush on the same dead connection, wasting an upstream call.
		return StreamOutcome{Interrupted: true, Reason: "client_write_failed", Kind: errorsx.KindCanceled, Resumable: false}
	}

	chatID := "chatcmpl-" + requestID
	if requestID == "" {
		chatID = "chatcmpl-anthropic-openai"
	}
	createdAt := time.Now().Unix()
	chunkModel := clientModel
	if chunkModel == "" {
		chunkModel = outboundModel
	}

	// P1-2 fix (2026-08-28): ctx is now a function parameter, removed from var block.
	var (
		inputTokens         int
		outputTokens        int
		finishReason        *string
		toolCallIndex       int
		emittedRole         bool
		chunkCount          int
		bufferedText        strings.Builder
		hasEmittedToolCalls bool
		bufferedToolArgs    strings.Builder
		currentToolCallID   string
		initialArgsSent     bool
		messageStopReceived bool
		emittedContent      bool
		// Anthropic signature_delta has no OpenAI Chat Completions wire
		// representation. Keep the opaque token in bridge-local state and
		// expose only a stable digest to audit; never invent an OpenAI field.
		thinkingSignatures = make(map[int]string)
		// 2026-08-29: Tool call completeness validator to detect missing
		// tool_result blocks (see tool_call_validator.go for rationale).
		toolCallValidator = NewToolCallValidator()
	)

	clientWriter := newClientStreamWriter(w, flusher)
	defer func() {
		applyClientDisconnectOutcome(&outcome, clientWriter, !outcome.Interrupted)
	}()

	writeChunk := func(chunk *ir.StreamChunk) {
		if chunk == nil {
			return
		}

		sseLine := chunk.SerializeOpenAI(chatID, chunkModel, createdAt)
		// Count converted tool calls as emitted evidence before the write:
		// a client disconnect must not look like the gateway dropped them.
		diagnosticCollector.observeEmittedChunk(chunk)
		written := clientWriter.write(sseLine)

		// The translated chunk is part of client-visible accounting only when
		// the write/flush succeeded. The pending capture still receives the
		// translated frame after a disconnect so it can be replayed.
		if pc != nil {
			pc.append(sseLine)
		}

		if capture != nil {
			capture.ObserveChunk(chunk)
			if written {
				capture.RecordChunkSent()
			}
		}

		if written {
			chunkCount++
			if chunk.Type == ir.ChunkTypeDelta && chunk.Delta != nil &&
				(chunk.Delta.Content != "" || chunk.Delta.ReasoningContent != "" ||
					chunk.Delta.ThinkingSignature != "" || len(chunk.Delta.ToolCalls) > 0) {
				emittedContent = true
			}
		}
	}

	flushBufferedText := func() {
		if bufferedText.Len() == 0 {
			return
		}
		think, rest, ok := textsplit.SplitLeadingThink(bufferedText.String())
		if ok {
			if think != "" {
				writeChunk(&ir.StreamChunk{
					Type:           ir.ChunkTypeDelta,
					Delta:          &ir.StreamDelta{ReasoningContent: think},
					SourceProtocol: ir.ProtocolAnthropicMessages,
				})
			}
			if rest != "" {
				writeChunk(&ir.StreamChunk{
					Type:           ir.ChunkTypeDelta,
					Delta:          &ir.StreamDelta{Content: rest},
					SourceProtocol: ir.ProtocolAnthropicMessages,
				})
			}
		} else {
			writeChunk(&ir.StreamChunk{
				Type:           ir.ChunkTypeDelta,
				Delta:          &ir.StreamDelta{Content: bufferedText.String()},
				SourceProtocol: ir.ProtocolAnthropicMessages,
			})
		}
		bufferedText.Reset()
	}

	// The caller-supplied ctx is authoritative: it carries the dispatch/survival
	// cancellation boundary. Only fall back to the response request context when
	// the caller did not provide one.
	if ctx == nil {
		if resp.Request != nil {
			ctx = resp.Request.Context()
		} else {
			ctx = context.Background()
		}
	}

	runtimeCfg := currentStreamRuntimeConfig()
	reader := bufio.NewReaderSize(resp.Body, anthropicSSEBufSize)

	// Added panic recovery (2026-07-19): Prevent SSE parser crashes from killing the stream
	defer func() {
		if r := recover(); r != nil {
			slog.Error("anthropic_to_openai: stream reader panic",
				"panic", r,
				"request_id", requestID,
				"chunk_count", chunkCount)
			if capture != nil {
				capture.MarkInterruptedWithReason("stream_panic")
			}
			if attemptHasClientSemanticOutput(gate, chunkCount) {
				emitAnthropicBridgeErrorChunk(w, "stream_panic",
					fmt.Sprintf("internal error: %v", r), flusher)
			}
			outcome.Interrupted = true
			outcome.Reason = "stream_panic"
			outcome.Kind = errorsx.KindUpstreamDown
			outcome.ChunkCount = chunkCount
		}
	}()

	for {
		eventType, data, rawFrame, err := readAnthropicSSEEventWithTimeoutRaw(
			ctx, reader, resp.Body, runtimeCfg.streamChunkTimeout,
		)
		if errors.Is(err, context.DeadlineExceeded) {
			slog.Warn("anthropic_to_openai: chunk timeout",
				"timeout_seconds", runtimeCfg.streamChunkTimeout.Seconds(),
				"chunks_received", chunkCount,
				"request_id", requestID)
			if capture != nil {
				capture.MarkInterruptedWithReason("stream_chunk_timeout")
			}
			if attemptHasClientSemanticOutput(gate, chunkCount) {
				emitAnthropicBridgeErrorChunk(w, "stream_chunk_timeout",
					fmt.Sprintf("no data received for %v", runtimeCfg.streamChunkTimeout), flusher)
			}
			outcome.Interrupted = true
			outcome.Reason = "chunk_timeout"
			outcome.Kind = errorsx.KindStreamTimeout
			outcome.Resumable = !attemptHasClientSemanticOutput(gate, chunkCount)
			outcome.ChunkCount = chunkCount

			if pc != nil {
				pc.markInterrupted("chunk_timeout")
			}
			return outcome
		}

			if err != nil {
				if errors.Is(err, io.EOF) {
					// EOF without an Anthropic message_stop is an upstream
					// interruption, not a successful completion. Only a stream
					// that supplied its protocol terminal event may finalize.
					if !messageStopReceived {
						// 2026-08-29: Check if EOF happened during tool execution
						if toolCallValidator.HasPendingToolUses() {
							slog.Warn("anthropic_to_openai: EOF during tool execution",
								"request_id", requestID,
								"pending_tool_uses", toolCallValidator.PendingCount(),
								"client_model", clientModel,
							)
							if capture != nil {
								capture.MarkInterruptedWithReason("incomplete_tool_call_interrupted")
							}
							kind, resumable, reason := classifyIncompleteToolCall(false)
							metrics.Global().RecordIncompleteToolCall(clientModel, reason)
							outcome = StreamOutcome{
								Interrupted: true,
								Reason:      reason,
								Kind:        kind,
								Resumable:   resumable,
								ChunkCount:  chunkCount,
							}
							return outcome
						}
						outcome = StreamOutcome{
							Interrupted: true,
							Reason:      "eof_without_done",
							Kind:        errorsx.KindUpstreamDown,
							Resumable:   !attemptHasClientSemanticOutput(gate, chunkCount),
							ChunkCount:  chunkCount,
						}
						if capture != nil {
							capture.MarkInterruptedWithReason(outcome.Reason)
						}
						return outcome
					}
					// SR-W1: pending REAL text must still be delivered even while
					// the gate holds an uncommitted attempt — delivering it
					// commits the attempt and the closing usage/done chunks
					// follow. An EOF with nothing delivered (empty stream) stays
					// droppable: no completed stream is fabricated.
					if gate.MayWriteTerminal() || bufferedText.Len() > 0 {
						flushBufferedText()
						// Incremental integrity breach on the flushed text: cut
						// before emitting the closing usage/done chunks so the
						// executor can failover. Mirrors stream.go.
						if capture != nil && capture.IntegrityBreached() {
							return integrityBreachOutcome(capture, chunkCount)
						}
						// 2026-08-29: Validate tool call completeness on clean EOF
						if err := toolCallValidator.ValidateComplete(); err != nil {
							slog.Warn("anthropic_to_openai: incomplete tool call on EOF",
								"request_id", requestID,
								"error", err.Error(),
								"client_model", clientModel,
							)
							if capture != nil {
								capture.MarkInterruptedWithReason("incomplete_tool_call")
							}
							kind, resumable, reason := classifyIncompleteToolCall(true)
							metrics.Global().RecordIncompleteToolCall(clientModel, reason)
							return StreamOutcome{
								Interrupted: true,
								Reason:      reason,
								Kind:        kind,
								Resumable:   resumable,
								ChunkCount:  chunkCount,
							}
						}
						if gate.MayWriteTerminal() {
							if inputTokens > 0 || outputTokens > 0 {
								writeChunk(&ir.StreamChunk{
									Type: ir.ChunkTypeUsage,
									Usage: &ir.StreamUsage{
										PromptTokens:     inputTokens,
										CompletionTokens: outputTokens,
										TotalTokens:      inputTokens + outputTokens,
									},
									FinishReason:   "stop",
									SourceProtocol: ir.ProtocolAnthropicMessages,
								})
							}
							writeChunk(&ir.StreamChunk{Type: ir.ChunkTypeDone, SourceProtocol: ir.ProtocolAnthropicMessages})
						}
					}
				return StreamOutcome{ChunkCount: chunkCount}
			}
			failure := streamReadFailureOutcome(err, chunkCount)
			outcome = failure
			// Gate-aware resumability. streamReadFailureOutcome hardcodes
			// Resumable=true; a read failure after the client already saw
			// semantic output must NOT be transparently retried — the next
			// supplier node would duplicate committed bytes. Mirrors the
			// eof_without_done, stream_timeout, and stream_chunk_timeout
			// branches in this function.
			outcome.Resumable = !attemptHasClientSemanticOutput(gate, chunkCount)
			if capture != nil {
				capture.MarkInterruptedWithReason(failure.Reason)
			}
			if attemptHasClientSemanticOutput(gate, chunkCount) {
				emitAnthropicBridgeErrorChunk(w, "stream_read_error", err.Error(), flusher)
			}
			return outcome
		}

		if eventType == "" || len(data) == 0 {
			continue
		}

		logRawUpstreamFrame(diagnostics, auditFromDiagnostics(diagnostics, requestID, "anthropic-messages"), rawFrame)
		diagnosticCollector.observeRaw(data)

		if isOpenAIFormatData(data) {
			slog.Warn("anthropic_to_openai: detected OpenAI-format data, dropping",
				"event_type", eventType,
				"data_preview", truncateForLog(string(data), 100),
				"request_id", requestID)
			continue
		}

		chunk, err := ir.ParseAnthropicStreamEvent(eventType, data)
		if err != nil {
			slog.Warn("anthropic_to_openai: parse failed",
				"event_type", eventType,
				"error", err,
				"request_id", requestID)

			reportConversionAnomaly(
				diagnostics, requestID, "anthropic-messages", "openai-completions", "parse_stream_event", data, err,
				map[string]interface{}{"event_type": eventType},
			)
			continue
		}

		if chunk != nil {
			diagnosticCollector.observeChunk(chunk)
		}

		switch chunk.Type {
		case ir.ChunkTypeUsage:
			if chunk.Usage != nil {
				if chunk.Usage.PromptTokens > 0 {
					inputTokens = chunk.Usage.PromptTokens
				}
				if chunk.Usage.CompletionTokens > 0 {
					outputTokens = chunk.Usage.CompletionTokens
				}
			}

			if chunk.ID != "" && !emittedRole {
				writeChunk(&ir.StreamChunk{
					Type:           ir.ChunkTypeDelta,
					Delta:          &ir.StreamDelta{Role: "assistant"},
					SourceProtocol: ir.ProtocolAnthropicMessages,
				})
				emittedRole = true
			}

			if chunk.FinishReason != "" {
				fr := chunk.FinishReason
				finishReason = &fr
			}

		case ir.ChunkTypeDelta:
			if chunk.FinishReason != "" {
				fr := chunk.FinishReason
				finishReason = &fr
			}

			var baseCheck struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(data, &baseCheck); err == nil {
				switch baseCheck.Type {
				case "content_block_start":
					var evt anthropicBridgeContentBlockStart
					if err := json.Unmarshal(data, &evt); err == nil && evt.ContentBlock.Type == "tool_use" {
						currentToolCallID = evt.ContentBlock.ID
						// 2026-08-29: Register tool_use with validator
						toolCallValidator.OnToolUse(evt.ContentBlock.ID, evt.ContentBlock.Name, evt.Index)
						if len(evt.ContentBlock.InputRaw) > 0 && string(evt.ContentBlock.InputRaw) != "{}" {
							args := string(evt.ContentBlock.InputRaw)
							writeChunk(buildAnthropicBridgeToolCallChunk(toolCallIndex, evt.ContentBlock.ID, evt.ContentBlock.Name, &args, true))
							toolCallIndex++
							initialArgsSent = true
						} else {
							writeChunk(buildAnthropicBridgeToolCallChunk(toolCallIndex, evt.ContentBlock.ID, evt.ContentBlock.Name, nil, false))
							toolCallIndex++
							initialArgsSent = false
						}
						hasEmittedToolCalls = true
					} else if err == nil && evt.ContentBlock.Type == "tool_result" {
						// 2026-08-29: Register tool_result with validator
						var toolResultEvt struct {
							Type         string `json:"type"`
							Index        int    `json:"index"`
							ContentBlock struct {
								Type      string `json:"type"`
								ToolUseID string `json:"tool_use_id"`
							} `json:"content_block"`
						}
						if err := json.Unmarshal(data, &toolResultEvt); err == nil {
							toolCallValidator.OnToolResult(toolResultEvt.ContentBlock.ToolUseID)
						}
					} else if err == nil && evt.ContentBlock.Type == "thinking" && capture != nil {
						// 2026-07-27 并发修复：走带锁 setter（audit.StreamCapture.SetHasThinking）。
						capture.SetHasThinking()
					}

				case "content_block_delta":
					var evt struct {
						Index int `json:"index"`
						Delta struct {
							Type        string `json:"type"`
							Text        string `json:"text"`
							Thinking    string `json:"thinking"`
							PartialJSON string `json:"partial_json"`
							Signature   string `json:"signature"`
						} `json:"delta"`
					}
					if err := json.Unmarshal(data, &evt); err == nil {
						switch evt.Delta.Type {
						case "text", "text_delta":
							bufferedText.WriteString(evt.Delta.Text)
						case "thinking", "thinking_delta":
							writeChunk(&ir.StreamChunk{
								Type:           ir.ChunkTypeDelta,
								Delta:          &ir.StreamDelta{ReasoningContent: evt.Delta.Thinking},
								SourceProtocol: ir.ProtocolAnthropicMessages,
							})
						case "input_json_delta":
							if !initialArgsSent && evt.Delta.PartialJSON != "" {
								bufferedToolArgs.WriteString(evt.Delta.PartialJSON)
							}
						case "signature_delta":
							if evt.Delta.Signature != "" {
								thinkingSignatures[evt.Index] = evt.Delta.Signature
								if capture != nil {
									// The token is opaque and must not be logged. A digest
									// makes presence/identity observable without exposing it.
									digest := sha256.Sum256([]byte(evt.Delta.Signature))
									capture.AddQualityFlag("anthropic_signature_delta:" + hex.EncodeToString(digest[:8]))
								}
								// signature_delta is a thinking-block terminal marker; the
								// stream delivered semantic structure even if the wire bytes
								// were opaque to OpenAI. Count it as content emission so
								// the empty-response detector does not fire on a thinking-only
								// turn.
								emittedContent = true
							}
						default:
							slog.Warn("unknown_delta_type_in_stream",
								"delta_type", evt.Delta.Type,
								"has_text", evt.Delta.Text != "",
								"has_thinking", evt.Delta.Thinking != "",
								"request_id", requestID)
							if evt.Delta.Text != "" {
								bufferedText.WriteString(evt.Delta.Text)
							} else if evt.Delta.Thinking != "" {
								writeChunk(&ir.StreamChunk{
									Type:           ir.ChunkTypeDelta,
									Delta:          &ir.StreamDelta{ReasoningContent: evt.Delta.Thinking},
									SourceProtocol: ir.ProtocolAnthropicMessages,
								})
							}
						}
					}

				case "content_block_stop":
					flushBufferedText()
					if !initialArgsSent && bufferedToolArgs.Len() > 0 {
						args := bufferedToolArgs.String()
						chunk.AnnotateArgumentsJSON(args)
						if chunk.Quality != "verified" {
							slog.Warn("anthropic_to_openai: streaming tool arguments quality",
								"request_id", requestID,
								"quality", chunk.Quality,
								"reason", chunk.ArgumentsJSONReason)
						}
						validated, _, vErr := anthropictransform.ValidateStreamingToolArgs(args)
						if vErr != nil {
							if capture != nil {
								capture.AddQualityFlag("malformed_tool_args_blocked")
							}
							if attemptHasClientSemanticOutput(gate, chunkCount) {
								emitAnthropicBridgeErrorChunk(w, "malformed_tool_args", "upstream tool arguments are invalid JSON", flusher)
							}
							outcome.Interrupted = true
							outcome.Reason = "malformed_tool_args"
							outcome.Kind = errorsx.KindUpstreamDown
							outcome.Resumable = !attemptHasClientSemanticOutput(gate, chunkCount)
							outcome.ChunkCount = chunkCount
							return outcome
						}
						writeChunk(buildAnthropicBridgeToolCallChunk(toolCallIndex-1, currentToolCallID, "", &validated, true))
						bufferedToolArgs.Reset()
					}
					currentToolCallID = ""
					initialArgsSent = false

				case "message_start", "message_delta":
				}

			}

			case ir.ChunkTypeDone:
				messageStopReceived = true
				flushBufferedText()
				
				// 2026-08-29: Validate tool call completeness before marking stream done
				if err := toolCallValidator.ValidateComplete(); err != nil {
					slog.Warn("anthropic_to_openai: incomplete tool call detected",
						"request_id", requestID,
						"error", err.Error(),
						"pending_count", toolCallValidator.PendingCount(),
						"client_model", clientModel,
					)
					if capture != nil {
						capture.MarkInterruptedWithReason("incomplete_tool_call")
					}
					kind, resumable, reason := classifyIncompleteToolCall(true)
					metrics.Global().RecordIncompleteToolCall(clientModel, reason)
					return StreamOutcome{
						Interrupted: true,
						Reason:      reason,
						Kind:        kind,
						Resumable:   resumable,
						ChunkCount:  chunkCount,
					}
				}
				
				// Empty-response detection (audit-24h-20260828-r4 parity):
				// surface Anthropic-compat streams that close cleanly but emit
				// zero semantic bytes. The non-stream detector
				// (executor_anthropic.go) already returns KindEmptyResponse for
				// the parallel case; the live Q3 translator now mirrors it.
				//
				// Per the documented contract on anthropic.IsAnthropicStreamEmpty,
				// an upstream that reports usage in message_start but no content
				// IS empty (not just absence of usage). The r3 transformation
				// path's stricter `inputTokens==0 && outputTokens==0` requirement
				// was a regression — restored here so Q3 / Q-E / non-stream all
				// agree on the same shape.
				//
				// hasPendingReplay (pc != nil) short-circuits the interrupt: the
				// caller wired a pending replay buffer for client-disconnect
				// recovery and MUST see a completed body even on empty streams.
				// The downstream executor (executor_anthropic.go) decides whether
				// to re-attempt or surface the empty body. Without this guard
				// every pc-equipped empty fixture (e.g.
				// TestStreamAnthropicSSEToOpenAI_DisconnectsKeepsCapturer) was
				// wrongly marked as empty_response and the [DONE] chunk was
				// never written to the capturer.
				if anthropictransform.IsAnthropicStreamEmpty(emittedContent, inputTokens, outputTokens, pc != nil) {
					if capture != nil {
						capture.MarkInterruptedWithReason("anthropic_empty_response")
					}
					return StreamOutcome{Interrupted: true, Reason: "anthropic_empty_response", Kind: errorsx.KindEmptyResponse, Resumable: true, ChunkCount: chunkCount}
				}
			if finishReason != nil && *finishReason == "tool_calls" && !hasEmittedToolCalls {
				slog.Warn("inconsistent_tool_calls_finish_reason",
					"request_id", requestID,
					"model", clientModel,
					"prompt_tokens", inputTokens,
					"completion_tokens", outputTokens,
					"action", "correcting_to_stop",
					"original_finish_reason", "tool_calls")
				stop := "stop"
				finishReason = &stop
			}

			fr := "stop"
			if finishReason != nil {
				fr = *finishReason
			}
			writeChunk(&ir.StreamChunk{
				Type:           ir.ChunkTypeDelta,
				Delta:          &ir.StreamDelta{},
				FinishReason:   fr,
				SourceProtocol: ir.ProtocolAnthropicMessages,
			})
			if inputTokens > 0 || outputTokens > 0 {
				writeChunk(&ir.StreamChunk{
					Type: ir.ChunkTypeUsage,
					Usage: &ir.StreamUsage{
						PromptTokens:     inputTokens,
						CompletionTokens: outputTokens,
						TotalTokens:      inputTokens + outputTokens,
					},
					FinishReason:   fr,
					SourceProtocol: ir.ProtocolAnthropicMessages,
				})
			}
			writeChunk(&ir.StreamChunk{Type: ir.ChunkTypeDone, SourceProtocol: ir.ProtocolAnthropicMessages})
			return StreamOutcome{ChunkCount: chunkCount}

		case ir.ChunkTypeError:
			if capture != nil {
				capture.MarkInterruptedWithReason("upstream_error")
			}
			errType := ""
			errMsg := ""
			if chunk.Error != nil {
				errType = chunk.Error.Type
				errMsg = chunk.Error.Message
			}
			kind := classifyAnthropicStreamError(errType, data)
			// Buffered-but-unflushed text was never written via writeChunk, so
			// it is not client-visible: chunkCount stays 0 and the gate is
			// uncommitted. Do NOT flush it here — flushing would commit an
			// otherwise-transient interruption, flipping Resumable to false and
			// duplicating the text on retry. The EOF path flushes because the
			// upstream terminated without a hard error; a hard error means the
			// buffered prefix belongs to a failed attempt and is discarded.
			if attemptHasClientSemanticOutput(gate, chunkCount) {
				// Relay upstreams pack internal diagnostics (provider UUIDs,
				// request IDs, multi-line "Turn execution failed / reason=…"
				// blobs) into the error message. Never forward that text: emit
				// the gateway's own sanitized envelope carrying only the error
				// type. The raw payload stays in the audit capture above.
				code := errType
				if code == "" {
					code = string(kind)
					if code == "" {
						code = "api_error"
					}
				}
				emitAnthropicBridgeErrorChunk(w, code, "upstream stream error: "+code, flusher)
			}
			slog.Warn("anthropic_to_openai: upstream terminal error event",
				"request_id", requestID,
				"client_model", clientModel,
				"outbound_model", outboundModel,
				"error_type", errType,
				"kind", string(kind),
				"client_visible_chunks", chunkCount,
				// Raw payload stays in the audit capture; slog must treat the
				// relay blob as opaque (it embeds provider UUIDs/request IDs).
				// Log the first line only — it carries the vendor's headline
				// without the internal diagnostics.
				"error_headline", truncateForLog(firstLineOfLogSafe(errMsg), 120),
			)
			outcome.Interrupted = true
			outcome.Reason = "upstream_error"
			outcome.Kind = kind
			outcome.Resumable = !attemptHasClientSemanticOutput(gate, chunkCount)
			outcome.ChunkCount = chunkCount
			return outcome
		}

		// Incremental integrity breach (repeated-content loop): cut the
		// stream so the executor can failover. Mirrors stream.go.
		if capture != nil && capture.IntegrityBreached() {
			return integrityBreachOutcome(capture, chunkCount)
		}
	}
}

type anthropicBridgeContentBlockStart struct {
	Type         string `json:"type"`
	Index        int    `json:"index"`
	ContentBlock struct {
		Type     string          `json:"type"`
		ID       string          `json:"id"`
		Name     string          `json:"name"`
		InputRaw json.RawMessage `json:"input"`
	} `json:"content_block"`
}

func buildAnthropicBridgeToolCallChunk(index int, id, name string, args *string, hasArgs bool) *ir.StreamChunk {
	tc := ir.StreamToolCallDelta{
		Index: index,
		ID:    id,
		Type:  "function",
		Name:  name,
	}
	if hasArgs && args != nil {
		tc.Arguments = *args
	}
	return &ir.StreamChunk{
		Type:           ir.ChunkTypeDelta,
		Delta:          &ir.StreamDelta{ToolCalls: []ir.StreamToolCallDelta{tc}},
		SourceProtocol: ir.ProtocolAnthropicMessages,
	}
}

// firstLineOfLogSafe extracts the first line of a possibly multi-line
// upstream diagnostic for log fields. Relay blobs put structured internals
// (provider=, request=, reason=…) on subsequent lines; the term "safe" here
// means "structurally incapable of carrying the relay kv pairs", not
// sanitization of arbitrary PII.
func firstLineOfLogSafe(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func emitAnthropicBridgeErrorChunk(w http.ResponseWriter, code, message string, flusher http.Flusher) {
	errBody := map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	}
	body, _ := json.Marshal(errBody)
	_, _ = w.Write([]byte("data: "))
	_, _ = w.Write(body)
	_, _ = w.Write([]byte("\n\n"))
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
	flusher.Flush()
}

// ConvertChatRequestToAnthropic is the live re-export of the Q2
// OpenAI→Anthropic request body converter used by the executor when
// an OpenAI-completions client must be routed to an
// anthropic-messages upstream.
//
// Fix (2026-07-02): Replaced simplified version with full message
// structure conversion to correctly handle multi-turn conversations,
// tool_calls, and multimodal content. The previous implementation
// dropped tool_calls and other complex message structures, causing
// Claude Sonnet 4-6 to lose conversation context.
func ConvertChatRequestToAnthropic(in []byte) ([]byte, error) {
	return anthropictransform.ConvertChatRequestToAnthropic(in)
}

// ConvertAnthropicResponseToChat converts an Anthropic Messages
// response (non-stream) into OpenAI Chat Completions response.
// Used for Q3 (openai client <- anthropic upstream).
//
// Enhanced (2026-06-20): thinking blocks are preserved in the
// reasoning_content field (OpenAI o1-style extended thinking support).
func ConvertAnthropicResponseToChat(in []byte, clientModel string) ([]byte, error) {
	var src struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Role    string `json:"role"`
		Model   string `json:"model"`
		Content []struct {
			Type      string         `json:"type"`
			Text      string         `json:"text"`
			ID        string         `json:"id"`
			Name      string         `json:"name"`
			Input     map[string]any `json:"input"`
			Thinking  string         `json:"thinking"`
			Signature string         `json:"signature"`
		} `json:"content"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
		StopReason string `json:"stop_reason"`
	}
	if err := json.Unmarshal(in, &src); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	outModel := src.Model
	if clientModel != "" {
		outModel = clientModel
	}
	var textParts []string
	var toolCalls []map[string]any
	var thinkingParts []string
	thinkingBlocks := 0
	for _, c := range src.Content {
		switch c.Type {
		case "text":
			if c.Text != "" {
				textParts = append(textParts, c.Text)
			}
		case "tool_use":
			argsJSON, err := json.Marshal(c.Input)
			if err != nil {
				slog.Warn("tool_use_marshal_failed",
					"error", err,
					"tool_use_id", c.ID,
					"tool_name", c.Name,
					"model", src.Model,
					"message_id", src.ID)
				continue
			}
			toolCalls = append(toolCalls, map[string]any{
				"id":   c.ID,
				"type": "function",
				"function": map[string]any{
					"name":      c.Name,
					"arguments": string(argsJSON),
				},
			})
		case "thinking":
			thinkingBlocks++
			if c.Thinking != "" {
				thinkingParts = append(thinkingParts, c.Thinking)
			}
		default:
			if c.Text != "" {
				textParts = append(textParts, c.Text)
			} else if c.Thinking != "" {
				thinkingParts = append(thinkingParts, c.Thinking)
			}
		}
	}
	msg := map[string]any{"role": "assistant"}
	if len(textParts) > 0 {
		msg["content"] = joinTextParts(textParts)
	} else if len(toolCalls) > 0 {
		msg["content"] = nil
	} else {
		msg["content"] = ""
	}
	if len(thinkingParts) > 0 {
		msg["reasoning_content"] = joinTextParts(thinkingParts)
	}
	if len(toolCalls) > 0 {
		msg["tool_calls"] = toolCalls
	}
	if len(textParts) == 0 && len(toolCalls) == 0 && len(thinkingParts) == 0 {
		return nil, fmt.Errorf("empty response from model %s: %d content blocks produced no extractable text/tool/thinking content",
			src.Model, len(src.Content))
	}
	finishReason := mapAnthropicFinishReasonToChat(src.StopReason)
	totalTokens := src.Usage.InputTokens + src.Usage.OutputTokens
	out := map[string]any{
		"id":      src.ID,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   outModel,
		"choices": []map[string]any{{
			"index":         0,
			"message":       msg,
			"finish_reason": finishReason,
		}},
		"usage": map[string]any{
			"prompt_tokens":     src.Usage.InputTokens,
			"completion_tokens": src.Usage.OutputTokens,
			"total_tokens":      totalTokens,
		},
	}
	if thinkingBlocks > 0 {
		reasoningContent, _ := msg["reasoning_content"].(string)
		out["_kxg_meta"] = map[string]any{
			"has_thinking":            true,
			"thinking_blocks_count":   thinkingBlocks,
			"reasoning_content_chars": len(reasoningContent),
		}
	}
	result, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("marshal chat response: %w", err)
	}
	return result, nil
}

func joinTextParts(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += "\n"
		}
		out += p
	}
	return out
}

func mapAnthropicFinishReasonToChat(reason string) string {
	switch reason {
	case "end_turn":
		return "stop"
	case "tool_use":
		return "tool_calls"
	case "max_tokens":
		return "length"
	case "stop_sequence":
		return "stop"
	case "refusal":
		return "content_filter"
	default:
		return "stop"
	}
}

// convertBridgeChatMessageToAnthropic converts a single OpenAI message to Anthropic format.
// Handles text content, multimodal content, tool_calls, and tool results.
func convertBridgeChatMessageToAnthropic(msg map[string]any) map[string]any {
	role, _ := msg["role"].(string)
	out := map[string]any{"role": role}
	content := msg["content"]
	switch typed := content.(type) {
	case string:
		out["content"] = typed
	case []any:
		blocks := make([]any, 0, len(typed))
		for _, block := range typed {
			blockMap, _ := block.(map[string]any)
			switch blockMap["type"] {
			case "text":
				blocks = append(blocks, map[string]any{"type": "text", "text": blockMap["text"]})
			case "image_url":
				if imageURL, ok := blockMap["image_url"].(map[string]any); ok {
					if url, ok := imageURL["url"].(string); ok {
						source := map[string]any{"type": "url", "url": url}
						if mediaType, data, ok := parseBridgeImageDataURI(url); ok {
							source = map[string]any{
								"type":       "base64",
								"media_type": mediaType,
								"data":       data,
							}
						}
						blocks = append(blocks, map[string]any{
							"type":   "image",
							"source": source,
						})
					}
				}
			default:
				// 2026-07-23 auto-fix (Responses/Anthropic content audit):
				// OpenAI Responses API uses type prefix "input_*" (input_text,
				// input_image, ...). These land here when a Chat Completions
				// client slips a Responses-shaped block through /v1/messages.
				// Auto-normalize instead of dropping.
				if normalized, fromType, ok := normalizeOpenAIResponsesBlock(blockMap); ok {
					recordAutoNormalize("anthropic_bridge", fromType, role)
					blocks = append(blocks, normalized)
					continue
				}
				// Truly unknown or attachment-specific block type: preserve it
				// rather than silently dropping user-provided data.
				blocks = append(blocks, blockMap)
			}
		}
		out["content"] = blocks
	}
	if role == "tool" {
		if toolCallID, ok := msg["tool_call_id"].(string); ok {
			toolContent, _ := msg["content"].(string)
			out = map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type":        "tool_result",
						"tool_use_id": toolCallID,
						"content":     toolContent,
					},
				},
			}
		}
		return out
	}
	if toolCalls, ok := msg["tool_calls"].([]any); ok {
		var existing []any
		switch current := out["content"].(type) {
		case []any:
			existing = current
		default:
			existing = []any{}
		}
		for _, toolCall := range toolCalls {
			toolCallMap, _ := toolCall.(map[string]any)
			function, _ := toolCallMap["function"].(map[string]any)
			argsStr, _ := function["arguments"].(string)
			var args any
			if json.Unmarshal([]byte(argsStr), &args) != nil {
				args = map[string]any{}
			}
			existing = append(existing, map[string]any{
				"type":  "tool_use",
				"id":    toolCallMap["id"],
				"name":  function["name"],
				"input": args,
			})
		}
		out["content"] = existing
	}
	return out
}

func parseBridgeImageDataURI(value string) (mediaType, data string, ok bool) {
	if !strings.HasPrefix(value, "data:") {
		return "", "", false
	}
	comma := strings.IndexByte(value, ',')
	if comma <= len("data:") {
		return "", "", false
	}
	meta := strings.TrimPrefix(value[:comma], "data:")
	parts := strings.Split(meta, ";")
	if len(parts) < 2 || parts[len(parts)-1] != "base64" || parts[0] == "" {
		return "", "", false
	}
	return parts[0], value[comma+1:], true
}

// convertBridgeChatToolChoiceToAnthropic converts OpenAI tool_choice to Anthropic format.
func convertBridgeChatToolChoiceToAnthropic(toolChoice any) any {
	switch typed := toolChoice.(type) {
	case string:
		switch typed {
		case "auto":
			return map[string]any{"type": "auto"}
		case "none":
			return map[string]any{"type": "none"}
		case "required":
			return map[string]any{"type": "any"}
		}
	case map[string]any:
		if typed["type"] == "function" {
			if function, ok := typed["function"].(map[string]any); ok {
				if name, ok := function["name"].(string); ok {
					return map[string]any{"type": "tool", "name": name}
				}
			}
		}
	}
	return nil
}

// convertBridgeOpenAIToolToAnthropic converts OpenAI tool definition to Anthropic format.
func convertBridgeOpenAIToolToAnthropic(tool map[string]any) (map[string]any, bool) {
	normalized := normalizeBridgeOpenAIToolDefinitions([]any{tool})
	if len(normalized) != 1 {
		return nil, false
	}
	toolMap, ok := normalized[0].(map[string]any)
	if !ok {
		return nil, false
	}
	function, _ := toolMap["function"].(map[string]any)
	if function == nil {
		return nil, false
	}
	name, _ := function["name"].(string)
	if name == "" {
		return nil, false
	}
	anthropicTool := map[string]any{"name": name}
	if description, ok := function["description"].(string); ok && description != "" {
		anthropicTool["description"] = description
	}
	if parameters, ok := function["parameters"]; ok {
		anthropicTool["input_schema"] = parameters
	} else {
		anthropicTool["input_schema"] = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return anthropicTool, true
}

// normalizeBridgeOpenAIToolDefinitions normalizes tool definitions to standard format.
func normalizeBridgeOpenAIToolDefinitions(tools []any) []any {
	if len(tools) == 0 {
		return tools
	}
	out := make([]any, 0, len(tools))
	for _, item := range tools {
		tool, ok := item.(map[string]any)
		if !ok {
			out = append(out, item)
			continue
		}
		if function, ok := tool["function"].(map[string]any); ok {
			if name, _ := function["name"].(string); name != "" {
				out = append(out, map[string]any{
					"type":     "function",
					"function": function,
				})
				continue
			}
		}
		if name, _ := tool["name"].(string); name != "" {
			if schema, hasSchema := tool["input_schema"]; hasSchema {
				function := map[string]any{"name": name}
				if description, ok := tool["description"].(string); ok && description != "" {
					function["description"] = description
				}
				if schema != nil {
					function["parameters"] = schema
				}
				out = append(out, map[string]any{"type": "function", "function": function})
				continue
			}
			if _, hasParams := tool["parameters"]; hasParams || tool["type"] == "function" {
				function := map[string]any{"name": name}
				if description, ok := tool["description"].(string); ok && description != "" {
					function["description"] = description
				}
				if parameters, ok := tool["parameters"]; ok {
					function["parameters"] = parameters
				} else {
					function["parameters"] = map[string]any{}
				}
				out = append(out, map[string]any{"type": "function", "function": function})
				continue
			}
		}
		out = append(out, tool)
	}
	return out
}
