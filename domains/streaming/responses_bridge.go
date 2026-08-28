package streaming

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/transformation/anthropic"
	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

// Phase E (2026-07-01): Responses API SSE bridges. Two orchestrators that
// read upstream SSE in their native wire format, lift chunks into the IR
// StreamChunk superset via the IR parsers, then emit Responses API SSE
// via the IR Responses serializer.
//
// This replaces the hand-written chunk-shape translation previously
// living in domains/streaming/responses_stream.go:StreamResponsesSSE,
// which only handled the OpenAI→Responses case and routed through
// params.StreamWrapper (silently bypassed when upstream was
// anthropic-messages). The new bridges cover all four upstream↔client
// direction pairs in the IR matrix:
//
//	Anthropic SSE ──→ IR ──→ Responses API SSE  (this file)
//	OpenAI SSE    ──→ IR ──→ Responses API SSE  (this file)
//
// Initial/final event scaffolding (response.created,
// response.output_item.added, response.content_part.added,
// response.output_text.done, response.output_item.done,
// response.completed) is owned by these orchestrators; the IR
// SerializeResponses method emits per-chunk events only.

// responsesScaffold holds the IDs and writer state shared by both
// orchestrators. Created once per stream, used until response.completed.
const (
	// These limits keep a malformed or unusually verbose upstream stream from
	// retaining unbounded data while the bridge waits for its terminal event.
	maxResponsesBridgeTextBytes          = 4 * 1024 * 1024
	maxResponsesBridgeToolArgumentsBytes = 1 * 1024 * 1024
)

type responsesToolTerminalState struct {
	ID        string
	Name      string
	Arguments strings.Builder
}

type responsesScaffold struct {
	w           http.ResponseWriter
	flusher     http.Flusher
	requestID   string
	clientModel string

	respID  string // "resp_" + requestID-derived suffix
	msgID   string // "msg_" + requestID-derived suffix
	created int64  // unix timestamp

	// Terminal state is kept separately from wire emission so reasoning and
	// tool-call streams are represented in the final Responses envelope too.
	reasoningText strings.Builder
	toolStates    map[int]*responsesToolTerminalState

	// pc is the optional pending capturer so initial/final envelope events
	// are still recorded when the client has already gone away (Track C C5).
	pc *pendingCapturer
	// clientWriter latches the disconnected state so the scaffold stops
	// hitting a closed client connection while still appending to pc.
	clientWriter *clientStreamWriter
}

// newResponsesScaffold derives the deterministic response/msg IDs from the
// request ID, mirroring the convention previously in StreamResponsesSSE.
// Keeping IDs stable means client-side dedup / replay tokens continue to
// work across the IR bridge rollout.
func newResponsesScaffold(w http.ResponseWriter, flusher http.Flusher, requestID, clientModel string) *responsesScaffold {
	respID := "resp_"
	msgID := "msg_"
	if len(requestID) > 24 {
		respID += requestID[:24]
		msgID += requestID[8:24]
	} else if requestID != "" {
		respID += requestID
		msgID += requestID
	} else {
		respID += "no_request_id"
		msgID += "no_request_id"
	}
	return &responsesScaffold{
		w:           w,
		flusher:     flusher,
		requestID:   requestID,
		clientModel: clientModel,
		respID:      respID,
		msgID:       msgID,
		created:     time.Now().Unix(),
		toolStates:  make(map[int]*responsesToolTerminalState),
	}

}

func (s *responsesScaffold) attachCapturer(pc *pendingCapturer, cw *clientStreamWriter) {
	s.pc = pc
	s.clientWriter = cw
}

// appendResponsesBounded appends only while the bridge's terminal-state
// accumulator remains within its local bound. The caller turns false into a
// conversion outcome; it must never silently emit a truncated terminal value.
func appendResponsesBounded(builder *strings.Builder, value string, limit int) bool {
	if builder == nil || len(value) > limit-builder.Len() {
		return false
	}
	builder.WriteString(value)
	return true
}

// responsesDeltaFits checks accumulator capacity before the corresponding
// Responses SSE delta is written. This prevents a client-visible delta from
// being omitted from the terminal response after an overflow is detected.
func responsesDeltaFits(scaffold *responsesScaffold, fullText *strings.Builder, chunk *ir.StreamChunk) bool {
	if scaffold == nil || fullText == nil || chunk == nil || chunk.Delta == nil {
		return true
	}
	delta := chunk.Delta
	if len(delta.Content) > maxResponsesBridgeTextBytes-fullText.Len() ||
		len(delta.ReasoningContent) > maxResponsesBridgeTextBytes-scaffold.reasoningText.Len() {
		return false
	}
	for _, tc := range delta.ToolCalls {
		state := scaffold.toolStates[tc.Index]
		used := 0
		if state != nil {
			used = state.Arguments.Len()
		}
		if len(tc.Arguments) > maxResponsesBridgeToolArgumentsBytes-used {
			return false
		}
	}
	return true
}

