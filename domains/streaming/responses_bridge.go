package streaming

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
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

// writeInitialEvents emits the Responses API opening sequence so SDK
// clients see a well-formed response envelope from the first event.
func (s *responsesScaffold) writeInitialEvents() {
	writeSSE(s.w, "response.created", map[string]any{
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
	s.flusher.Flush()

	writeSSE(s.w, "response.output_item.added", map[string]any{
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
	s.flusher.Flush()

	writeSSE(s.w, "response.content_part.added", map[string]any{
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
	s.flusher.Flush()
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
	writeSSE(s.w, "response.output_text.done", textDone)

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
	writeSSE(s.w, "response.output_item.done", itemDone)

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
	writeSSE(s.w, "response.completed", completed)
	s.flusher.Flush()
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
			if pc != nil {
				pc.markInterrupted("stream_panic")
			}
		}
		if pc != nil {
			pc.finalize(outcome)
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
	flusher.Flush()

	if clientModel == "" {
		clientModel = outboundModel
	}

	scaffold := newResponsesScaffold(w, flusher, requestID, clientModel)
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
	)

	// writeChunkIR serializes one IR StreamChunk via the Responses API
	// serializer, writes the SSE event(s) to the client, and updates
	// the audit capturer + chunk counter.
	writeChunkIR := func(chunk *ir.StreamChunk) {
		if chunk == nil {
			return
		}
		sseLine := chunk.SerializeResponses(scaffold.msgID)
		if sseLine == "" {
			// Done/Usage chunks emit no standalone event — orchestrator
			// will surface usage in response.completed.
			return
		}
		_, _ = io.WriteString(w, sseLine)
		flusher.Flush()
		if pc != nil {
			pc.append(sseLine)
		}
		if capture != nil {
			capture.ObserveChunk(chunk)
		}
		chunkCount++

		// Track visible text for the final response.output_text.done payload.
		if chunk.Type == ir.ChunkTypeDelta && chunk.Delta != nil {
			fullText.WriteString(chunk.Delta.Content)
		}
	}

	for {
		readCtx, readCancel := context.WithTimeout(ctx, runtimeCfg.streamChunkTimeout)
		resultCh := make(chan struct {
			eventType string
			data      []byte
			err       error
		}, 1)
		go func() {
			et, d, e := readAnthropicSSEEvent(readCtx, reader)
			resultCh <- struct {
				eventType string
				data      []byte
				err       error
			}{et, d, e}
		}()

		var (
			eventType string
			data      []byte
			err       error
		)
		select {
		case res := <-resultCh:
			eventType, data, err = res.eventType, res.data, res.err
			readCancel()
		case <-readCtx.Done():
			readCancel()
			slog.Warn("anthropic_to_responses: chunk timeout",
				"timeout_seconds", runtimeCfg.streamChunkTimeout.Seconds(),
				"chunks_received", chunkCount,
				"request_id", requestID)
			if capture != nil {
				capture.MarkInterruptedWithReason("stream_chunk_timeout")
			}
			totalTokens := inputTokens + outputTokens
			scaffold.writeFinalEvents(fullText.String(), finishReason, inputTokens, outputTokens, totalTokens)
			outcome.Interrupted = true
			outcome.Reason = "chunk_timeout"
			outcome.ChunkCount = chunkCount
			if pc != nil {
				pc.markInterrupted("chunk_timeout")
			}
			return outcome
		}

		if err != nil {
			if err == io.EOF || readCtx.Err() != nil {
				totalTokens := inputTokens + outputTokens
				scaffold.writeFinalEvents(fullText.String(), finishReason, inputTokens, outputTokens, totalTokens)
				return StreamOutcome{ChunkCount: chunkCount}
			}
			outcome.Interrupted = true
			outcome.Reason = "read_error"
			if capture != nil {
				capture.MarkInterruptedWithReason("anthropic_to_responses_read_error")
			}
			scaffold.writeFinalEvents(fullText.String(), finishReason, inputTokens, outputTokens, inputTokens+outputTokens)
			outcome.ChunkCount = chunkCount
			return outcome
		}

		if eventType == "" || len(data) == 0 {
			continue
		}

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
			continue
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
			if pc != nil {
				pc.markInterrupted("stream_panic")
			}
		}
		if pc != nil {
			pc.finalize(outcome)
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
	flusher.Flush()

	if clientModel == "" {
		clientModel = outboundModel
	}

	scaffold := newResponsesScaffold(w, flusher, requestID, clientModel)
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
	)

	writeChunkIR := func(chunk *ir.StreamChunk) {
		if chunk == nil {
			return
		}
		sseLine := chunk.SerializeResponses(scaffold.msgID)
		if sseLine == "" {
			return
		}
		_, _ = io.WriteString(w, sseLine)
		flusher.Flush()
		if pc != nil {
			pc.append(sseLine)
		}
		if capture != nil {
			capture.ObserveChunk(chunk)
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
				scaffold.writeFinalEvents(fullText.String(), finishReason, inputTokens, outputTokens, inputTokens+outputTokens)
				outcome.Interrupted = true
				outcome.Reason = "client_cancel"
				return outcome
			case streamReadEOF:
				scaffold.writeFinalEvents(fullText.String(), finishReason, inputTokens, outputTokens, inputTokens+outputTokens)
				return StreamOutcome{ChunkCount: chunkCount}
			case streamReadTimeout:
				slog.Warn("openai_to_responses: stream read timeout", "error", readResult.err)
				if capture != nil {
					capture.MarkInterruptedWithReason("stream_timeout")
				}
				scaffold.writeFinalEvents(fullText.String(), finishReason, inputTokens, outputTokens, inputTokens+outputTokens)
				outcome.Interrupted = true
				outcome.Reason = "stream_timeout"
				return outcome
			default:
				slog.Warn("openai_to_responses: stream read error", "error", readResult.err)
				if capture != nil {
					capture.MarkInterruptedWithReason("stream_error")
				}
				scaffold.writeFinalEvents(fullText.String(), finishReason, inputTokens, outputTokens, inputTokens+outputTokens)
				outcome.Interrupted = true
				outcome.Reason = "read_error"
				return outcome
			}
		}

		line := readResult.line
		if line == "" {
			continue
		}

		// Standard OpenAI SSE framing: data: {...}\n\n and sentinel data: [DONE].
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(trimmed, "data: ")
		if payload == "[DONE]" {
			scaffold.writeFinalEvents(fullText.String(), finishReason, inputTokens, outputTokens, inputTokens+outputTokens)
			return StreamOutcome{ChunkCount: chunkCount}
		}

		chunk, perr := ir.ParseOpenAIStreamChunk(trimmed)
		if perr != nil {
			slog.Warn("openai_to_responses: parse failed",
				"data_preview", truncateForLog(payload, 100),
				"error", perr,
				"request_id", requestID)
			continue
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
	}
}
