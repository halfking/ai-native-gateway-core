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
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
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
type responsesScaffold struct {
	w           http.ResponseWriter
	flusher     http.Flusher
	requestID   string
	clientModel string

	respID  string // "resp_" + requestID-derived suffix
	msgID   string // "msg_" + requestID-derived suffix
	created int64  // unix timestamp

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
	}
}

func (s *responsesScaffold) attachCapturer(pc *pendingCapturer, cw *clientStreamWriter) {
	s.pc = pc
	s.clientWriter = cw
}

// writeSSEEvent writes a single Responses API SSE event. When pc is
// attached, the same bytes are appended to the capturer so the envelope
// survives a client disconnect.
func (s *responsesScaffold) writeSSEEvent(event string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	line := fmt.Sprintf("event: %s\ndata: %s\n\n", event, data)
	if s.clientWriter != nil {
		s.clientWriter.write(line)
	} else {
		writeSSE(s.w, event, payload)
	}
	if s.pc != nil {
		s.pc.append(line)
	}
}

// writeInitialEvents emits the Responses API opening sequence so SDK
// clients see a well-formed response envelope from the first event.
func (s *responsesScaffold) writeInitialEvents() {
	s.writeSSEEvent("response.created", map[string]any{
		"type": "response.created",
		"response": map[string]any{
			"id":         s.respID,
			"object":     "response",
			"created_at": s.created,
			"model":      s.clientModel,
			"status":     "in_progress",
			"output":     []any{},
		},
	})

	s.writeSSEEvent("response.output_item.added", map[string]any{
		"type":         "response.output_item.added",
		"output_index": 0,
		"item": map[string]any{
			"type":    "message",
			"id":      s.msgID,
			"status":  "in_progress",
			"role":    "assistant",
			"content": []any{},
		},
	})

	s.writeSSEEvent("response.content_part.added", map[string]any{
		"type":          "response.content_part.added",
		"item_id":       s.msgID,
		"output_index":  0,
		"content_index": 0,
		"part": map[string]any{
			"type":        "output_text",
			"text":        "",
			"annotations": []any{},
		},
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

	textDone := map[string]any{
		"type":          "response.output_text.done",
		"item_id":       s.msgID,
		"output_index":  0,
		"content_index": 0,
		"text":          fullText,
	}
	s.writeSSEEvent("response.output_text.done", textDone)

	itemDone := map[string]any{
		"type":         "response.output_item.done",
		"output_index": 0,
		"item": map[string]any{
			"type":   "message",
			"id":     s.msgID,
			"status": status,
			"role":   "assistant",
			"content": []map[string]any{
				{"type": "output_text", "text": fullText, "annotations": []any{}},
			},
		},
	}
	s.writeSSEEvent("response.output_item.done", itemDone)

	completed := map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"id":         s.respID,
			"object":     "response",
			"created_at": s.created,
			"model":      s.clientModel,
			"status":     status,
			"output": []map[string]any{
				{
					"type":   "message",
					"id":     s.msgID,
					"status": status,
					"role":   "assistant",
					"content": []map[string]any{
						{"type": "output_text", "text": fullText, "annotations": []any{}},
					},
				},
			},
			"usage": map[string]any{
				"input_tokens":  inputTokens,
				"output_tokens": outputTokens,
				"total_tokens":  totalTokens,
			},
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
func StreamAnthropicSSEToResponses(
	w http.ResponseWriter,
	resp *http.Response,
	clientModel, outboundModel, requestID string,
	capture *audit.StreamCapture,
	pc *pendingCapturer,
) (outcome StreamOutcome) {
	return StreamAnthropicSSEToResponsesWithDiagnostics(
		w, resp, clientModel, outboundModel, requestID, capture, pc, nil,
	)
}

// StreamAnthropicSSEToResponsesWithDiagnostics converts an Anthropic stream
// to Responses SSE with optional best-effort diagnostics.
func StreamAnthropicSSEToResponsesWithDiagnostics(
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
	w, gate = wrapAttemptWriter(w, ProtocolOpenAIResponses)
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

	var ctx context.Context
	if resp.Request != nil {
		ctx = resp.Request.Context()
	} else {
		ctx = context.Background()
	}

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
		clientWriter.write(sseLine)
		diagnosticCollector.observeEmittedChunk(chunk)
		if pc != nil {
			pc.append(sseLine)
		}
		if capture != nil {
			capture.ObserveChunk(chunk)
			if !clientWriter.clientDisconnected {
				capture.RecordChunkSent()
			}
		}
		chunkCount++

		// Track visible text for the final response.output_text.done payload.
		if chunk.Type == ir.ChunkTypeDelta && chunk.Delta != nil {
			fullText.WriteString(chunk.Delta.Content)
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
			}
			failure := streamReadFailureOutcome(err, chunkCount)
			failure.Resumable = !attemptHasClientSemanticOutput(gate, chunkCount)
			if !failure.Resumable {
				scaffold.finishInterrupted(gate, fullText.String(), failure.Reason, inputTokens, outputTokens)
			}
			outcome = failure
			// Gate-aware resumability. streamReadFailureOutcome hardcodes
			// Resumable=true; a read failure after the client already saw
			// semantic output must NOT be transparently retried — the next
			// supplier node would duplicate committed bytes. Mirrors the
			// eof_without_done and stream_timeout branches in this function.
			outcome.Resumable = !attemptHasClientSemanticOutput(gate, chunkCount)
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
func StreamOpenAIToResponsesSSE(
	w http.ResponseWriter,
	resp *http.Response,
	clientModel, outboundModel, requestID string,
	capture *audit.StreamCapture,
	pc *pendingCapturer,
) (outcome StreamOutcome) {
	return StreamOpenAIToResponsesSSEWithDiagnostics(
		w, resp, clientModel, outboundModel, requestID, capture, pc, nil,
	)
}

// StreamOpenAIToResponsesSSEWithDiagnostics converts an OpenAI stream to
// Responses SSE with optional best-effort diagnostics.
func StreamOpenAIToResponsesSSEWithDiagnostics(
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
	w, gate = wrapAttemptWriter(w, ProtocolOpenAIResponses)
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

	var ctx context.Context
	if resp.Request != nil {
		ctx = resp.Request.Context()
	} else {
		ctx = context.Background()
	}

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
	)

	writeChunkIR := func(chunk *ir.StreamChunk) {
		if chunk == nil {
			return
		}
		if chunk.Type == ir.ChunkTypeDelta && chunk.Delta != nil {
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
		clientWriter.write(sseLine)
		diagnosticCollector.observeEmittedChunk(chunk)
		if pc != nil {
			pc.append(sseLine)
		}
		if capture != nil {
			capture.ObserveChunk(chunk)
			if !clientWriter.clientDisconnected {
				capture.RecordChunkSent()
			}
		}
		chunkCount++
		if chunk.Type == ir.ChunkTypeDelta && chunk.Delta != nil {
			fullText.WriteString(chunk.Delta.Content)
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
				// Gate-aware resumability. streamReadFailureOutcome hardcodes
				// Resumable=true; a read failure after the client already saw
				// semantic output must NOT be transparently retried — the next
				// supplier node would duplicate committed bytes. Mirrors the
				// eof_without_done and stream_timeout branches in this function.
				outcome.Resumable = !attemptHasClientSemanticOutput(gate, chunkCount)
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

		// Incremental integrity breach (repeated-content loop): cut the
		// stream so the executor can failover. Mirrors stream.go.
		if capture != nil && capture.IntegrityBreached() {
			scaffold.finishAttempt(gate, fullText.String(), finishReason, inputTokens, outputTokens, inputTokens+outputTokens)
			return integrityBreachOutcome(capture, chunkCount)
		}
	}
}