// writeSSEEvent writes a single Responses API SSE event. When pc is
// attached, the same bytes are appended to the capturer so the envelope
// survives a client disconnect.
func (s *responsesScaffold) writeSSEEvent(event string, payload any) bool {
	data, err := json.Marshal(payload)
	if err != nil {
		return false
	}
	line := fmt.Sprintf("event: %s\ndata: %s\n\n", event, data)
	ok := true
	if s.clientWriter != nil {
		ok = s.clientWriter.write(line)
	} else {
		ok = safeWriteSSE(s.w, line)
	}
	if s.pc != nil {
		s.pc.append(line)
	}
	return ok
}

// writeInitialEvents emits the Responses API opening sequence so SDK
// clients see a well-formed response envelope from the first event.
func (s *responsesScaffold) writeInitialEvents() {
	s.writeSSEEvent("response.created", map[string]any{
		"type": "response.created", "response": map[string]any{
			"id": s.respID, "object": "response", "created_at": s.created,
			"model": s.clientModel, "status": "in_progress", "output": []any{},
		},
	})
	s.writeSSEEvent("response.output_item.added", map[string]any{
		"type": "response.output_item.added", "output_index": 0,
		"item": map[string]any{"type": "message", "id": s.msgID, "status": "in_progress", "role": "assistant", "content": []any{}},
	})
	s.writeSSEEvent("response.content_part.added", map[string]any{
		"type": "response.content_part.added", "item_id": s.msgID, "output_index": 0,
		"content_index": 0, "part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}},
	})
}

// finishAttempt writes the terminal Responses events unless the attempt
// commit gate still holds an uncommitted attempt (survival deferred mode) —
// in that case the coordinator owns the final protocol rendering and the
// bridge must return a structured outcome only (doc 18 §9.3).
func (s *responsesScaffold) finishAttempt(gate *AttemptCommitGate, fullText, finishReason string, inputTokens, outputTokens, totalTokens int) {
	if !gate.MayWriteTerminal() {
		// A detached client may leave a buffered gate uncommitted even though the
		// upstream completed normally. The pending capturer still needs a complete
		// replay body; clientStreamWriter suppresses the dead network write.
		if s.pc == nil || s.clientWriter == nil || !s.clientWriter.clientDisconnected {
			return
		}
	}
	s.writeFinalEvents(fullText, finishReason, inputTokens, outputTokens, totalTokens)
}

func (s *responsesScaffold) finishInterrupted(gate *AttemptCommitGate, fullText, reason string, inputTokens, outputTokens int) {
	if !gate.MayWriteTerminal() {
		return
	}
	s.writeFinalEvents(fullText, "length", inputTokens, outputTokens, inputTokens+outputTokens)
}

// writeFinalEvents emits response.output_text.done, response.output_item.done,
// and response.completed with aggregated usage. fullText is the
// accumulated visible text from all delta chunks. finishReason is the
// raw OpenAI-form value ("stop" | "length" | "tool_calls" | ""); status
// is the Responses API form ("completed" | "incomplete").
func (s *responsesScaffold) writeFinalEvents(fullText, finishReason string, inputTokens, outputTokens, totalTokens int) {
	status := "completed"
	if finishReason == "length" {
		status = "incomplete"
	}

	// Close every semantic stream explicitly. A tool-only (or mixed) response
	// must not be represented as a message-only response in the terminal
	// envelope. Keep the legacy message item for text/reasoning streams.
	if s.reasoningText.Len() > 0 {
		s.writeSSEEvent("response.reasoning_text.done", map[string]any{
			"type": "response.reasoning_text.done", "item_id": s.msgID,
			"output_index": 0, "content_index": 0, "text": s.reasoningText.String(),
		})
	}
	indices := make([]int, 0, len(s.toolStates))
	for index := range s.toolStates {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for _, index := range indices {
		state := s.toolStates[index]
		if state == nil {
			continue
		}
		item := map[string]any{
			"type": "function_call", "id": state.ID, "call_id": state.ID,
			"name": state.Name, "arguments": state.Arguments.String(), "status": status,
		}
		s.writeSSEEvent("response.function_call_arguments.done", map[string]any{
			"type": "response.function_call_arguments.done", "item_id": state.ID,
			"output_index": index, "call_id": state.ID, "name": state.Name,
			"arguments": state.Arguments.String(),
		})
		s.writeSSEEvent("response.output_item.done", map[string]any{
			"type": "response.output_item.done", "output_index": index, "item": item,
		})
	}

	// Preserve the historical message terminal events for ordinary and mixed
	// streams, but omit the synthetic message for a tool-only response.
	hasMessage := len(indices) == 0 || fullText != "" || s.reasoningText.Len() > 0
	if hasMessage {
		s.writeSSEEvent("response.output_text.done", map[string]any{
			"type": "response.output_text.done", "item_id": s.msgID,
			"output_index": 0, "content_index": 0, "text": fullText,
		})
		s.writeSSEEvent("response.output_item.done", map[string]any{
			"type": "response.output_item.done", "output_index": 0,
			"item": map[string]any{
				"type": "message", "id": s.msgID, "status": status,
				"role": "assistant", "content": []map[string]any{
					{"type": "output_text", "text": fullText, "annotations": []any{}},
				},
			},
		})
	}

	output := make([]map[string]any, 0, len(indices)+1)
	for _, index := range indices {
		state := s.toolStates[index]
		if state == nil {
			continue
		}
		output = append(output, map[string]any{
			"type": "function_call", "id": state.ID, "call_id": state.ID,
			"name": state.Name, "arguments": state.Arguments.String(), "status": status,
		})
	}
	if hasMessage {
		output = append(output, map[string]any{
			"type": "message", "id": s.msgID, "status": status, "role": "assistant",
			"content": []map[string]any{{"type": "output_text", "text": fullText, "annotations": []any{}}},
		})
	}
	completed := map[string]any{
		"type": "response.completed", "response": map[string]any{
			"id": s.respID, "object": "response", "created_at": s.created,
			"model": s.clientModel, "status": status, "output": output,
			"usage": map[string]any{"input_tokens": inputTokens, "output_tokens": outputTokens, "total_tokens": totalTokens},
		},
	}
	s.writeSSEEvent("response.completed", completed)
}

// StreamAnthropicSSEToResponses reads Anthropic SSE upstream and writes
// OpenAI Responses API SSE to the client. Mirrors the architecture of
// StreamAnthropicSSEToOpenAI (anthropic_bridge.go:201) but emits
// `event: response.output_text.delta` instead of `data: {...choices...}`.
//
// Wire invariants:
//   - Never forward raw `event: message_start` or
//     `event: content_block_delta` payloads — Responses API clients
//     validate each event against the Responses schema.
//   - The opening `response.created` / `response.output_item.added` /
//     `response.content_part.added` sequence must precede the first
//     delta; the closing `response.output_text.done` /
//     `response.output_item.done` / `response.completed` sequence must
//     follow the last delta.
//   - Accumulated usage flows into `response.completed.usage` — never
//     emitted as a standalone event.
//
// P1-2 fix (2026-08-28): Added ctx parameter for context propagation to gate.
func StreamAnthropicSSEToResponses(
	ctx context.Context,
	w http.ResponseWriter,
	resp *http.Response,
	clientModel, outboundModel, requestID string,
	capture *audit.StreamCapture,
	pc *pendingCapturer,
) (outcome StreamOutcome) {
	return StreamAnthropicSSEToResponsesWithDiagnostics(
		ctx, w, resp, clientModel, outboundModel, requestID, capture, pc, nil,
	)
}

// StreamAnthropicSSEToResponsesWithDiagnostics converts an Anthropic stream
// to Responses SSE with optional best-effort diagnostics.
// P1-2 fix (2026-08-28): Added ctx parameter for context propagation to gate.
func StreamAnthropicSSEToResponsesWithDiagnostics(
	ctx context.Context,
	w http.ResponseWriter,
	resp *http.Response,
	clientModel, outboundModel, requestID string,
	capture *audit.StreamCapture,
	pc *pendingCapturer,
	diagnostics *DiagnosticContext,
) (outcome StreamOutcome) {
	// Declare the gate before panic recovery so a panic after client-visible
	// semantic output can never be classified as transparently resumable.
	var gate *AttemptCommitGate
	//nolint:errcheck // best-effort close
	defer resp.Body.Close()
	defer func() {
		if r := recover(); r != nil {
			slog.Error("anthropic-to-responses stream panic recovered",
				"panic", r, "stack", string(debug.Stack()), "request_id", requestID)
			if capture != nil {
				capture.MarkInterruptedWithReason("stream_panic")
			}
			outcome.Interrupted = true
			outcome.Reason = "stream_panic"
			outcome.Kind = errorsx.KindUpstreamDown
			outcome.Resumable = !attemptHasClientSemanticOutput(gate, 0)
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
		diagnosticCollector.report(diagnostics, requestID, "anthropic-messages", "openai-responses", outcome.Interrupted)
	}()

	// SR-W1: route client frames through the attempt commit gate.
	// Disabled (default) this is the identity function — legacy wire bytes.
	// P1-2 fix (2026-08-28): Pass context to gate for checkpoint propagation.
	w, gate = wrapAttemptWriter(ctx, w, ProtocolOpenAIResponses)
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
		return StreamOutcome{Interrupted: true, Reason: "client_write_failed", Kind: errorsx.KindCanceled, Resumable: false}
		// Initial flush write failed — client disconnected before any frame left;
		// retry is pointless and would violate the no-replay contract.
	}

	if clientModel == "" {
		clientModel = outboundModel
	}

	clientWriter := newClientStreamWriter(w, flusher)
	defer func() {
		applyClientDisconnectOutcome(&outcome, clientWriter, !outcome.Interrupted)
	}()
	scaffold := newResponsesScaffold(w, flusher, requestID, clientModel)
	scaffold.attachCapturer(pc, clientWriter)
	scaffold.writeInitialEvents()
	if clientWriter.clientDisconnected {
		if capture != nil {
			capture.MarkInterruptedWithReason("client_write_failed")
		}
		if pc != nil {
			pc.markInterrupted("client_write_failed")
		}
		return StreamOutcome{Interrupted: true, Reason: "client_write_failed", Kind: errorsx.KindCanceled, Resumable: false}
	}

	// P1-2 fix (2026-08-28): ctx is now a function parameter, removed redundant declaration.

	runtimeCfg := currentStreamRuntimeConfig()
	reader := bufio.NewReaderSize(resp.Body, anthropicSSEBufSize)

	var (
		inputTokens  int
		outputTokens int
		fullText     strings.Builder
		finishReason string
		chunkCount   int
		// toolCallIDs maps Anthropic content_block index → tool_use id so
		// subsequent input_json_delta events (which carry only the
		// index + partial JSON, not the id) can be emitted with the
		// correct item_id. The IR StreamChunk from
		// ParseAnthropicStreamEvent sets tc.Index but leaves tc.ID empty
		// for input_json_delta, so the bridge maintains this lookup.
		toolCallIDs         = make(map[int]string)
		messageStopReceived bool
		conversionOverflow  bool
		clientWriteFailed   bool
		// emittedContent (audit-24h-20260828-r3 P1-B parity, Phase E):
		// tracks whether any client-visible semantic bytes — text,
		// thinking, tool-call deltas — reached the wire. Set true inside
		// writeChunkIR for content-bearing deltas; read at the two
		// clean-EOF return paths (lines ~460 and ~479) to decide
		// whether to surface KindEmptyResponse for fail-over, matching
		// the non-stream detector at executor_anthropic.go:1273 and the
		// passthrough detector at anthropic_passthrough_stream.go:147-178.
		emittedContent bool
	)

	// writeChunkIR serializes one IR StreamChunk via the Responses API
	// serializer, writes the SSE event(s) to the client, and updates
	// the audit capturer + chunk counter. Also fills in tc.ID for tool
	// call argument deltas using the bridge's toolCallIDs state
	// (Anthropic's input_json_delta events don't carry the tool_use
	// id — only the index — so the IR chunk has an empty ID).
	writeChunkIR := func(chunk *ir.StreamChunk) {
		if chunk == nil {
			return
		}
		if chunk.Type == ir.ChunkTypeDelta && chunk.Delta != nil {
			if !responsesDeltaFits(scaffold, &fullText, chunk) {
				conversionOverflow = true
				return
			}
			for i := range chunk.Delta.ToolCalls {
				tc := &chunk.Delta.ToolCalls[i]
				if tc.Name != "" && tc.ID != "" {
					toolCallIDs[tc.Index] = tc.ID
				}
				if tc.ID == "" {
					if id, ok := toolCallIDs[tc.Index]; ok {
						tc.ID = id
					}
				}
			}
		}
		sseLine := chunk.SerializeResponses(scaffold.msgID)
		if sseLine == "" {
			return
		}
		written := clientWriter.write(sseLine)
		if !written {
			// Keep consuming upstream and building the pending replay body, but
			// do not count this failed frame as client-visible output.
			clientWriteFailed = true
		}
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
			diagnosticCollector.observeEmittedChunk(chunk)
			chunkCount++
		}

		// Mark semantic emission: any non-empty text / thinking /
		// tool-call delta reaches the client as part of sseLine above.
		// Envelope / scaffold events (e.g. response.created) are NOT
		// semantic emission.
		if chunk.Type == ir.ChunkTypeDelta && chunk.Delta != nil {
			if chunk.Delta.Content != "" || chunk.Delta.ReasoningContent != "" {
				emittedContent = true
			}
			for _, tc := range chunk.Delta.ToolCalls {
				if tc.Arguments != "" || tc.Name != "" {
					emittedContent = true
					break
				}
			}
		}

		// Track terminal state with bounded accumulators; overflow is handled
		// by the orchestrator instead of emitting a silently truncated result.
		if chunk.Type == ir.ChunkTypeDelta && chunk.Delta != nil {
			if !appendResponsesBounded(&fullText, chunk.Delta.Content, maxResponsesBridgeTextBytes) ||
				!appendResponsesBounded(&scaffold.reasoningText, chunk.Delta.ReasoningContent, maxResponsesBridgeTextBytes) {
				conversionOverflow = true
				return
			}
			for _, tc := range chunk.Delta.ToolCalls {
				state := scaffold.toolStates[tc.Index]
				if state == nil {
					state = &responsesToolTerminalState{}
					scaffold.toolStates[tc.Index] = state
				}
				if tc.ID != "" {
					state.ID = tc.ID
				}
				if tc.Name != "" {
					state.Name = tc.Name
				}
				if !appendResponsesBounded(&state.Arguments, tc.Arguments, maxResponsesBridgeToolArgumentsBytes) {
					conversionOverflow = true
					return
				}
			}
		}
	}

	for {
		eventType, data, rawFrame, err := readAnthropicSSEEventWithTimeoutRaw(
			ctx, reader, resp.Body, runtimeCfg.streamChunkTimeout,
		)
		if errors.Is(err, context.DeadlineExceeded) {
			slog.Warn("anthropic_to_responses: chunk timeout",
				"timeout_seconds", runtimeCfg.streamChunkTimeout.Seconds(),
				"chunks_received", chunkCount,
				"request_id", requestID)
			if capture != nil {
				capture.MarkInterruptedWithReason("stream_chunk_timeout")
			}
			outcome = StreamOutcome{
				Interrupted: true,
				Reason:      "chunk_timeout",
				Kind:        errorsx.KindStreamTimeout,
				Resumable:   !attemptHasClientSemanticOutput(gate, chunkCount),
				ChunkCount:  chunkCount,
			}
			if !outcome.Resumable {
				scaffold.finishInterrupted(gate, fullText.String(), outcome.Reason, inputTokens, outputTokens)
			}

			if pc != nil {
				pc.markInterrupted(outcome.Reason)
			}
			return outcome
		}

		if err != nil {
			if errors.Is(err, io.EOF) {
				if !messageStopReceived {
					// 2026-08-23: same recovery as stream.go / anthropic_stream.go —
					// if a finish_reason has already been accumulated, treat
					// the missing message_stop as benign (some upstreams —
					// notably minimax via the Anthropic bridge — close the
					// stream right after the finish_reason chunk instead of
					// emitting a terminal event).
					if finishReason != "" {
						if anthropic.IsAnthropicStreamEmpty(emittedContent, inputTokens, outputTokens) {
							if capture != nil {
								capture.MarkInterruptedWithReason("anthropic_empty_response")
							}
							if pc != nil {
								pc.markInterrupted("anthropic_empty_response")
							}
							return StreamOutcome{Interrupted: true, Reason: "anthropic_empty_response", Kind: errorsx.KindEmptyResponse, Resumable: true, ChunkCount: chunkCount}
						}
						return StreamOutcome{ChunkCount: chunkCount}
					}
					outcome = StreamOutcome{
						Interrupted: true,
						Reason:      "eof_without_done",
						Kind:        errorsx.KindUpstreamDown,
						Resumable:   !attemptHasClientSemanticOutput(gate, chunkCount),
						ChunkCount:  chunkCount,
					}
					if !outcome.Resumable {
						scaffold.finishInterrupted(gate, fullText.String(), outcome.Reason, inputTokens, outputTokens)
					}

					if capture != nil {
						capture.MarkInterruptedWithReason(outcome.Reason)
					}
					return outcome
				}
				if anthropic.IsAnthropicStreamEmpty(emittedContent, inputTokens, outputTokens) {
					if capture != nil {
						capture.MarkInterruptedWithReason("anthropic_empty_response")
					}
					if pc != nil {
						pc.markInterrupted("anthropic_empty_response")
					}
					return StreamOutcome{Interrupted: true, Reason: "anthropic_empty_response", Kind: errorsx.KindEmptyResponse, Resumable: true, ChunkCount: chunkCount}
				}
				scaffold.finishAttempt(gate, fullText.String(), finishReason, inputTokens, outputTokens, inputTokens+outputTokens)
				// audit-24h-20260828-r3 P1-B parity (Phase E): empty-response
				// check at the normal message_stop terminal path. An
				// Anthropic stream that closed cleanly but emitted no
				// semantic bytes and no usage tokens fails over to the
				// next candidate instead of being recorded as a successful
				// empty stream.
				if anthropic.IsAnthropicStreamEmpty(emittedContent, inputTokens, outputTokens) {
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
				return StreamOutcome{ChunkCount: chunkCount}
			}
			failure := streamReadFailureOutcome(err, chunkCount)
			failure.Resumable = !attemptHasClientSemanticOutput(gate, chunkCount)
			if !failure.Resumable {
				scaffold.finishInterrupted(gate, fullText.String(), failure.Reason, inputTokens, outputTokens)
			}
			outcome = failure
			if capture != nil {
				capture.MarkInterruptedWithReason(failure.Reason)
			}
			return outcome
		}

		if eventType == "" || len(data) == 0 {
			continue
		}

		logRawUpstreamFrame(diagnostics, auditFromDiagnostics(diagnostics, requestID, "anthropic-messages"), rawFrame)
		diagnosticCollector.observeRaw(data)

		// Defensive: detect OpenAI-format data and skip (some proxies
		// mislabel). Same guard as StreamAnthropicSSEToOpenAI.
		if isOpenAIFormatData(data) {
			slog.Warn("anthropic_to_responses: detected OpenAI-format data, dropping",
				"event_type", eventType,
				"data_preview", truncateForLog(string(data), 100),
				"request_id", requestID)
			continue
		}

		chunk, perr := ir.ParseAnthropicStreamEvent(eventType, data)
		if perr != nil {
			slog.Warn("anthropic_to_responses: parse failed",
				"event_type", eventType,
				"error", perr,
				"request_id", requestID)

			reportConversionAnomaly(
				diagnostics, requestID, "anthropic-messages", "openai-responses", "parse_stream_event", data, perr,
				map[string]interface{}{"event_type": eventType},
			)
			continue
		}

		if chunk != nil {
			diagnosticCollector.observeChunk(chunk)
			if chunk.Type == ir.ChunkTypeError {
				outcome = StreamOutcome{
					Interrupted: true,
					Reason:      "upstream_error",
					Kind:        errorsx.KindUpstreamDown,
					Resumable:   !attemptHasClientSemanticOutput(gate, chunkCount),
					ChunkCount:  chunkCount,
				}
				if !outcome.Resumable {
					scaffold.finishInterrupted(gate, fullText.String(), outcome.Reason, inputTokens, outputTokens)
				}
				if capture != nil {
					capture.MarkInterruptedWithReason(outcome.Reason)
				}
				if pc != nil {
					pc.markInterrupted(outcome.Reason)
				}
				return outcome
			}
		}

		if eventType == "message_stop" {
			messageStopReceived = true
		}

		// Track usage + finish_reason as they arrive so the final

		// response.completed carries accurate metadata.
		//
		// FinishReason arrives in OpenAI form (the IR parser already
		// maps Anthropic stop_reason → OpenAI finish_reason via
		// internal/ir/stream.go:mapAnthropicFinishReasonToOpenAI).
		// We pass it straight through to writeFinalEvents which checks
		// for "length" / "content_filter" → "incomplete".
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
		}
		if chunk.FinishReason != "" {
			finishReason = chunk.FinishReason
		}

		writeChunkIR(chunk)
		if conversionOverflow {
			outcome = StreamOutcome{Interrupted: true, Reason: "responses_conversion_accumulator_limit", Kind: errorsx.KindConversion, Resumable: false, ChunkCount: chunkCount}
			if capture != nil {
				capture.MarkInterruptedWithReason(outcome.Reason)
			}
			scaffold.finishInterrupted(gate, fullText.String(), outcome.Reason, inputTokens, outputTokens)
			return outcome
		}

		// Incremental integrity breach (repeated-content loop): cut the
		// stream so the executor can failover. Mirrors stream.go.
		if capture != nil && capture.IntegrityBreached() {
			scaffold.finishAttempt(gate, fullText.String(), finishReason, inputTokens, outputTokens, inputTokens+outputTokens)
			return integrityBreachOutcome(capture, chunkCount)
		}
	}
}

// mapAnthropicToResponsesFinishReason was originally invoked here but
// the IR layer already performs the Anthropic→OpenAI stop_reason
// translation, so the bridge just propagates chunk.FinishReason. Kept
// exported only for unit tests below.

// StreamOpenAIToResponsesSSE reads OpenAI chat.completion.chunk SSE
// upstream and writes OpenAI Responses API SSE to the client. The mirror
// of StreamAnthropicSSEToResponses for the OpenAI upstream path.
// P1-2 fix (2026-08-28): Added ctx parameter for context propagation to gate.
func StreamOpenAIToResponsesSSE(
	ctx context.Context,
	w http.ResponseWriter,
	resp *http.Response,
	clientModel, outboundModel, requestID string,
	capture *audit.StreamCapture,
	pc *pendingCapturer,
) (outcome StreamOutcome) {
	return StreamOpenAIToResponsesSSEWithDiagnostics(
		ctx, w, resp, clientModel, outboundModel, requestID, capture, pc, nil,
	)
}

// StreamOpenAIToResponsesSSEWithDiagnostics converts an OpenAI stream to
// Responses SSE with optional best-effort diagnostics.
// P1-2 fix (2026-08-28): Added ctx parameter for context propagation to gate.
func StreamOpenAIToResponsesSSEWithDiagnostics(
	ctx context.Context,
	w http.ResponseWriter,
	resp *http.Response,
	clientModel, outboundModel, requestID string,
	capture *audit.StreamCapture,
	pc *pendingCapturer,
	diagnostics *DiagnosticContext,
) (outcome StreamOutcome) {
	// Declare the gate before panic recovery so a panic after client-visible
	// semantic output can never be classified as transparently resumable.
	var gate *AttemptCommitGate
	//nolint:errcheck // best-effort close
	defer resp.Body.Close()
	defer func() {
		if r := recover(); r != nil {
			slog.Error("openai-to-responses stream panic recovered",
				"panic", r, "stack", string(debug.Stack()), "request_id", requestID)
			if capture != nil {
				capture.MarkInterruptedWithReason("stream_panic")
			}
			outcome.Interrupted = true
			outcome.Reason = "stream_panic"
			outcome.Kind = errorsx.KindUpstreamDown
			outcome.Resumable = !attemptHasClientSemanticOutput(gate, 0)
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
		diagnosticCollector.report(diagnostics, requestID, "openai-completions", "openai-responses", outcome.Interrupted)
	}()

	// SR-W1: route client frames through the attempt commit gate.
	// Disabled (default) this is the identity function — legacy wire bytes.
	// P1-2 fix (2026-08-28): Pass context to gate for checkpoint propagation.
	w, gate = wrapAttemptWriter(ctx, w, ProtocolOpenAIResponses)
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
		return StreamOutcome{Interrupted: true, Reason: "client_write_failed", Kind: errorsx.KindCanceled, Resumable: false}
		// Initial flush write failed — client disconnected before any frame left;
		// retry is pointless and would violate the no-replay contract.
	}

	if clientModel == "" {
		clientModel = outboundModel
	}

	clientWriter := newClientStreamWriter(w, flusher)
	defer func() {
		applyClientDisconnectOutcome(&outcome, clientWriter, !outcome.Interrupted)
	}()
	scaffold := newResponsesScaffold(w, flusher, requestID, clientModel)
	scaffold.attachCapturer(pc, clientWriter)
	scaffold.writeInitialEvents()
	if clientWriter.clientDisconnected {
		if capture != nil {
			capture.MarkInterruptedWithReason("client_write_failed")
		}
		if pc != nil {
			pc.markInterrupted("client_write_failed")
		}
		return StreamOutcome{Interrupted: true, Reason: "client_write_failed", Kind: errorsx.KindCanceled, Resumable: false}
	}

	// P1-2 fix (2026-08-28): ctx is now a function parameter, removed redundant declaration.

	runtimeCfg := currentStreamRuntimeConfig()
	bodyCloser := resp.Body
	reader := bufio.NewReaderSize(bodyCloser, streamBufSize)

	var (
		inputTokens  int
		outputTokens int
		fullText     strings.Builder
		finishReason string
		chunkCount   int
		// OpenAI's tool_calls streaming protocol only carries the id
		// in the FIRST chunk for a given index; subsequent chunks only
		// carry the new arguments. The bridge maintains this lookup
		// so each function_call_arguments.delta can reference the
		// correct item_id (matching the Responses API contract).
		toolCallIDs          = make(map[int]string)
		upstreamDoneReceived bool
		conversionOverflow   bool
		clientWriteFailed    bool
	)

	writeChunkIR := func(chunk *ir.StreamChunk) {
		if chunk == nil {
			return
		}
		if chunk.Type == ir.ChunkTypeDelta && chunk.Delta != nil {
			if !responsesDeltaFits(scaffold, &fullText, chunk) {
				conversionOverflow = true
				return
			}
			for i := range chunk.Delta.ToolCalls {
				tc := &chunk.Delta.ToolCalls[i]
				if tc.Name != "" && tc.ID != "" {
					toolCallIDs[tc.Index] = tc.ID
				}
				if tc.ID == "" {
					if id, ok := toolCallIDs[tc.Index]; ok {
						tc.ID = id
					}
				}
			}
		}
		sseLine := chunk.SerializeResponses(scaffold.msgID)
		if sseLine == "" {
			return
		}
		written := clientWriter.write(sseLine)
		if !written {
			// Keep consuming upstream and building the pending replay body, but
			// do not count this failed frame as client-visible output.
			clientWriteFailed = true
		}
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
			diagnosticCollector.observeEmittedChunk(chunk)
			chunkCount++
		}
		if chunk.Type == ir.ChunkTypeDelta && chunk.Delta != nil {
			if !appendResponsesBounded(&fullText, chunk.Delta.Content, maxResponsesBridgeTextBytes) ||
				!appendResponsesBounded(&scaffold.reasoningText, chunk.Delta.ReasoningContent, maxResponsesBridgeTextBytes) {
				conversionOverflow = true
				return
			}
			for _, tc := range chunk.Delta.ToolCalls {
				state := scaffold.toolStates[tc.Index]
				if state == nil {
					state = &responsesToolTerminalState{}
					scaffold.toolStates[tc.Index] = state
				}
				if tc.ID != "" {
					state.ID = tc.ID
				}
				if tc.Name != "" {
					state.Name = tc.Name
				}
				if !appendResponsesBounded(&state.Arguments, tc.Arguments, maxResponsesBridgeToolArgumentsBytes) {
					conversionOverflow = true
					return
				}
			}
		}
	}

	for {
		lastSend := time.Time{}
		readResult := readNextStreamLine(ctx, reader, bodyCloser, w, &lastSend, runtimeCfg)
		if readResult.err != nil {
			switch readResult.state {
			case streamReadCanceled:
				slog.Debug("openai_to_responses: client disconnected")
				if capture != nil {
					capture.MarkInterruptedWithReason("client_disconnected")
				}
				scaffold.finishAttempt(gate, fullText.String(), finishReason, inputTokens, outputTokens, inputTokens+outputTokens)
				outcome.Interrupted = true
				outcome.Reason = "client_cancel"
				outcome.Kind = errorsx.KindCanceled
				return outcome
			case streamReadEOF:
				if !upstreamDoneReceived {
					outcome = StreamOutcome{
						Interrupted: true,
						Reason:      "eof_without_done",
						Kind:        errorsx.KindUpstreamDown,
						Resumable:   !attemptHasClientSemanticOutput(gate, chunkCount),
						ChunkCount:  chunkCount,
					}
					if !outcome.Resumable {
						scaffold.finishInterrupted(gate, fullText.String(), outcome.Reason, inputTokens, outputTokens)
					}

					if capture != nil {
						capture.MarkInterruptedWithReason(outcome.Reason)
					}
					return outcome
				}
				scaffold.finishAttempt(gate, fullText.String(), finishReason, inputTokens, outputTokens, inputTokens+outputTokens)
				return StreamOutcome{ChunkCount: chunkCount}
			case streamReadTimeout:
				slog.Warn("openai_to_responses: stream read timeout", "error", readResult.err)
				if capture != nil {
					capture.MarkInterruptedWithReason("stream_timeout")
				}
				outcome.Interrupted = true
				outcome.Reason = "stream_timeout"
				outcome.Kind = errorsx.KindStreamTimeout
				outcome.Resumable = !attemptHasClientSemanticOutput(gate, chunkCount)
				outcome.ChunkCount = chunkCount
				if !outcome.Resumable {
					scaffold.finishInterrupted(gate, fullText.String(), outcome.Reason, inputTokens, outputTokens)
				}

				return outcome

			default:
				failure := streamReadFailureOutcome(readResult.err, chunkCount)
				failure.Resumable = !attemptHasClientSemanticOutput(gate, chunkCount)
				slog.Warn("openai_to_responses: stream read error", "error", readResult.err, "kind", failure.Kind, "reason", failure.Reason)
				if capture != nil {
					capture.MarkInterruptedWithReason(failure.Reason)
				}
				if !failure.Resumable {
					scaffold.finishInterrupted(gate, fullText.String(), failure.Reason, inputTokens, outputTokens)
				}
				outcome = failure
				return outcome
			}
		}

		line := readResult.line
		if line == "" {
			continue
		}
		normalizedLine, hasCombinedDone := splitCombinedDoneFrame(line)
		line = normalizedLine
		if hasCombinedDone {
			reader = prependDoneFrame(reader)
		}

		// Standard OpenAI SSE framing: data: {...}\n\n and sentinel data: [DONE].
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(trimmed, "data: ")
		if payload == "[DONE]" {
			upstreamDoneReceived = true
			scaffold.finishAttempt(gate, fullText.String(), finishReason, inputTokens, outputTokens, inputTokens+outputTokens)
			return StreamOutcome{ChunkCount: chunkCount}
		}

		rawFrame := []byte(line)
		logRawUpstreamFrame(diagnostics, auditFromDiagnostics(diagnostics, requestID, "openai-completions"), rawFrame)
		diagnosticCollector.observeRaw([]byte(payload))

		chunk, perr := ir.ParseOpenAIStreamChunk(trimmed)
		if perr != nil {
			slog.Warn("openai_to_responses: parse failed",
				"data_preview", truncateForLog(payload, 100),
				"error", perr,
				"request_id", requestID)

			reportConversionAnomaly(
				diagnostics, requestID, "openai-completions", "openai-responses", "parse_stream_chunk", []byte(payload), perr, nil,
			)
			continue
		}

		if chunk != nil {
			diagnosticCollector.observeChunk(chunk)
			if chunk.Type == ir.ChunkTypeError {
				outcome = StreamOutcome{
					Interrupted: true,
					Reason:      "upstream_error",
					Kind:        errorsx.KindUpstreamDown,
					Resumable:   !attemptHasClientSemanticOutput(gate, chunkCount),
					ChunkCount:  chunkCount,
				}
				if !outcome.Resumable {
					scaffold.finishInterrupted(gate, fullText.String(), outcome.Reason, inputTokens, outputTokens)
				}
				if capture != nil {
					capture.MarkInterruptedWithReason(outcome.Reason)
				}
				if pc != nil {
					pc.markInterrupted(outcome.Reason)
				}
				return outcome
			}
		}

		// Track usage + finish_reason for response.completed.
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
		}
		if chunk.FinishReason != "" {
			finishReason = chunk.FinishReason
		}

		writeChunkIR(chunk)
		if conversionOverflow {
			outcome = StreamOutcome{Interrupted: true, Reason: "responses_conversion_accumulator_limit", Kind: errorsx.KindConversion, Resumable: false, ChunkCount: chunkCount}
			if capture != nil {
				capture.MarkInterruptedWithReason(outcome.Reason)
			}
			scaffold.finishInterrupted(gate, fullText.String(), outcome.Reason, inputTokens, outputTokens)
			return outcome
		}

		// Incremental integrity breach (repeated-content loop): cut the
		// stream so the executor can failover. Mirrors stream.go.
		if capture != nil && capture.IntegrityBreached() {
			scaffold.finishAttempt(gate, fullText.String(), finishReason, inputTokens, outputTokens, inputTokens+outputTokens)
			return integrityBreachOutcome(capture, chunkCount)
		}
	}
}
